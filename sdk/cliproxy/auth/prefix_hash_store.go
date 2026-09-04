package auth

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	log "github.com/sirupsen/logrus"

	"github.com/router-for-me/CLIProxyAPI/v7/sdk/oagmsg"
)

const (
	defaultPrefixHashTailK      = 8
	defaultPrefixHashMaxEntries = 4096
	defaultPrefixHashTTL        = 24 * time.Hour
	prefixHashFileName          = "prefix_cache.json"
)

// PrefixFingerprint contains the lookup and post-success hashes for a request.
type PrefixFingerprint = oagmsg.PrefixFingerprint

type prefixHashRecord struct {
	CacheID   string    `json:"cache_id"`
	Model     string    `json:"model"`
	AuthID    string    `json:"auth_id"`
	CreatedAt time.Time `json:"created_at"`
}

type prefixHashDiskState struct {
	Version int                `json:"version"`
	Records []prefixHashRecord `json:"records"`
}

// PrefixHashStore persists bounded cache prefix to auth mappings under auth-dir/data.
type PrefixHashStore struct {
	mu      sync.Mutex
	path    string
	ttl     time.Duration
	max     int
	records []prefixHashRecord
	closed  bool
	tailK   int
	now     func() time.Time
}

// PrefixHashStoreOptions configures a PrefixHashStore.
type PrefixHashStoreOptions struct {
	TailK int
	TTL   time.Duration
	Max   int
}

// NewPrefixHashStore opens a persistent prefix store under authDir/data.
func NewPrefixHashStore(authDir string, tailK int, ttl time.Duration) (*PrefixHashStore, error) {
	return NewPrefixHashStoreWithOptions(authDir, PrefixHashStoreOptions{
		TailK: tailK,
		TTL:   ttl,
	})
}

// NewPrefixHashStoreWithOptions opens a persistent prefix store under authDir/data.
func NewPrefixHashStoreWithOptions(authDir string, opts PrefixHashStoreOptions) (*PrefixHashStore, error) {
	authDir = strings.TrimSpace(authDir)
	if authDir == "" {
		return nil, fmt.Errorf("prefix hash store: auth dir is required")
	}
	if opts.TTL <= 0 {
		opts.TTL = defaultPrefixHashTTL
	}
	if opts.Max <= 0 {
		opts.Max = defaultPrefixHashMaxEntries
	}
	if opts.TailK <= 0 {
		opts.TailK = defaultPrefixHashTailK
	}
	path := filepath.Join(authDir, "data", prefixHashFileName)
	if errMkdir := os.MkdirAll(filepath.Dir(path), 0o755); errMkdir != nil {
		return nil, fmt.Errorf("prefix hash store: create data dir: %w", errMkdir)
	}
	store := &PrefixHashStore{
		path:  path,
		ttl:   opts.TTL,
		max:   opts.Max,
		tailK: opts.TailK,
		now:   time.Now,
	}
	if errLoad := store.load(); errLoad != nil {
		return nil, errLoad
	}
	return store, nil
}

// Close marks the store closed. Future lookups and appends become no-ops.
func (s *PrefixHashStore) Close() error {
	if s == nil {
		return nil
	}
	s.mu.Lock()
	s.closed = true
	s.mu.Unlock()
	return nil
}

func (s *PrefixHashStore) load() error {
	data, errRead := os.ReadFile(s.path)
	if errRead != nil {
		if os.IsNotExist(errRead) {
			return nil
		}
		return fmt.Errorf("prefix hash store: read: %w", errRead)
	}
	if len(strings.TrimSpace(string(data))) == 0 {
		return nil
	}
	var state prefixHashDiskState
	if errJSON := json.Unmarshal(data, &state); errJSON != nil {
		recoverPath := s.path + ".corrupt-" + time.Now().UTC().Format("20060102T150405.000000000")
		if errRename := os.Rename(s.path, recoverPath); errRename != nil {
			log.Warnf("prefix hash store: failed to move corrupt store aside: %v", errRename)
		} else {
			log.Warnf("prefix hash store: moved corrupt store to %s", recoverPath)
		}
		s.records = nil
		return nil
	}
	s.records = s.prunedLocked(state.Records, s.now())
	return s.persistLocked()
}

// Lookup returns the newest unexpired auth mapped for cacheID and model.
func (s *PrefixHashStore) Lookup(cacheID, model string) string {
	if s == nil {
		return ""
	}
	cacheID = strings.TrimSpace(cacheID)
	model = strings.TrimSpace(model)
	if cacheID == "" {
		return ""
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return ""
	}
	now := s.now()
	s.records = s.prunedLocked(s.records, now)
	for i := len(s.records) - 1; i >= 0; i-- {
		record := s.records[i]
		if record.CacheID == cacheID && record.Model == model {
			return record.AuthID
		}
	}
	return ""
}

// Append stores a new cacheID to auth mapping. It is append-only for lookup ordering.
func (s *PrefixHashStore) Append(cacheID, model, authID string) {
	if s == nil {
		return
	}
	cacheID = strings.TrimSpace(cacheID)
	model = strings.TrimSpace(model)
	authID = strings.TrimSpace(authID)
	if cacheID == "" || authID == "" {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return
	}
	now := s.now()
	s.records = append(s.prunedLocked(s.records, now), prefixHashRecord{
		CacheID:   cacheID,
		Model:     model,
		AuthID:    authID,
		CreatedAt: now.UTC(),
	})
	if len(s.records) > s.max {
		s.records = append([]prefixHashRecord(nil), s.records[len(s.records)-s.max:]...)
	}
	if errPersist := s.persistLocked(); errPersist != nil {
		log.Warnf("prefix hash store: persist failed: %v", errPersist)
	}
}

// Cleanup removes expired entries and returns the number removed.
func (s *PrefixHashStore) Cleanup() int {
	if s == nil {
		return 0
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return 0
	}
	before := len(s.records)
	s.records = s.prunedLocked(s.records, s.now())
	removed := before - len(s.records)
	if removed > 0 {
		if errPersist := s.persistLocked(); errPersist != nil {
			log.Warnf("prefix hash store: persist after cleanup failed: %v", errPersist)
		}
	}
	return removed
}

func (s *PrefixHashStore) Fingerprint(format oagmsg.Format, payload []byte) PrefixFingerprint {
	if s == nil || len(payload) == 0 {
		return PrefixFingerprint{}
	}
	return oagmsg.ComputePrefixFingerprint(format, payload, s.tailK)
}

func (s *PrefixHashStore) prunedLocked(records []prefixHashRecord, now time.Time) []prefixHashRecord {
	if len(records) == 0 {
		return nil
	}
	cutoff := now.Add(-s.ttl)
	out := make([]prefixHashRecord, 0, len(records))
	for _, record := range records {
		if record.CacheID == "" || record.AuthID == "" {
			continue
		}
		if !record.CreatedAt.IsZero() && record.CreatedAt.Before(cutoff) {
			continue
		}
		out = append(out, record)
	}
	if len(out) > s.max {
		out = out[len(out)-s.max:]
	}
	return out
}

func (s *PrefixHashStore) persistLocked() error {
	state := prefixHashDiskState{Version: 1, Records: s.records}
	data, errJSON := json.MarshalIndent(state, "", "  ")
	if errJSON != nil {
		return errJSON
	}
	data = append(data, '\n')
	tmp := s.path + ".tmp"
	if errWrite := os.WriteFile(tmp, data, 0o600); errWrite != nil {
		return errWrite
	}
	return os.Rename(tmp, s.path)
}
