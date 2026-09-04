package usage

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	plusstore "github.com/router-for-me/CLIProxyAPI/v7/internal/usage/plusvendor/store"
)

type UsageImportSession struct {
	ID             string                 `json:"id"`
	Filename       string                 `json:"filename"`
	Status         string                 `json:"status"`
	SizeBytes      int64                  `json:"size_bytes"`
	ReceivedBytes  int64                  `json:"received_bytes"`
	ChunkSizeBytes int64                  `json:"chunk_size_bytes"`
	CreatedAtMS    int64                  `json:"created_at_ms"`
	UpdatedAtMS    int64                  `json:"updated_at_ms"`
	ExpiresAtMS    int64                  `json:"expires_at_ms"`
	Retryable      bool                   `json:"retryable,omitempty"`
	Error          string                 `json:"error,omitempty"`
	Result         map[string]interface{} `json:"result,omitempty"`
	ResumeKey      string                 `json:"-"`
}

type usageImportSessionMetadata struct {
	UsageImportSession
	ResumeKey string `json:"resume_key,omitempty"`
}

type createUsageImportSessionRequest struct {
	Filename  string `json:"filename"`
	SizeBytes int64  `json:"size_bytes"`
	ResumeKey string `json:"resume_key,omitempty"`
}

type usageImportSessionManager struct {
	root string
	cfg  config.UsageImportSessionConfig
}

var importSessionIDPattern = regexp.MustCompile(`^[a-f0-9]{32}$`)
var importSessionLocks sync.Map

const importSessionCleanupBatchSize = 64

type usageImportSessionImporter func(context.Context, io.Reader) (plusstore.UsageImportResult, error)

func (h *Handlers) CreateUsageImportSession(c *gin.Context) {
	manager, ok := h.importSessionManager(c)
	if !ok {
		return
	}
	var req createUsageImportSessionRequest
	if !decodeStrictJSON(c, &req, 1<<20) {
		return
	}
	session, err := manager.Create(c.Request.Context().Done(), req)
	if err != nil {
		writeImportSessionError(c, err)
		return
	}
	c.JSON(http.StatusCreated, session)
}

func (h *Handlers) GetUsageImportSession(c *gin.Context) {
	manager, ok := h.importSessionManager(c)
	if !ok {
		return
	}
	session, found, err := manager.Get(c.Param("id"))
	if err != nil {
		writeImportSessionError(c, err)
		return
	}
	if !found {
		c.JSON(http.StatusNotFound, gin.H{"error": "usage import session not found", "code": "usage_import_session_not_found"})
		return
	}
	c.JSON(http.StatusOK, session)
}

func (h *Handlers) UploadUsageImportSessionChunk(c *gin.Context) {
	manager, ok := h.importSessionManager(c)
	if !ok {
		return
	}
	offset, err := parseImportSessionOffset(c.Query("offset"))
	if err != nil {
		writeImportSessionError(c, err)
		return
	}
	session, err := manager.AppendChunk(c.Request.Context(), c.Param("id"), offset, c.Request.Body)
	if err != nil {
		writeImportSessionError(c, err)
		return
	}
	c.JSON(http.StatusOK, session)
}

func (h *Handlers) CompleteUsageImportSession(c *gin.Context) {
	manager, ok := h.importSessionManager(c)
	if !ok {
		return
	}
	svc, closeStore, ok := h.auxiliaryService(c)
	if !ok {
		return
	}
	defer closeStore()
	completeCtx := context.Background()
	if h != nil && h.bridge != nil {
		completeCtx = h.bridge.Context()
	}
	session, err := manager.Complete(completeCtx, c.Param("id"), svc.ImportEventsJSONL)
	if err != nil {
		writeImportSessionError(c, err)
		return
	}
	if session.Status == "completed" {
		wakeUsageImportRollups(completeCtx, c, h)
	}
	c.JSON(http.StatusOK, session)
}

func (h *Handlers) CancelUsageImportSession(c *gin.Context) {
	manager, ok := h.importSessionManager(c)
	if !ok {
		return
	}
	session, err := manager.Cancel(c.Param("id"))
	if err != nil {
		writeImportSessionError(c, err)
		return
	}
	c.JSON(http.StatusOK, session)
}

func wakeUsageImportRollups(ctx context.Context, c *gin.Context, h *Handlers) {
	if h == nil || h.bridge == nil {
		return
	}
	dbPath := strings.TrimSpace(h.bridge.DBPath())
	if dbPath == "" {
		return
	}
	store, closeStore, err := openUsageStore(dbPath)
	if err != nil {
		_ = c.Error(err)
		return
	}
	defer closeStore()
	_, _ = store.CatchUpHourlyRollups(ctx, plusstore.RollupOptions{
		ThroughMS:  time.Now().UnixMilli(),
		MaxBatches: 16,
		Owner:      "usage-import-complete",
	})
}

func (h *Handlers) importSessionManager(c *gin.Context) (*usageImportSessionManager, bool) {
	if h == nil || h.bridge == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "usage bridge is unavailable", "code": "usage_bridge_unavailable"})
		return nil, false
	}
	cfg := h.effectiveImportSessionConfig()
	if err := cfg.Validate(); err != nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{
			"error": err.Error(),
			"code":  "usage_import_session_config_invalid",
		})
		return nil, false
	}
	root := strings.TrimSpace(cfg.Dir)
	if root == "" {
		dbPath := strings.TrimSpace(h.bridge.DBPath())
		if dbPath == "" {
			c.JSON(http.StatusServiceUnavailable, gin.H{"error": "usage store is unavailable", "code": "usage_store_unavailable"})
			return nil, false
		}
		root = filepath.Join(filepath.Dir(dbPath), "usage-import-sessions")
	}
	manager, err := newUsageImportSessionManager(root, cfg)
	if err != nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": err.Error(), "code": "usage_import_session_config_invalid"})
		return nil, false
	}
	return manager, true
}

func (h *Handlers) effectiveImportSessionConfig() config.UsageImportSessionConfig {
	if h == nil {
		return config.DefaultUsageImportSessionConfig()
	}
	return h.importSessionConfig.WithDefaults()
}

func newUsageImportSessionManager(root string, cfg config.UsageImportSessionConfig) (*usageImportSessionManager, error) {
	cfg = cfg.WithDefaults()
	cleanRoot, err := secureImportSessionRoot(root)
	if err != nil {
		return nil, err
	}
	manager := &usageImportSessionManager{root: cleanRoot, cfg: cfg}
	if err := manager.RecoverAndCleanup(); err != nil {
		return nil, err
	}
	return manager, nil
}

func secureImportSessionRoot(root string) (string, error) {
	root = strings.TrimSpace(root)
	if root == "" {
		return "", errors.New("usage import session dir is required")
	}
	abs, err := filepath.Abs(root)
	if err != nil {
		return "", fmt.Errorf("usage import session dir: %w", err)
	}
	if info, err := os.Lstat(abs); err == nil {
		if info.Mode()&os.ModeSymlink != 0 {
			return "", fmt.Errorf("usage import session dir must not be a symlink")
		}
		if !info.IsDir() {
			return "", fmt.Errorf("usage import session dir must be a directory")
		}
	} else if os.IsNotExist(err) {
		if err := os.MkdirAll(abs, 0o700); err != nil {
			return "", fmt.Errorf("create usage import session dir: %w", err)
		}
	} else {
		return "", fmt.Errorf("stat usage import session dir: %w", err)
	}
	if err := os.Chmod(abs, 0o700); err != nil {
		return "", fmt.Errorf("chmod usage import session dir: %w", err)
	}
	return abs, nil
}

func (m *usageImportSessionManager) Create(done <-chan struct{}, req createUsageImportSessionRequest) (UsageImportSession, error) {
	if done != nil {
		select {
		case <-done:
			return UsageImportSession{}, contextCanceledError()
		default:
		}
	}
	filename := sanitizeImportFilename(req.Filename)
	if filename == "" {
		return UsageImportSession{}, errImportSessionBadRequest("filename is required")
	}
	if req.SizeBytes < 0 {
		return UsageImportSession{}, errImportSessionBadRequest("size_bytes must be greater than or equal to 0")
	}
	if req.SizeBytes > m.cfg.MaxSessionBytes {
		return UsageImportSession{}, errImportSessionTooLarge("size_bytes exceeds max-session-bytes")
	}
	if strings.TrimSpace(req.ResumeKey) != "" {
		if session, ok, err := m.findByResumeKey(strings.TrimSpace(req.ResumeKey), filename, req.SizeBytes); err != nil {
			return UsageImportSession{}, err
		} else if ok {
			return session, nil
		}
	}
	active, err := m.countActive()
	if err != nil {
		return UsageImportSession{}, err
	}
	if active >= m.cfg.MaxActive {
		return UsageImportSession{}, errImportSessionTooMany("too many active usage import sessions")
	}
	now := time.Now().UnixMilli()
	id, err := randomImportSessionID()
	if err != nil {
		return UsageImportSession{}, err
	}
	session := UsageImportSession{
		ID:             id,
		Filename:       filename,
		Status:         "uploading",
		SizeBytes:      req.SizeBytes,
		ReceivedBytes:  0,
		ChunkSizeBytes: m.cfg.ChunkSizeBytes,
		CreatedAtMS:    now,
		UpdatedAtMS:    now,
		ExpiresAtMS:    now + int64(m.cfg.TTLMinutes)*60*1000,
		ResumeKey:      strings.TrimSpace(req.ResumeKey),
	}
	if err := m.writeSession(session); err != nil {
		return UsageImportSession{}, err
	}
	return session, nil
}

func (m *usageImportSessionManager) Get(id string) (UsageImportSession, bool, error) {
	if !importSessionIDPattern.MatchString(id) {
		return UsageImportSession{}, false, nil
	}
	session, err := m.readSession(id)
	if os.IsNotExist(err) {
		return UsageImportSession{}, false, nil
	}
	if err != nil {
		return UsageImportSession{}, false, err
	}
	return session, true, nil
}

func (m *usageImportSessionManager) AppendChunk(ctx context.Context, id string, offset int64, r io.Reader) (UsageImportSession, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if offset < 0 {
		return UsageImportSession{}, errImportSessionBadRequest("offset must be greater than or equal to 0")
	}
	if r == nil {
		r = strings.NewReader("")
	}
	unlock := m.lockSession(id)
	defer unlock()
	session, err := m.readExistingSession(id)
	if err != nil {
		return UsageImportSession{}, err
	}
	if session.Status != "uploading" && session.Status != "ready" {
		return UsageImportSession{}, errImportSessionConflict("usage import session is not accepting chunks")
	}
	if time.Now().UnixMilli() >= session.ExpiresAtMS {
		return UsageImportSession{}, errImportSessionConflict("usage import session expired")
	}
	if offset != session.ReceivedBytes {
		return UsageImportSession{}, errImportSessionConflict("chunk offset does not match received bytes")
	}
	partPath, err := m.sessionPartPath(id)
	if err != nil {
		return UsageImportSession{}, err
	}
	if err := rejectSymlinkPath(partPath, "usage import session part"); err != nil {
		return UsageImportSession{}, err
	}
	file, err := os.OpenFile(partPath, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return UsageImportSession{}, fmt.Errorf("open usage import session part: %w", err)
	}
	closed := false
	closeFile := func() error {
		if closed {
			return nil
		}
		closed = true
		return file.Close()
	}
	info, err := file.Stat()
	if err != nil {
		_ = closeFile()
		return UsageImportSession{}, fmt.Errorf("stat usage import session part: %w", err)
	}
	if info.Size() < offset {
		_ = closeFile()
		return UsageImportSession{}, errImportSessionConflict("usage import session part is shorter than received bytes")
	}
	if info.Size() > offset {
		if err := file.Truncate(offset); err != nil {
			_ = closeFile()
			return UsageImportSession{}, fmt.Errorf("truncate usage import session part: %w", err)
		}
	}
	if _, err := file.Seek(offset, io.SeekStart); err != nil {
		_ = closeFile()
		return UsageImportSession{}, fmt.Errorf("seek usage import session part: %w", err)
	}
	maxBytes := session.SizeBytes - offset
	if maxBytes > m.cfg.ChunkSizeBytes {
		maxBytes = m.cfg.ChunkSizeBytes
	}
	if maxBytes < 0 || offset > m.cfg.MaxSessionBytes {
		_ = closeFile()
		return UsageImportSession{}, errImportSessionTooLarge("offset exceeds session size")
	}
	limited := &io.LimitedReader{R: r, N: maxBytes + 1}
	copied, err := io.Copy(file, limited)
	if err != nil {
		_ = file.Truncate(offset)
		_ = closeFile()
		if ctxErr := ctx.Err(); ctxErr != nil {
			return UsageImportSession{}, contextCanceledError()
		}
		return UsageImportSession{}, fmt.Errorf("append usage import session chunk: %w", err)
	}
	if copied > maxBytes {
		_ = file.Truncate(offset)
		_ = closeFile()
		return UsageImportSession{}, errImportSessionTooLarge("chunk exceeds remaining session bytes")
	}
	if err := ctx.Err(); err != nil {
		_ = file.Truncate(offset)
		_ = closeFile()
		return UsageImportSession{}, contextCanceledError()
	}
	if err := file.Sync(); err != nil {
		_ = file.Truncate(offset)
		_ = closeFile()
		return UsageImportSession{}, fmt.Errorf("sync usage import session chunk: %w", err)
	}
	if err := closeFile(); err != nil {
		_ = os.Truncate(partPath, offset)
		return UsageImportSession{}, fmt.Errorf("close usage import session chunk: %w", err)
	}
	session.ReceivedBytes += copied
	session.UpdatedAtMS = time.Now().UnixMilli()
	session.Error = ""
	session.Retryable = false
	session.Result = nil
	if session.ReceivedBytes == session.SizeBytes {
		session.Status = "ready"
	} else {
		session.Status = "uploading"
	}
	if err := m.writeSession(session); err != nil {
		_ = os.Truncate(partPath, offset)
		return UsageImportSession{}, err
	}
	return session, nil
}

func (m *usageImportSessionManager) Complete(ctx context.Context, id string, importer usageImportSessionImporter) (UsageImportSession, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if importer == nil {
		return UsageImportSession{}, errImportSessionBadRequest("usage import session importer is required")
	}
	unlock := m.lockSession(id)
	defer unlock()
	session, err := m.readExistingSession(id)
	if err != nil {
		return UsageImportSession{}, err
	}
	if session.Status == "completed" || session.Status == "cancelled" {
		return session, nil
	}
	if session.Status != "ready" && !(session.Status == "uploading" && session.ReceivedBytes == session.SizeBytes) && !(session.Status == "failed" && session.Retryable) {
		return UsageImportSession{}, errImportSessionConflict("usage import session is not complete")
	}
	if session.ReceivedBytes != session.SizeBytes {
		return UsageImportSession{}, errImportSessionConflict("usage import session is incomplete")
	}
	partPath, err := m.sessionPartPath(id)
	if err != nil {
		return UsageImportSession{}, err
	}
	if err := rejectSymlinkPath(partPath, "usage import session part"); err != nil {
		return UsageImportSession{}, err
	}
	file, err := os.Open(partPath)
	if err != nil {
		if os.IsNotExist(err) && session.SizeBytes == 0 {
			file, err = os.OpenFile(partPath, os.O_CREATE|os.O_RDONLY, 0o600)
		}
		if err != nil {
			return UsageImportSession{}, fmt.Errorf("open usage import session part: %w", err)
		}
	}
	session.Status = "processing"
	session.UpdatedAtMS = time.Now().UnixMilli()
	session.Error = ""
	session.Retryable = false
	session.Result = nil
	if err := m.writeSession(session); err != nil {
		_ = file.Close()
		return UsageImportSession{}, err
	}
	result, err := importer(ctx, file)
	if errClose := file.Close(); err == nil && errClose != nil {
		err = errClose
	}
	session.Result = usageImportResultMap(result)
	session.UpdatedAtMS = time.Now().UnixMilli()
	if err != nil {
		session.Status = "failed"
		session.Error = err.Error()
		session.Retryable = plusstore.IsUsageImportRetryable(err)
		if !session.Retryable {
			_ = os.Remove(partPath)
		}
		if errWrite := m.writeSession(session); errWrite != nil {
			return UsageImportSession{}, errWrite
		}
		return session, nil
	}
	if result.Failed > 0 || result.Unsupported > 0 {
		session.Status = "failed"
		session.Retryable = false
		session.Error = "usage import session contains non-importable records"
		_ = os.Remove(partPath)
		if errWrite := m.writeSession(session); errWrite != nil {
			return UsageImportSession{}, errWrite
		}
		return session, nil
	}
	session.Status = "completed"
	session.Retryable = false
	session.Error = ""
	_ = os.Remove(partPath)
	if err := m.writeSession(session); err != nil {
		return UsageImportSession{}, err
	}
	return session, nil
}

func (m *usageImportSessionManager) Cancel(id string) (UsageImportSession, error) {
	unlock := m.lockSession(id)
	defer unlock()
	session, err := m.readExistingSession(id)
	if err != nil {
		return UsageImportSession{}, err
	}
	if session.Status == "completed" || session.Status == "cancelled" {
		return session, nil
	}
	session.Status = "cancelled"
	session.Retryable = false
	session.UpdatedAtMS = time.Now().UnixMilli()
	session.Error = "usage import cancelled"
	session.Result = nil
	if partPath, err := m.sessionPartPath(id); err == nil {
		_ = os.Remove(partPath)
	}
	if err := m.writeSession(session); err != nil {
		return UsageImportSession{}, err
	}
	return session, nil
}

func (m *usageImportSessionManager) RecoverAndCleanup() error {
	if m == nil {
		return nil
	}
	if err := m.recoverSessions(); err != nil {
		return err
	}
	return m.cleanupExpiredSessions()
}

func (m *usageImportSessionManager) StartCleanupLoop(ctx context.Context, interval time.Duration) <-chan struct{} {
	done := make(chan struct{})
	if ctx == nil {
		ctx = context.Background()
	}
	if interval <= 0 {
		interval = time.Minute
	}
	go func() {
		defer close(done)
		ticker := time.NewTicker(interval)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				_ = m.RecoverAndCleanup()
			}
		}
	}()
	return done
}

func (m *usageImportSessionManager) recoverSessions() error {
	entries, err := os.ReadDir(m.root)
	if err != nil {
		return fmt.Errorf("recover usage import sessions: %w", err)
	}
	for _, entry := range entries {
		if !entry.IsDir() || !importSessionIDPattern.MatchString(entry.Name()) {
			continue
		}
		id := entry.Name()
		unlock := m.lockSession(id)
		if errRecover := m.recoverSessionLocked(id); errRecover != nil {
			unlock()
			return errRecover
		}
		unlock()
	}
	return nil
}

func (m *usageImportSessionManager) recoverSessionLocked(id string) error {
	session, err := m.readSession(id)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return err
	}
	if session.Status == "completed" || session.Status == "cancelled" || (session.Status == "failed" && !session.Retryable) {
		return nil
	}
	partPath, err := m.sessionPartPath(id)
	if err != nil {
		return err
	}
	partSize := int64(0)
	partInfo, err := os.Stat(partPath)
	if err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("stat usage import session part: %w", err)
	}
	if err == nil {
		partSize = partInfo.Size()
	}
	if partSize > session.SizeBytes {
		if err := os.Truncate(partPath, session.SizeBytes); err != nil {
			return fmt.Errorf("truncate usage import session part: %w", err)
		}
		partSize = session.SizeBytes
	}
	changed := false
	if session.Status == "processing" {
		session.Status = "failed"
		session.Retryable = true
		session.Error = "usage import interrupted before completion"
		changed = true
	}
	if session.Status == "cancel_requested" {
		session.Status = "cancelled"
		session.Retryable = false
		session.Error = "usage import cancelled"
		_ = os.Remove(partPath)
		changed = true
	}
	if session.ReceivedBytes != partSize {
		session.ReceivedBytes = partSize
		if session.Status == "uploading" || session.Status == "ready" {
			if session.ReceivedBytes == session.SizeBytes {
				session.Status = "ready"
			} else {
				session.Status = "uploading"
			}
		}
		changed = true
	}
	if changed {
		session.UpdatedAtMS = time.Now().UnixMilli()
		return m.writeSession(session)
	}
	return nil
}

func (m *usageImportSessionManager) cleanupExpiredSessions() error {
	now := time.Now().UnixMilli()
	entries, err := os.ReadDir(m.root)
	if err != nil {
		return fmt.Errorf("cleanup usage import sessions: %w", err)
	}
	removed := 0
	for _, entry := range entries {
		if !entry.IsDir() || !importSessionIDPattern.MatchString(entry.Name()) {
			continue
		}
		id := entry.Name()
		unlock := m.lockSession(id)
		cleaned, errCleanup := m.cleanupExpiredSessionLocked(id, now)
		unlock()
		if errCleanup != nil {
			return errCleanup
		}
		if cleaned {
			removed++
			if removed >= importSessionCleanupBatchSize {
				return nil
			}
		}
	}
	return nil
}

func (m *usageImportSessionManager) cleanupExpiredSessionLocked(id string, now int64) (bool, error) {
	session, err := m.readSession(id)
	if os.IsNotExist(err) {
		return m.cleanupOrphanSessionDir(id, now)
	}
	if err != nil {
		return false, err
	}
	if session.ExpiresAtMS > now || isImportSessionCleanupProtected(session) {
		return false, nil
	}
	dir, err := m.sessionDir(id)
	if err != nil {
		return false, err
	}
	return true, os.RemoveAll(dir)
}

func (m *usageImportSessionManager) cleanupOrphanSessionDir(id string, now int64) (bool, error) {
	dir, err := m.sessionDir(id)
	if err != nil {
		return false, err
	}
	info, err := os.Stat(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return false, nil
		}
		return false, err
	}
	if now-info.ModTime().UnixMilli() < int64(m.cfg.TTLMinutes)*60*1000 {
		return false, nil
	}
	return true, os.RemoveAll(dir)
}

func (m *usageImportSessionManager) findByResumeKey(resumeKey, filename string, sizeBytes int64) (UsageImportSession, bool, error) {
	sessions, err := m.listSessions()
	if err != nil {
		return UsageImportSession{}, false, err
	}
	for _, session := range sessions {
		if session.ResumeKey == resumeKey && session.Filename == filename && session.SizeBytes == sizeBytes && isImportSessionActive(session) {
			return session, true, nil
		}
	}
	return UsageImportSession{}, false, nil
}

func (m *usageImportSessionManager) countActive() (int, error) {
	sessions, err := m.listSessions()
	if err != nil {
		return 0, err
	}
	active := 0
	for _, session := range sessions {
		if isImportSessionActive(session) {
			active++
		}
	}
	return active, nil
}

func (m *usageImportSessionManager) listSessions() ([]UsageImportSession, error) {
	entries, err := os.ReadDir(m.root)
	if err != nil {
		return nil, fmt.Errorf("list usage import sessions: %w", err)
	}
	out := []UsageImportSession{}
	for _, entry := range entries {
		if !entry.IsDir() || !importSessionIDPattern.MatchString(entry.Name()) {
			continue
		}
		session, err := m.readSession(entry.Name())
		if err == nil {
			out = append(out, session)
		}
	}
	return out, nil
}

func (m *usageImportSessionManager) readSession(id string) (UsageImportSession, error) {
	path, err := m.sessionMetadataPath(id)
	if err != nil {
		return UsageImportSession{}, err
	}
	dir := filepath.Dir(path)
	if info, err := os.Lstat(dir); err != nil {
		return UsageImportSession{}, err
	} else if info.Mode()&os.ModeSymlink != 0 {
		return UsageImportSession{}, fmt.Errorf("usage import session dir must not be a symlink")
	} else if !info.IsDir() {
		return UsageImportSession{}, fmt.Errorf("usage import session path must be a directory")
	}
	if err := rejectSymlinkPath(path, "usage import session metadata"); err != nil {
		return UsageImportSession{}, err
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return UsageImportSession{}, err
	}
	var session UsageImportSession
	var metadata usageImportSessionMetadata
	if err := json.Unmarshal(data, &metadata); err != nil {
		return UsageImportSession{}, fmt.Errorf("read usage import session metadata: %w", err)
	}
	session = metadata.UsageImportSession
	session.ResumeKey = metadata.ResumeKey
	return session, nil
}

func (m *usageImportSessionManager) readExistingSession(id string) (UsageImportSession, error) {
	if !importSessionIDPattern.MatchString(id) {
		return UsageImportSession{}, errImportSessionNotFound("usage import session not found")
	}
	session, err := m.readSession(id)
	if os.IsNotExist(err) {
		return UsageImportSession{}, errImportSessionNotFound("usage import session not found")
	}
	return session, err
}

func (m *usageImportSessionManager) writeSession(session UsageImportSession) error {
	dir, err := m.sessionDir(session.ID)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("create usage import session: %w", err)
	}
	if info, err := os.Lstat(dir); err != nil {
		return fmt.Errorf("stat usage import session: %w", err)
	} else if info.Mode()&os.ModeSymlink != 0 {
		return fmt.Errorf("usage import session dir must not be a symlink")
	}
	if err := os.Chmod(dir, 0o700); err != nil {
		return fmt.Errorf("chmod usage import session: %w", err)
	}
	metadataPath := filepath.Join(dir, "metadata.json")
	tmpPath := filepath.Join(dir, fmt.Sprintf("metadata.json.%d.%d.tmp", os.Getpid(), time.Now().UnixNano()))
	data, err := json.MarshalIndent(usageImportSessionMetadata{
		UsageImportSession: session,
		ResumeKey:          session.ResumeKey,
	}, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal usage import session metadata: %w", err)
	}
	if err := os.WriteFile(tmpPath, data, 0o600); err != nil {
		return fmt.Errorf("write usage import session metadata: %w", err)
	}
	if err := os.Rename(tmpPath, metadataPath); err != nil {
		return fmt.Errorf("commit usage import session metadata: %w", err)
	}
	return nil
}

func (m *usageImportSessionManager) sessionPartPath(id string) (string, error) {
	dir, err := m.sessionDir(id)
	if err != nil {
		return "", err
	}
	if info, err := os.Lstat(dir); err != nil {
		return "", fmt.Errorf("stat usage import session: %w", err)
	} else if info.Mode()&os.ModeSymlink != 0 {
		return "", fmt.Errorf("usage import session dir must not be a symlink")
	} else if !info.IsDir() {
		return "", fmt.Errorf("usage import session path must be a directory")
	}
	return filepath.Join(dir, "upload.part"), nil
}

func (m *usageImportSessionManager) sessionDir(id string) (string, error) {
	if !importSessionIDPattern.MatchString(id) {
		return "", fmt.Errorf("invalid usage import session id")
	}
	dir := filepath.Join(m.root, id)
	if !strings.HasPrefix(dir, m.root+string(os.PathSeparator)) {
		return "", fmt.Errorf("usage import session path escapes root")
	}
	return dir, nil
}

func (m *usageImportSessionManager) sessionMetadataPath(id string) (string, error) {
	dir, err := m.sessionDir(id)
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "metadata.json"), nil
}

func (m *usageImportSessionManager) lockSession(id string) func() {
	key := m.root + string(os.PathSeparator) + id
	value, _ := importSessionLocks.LoadOrStore(key, &sync.Mutex{})
	lock := value.(*sync.Mutex)
	lock.Lock()
	return lock.Unlock
}

func parseImportSessionOffset(value string) (int64, error) {
	value = strings.TrimSpace(value)
	if value == "" {
		return 0, errImportSessionBadRequest("offset is required")
	}
	offset, err := strconv.ParseInt(value, 10, 64)
	if err != nil || offset < 0 {
		return 0, errImportSessionBadRequest("offset must be greater than or equal to 0")
	}
	return offset, nil
}

func usageImportResultMap(result plusstore.UsageImportResult) map[string]interface{} {
	data, err := json.Marshal(result)
	if err != nil {
		return nil
	}
	out := map[string]interface{}{}
	if err := json.Unmarshal(data, &out); err != nil {
		return nil
	}
	return out
}

func decodeStrictJSON(c *gin.Context, dst interface{}, limit int64) bool {
	c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, limit)
	decoder := json.NewDecoder(c.Request.Body)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(dst); err != nil {
		var maxBytesErr *http.MaxBytesError
		if errors.As(err, &maxBytesErr) {
			c.JSON(http.StatusRequestEntityTooLarge, gin.H{"error": "request body too large", "code": "usage_import_session_payload_too_large"})
			return false
		}
		badRequest(c, "invalid JSON payload")
		return false
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		badRequest(c, "invalid JSON payload")
		return false
	}
	return true
}

func sanitizeImportFilename(filename string) string {
	filename = filepath.Base(strings.TrimSpace(filename))
	if filename == "." || filename == string(os.PathSeparator) {
		return ""
	}
	return strings.TrimSpace(filename)
}

func randomImportSessionID() (string, error) {
	var raw [16]byte
	if _, err := rand.Read(raw[:]); err != nil {
		return "", fmt.Errorf("generate usage import session id: %w", err)
	}
	return hex.EncodeToString(raw[:]), nil
}

func isImportSessionActive(session UsageImportSession) bool {
	now := time.Now().UnixMilli()
	return (session.Status == "uploading" || session.Status == "ready" || session.Status == "processing" || (session.Status == "failed" && session.Retryable)) && session.ExpiresAtMS > now
}

func isImportSessionCleanupProtected(session UsageImportSession) bool {
	return session.Status == "processing" ||
		session.Status == "chunking" ||
		session.Retryable ||
		isImportSessionActive(session)
}

func importSessionConfigJSON(cfg config.UsageImportSessionConfig) gin.H {
	cfg = cfg.WithDefaults()
	return gin.H{
		"dir":               cfg.Dir,
		"chunk_size_bytes":  cfg.ChunkSizeBytes,
		"max_session_bytes": cfg.MaxSessionBytes,
		"max_active":        cfg.MaxActive,
		"ttl_minutes":       cfg.TTLMinutes,
	}
}

type importSessionHTTPError struct {
	status int
	code   string
	msg    string
}

func (e importSessionHTTPError) Error() string { return e.msg }

func errImportSessionBadRequest(msg string) error {
	return importSessionHTTPError{status: http.StatusBadRequest, code: "usage_import_session_bad_request", msg: msg}
}

func errImportSessionTooLarge(msg string) error {
	return importSessionHTTPError{status: http.StatusRequestEntityTooLarge, code: "usage_import_session_too_large", msg: msg}
}

func errImportSessionTooMany(msg string) error {
	return importSessionHTTPError{status: http.StatusTooManyRequests, code: "usage_import_session_limit_reached", msg: msg}
}

func errImportSessionConflict(msg string) error {
	return importSessionHTTPError{status: http.StatusConflict, code: "usage_import_session_conflict", msg: msg}
}

func errImportSessionNotFound(msg string) error {
	return importSessionHTTPError{status: http.StatusNotFound, code: "usage_import_session_not_found", msg: msg}
}

func errImportSessionRetryable(msg string) error {
	return importSessionHTTPError{status: http.StatusServiceUnavailable, code: "usage_import_session_retryable", msg: msg}
}

func contextCanceledError() error {
	return importSessionHTTPError{status: http.StatusRequestTimeout, code: "usage_import_session_cancelled", msg: "request cancelled"}
}

func writeImportSessionError(c *gin.Context, err error) {
	var httpErr importSessionHTTPError
	if errors.As(err, &httpErr) {
		c.JSON(httpErr.status, gin.H{"error": httpErr.msg, "code": httpErr.code})
		return
	}
	serverError(c, err)
}

func rejectSymlinkPath(path string, label string) error {
	info, err := os.Lstat(path)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("stat %s: %w", label, err)
	}
	if info.Mode()&os.ModeSymlink != 0 {
		return fmt.Errorf("%s must not be a symlink", label)
	}
	return nil
}
