package usage

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	plusstore "github.com/router-for-me/CLIProxyAPI/v7/internal/usage/plusvendor/store"
)

func TestImportSessionCreateGetAndResume(t *testing.T) {
	gin.SetMode(gin.TestMode)
	root := filepath.Join(t.TempDir(), "sessions")
	router, cleanup := newImportSessionTestRouter(t, config.UsageImportSessionConfig{Dir: root})
	defer cleanup()

	createPayload := `{"filename":"../usage.jsonl","size_bytes":123,"resume_key":"resume-a"}`
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/v0/management/usage/import-sessions", strings.NewReader(createPayload))
	req.Header.Set("Content-Type", "application/json")
	router.ServeHTTP(rec, req)
	if rec.Code != http.StatusCreated {
		t.Fatalf("create status = %d body=%s", rec.Code, rec.Body.String())
	}
	var created UsageImportSession
	if err := json.Unmarshal(rec.Body.Bytes(), &created); err != nil {
		t.Fatalf("decode created session: %v", err)
	}
	if created.ID == "" || created.Filename != "usage.jsonl" || created.Status != "uploading" || created.ChunkSizeBytes != config.DefaultUsageImportSessionChunkSizeBytes {
		t.Fatalf("created session = %#v", created)
	}

	rec = httptest.NewRecorder()
	req = httptest.NewRequest(http.MethodPost, "/v0/management/usage/import-sessions", strings.NewReader(createPayload))
	req.Header.Set("Content-Type", "application/json")
	router.ServeHTTP(rec, req)
	if rec.Code != http.StatusCreated {
		t.Fatalf("resume create status = %d body=%s", rec.Code, rec.Body.String())
	}
	var resumed UsageImportSession
	if err := json.Unmarshal(rec.Body.Bytes(), &resumed); err != nil {
		t.Fatalf("decode resumed session: %v", err)
	}
	if resumed.ID != created.ID {
		t.Fatalf("resume id = %q, want %q", resumed.ID, created.ID)
	}

	rec = httptest.NewRecorder()
	router.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/v0/management/usage/import-sessions/"+created.ID, nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("get status = %d body=%s", rec.Code, rec.Body.String())
	}
	var got UsageImportSession
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode get session: %v", err)
	}
	if got.ID != created.ID || got.Filename != "usage.jsonl" {
		t.Fatalf("got session = %#v, want created session", got)
	}

	restarted, err := newUsageImportSessionManager(root, config.DefaultUsageImportSessionConfig())
	if err != nil {
		t.Fatalf("new restarted manager: %v", err)
	}
	fromDisk, found, err := restarted.Get(created.ID)
	if err != nil || !found {
		t.Fatalf("restart get = found:%v err:%v", found, err)
	}
	if fromDisk.ID != created.ID || fromDisk.ResumeKey != "resume-a" {
		t.Fatalf("restart session = %#v", fromDisk)
	}

	info, err := os.Stat(root)
	if err != nil {
		t.Fatalf("stat root: %v", err)
	}
	if info.Mode().Perm() != 0o700 {
		t.Fatalf("root mode = %o, want 0700", info.Mode().Perm())
	}
	metadataInfo, err := os.Stat(filepath.Join(root, created.ID, "metadata.json"))
	if err != nil {
		t.Fatalf("stat metadata: %v", err)
	}
	if metadataInfo.Mode().Perm() != 0o600 {
		t.Fatalf("metadata mode = %o, want 0600", metadataInfo.Mode().Perm())
	}
}

func TestImportSessionGetNotFoundAndRejectsInvalidCreate(t *testing.T) {
	gin.SetMode(gin.TestMode)
	router, cleanup := newImportSessionTestRouter(t, config.UsageImportSessionConfig{Dir: filepath.Join(t.TempDir(), "sessions")})
	defer cleanup()

	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/v0/management/usage/import-sessions/not-a-valid-id", nil))
	if rec.Code != http.StatusNotFound {
		t.Fatalf("invalid id status = %d body=%s", rec.Code, rec.Body.String())
	}

	rec = httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/v0/management/usage/import-sessions", strings.NewReader(`{"filename":"usage.jsonl","size_bytes":1,"unknown":true}`))
	req.Header.Set("Content-Type", "application/json")
	router.ServeHTTP(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("unknown field status = %d body=%s", rec.Code, rec.Body.String())
	}

	rec = httptest.NewRecorder()
	req = httptest.NewRequest(http.MethodPost, "/v0/management/usage/import-sessions", strings.NewReader(`{"filename":"usage.jsonl","size_bytes":1}{"filename":"two.jsonl","size_bytes":1}`))
	req.Header.Set("Content-Type", "application/json")
	router.ServeHTTP(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("multiple json status = %d body=%s", rec.Code, rec.Body.String())
	}
}

func TestImportSessionCreateEnforcesQuotaAndActiveLimit(t *testing.T) {
	gin.SetMode(gin.TestMode)
	router, cleanup := newImportSessionTestRouter(t, config.UsageImportSessionConfig{
		Dir:             filepath.Join(t.TempDir(), "sessions"),
		ChunkSizeBytes:  1024,
		MaxSessionBytes: 2048,
		MaxActive:       1,
		TTLMinutes:      60,
	})
	defer cleanup()

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/v0/management/usage/import-sessions", strings.NewReader(`{"filename":"too-large.jsonl","size_bytes":2049}`))
	req.Header.Set("Content-Type", "application/json")
	router.ServeHTTP(rec, req)
	if rec.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("oversize status = %d body=%s", rec.Code, rec.Body.String())
	}

	rec = httptest.NewRecorder()
	req = httptest.NewRequest(http.MethodPost, "/v0/management/usage/import-sessions", strings.NewReader(`{"filename":"one.jsonl","size_bytes":1}`))
	req.Header.Set("Content-Type", "application/json")
	router.ServeHTTP(rec, req)
	if rec.Code != http.StatusCreated {
		t.Fatalf("first create status = %d body=%s", rec.Code, rec.Body.String())
	}

	rec = httptest.NewRecorder()
	req = httptest.NewRequest(http.MethodPost, "/v0/management/usage/import-sessions", strings.NewReader(`{"filename":"two.jsonl","size_bytes":1}`))
	req.Header.Set("Content-Type", "application/json")
	router.ServeHTTP(rec, req)
	if rec.Code != http.StatusTooManyRequests {
		t.Fatalf("active limit status = %d body=%s", rec.Code, rec.Body.String())
	}
}

func TestImportSessionPathRejectsSymlinkRoot(t *testing.T) {
	gin.SetMode(gin.TestMode)
	base := t.TempDir()
	target := filepath.Join(base, "target")
	if err := os.Mkdir(target, 0o700); err != nil {
		t.Fatalf("mkdir target: %v", err)
	}
	link := filepath.Join(base, "sessions-link")
	if err := os.Symlink(target, link); err != nil {
		t.Fatalf("symlink: %v", err)
	}
	router, cleanup := newImportSessionTestRouter(t, config.UsageImportSessionConfig{Dir: link})
	defer cleanup()

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/v0/management/usage/import-sessions", strings.NewReader(`{"filename":"usage.jsonl","size_bytes":1}`))
	req.Header.Set("Content-Type", "application/json")
	router.ServeHTTP(rec, req)
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("symlink root status = %d body=%s", rec.Code, rec.Body.String())
	}
}

func TestImportSessionCreateRejectsInvalidConfig(t *testing.T) {
	gin.SetMode(gin.TestMode)
	router, cleanup := newImportSessionTestRouter(t, config.UsageImportSessionConfig{
		Dir:             filepath.Join(t.TempDir(), "sessions"),
		ChunkSizeBytes:  4096,
		MaxSessionBytes: 1024,
		MaxActive:       1,
		TTLMinutes:      60,
	})
	defer cleanup()

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/v0/management/usage/import-sessions", strings.NewReader(`{"filename":"usage.jsonl","size_bytes":1}`))
	req.Header.Set("Content-Type", "application/json")
	router.ServeHTTP(rec, req)
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("invalid config status = %d body=%s", rec.Code, rec.Body.String())
	}
	var body map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode invalid config body: %v", err)
	}
	if body["code"] != "usage_import_session_config_invalid" {
		t.Fatalf("invalid config body = %#v", body)
	}
}

func TestImportSessionChunkCompleteCancelFlow(t *testing.T) {
	gin.SetMode(gin.TestMode)
	root := filepath.Join(t.TempDir(), "sessions")
	router, cleanup := newImportSessionTestRouter(t, config.UsageImportSessionConfig{Dir: root})
	defer cleanup()

	body := `{"model":"gpt-5","input_tokens":1,"output_tokens":2,"total_tokens":3}` + "\n"
	session := createImportSessionForTest(t, router, "usage.jsonl", int64(len(body)))

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPut, "/v0/management/usage/import-sessions/"+session.ID+"/chunk?offset=0", strings.NewReader(body[:20]))
	router.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("first chunk status = %d body=%s", rec.Code, rec.Body.String())
	}

	rec = httptest.NewRecorder()
	req = httptest.NewRequest(http.MethodPut, "/v0/management/usage/import-sessions/"+session.ID+"/chunk?offset=0", strings.NewReader("again"))
	router.ServeHTTP(rec, req)
	if rec.Code != http.StatusConflict {
		t.Fatalf("offset mismatch status = %d body=%s", rec.Code, rec.Body.String())
	}

	rec = httptest.NewRecorder()
	req = httptest.NewRequest(http.MethodPut, "/v0/management/usage/import-sessions/"+session.ID+"/chunk?offset=20", strings.NewReader(body[20:]))
	router.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("second chunk status = %d body=%s", rec.Code, rec.Body.String())
	}
	var uploaded UsageImportSession
	if err := json.Unmarshal(rec.Body.Bytes(), &uploaded); err != nil {
		t.Fatalf("decode uploaded session: %v", err)
	}
	if uploaded.Status != "ready" || uploaded.ReceivedBytes != int64(len(body)) {
		t.Fatalf("uploaded session = %#v", uploaded)
	}

	rec = httptest.NewRecorder()
	req = httptest.NewRequest(http.MethodPost, "/v0/management/usage/import-sessions/"+session.ID+"/complete", nil)
	router.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("complete status = %d body=%s", rec.Code, rec.Body.String())
	}
	var completed UsageImportSession
	if err := json.Unmarshal(rec.Body.Bytes(), &completed); err != nil {
		t.Fatalf("decode completed session: %v", err)
	}
	if completed.Status != "completed" || completed.Result["added"].(float64) != 1 {
		t.Fatalf("completed session = %#v", completed)
	}

	rec = httptest.NewRecorder()
	req = httptest.NewRequest(http.MethodDelete, "/v0/management/usage/import-sessions/"+session.ID, nil)
	router.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("cancel completed status = %d body=%s", rec.Code, rec.Body.String())
	}
}

func TestImportSessionChunkQuotaRollback(t *testing.T) {
	gin.SetMode(gin.TestMode)
	root := filepath.Join(t.TempDir(), "sessions")
	router, cleanup := newImportSessionTestRouter(t, config.UsageImportSessionConfig{Dir: root})
	defer cleanup()

	session := createImportSessionForTest(t, router, "usage.jsonl", 5)
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPut, "/v0/management/usage/import-sessions/"+session.ID+"/chunk?offset=0", strings.NewReader("123456"))
	router.ServeHTTP(rec, req)
	if rec.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("oversize chunk status = %d body=%s", rec.Code, rec.Body.String())
	}

	rec = httptest.NewRecorder()
	router.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/v0/management/usage/import-sessions/"+session.ID, nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("get after rollback status = %d body=%s", rec.Code, rec.Body.String())
	}
	var got UsageImportSession
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode rollback session: %v", err)
	}
	if got.ReceivedBytes != 0 || got.Status != "uploading" {
		t.Fatalf("rollback session = %#v", got)
	}
	partInfo, err := os.Stat(filepath.Join(root, session.ID, "upload.part"))
	if err != nil {
		t.Fatalf("stat rollback part: %v", err)
	}
	if partInfo.Size() != 0 {
		t.Fatalf("rollback part size = %d, want 0", partInfo.Size())
	}
}

func TestImportSessionCompleteRetryableAndNonRetryablePartDisposition(t *testing.T) {
	root := filepath.Join(t.TempDir(), "sessions")
	manager, err := newUsageImportSessionManager(root, config.DefaultUsageImportSessionConfig())
	if err != nil {
		t.Fatalf("new manager: %v", err)
	}

	retryable, err := manager.Create(nil, createUsageImportSessionRequest{Filename: "retry.jsonl", SizeBytes: 1})
	if err != nil {
		t.Fatalf("create retryable session: %v", err)
	}
	if _, err := manager.AppendChunk(context.Background(), retryable.ID, 0, strings.NewReader("x")); err != nil {
		t.Fatalf("append retryable chunk: %v", err)
	}
	retryable, err = manager.Complete(context.Background(), retryable.ID, func(context.Context, io.Reader) (plusstore.UsageImportResult, error) {
		return plusstore.UsageImportResult{Format: "jsonl"}, context.Canceled
	})
	if err != nil {
		t.Fatalf("complete retryable: %v", err)
	}
	if retryable.Status != "failed" || !retryable.Retryable {
		t.Fatalf("retryable session = %#v", retryable)
	}
	if _, err := os.Stat(filepath.Join(root, retryable.ID, "upload.part")); err != nil {
		t.Fatalf("retryable part should remain: %v", err)
	}

	nonRetryable, err := manager.Create(nil, createUsageImportSessionRequest{Filename: "bad.jsonl", SizeBytes: 1})
	if err != nil {
		t.Fatalf("create non-retryable session: %v", err)
	}
	if _, err := manager.AppendChunk(context.Background(), nonRetryable.ID, 0, strings.NewReader("x")); err != nil {
		t.Fatalf("append non-retryable chunk: %v", err)
	}
	nonRetryable, err = manager.Complete(context.Background(), nonRetryable.ID, func(context.Context, io.Reader) (plusstore.UsageImportResult, error) {
		return plusstore.UsageImportResult{Format: "jsonl"}, errors.New("invalid import stream")
	})
	if err != nil {
		t.Fatalf("complete non-retryable: %v", err)
	}
	if nonRetryable.Status != "failed" || nonRetryable.Retryable {
		t.Fatalf("non-retryable session = %#v", nonRetryable)
	}
	if _, err := os.Stat(filepath.Join(root, nonRetryable.ID, "upload.part")); !os.IsNotExist(err) {
		t.Fatalf("non-retryable part err = %v, want not exists", err)
	}
}

func TestImportSessionCancelRace(t *testing.T) {
	gin.SetMode(gin.TestMode)
	root := filepath.Join(t.TempDir(), "sessions")
	router, cleanup := newImportSessionTestRouter(t, config.UsageImportSessionConfig{Dir: root})
	defer cleanup()

	body := strings.Repeat("x", 4096)
	session := createImportSessionForTest(t, router, "race.jsonl", int64(len(body)))
	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		rec := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodPut, "/v0/management/usage/import-sessions/"+session.ID+"/chunk?offset=0", strings.NewReader(body))
		router.ServeHTTP(rec, req)
		if rec.Code != http.StatusOK && rec.Code != http.StatusConflict && rec.Code != http.StatusServiceUnavailable {
			t.Errorf("race chunk status = %d body=%s", rec.Code, rec.Body.String())
		}
	}()
	go func() {
		defer wg.Done()
		rec := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodDelete, "/v0/management/usage/import-sessions/"+session.ID, nil)
		router.ServeHTTP(rec, req)
		if rec.Code != http.StatusOK && rec.Code != http.StatusConflict {
			t.Errorf("race cancel status = %d body=%s", rec.Code, rec.Body.String())
		}
	}()
	wg.Wait()

	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/v0/management/usage/import-sessions/"+session.ID, nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("get race session status = %d body=%s", rec.Code, rec.Body.String())
	}
	var got UsageImportSession
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode race session: %v", err)
	}
	if got.Status != "cancelled" && got.Status != "ready" {
		t.Fatalf("race final session = %#v", got)
	}
}

func TestImportSessionCompleteCancelRace(t *testing.T) {
	root := filepath.Join(t.TempDir(), "sessions")
	manager, err := newUsageImportSessionManager(root, config.DefaultUsageImportSessionConfig())
	if err != nil {
		t.Fatalf("new manager: %v", err)
	}
	session, err := manager.Create(nil, createUsageImportSessionRequest{Filename: "complete-race.jsonl", SizeBytes: 1})
	if err != nil {
		t.Fatalf("create session: %v", err)
	}
	if _, err := manager.AppendChunk(context.Background(), session.ID, 0, strings.NewReader("x")); err != nil {
		t.Fatalf("append chunk: %v", err)
	}

	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		_, err := manager.Complete(context.Background(), session.ID, func(context.Context, io.Reader) (plusstore.UsageImportResult, error) {
			return plusstore.UsageImportResult{Format: "jsonl", Added: 1, Total: 1}, nil
		})
		if err != nil {
			var httpErr importSessionHTTPError
			if !errors.As(err, &httpErr) || httpErr.status != http.StatusConflict {
				t.Errorf("complete race err = %v", err)
			}
		}
	}()
	go func() {
		defer wg.Done()
		if _, err := manager.Cancel(session.ID); err != nil {
			t.Errorf("cancel race err = %v", err)
		}
	}()
	wg.Wait()

	got, found, err := manager.Get(session.ID)
	if err != nil || !found {
		t.Fatalf("get race session found=%v err=%v", found, err)
	}
	if got.Status != "completed" && got.Status != "cancelled" {
		t.Fatalf("complete/cancel race final session = %#v", got)
	}
}

func TestImportSessionChunkDisconnectRollback(t *testing.T) {
	root := filepath.Join(t.TempDir(), "sessions")
	manager, err := newUsageImportSessionManager(root, config.DefaultUsageImportSessionConfig())
	if err != nil {
		t.Fatalf("new manager: %v", err)
	}
	session, err := manager.Create(nil, createUsageImportSessionRequest{Filename: "disconnect.jsonl", SizeBytes: 5})
	if err != nil {
		t.Fatalf("create session: %v", err)
	}

	_, err = manager.AppendChunk(context.Background(), session.ID, 0, failingImportChunkReader{})
	if err == nil {
		t.Fatal("append disconnect error = nil")
	}
	got, found, err := manager.Get(session.ID)
	if err != nil || !found {
		t.Fatalf("get after disconnect found=%v err=%v", found, err)
	}
	if got.ReceivedBytes != 0 || got.Status != "uploading" {
		t.Fatalf("disconnect rollback session = %#v", got)
	}
	partInfo, err := os.Stat(filepath.Join(root, session.ID, "upload.part"))
	if err != nil {
		t.Fatalf("stat disconnect part: %v", err)
	}
	if partInfo.Size() != 0 {
		t.Fatalf("disconnect part size = %d, want 0", partInfo.Size())
	}
}

func TestImportSessionSymlinkRootChunkRejected(t *testing.T) {
	gin.SetMode(gin.TestMode)
	base := t.TempDir()
	target := filepath.Join(base, "target")
	if err := os.Mkdir(target, 0o700); err != nil {
		t.Fatalf("mkdir target: %v", err)
	}
	link := filepath.Join(base, "sessions-link")
	if err := os.Symlink(target, link); err != nil {
		t.Fatalf("symlink: %v", err)
	}
	router, cleanup := newImportSessionTestRouter(t, config.UsageImportSessionConfig{Dir: link})
	defer cleanup()

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPut, "/v0/management/usage/import-sessions/00000000000000000000000000000000/chunk?offset=0", strings.NewReader("x"))
	router.ServeHTTP(rec, req)
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("symlink chunk status = %d body=%s", rec.Code, rec.Body.String())
	}
}

func TestImportSessionSymlinkPartChunkRejected(t *testing.T) {
	root := filepath.Join(t.TempDir(), "sessions")
	manager, err := newUsageImportSessionManager(root, config.DefaultUsageImportSessionConfig())
	if err != nil {
		t.Fatalf("new manager: %v", err)
	}
	session, err := manager.Create(nil, createUsageImportSessionRequest{Filename: "symlink.jsonl", SizeBytes: 1})
	if err != nil {
		t.Fatalf("create session: %v", err)
	}
	target := filepath.Join(t.TempDir(), "target")
	if err := os.WriteFile(target, []byte("safe"), 0o600); err != nil {
		t.Fatalf("write target: %v", err)
	}
	if err := os.Symlink(target, filepath.Join(root, session.ID, "upload.part")); err != nil {
		t.Fatalf("symlink part: %v", err)
	}
	if _, err := manager.AppendChunk(context.Background(), session.ID, 0, strings.NewReader("x")); err == nil {
		t.Fatal("append symlink part error = nil")
	}
	data, err := os.ReadFile(target)
	if err != nil {
		t.Fatalf("read target: %v", err)
	}
	if string(data) != "safe" {
		t.Fatalf("symlink target mutated: %q", string(data))
	}
}

func TestImportSessionRecoveryCleanupRestart(t *testing.T) {
	root := filepath.Join(t.TempDir(), "sessions")
	manager, err := newUsageImportSessionManager(root, config.DefaultUsageImportSessionConfig())
	if err != nil {
		t.Fatalf("new manager: %v", err)
	}

	retryable, err := manager.Create(nil, createUsageImportSessionRequest{Filename: "retry.jsonl", SizeBytes: 1})
	if err != nil {
		t.Fatalf("create retryable: %v", err)
	}
	if _, err := manager.AppendChunk(context.Background(), retryable.ID, 0, strings.NewReader("x")); err != nil {
		t.Fatalf("append retryable: %v", err)
	}
	retryable.Status = "processing"
	retryable.ReceivedBytes = 0
	if err := manager.writeSession(retryable); err != nil {
		t.Fatalf("write interrupted retryable: %v", err)
	}

	cleaned, err := manager.Create(nil, createUsageImportSessionRequest{Filename: "done.jsonl", SizeBytes: 1})
	if err != nil {
		t.Fatalf("create cleaned: %v", err)
	}
	cleaned.Status = "completed"
	cleaned.ExpiresAtMS = time.Now().Add(-time.Minute).UnixMilli()
	if err := manager.writeSession(cleaned); err != nil {
		t.Fatalf("write cleaned: %v", err)
	}

	restarted, err := newUsageImportSessionManager(root, config.DefaultUsageImportSessionConfig())
	if err != nil {
		t.Fatalf("new restarted manager: %v", err)
	}
	got, found, err := restarted.Get(retryable.ID)
	if err != nil || !found {
		t.Fatalf("get recovered retryable found=%v err=%v", found, err)
	}
	if got.Status != "failed" || !got.Retryable || got.ReceivedBytes != 1 {
		t.Fatalf("recovered session = %#v", got)
	}
	if _, err := os.Stat(filepath.Join(root, retryable.ID, "upload.part")); err != nil {
		t.Fatalf("retryable part should remain: %v", err)
	}
	if _, err := os.Stat(filepath.Join(root, cleaned.ID)); !os.IsNotExist(err) {
		t.Fatalf("expired completed dir err=%v, want not exists", err)
	}
}

func TestImportSessionRecoveryReconcilesPartSizeAndCancelRequested(t *testing.T) {
	root := filepath.Join(t.TempDir(), "sessions")
	cfg := config.UsageImportSessionConfig{
		Dir:             root,
		ChunkSizeBytes:  16,
		MaxSessionBytes: 64,
		MaxActive:       8,
		TTLMinutes:      60,
	}
	manager, err := newUsageImportSessionManager(root, cfg)
	if err != nil {
		t.Fatalf("new manager: %v", err)
	}
	now := time.Now().UnixMilli()
	uploading := UsageImportSession{
		ID:             "00000000000000000000000000000001",
		Filename:       "uploading.jsonl",
		Status:         "uploading",
		SizeBytes:      5,
		ReceivedBytes:  2,
		ChunkSizeBytes: cfg.ChunkSizeBytes,
		CreatedAtMS:    now,
		UpdatedAtMS:    now,
		ExpiresAtMS:    now + int64(time.Hour/time.Millisecond),
	}
	cancelRequested := uploading
	cancelRequested.ID = "00000000000000000000000000000002"
	cancelRequested.Filename = "cancel.jsonl"
	cancelRequested.Status = "cancel_requested"
	cancelRequested.SizeBytes = 4
	cancelRequested.ReceivedBytes = 4
	for _, session := range []UsageImportSession{uploading, cancelRequested} {
		if err := manager.writeSession(session); err != nil {
			t.Fatalf("write session %s: %v", session.ID, err)
		}
	}
	if err := os.WriteFile(filepath.Join(root, uploading.ID, "upload.part"), []byte("1234567"), 0o600); err != nil {
		t.Fatalf("write uploading part: %v", err)
	}
	if err := os.WriteFile(filepath.Join(root, cancelRequested.ID, "upload.part"), []byte("abcd"), 0o600); err != nil {
		t.Fatalf("write cancel part: %v", err)
	}
	recovered, err := newUsageImportSessionManager(root, cfg)
	if err != nil {
		t.Fatalf("recover manager: %v", err)
	}
	gotUploading, found, err := recovered.Get(uploading.ID)
	if err != nil || !found {
		t.Fatalf("get uploading found=%v err=%v", found, err)
	}
	if gotUploading.Status != "ready" || gotUploading.ReceivedBytes != gotUploading.SizeBytes {
		t.Fatalf("uploading recovery = %#v", gotUploading)
	}
	if info, err := os.Stat(filepath.Join(root, uploading.ID, "upload.part")); err != nil || info.Size() != gotUploading.SizeBytes {
		t.Fatalf("uploading part info=%v err=%v", info, err)
	}
	gotCancel, found, err := recovered.Get(cancelRequested.ID)
	if err != nil || !found {
		t.Fatalf("get cancel found=%v err=%v", found, err)
	}
	if gotCancel.Status != "cancelled" || gotCancel.Retryable {
		t.Fatalf("cancel recovery = %#v", gotCancel)
	}
	if _, err := os.Stat(filepath.Join(root, cancelRequested.ID, "upload.part")); !os.IsNotExist(err) {
		t.Fatalf("cancel part err = %v, want not exists", err)
	}
}

func TestImportSessionCompleteDisconnectUsesBridgeRootContext(t *testing.T) {
	gin.SetMode(gin.TestMode)
	root := filepath.Join(t.TempDir(), "sessions")
	bridge, err := NewBridge(BridgeConfig{DBPath: filepath.Join(t.TempDir(), "usage.db")})
	if err != nil {
		t.Fatalf("new bridge: %v", err)
	}
	bridge.Start(context.Background())
	defer func() { _ = bridge.Close(context.Background()) }()
	router := gin.New()
	h := NewHandlers(bridge, WithImportSessionConfig(config.UsageImportSessionConfig{Dir: root}))
	router.POST("/v0/management/usage/import-sessions", h.CreateUsageImportSession)
	router.PUT("/v0/management/usage/import-sessions/:id/chunk", h.UploadUsageImportSessionChunk)
	router.POST("/v0/management/usage/import-sessions/:id/complete", h.CompleteUsageImportSession)

	body := `{"model":"gpt-5","input_tokens":1,"output_tokens":2,"total_tokens":3}` + "\n"
	session := createImportSessionForTest(t, router, "disconnect-complete.jsonl", int64(len(body)))
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPut, "/v0/management/usage/import-sessions/"+session.ID+"/chunk?offset=0", strings.NewReader(body))
	router.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("chunk status = %d body=%s", rec.Code, rec.Body.String())
	}

	requestCtx, cancel := context.WithCancel(context.Background())
	cancel()
	rec = httptest.NewRecorder()
	req = httptest.NewRequest(http.MethodPost, "/v0/management/usage/import-sessions/"+session.ID+"/complete", nil).WithContext(requestCtx)
	router.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("complete with disconnected request status = %d body=%s", rec.Code, rec.Body.String())
	}
	var completed UsageImportSession
	if err := json.Unmarshal(rec.Body.Bytes(), &completed); err != nil {
		t.Fatalf("decode completed: %v", err)
	}
	if completed.Status != "completed" {
		t.Fatalf("completed session = %#v", completed)
	}
}

func TestImportSessionLegacyPOSTEquivalence(t *testing.T) {
	gin.SetMode(gin.TestMode)
	body := `{"request_id":"legacy-equivalence","model":"gpt-5","input_tokens":1,"output_tokens":2,"total_tokens":3}` + "\n"
	legacy := importUsageViaLegacyPOSTForTest(t, body)
	session := importUsageViaSessionForTest(t, body)
	if legacy.Format != session.Format || legacy.Total != session.Total || legacy.Failed != session.Failed || legacy.Unsupported != session.Unsupported || legacy.Added != session.Added {
		t.Fatalf("legacy result = %#v, session result = %#v", legacy, session)
	}
}

func TestImportSessionCleanupLoopStopsWithBridge(t *testing.T) {
	bridge, err := NewBridge(BridgeConfig{
		DBPath:        filepath.Join(t.TempDir(), "usage.db"),
		ImportSession: config.UsageImportSessionConfig{Dir: filepath.Join(t.TempDir(), "sessions")},
	})
	if err != nil {
		t.Fatalf("new bridge: %v", err)
	}
	bridge.Start(context.Background())
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := bridge.Close(ctx); err != nil {
		t.Fatalf("bridge close: %v", err)
	}
}

func importUsageViaLegacyPOSTForTest(t *testing.T, body string) plusstore.UsageImportResult {
	t.Helper()
	bridge, err := NewBridge(BridgeConfig{DBPath: filepath.Join(t.TempDir(), "legacy.db")})
	if err != nil {
		t.Fatalf("new legacy bridge: %v", err)
	}
	defer func() { _ = bridge.Close(context.Background()) }()
	router := gin.New()
	router.POST("/v0/management/usage/import", NewHandlers(bridge).ImportUsage)
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/v0/management/usage/import", strings.NewReader(body))
	router.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("legacy import status = %d body=%s", rec.Code, rec.Body.String())
	}
	var result plusstore.UsageImportResult
	if err := json.Unmarshal(rec.Body.Bytes(), &result); err != nil {
		t.Fatalf("decode legacy result: %v", err)
	}
	return result
}

func importUsageViaSessionForTest(t *testing.T, body string) plusstore.UsageImportResult {
	t.Helper()
	router, cleanup := newImportSessionTestRouter(t, config.UsageImportSessionConfig{Dir: filepath.Join(t.TempDir(), "sessions")})
	defer cleanup()
	session := createImportSessionForTest(t, router, "session.jsonl", int64(len(body)))
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPut, "/v0/management/usage/import-sessions/"+session.ID+"/chunk?offset=0", strings.NewReader(body))
	router.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("session upload status = %d body=%s", rec.Code, rec.Body.String())
	}
	rec = httptest.NewRecorder()
	req = httptest.NewRequest(http.MethodPost, "/v0/management/usage/import-sessions/"+session.ID+"/complete", nil)
	router.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("session complete status = %d body=%s", rec.Code, rec.Body.String())
	}
	var completed UsageImportSession
	if err := json.Unmarshal(rec.Body.Bytes(), &completed); err != nil {
		t.Fatalf("decode completed session: %v", err)
	}
	data, err := json.Marshal(completed.Result)
	if err != nil {
		t.Fatalf("marshal completed result: %v", err)
	}
	var result plusstore.UsageImportResult
	if err := json.Unmarshal(data, &result); err != nil {
		t.Fatalf("decode completed result: %v", err)
	}
	return result
}

type failingImportChunkReader struct {
	done bool
}

func (r failingImportChunkReader) Read(p []byte) (int, error) {
	if r.done {
		return 0, io.EOF
	}
	copy(p, "abc")
	return 3, errors.New("client disconnected")
}

func newImportSessionTestRouter(t *testing.T, cfg config.UsageImportSessionConfig) (*gin.Engine, func()) {
	t.Helper()
	bridge, err := NewBridge(BridgeConfig{DBPath: filepath.Join(t.TempDir(), "usage.db")})
	if err != nil {
		t.Fatalf("new bridge: %v", err)
	}
	router := gin.New()
	h := NewHandlers(bridge, WithImportSessionConfig(cfg))
	router.POST("/v0/management/usage/import-sessions", h.CreateUsageImportSession)
	router.GET("/v0/management/usage/import-sessions/:id", h.GetUsageImportSession)
	router.PUT("/v0/management/usage/import-sessions/:id/chunk", h.UploadUsageImportSessionChunk)
	router.POST("/v0/management/usage/import-sessions/:id/complete", h.CompleteUsageImportSession)
	router.DELETE("/v0/management/usage/import-sessions/:id", h.CancelUsageImportSession)
	return router, func() { _ = bridge.Close(context.Background()) }
}

func createImportSessionForTest(t *testing.T, router *gin.Engine, filename string, sizeBytes int64) UsageImportSession {
	t.Helper()
	rec := httptest.NewRecorder()
	payload := `{"filename":"` + filename + `","size_bytes":` + strconv.FormatInt(sizeBytes, 10) + `}`
	req := httptest.NewRequest(http.MethodPost, "/v0/management/usage/import-sessions", strings.NewReader(payload))
	req.Header.Set("Content-Type", "application/json")
	router.ServeHTTP(rec, req)
	if rec.Code != http.StatusCreated {
		t.Fatalf("create session status = %d body=%s", rec.Code, rec.Body.String())
	}
	var session UsageImportSession
	if err := json.Unmarshal(rec.Body.Bytes(), &session); err != nil {
		t.Fatalf("decode session: %v", err)
	}
	return session
}
