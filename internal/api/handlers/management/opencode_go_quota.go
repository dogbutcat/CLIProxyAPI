package management

import (
	"context"
	"errors"
	"net/http"
	"sort"
	"strings"
	"unicode"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/quota"
)

const statusClientClosedRequest = 499

// OpenCodeGoQuotaRuntime is the narrow runtime surface exposed to Management APIs.
type OpenCodeGoQuotaRuntime interface {
	SnapshotOpenCodeGoQuota() map[string]*quota.PollResult
	RefreshOpenCodeGoQuota(ctx context.Context, entryName string) (*quota.PollResult, bool, error)
}

type openCodeGoQuotaSnapshot struct {
	EntryName string                 `json:"entry-name"`
	Quota     *quota.OpenCodeGoQuota `json:"quota,omitempty"`
	Error     string                 `json:"error,omitempty"`
	Timestamp string                 `json:"timestamp,omitempty"`
}

// GetOpenCodeGoQuota returns redacted quota polling snapshots.
func (h *Handler) GetOpenCodeGoQuota(c *gin.Context) {
	runtime := h.openCodeGoQuotaRuntimeSnapshot()
	if runtime == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "opencode-go quota runtime unavailable"})
		return
	}
	c.JSON(http.StatusOK, gin.H{"quota": openCodeGoQuotaSnapshots(runtime.SnapshotOpenCodeGoQuota())})
}

// PostOpenCodeGoQuotaRefresh performs a manual quota refresh for one entry.
func (h *Handler) PostOpenCodeGoQuotaRefresh(c *gin.Context) {
	entryName := strings.TrimSpace(c.Param("entry"))
	h.refreshOpenCodeGoQuota(c, entryName, true)
}

// GetOpenCodeGoQuotaEntry preserves the legacy live quota lookup endpoint shape.
func (h *Handler) GetOpenCodeGoQuotaEntry(c *gin.Context) {
	entryName := strings.TrimSpace(c.Param("entry"))
	h.refreshOpenCodeGoQuota(c, entryName, false)
}

func (h *Handler) refreshOpenCodeGoQuota(c *gin.Context, entryName string, wrapped bool) {
	if !validOpenCodeGoQuotaEntryName(entryName) {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid entry name"})
		return
	}
	runtime := h.openCodeGoQuotaRuntimeSnapshot()
	if runtime == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "opencode-go quota runtime unavailable"})
		return
	}
	ctx := c.Request.Context()
	if err := ctx.Err(); err != nil {
		writeOpenCodeGoQuotaCanceled(c, err)
		return
	}
	result, runtimeAvailable, errRefresh := runtime.RefreshOpenCodeGoQuota(ctx, entryName)
	if errRefresh != nil {
		if errors.Is(errRefresh, context.Canceled) || errors.Is(errRefresh, context.DeadlineExceeded) {
			writeOpenCodeGoQuotaCanceled(c, errRefresh)
			return
		}
		c.JSON(http.StatusInternalServerError, gin.H{"error": errRefresh.Error()})
		return
	}
	if !runtimeAvailable {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "opencode-go quota runtime unavailable"})
		return
	}
	if result == nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "entry not found"})
		return
	}
	if result.Error != nil && (errors.Is(result.Error, context.Canceled) || errors.Is(result.Error, context.DeadlineExceeded)) {
		writeOpenCodeGoQuotaCanceled(c, result.Error)
		return
	}
	snapshot := openCodeGoQuotaSnapshotFor(result)
	if wrapped {
		c.JSON(http.StatusOK, gin.H{"quota": snapshot})
		return
	}
	c.JSON(http.StatusOK, snapshot)
}

func (h *Handler) openCodeGoQuotaRuntimeSnapshot() OpenCodeGoQuotaRuntime {
	if h == nil {
		return nil
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.openCodeGoQuotaRuntime
}

func openCodeGoQuotaSnapshots(results map[string]*quota.PollResult) []openCodeGoQuotaSnapshot {
	names := make([]string, 0, len(results))
	for name := range results {
		names = append(names, name)
	}
	sort.Strings(names)
	out := make([]openCodeGoQuotaSnapshot, 0, len(names))
	for _, name := range names {
		out = append(out, openCodeGoQuotaSnapshotFor(results[name]))
	}
	return out
}

func openCodeGoQuotaSnapshotFor(result *quota.PollResult) openCodeGoQuotaSnapshot {
	if result == nil {
		return openCodeGoQuotaSnapshot{}
	}
	out := openCodeGoQuotaSnapshot{
		EntryName: result.EntryName,
		Quota:     result.Quota,
	}
	if result.Error != nil {
		out.Error = result.Error.Error()
	}
	if !result.Timestamp.IsZero() {
		out.Timestamp = result.Timestamp.UTC().Format("2006-01-02T15:04:05.000000000Z07:00")
	}
	return out
}

func validOpenCodeGoQuotaEntryName(name string) bool {
	if name == "" || len(name) > 256 {
		return false
	}
	for _, r := range name {
		if unicode.IsControl(r) || r == '/' || r == '\\' {
			return false
		}
	}
	return true
}

func writeOpenCodeGoQuotaCanceled(c *gin.Context, err error) {
	status := statusClientClosedRequest
	if errors.Is(err, context.DeadlineExceeded) {
		status = http.StatusGatewayTimeout
	}
	c.JSON(status, gin.H{"error": err.Error()})
}
