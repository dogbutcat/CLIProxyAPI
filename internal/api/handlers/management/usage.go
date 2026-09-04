package management

import (
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/redisqueue"
	usagebridge "github.com/router-for-me/CLIProxyAPI/v7/internal/usage"
)

type usageQueueRecord []byte

func (r usageQueueRecord) MarshalJSON() ([]byte, error) {
	if json.Valid(r) {
		return append([]byte(nil), r...), nil
	}
	return json.Marshal(string(r))
}

// GetUsageQueue pops queued usage records from the usage queue.
func (h *Handler) GetUsageQueue(c *gin.Context) {
	if h == nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "handler unavailable"})
		return
	}

	count, errCount := parseUsageQueueCount(c.Query("count"))
	if errCount != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": errCount.Error()})
		return
	}

	items := redisqueue.PopOldest(count)
	records := make([]usageQueueRecord, 0, len(items))
	for _, item := range items {
		records = append(records, usageQueueRecord(append([]byte(nil), item...)))
	}

	c.JSON(http.StatusOK, records)
}

func parseUsageQueueCount(value string) (int, error) {
	value = strings.TrimSpace(value)
	if value == "" {
		return 1, nil
	}
	count, errCount := strconv.Atoi(value)
	if errCount != nil || count <= 0 {
		return 0, errors.New("count must be a positive integer")
	}
	return count, nil
}

func (h *Handler) SetUsageBridge(bridge *usagebridge.Bridge) {
	if h == nil {
		return
	}
	h.mu.Lock()
	h.usageBridge = bridge
	h.mu.Unlock()
}

func (h *Handler) SetUsageMetadataProvider(provider usagebridge.MonitoringAuthMetadataProvider) {
	if h == nil {
		return
	}
	h.mu.Lock()
	h.usageMetadataProvider = provider
	h.mu.Unlock()
}

func (h *Handler) GetUsageServiceInfo(c *gin.Context) {
	h.usageHandlers().UsageServiceInfo(c)
}

func (h *Handler) GetUsageServiceConfig(c *gin.Context) {
	h.usageHandlers().UsageServiceConfig(c)
}

func (h *Handler) GetUsageCapabilities(c *gin.Context) {
	h.usageHandlers().Capabilities(c)
}

func (h *Handler) GetUsageStoreStatus(c *gin.Context) {
	h.usageHandlers().Status(c)
}

func (h *Handler) GetDashboardSummary(c *gin.Context) {
	h.usageHandlers().DashboardSummary(c)
}

func (h *Handler) GetMonitoringAccounts(c *gin.Context) {
	h.usageHandlers().MonitoringAccounts(c)
}

func (h *Handler) GetMonitoringKeys(c *gin.Context) {
	h.usageHandlers().MonitoringKeys(c)
}

func (h *Handler) GetMonitoringRealtime(c *gin.Context) {
	h.usageHandlers().MonitoringRealtime(c)
}

func (h *Handler) GetMonitoringSelectors(c *gin.Context) {
	h.usageHandlers().MonitoringSelectors(c)
}

func (h *Handler) PostMonitoringAnalytics(c *gin.Context) {
	h.usageHandlers().MonitoringAnalyticsAPI(c)
}

func (h *Handler) GetMonitoringHeaderSnapshots(c *gin.Context) {
	h.usageHandlers().MonitoringHeaderSnapshots(c)
}

func (h *Handler) GetModelPrices(c *gin.Context) {
	h.usageHandlers().GetModelPrices(c)
}

func (h *Handler) PutModelPrices(c *gin.Context) {
	h.usageHandlers().PutModelPrices(c)
}

func (h *Handler) DeleteModelPrice(c *gin.Context) {
	h.usageHandlers().DeleteModelPrice(c)
}

func (h *Handler) GetModelPriceUsageSummary(c *gin.Context) {
	h.usageHandlers().GetModelPriceUsageSummary(c)
}

func (h *Handler) PostModelPricesSync(c *gin.Context) {
	h.usageHandlers().PostModelPricesSync(c)
}

func (h *Handler) GetAPIKeyAliases(c *gin.Context) {
	h.usageHandlers().GetAPIKeyAliases(c)
}

func (h *Handler) PutAPIKeyAliases(c *gin.Context) {
	h.usageHandlers().PutAPIKeyAliases(c)
}

func (h *Handler) DeleteAPIKeyAlias(c *gin.Context) {
	h.usageHandlers().DeleteAPIKeyAlias(c)
}

func (h *Handler) ExportUsage(c *gin.Context) {
	h.usageHandlers().ExportUsage(c)
}

func (h *Handler) ImportUsage(c *gin.Context) {
	h.usageHandlers().ImportUsage(c)
}

func (h *Handler) CreateUsageImportSession(c *gin.Context) {
	h.usageHandlers().CreateUsageImportSession(c)
}

func (h *Handler) GetUsageImportSession(c *gin.Context) {
	h.usageHandlers().GetUsageImportSession(c)
}

func (h *Handler) UploadUsageImportSessionChunk(c *gin.Context) {
	h.usageHandlers().UploadUsageImportSessionChunk(c)
}

func (h *Handler) CompleteUsageImportSession(c *gin.Context) {
	h.usageHandlers().CompleteUsageImportSession(c)
}

func (h *Handler) CancelUsageImportSession(c *gin.Context) {
	h.usageHandlers().CancelUsageImportSession(c)
}

func (h *Handler) ListAccountActionCandidates(c *gin.Context) {
	h.usageHandlers().ListAccountActionCandidates(c)
}

func (h *Handler) IgnoreAccountActionCandidate(c *gin.Context) {
	h.usageHandlers().IgnoreAccountActionCandidate(c)
}

func (h *Handler) ResolveAccountActionCandidate(c *gin.Context) {
	h.usageHandlers().ResolveAccountActionCandidate(c)
}

func (h *Handler) EnableAccountActionCandidate(c *gin.Context) {
	h.usageHandlers().EnableAccountActionCandidate(c)
}

func (h *Handler) DeleteAccountActionCandidateAuthFile(c *gin.Context) {
	h.usageHandlers().DeleteAccountActionCandidateAuthFile(c)
}

func (h *Handler) usageHandlers() *usagebridge.Handlers {
	if h == nil {
		return usagebridge.NewHandlers(nil)
	}
	h.mu.Lock()
	bridge := h.usageBridge
	provider := h.usageMetadataProvider
	var importSessionConfig config.UsageImportSessionConfig
	if h.cfg != nil {
		importSessionConfig = h.cfg.UsageImportSession
	}
	h.mu.Unlock()
	return usagebridge.NewHandlers(
		bridge,
		usagebridge.WithMonitoringAuthMetadataProvider(provider),
		usagebridge.WithImportSessionConfig(importSessionConfig),
	)
}
