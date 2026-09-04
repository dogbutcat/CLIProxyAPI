package usage

import (
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	plusvendor "github.com/router-for-me/CLIProxyAPI/v7/internal/usage/plusvendor"
	plusstore "github.com/router-for-me/CLIProxyAPI/v7/internal/usage/plusvendor/store"
)

type modelPricesRequest struct {
	Prices map[string]plusstore.ModelPrice `json:"prices"`
}

type modelPricesSyncRequest struct {
	Models []string `json:"models"`
}

type apiKeyAliasesRequest struct {
	Items                   []plusstore.APIKeyAlias `json:"items"`
	ActiveAPIKeyHashes      []string                `json:"activeApiKeyHashes"`
	AllowOrphanAliasCleanup bool                    `json:"allowOrphanAliasCleanup"`
}

func (h *Handlers) GetModelPrices(c *gin.Context) {
	svc, closeStore, ok := h.auxiliaryService(c)
	if !ok {
		return
	}
	defer closeStore()
	prices, err := svc.ModelPrices(c.Request.Context())
	if err != nil {
		serverError(c, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"prices": prices})
}

func (h *Handlers) PutModelPrices(c *gin.Context) {
	var req modelPricesRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		badRequest(c, "invalid model prices payload")
		return
	}
	if req.Prices == nil {
		req.Prices = map[string]plusstore.ModelPrice{}
	}
	svc, closeStore, ok := h.auxiliaryService(c)
	if !ok {
		return
	}
	defer closeStore()
	prices, err := svc.SaveModelPrices(c.Request.Context(), req.Prices)
	if err != nil {
		if isUsageValidationError(err) {
			badRequest(c, err.Error())
			return
		}
		serverError(c, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"prices": prices})
}

func (h *Handlers) DeleteModelPrice(c *gin.Context) {
	model := c.Param("model")
	svc, closeStore, ok := h.auxiliaryService(c)
	if !ok {
		return
	}
	defer closeStore()
	prices, err := svc.DeleteModelPrice(c.Request.Context(), model)
	if err != nil {
		if isUsageValidationError(err) {
			badRequest(c, err.Error())
			return
		}
		serverError(c, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"prices": prices})
}

func (h *Handlers) GetModelPriceUsageSummary(c *gin.Context) {
	limit, ok := optionalBoundedInt(c, "limit", 0, plusvendor.MaxUsageQueryLimit)
	if !ok {
		return
	}
	svc, closeStore, ok := h.auxiliaryService(c)
	if !ok {
		return
	}
	defer closeStore()
	summary, err := svc.ModelPriceUsageSummary(c.Request.Context(), limit)
	if err != nil {
		serverError(c, err)
		return
	}
	c.JSON(http.StatusOK, summary)
}

func (h *Handlers) PostModelPricesSync(c *gin.Context) {
	var req modelPricesSyncRequest
	if c.Request.Body != nil && c.Request.ContentLength != 0 {
		if err := c.ShouldBindJSON(&req); err != nil {
			badRequest(c, "invalid model price sync payload")
			return
		}
	}
	svc, closeStore, ok := h.auxiliaryService(c)
	if !ok {
		return
	}
	defer closeStore()
	result, err := svc.SyncModelPrices(c.Request.Context(), req.Models)
	if err != nil {
		serverError(c, err)
		return
	}
	c.JSON(http.StatusOK, result)
}

func (h *Handlers) GetAPIKeyAliases(c *gin.Context) {
	svc, closeStore, ok := h.auxiliaryService(c)
	if !ok {
		return
	}
	defer closeStore()
	items, err := svc.APIKeyAliases(c.Request.Context())
	if err != nil {
		serverError(c, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"items": items})
}

func (h *Handlers) PutAPIKeyAliases(c *gin.Context) {
	var req apiKeyAliasesRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		badRequest(c, "invalid api key aliases payload")
		return
	}
	svc, closeStore, ok := h.auxiliaryService(c)
	if !ok {
		return
	}
	defer closeStore()
	items, err := svc.SaveAPIKeyAliases(c.Request.Context(), req.Items, req.ActiveAPIKeyHashes, req.AllowOrphanAliasCleanup)
	if err != nil {
		if isUsageValidationError(err) {
			badRequest(c, err.Error())
			return
		}
		serverError(c, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"items": items})
}

func (h *Handlers) DeleteAPIKeyAlias(c *gin.Context) {
	apiKeyHash := c.Param("api_key_hash")
	svc, closeStore, ok := h.auxiliaryService(c)
	if !ok {
		return
	}
	defer closeStore()
	if err := svc.DeleteAPIKeyAlias(c.Request.Context(), apiKeyHash); err != nil {
		if isUsageValidationError(err) {
			badRequest(c, err.Error())
			return
		}
		serverError(c, err)
		return
	}
	c.Status(http.StatusNoContent)
}

func (h *Handlers) ExportUsage(c *gin.Context) {
	svc, closeStore, ok := h.auxiliaryService(c)
	if !ok {
		return
	}
	defer closeStore()
	filename := "usage-events-" + strconv.FormatInt(time.Now().UnixMilli(), 10) + ".jsonl"
	c.Header("Content-Type", "application/jsonl")
	c.Header("Content-Disposition", `attachment; filename="`+filename+`"`)
	c.Status(http.StatusOK)
	if _, err := svc.ExportEventsJSONL(c.Request.Context(), c.Writer); err != nil {
		_ = c.Error(err)
		return
	}
}

func (h *Handlers) ImportUsage(c *gin.Context) {
	svc, closeStore, ok := h.auxiliaryService(c)
	if !ok {
		return
	}
	defer closeStore()
	result, err := svc.ImportEventsJSONL(c.Request.Context(), c.Request.Body)
	if err != nil {
		serverError(c, err)
		return
	}
	c.JSON(http.StatusOK, result)
}

func (h *Handlers) auxiliaryService(c *gin.Context) (*plusvendor.UsageService, func(), bool) {
	if h == nil || h.bridge == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "usage bridge is unavailable", "status": plusvendor.QueryStatus{State: "unavailable"}})
		return nil, func() {}, false
	}
	store, closeStore, err := openUsageStore(strings.TrimSpace(h.bridge.DBPath()))
	if err != nil {
		serverError(c, err)
		return nil, func() {}, false
	}
	if collector := h.bridge.Collector(); collector != nil {
		_ = collector.Flush(c.Request.Context())
	}
	return plusvendor.NewUsageService(store), closeStore, true
}

func isUsageValidationError(err error) bool {
	if err == nil {
		return false
	}
	msg := strings.ToLower(err.Error())
	return strings.Contains(msg, "required") ||
		strings.Contains(msg, "invalid") ||
		strings.Contains(msg, "duplicate")
}
