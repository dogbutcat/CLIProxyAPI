package usage

import (
	"database/sql"
	"errors"
	"net/http"
	"strconv"
	"strings"

	"github.com/gin-gonic/gin"
	plusstore "github.com/router-for-me/CLIProxyAPI/v7/internal/usage/plusvendor/store"
)

const accountActionLocalMutationUnavailable = "account auth-file mutation is not available from the integrated usage handler"

func (h *Handlers) ListAccountActionCandidates(c *gin.Context) {
	store, closeStore, ok := h.accountActionStore(c)
	if !ok {
		return
	}
	defer closeStore()
	if collector := h.bridge.Collector(); collector != nil {
		_ = collector.Flush(c.Request.Context())
	}
	if _, err := store.SyncAccountActionCandidates(c.Request.Context(), 50000); err != nil {
		serverError(c, err)
		return
	}
	status := strings.TrimSpace(c.Query("status"))
	if strings.EqualFold(status, "all") {
		status = ""
	}
	limit, _ := strconv.Atoi(c.DefaultQuery("limit", "100"))
	items, err := store.ListAccountActionCandidates(c.Request.Context(), status, limit)
	if err != nil {
		serverError(c, err)
		return
	}
	pendingCount, err := store.CountAccountActionCandidates(c.Request.Context(), plusstore.AccountActionStatusPending)
	if err != nil {
		serverError(c, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{
		"items":         items,
		"pendingCount":  pendingCount,
		"pending_count": pendingCount,
	})
}

func (h *Handlers) IgnoreAccountActionCandidate(c *gin.Context) {
	h.updateAccountActionCandidateStatus(c, plusstore.AccountActionStatusIgnored)
}

func (h *Handlers) ResolveAccountActionCandidate(c *gin.Context) {
	h.updateAccountActionCandidateStatus(c, plusstore.AccountActionStatusResolved)
}

func (h *Handlers) EnableAccountActionCandidate(c *gin.Context) {
	h.recordAccountActionMutationUnavailable(c)
}

func (h *Handlers) DeleteAccountActionCandidateAuthFile(c *gin.Context) {
	h.recordAccountActionMutationUnavailable(c)
}

func (h *Handlers) updateAccountActionCandidateStatus(c *gin.Context, status string) {
	id, ok := accountActionIDParam(c)
	if !ok {
		return
	}
	store, closeStore, ok := h.accountActionStore(c)
	if !ok {
		return
	}
	defer closeStore()
	item, err := store.UpdatePendingAccountActionCandidateStatus(c.Request.Context(), id, status)
	if errors.Is(err, sql.ErrNoRows) {
		if existing, found, getErr := store.GetAccountActionCandidate(c.Request.Context(), id); getErr != nil {
			serverError(c, getErr)
		} else if found {
			c.JSON(http.StatusConflict, gin.H{"error": "account action candidate is not pending", "item": existing})
		} else {
			c.JSON(http.StatusNotFound, gin.H{"error": "account action candidate not found"})
		}
		return
	}
	if err != nil {
		serverError(c, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"item": item})
}

func (h *Handlers) recordAccountActionMutationUnavailable(c *gin.Context) {
	id, ok := accountActionIDParam(c)
	if !ok {
		return
	}
	store, closeStore, ok := h.accountActionStore(c)
	if !ok {
		return
	}
	defer closeStore()
	item, err := store.RecordAccountActionCandidateFailure(c.Request.Context(), id, accountActionLocalMutationUnavailable)
	if errors.Is(err, sql.ErrNoRows) {
		c.JSON(http.StatusNotFound, gin.H{"error": "account action candidate not found"})
		return
	}
	if err != nil {
		serverError(c, err)
		return
	}
	c.JSON(http.StatusConflict, gin.H{
		"error": accountActionLocalMutationUnavailable,
		"item":  item,
	})
}

func (h *Handlers) accountActionStore(c *gin.Context) (*plusstore.Store, func(), bool) {
	if h == nil || h.bridge == nil {
		monitoringUnavailable(c)
		return nil, nil, false
	}
	store, closeStore, err := openUsageStore(strings.TrimSpace(h.bridge.DBPath()))
	if err != nil {
		serverError(c, err)
		return nil, nil, false
	}
	return store, closeStore, true
}

func accountActionIDParam(c *gin.Context) (int64, bool) {
	id, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil || id <= 0 {
		badRequest(c, "id must be a positive integer")
		return 0, false
	}
	return id, true
}
