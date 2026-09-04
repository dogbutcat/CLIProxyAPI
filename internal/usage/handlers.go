package usage

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	plusvendor "github.com/router-for-me/CLIProxyAPI/v7/internal/usage/plusvendor"
	plusstore "github.com/router-for-me/CLIProxyAPI/v7/internal/usage/plusvendor/store"
)

type MonitoringAuthMetadata struct {
	AccountName   string
	AuthLabel     string
	AuthFile      string
	Provider      string
	AuthProvider  string
	Protocol      string
	GeneratedName string
	ProjectID     string
}

type MonitoringAuthMetadataProvider func(context.Context) map[string]MonitoringAuthMetadata

type RouteOption func(*Handlers)

func WithMonitoringAuthMetadataProvider(provider MonitoringAuthMetadataProvider) RouteOption {
	return func(h *Handlers) {
		h.authMetadataProvider = provider
	}
}

func WithImportSessionConfig(cfg config.UsageImportSessionConfig) RouteOption {
	return func(h *Handlers) {
		h.importSessionConfig = cfg
	}
}

type Handlers struct {
	bridge               *Bridge
	authMetadataProvider MonitoringAuthMetadataProvider
	importSessionConfig  config.UsageImportSessionConfig
}

func NewHandlers(bridge *Bridge, opts ...RouteOption) *Handlers {
	h := &Handlers{bridge: bridge}
	for _, opt := range opts {
		if opt != nil {
			opt(h)
		}
	}
	return h
}

func RegisterRoutes(group *gin.RouterGroup, publicRoot gin.IRouter, protectedRoot gin.IRouter, bridge *Bridge, opts ...RouteOption) {
	h := NewHandlers(bridge, opts...)
	if publicRoot != nil {
		publicRoot.GET("/usage-service/info", h.UsageServiceInfo)
	}
	if protectedRoot != nil {
		protectedRoot.GET("/usage-service/config", h.UsageServiceConfig)
		protectedRoot.GET("/status", h.Status)
	}
	if group == nil {
		return
	}
	usageGroup := group.Group("/usage")
	usageGroup.GET("/dashboard/summary", h.DashboardSummary)
	usageGroup.GET("/capabilities", h.Capabilities)
	usageGroup.GET("/monitoring/accounts", h.MonitoringAccounts)
	usageGroup.GET("/monitoring/keys", h.MonitoringKeys)
	usageGroup.GET("/monitoring/realtime", h.MonitoringRealtime)
	usageGroup.GET("/monitoring/selectors", h.MonitoringSelectors)
	usageGroup.POST("/monitoring/analytics", h.MonitoringAnalyticsAPI)
	usageGroup.GET("/monitoring/header-snapshots", h.MonitoringHeaderSnapshots)
	usageGroup.GET("/model-prices", h.GetModelPrices)
	usageGroup.PUT("/model-prices", h.PutModelPrices)
	usageGroup.DELETE("/model-prices/:model", h.DeleteModelPrice)
	usageGroup.GET("/model-prices/usage-summary", h.GetModelPriceUsageSummary)
	usageGroup.POST("/model-prices/sync", h.PostModelPricesSync)
	usageGroup.GET("/api-key-aliases", h.GetAPIKeyAliases)
	usageGroup.PUT("/api-key-aliases", h.PutAPIKeyAliases)
	usageGroup.DELETE("/api-key-aliases/:api_key_hash", h.DeleteAPIKeyAlias)
	usageGroup.GET("/export", h.ExportUsage)
	usageGroup.POST("/import", h.ImportUsage)
	usageGroup.POST("/import-sessions", h.CreateUsageImportSession)
	usageGroup.GET("/import-sessions/:id", h.GetUsageImportSession)
	usageGroup.PUT("/import-sessions/:id/chunk", h.UploadUsageImportSessionChunk)
	usageGroup.POST("/import-sessions/:id/complete", h.CompleteUsageImportSession)
	usageGroup.DELETE("/import-sessions/:id", h.CancelUsageImportSession)
	usageGroup.GET("/status", h.Status)
}

func (h *Handlers) UsageServiceInfo(c *gin.Context) {
	ready := h != nil && h.bridge != nil && strings.TrimSpace(h.bridge.DBPath()) != ""
	c.JSON(http.StatusOK, gin.H{
		"service":            "cpa-usage-service",
		"mode":               "integrated",
		"source":             "integrated",
		"configured":         ready,
		"adminReady":         true,
		"projectInitialized": true,
		"dataKeyReady":       ready,
		"migrationStatus":    statusString(ready, "ok", "unavailable"),
	})
}

func (h *Handlers) UsageServiceConfig(c *gin.Context) {
	c.JSON(http.StatusOK, gin.H{
		"enabled": true,
		"usage": gin.H{
			"queryLimit":  plusvendor.MaxUsageQueryLimit,
			"query_limit": plusvendor.MaxUsageQueryLimit,
			"dashboard": gin.H{
				"healthRows":             plusvendor.DashboardHealthRows,
				"healthTimelineBucketMs": plusvendor.DashboardHealthTimelineBucketMS,
				"healthTimelineBuckets":  plusvendor.DashboardHealthTimelineBuckets,
			},
			"externalUsageService": gin.H{"enabled": false, "serviceBase": ""},
			"importSession":        importSessionConfigJSON(h.effectiveImportSessionConfig()),
			"import_session":       importSessionConfigJSON(h.effectiveImportSessionConfig()),
		},
		"externalUsageService": gin.H{"enabled": false, "serviceBase": ""},
		"capabilities":         usageCapabilities(),
		"source":               "integrated",
	})
}

func (h *Handlers) Capabilities(c *gin.Context) {
	c.JSON(http.StatusOK, usageCapabilities())
}

func usageCapabilities() gin.H {
	return gin.H{
		"source":         "integrated",
		"schema_version": 1,
		"account_actions": gin.H{
			"supported": true,
			"reason":    "",
			"version":   "local-sqlite-v1",
		},
	}
}

func (h *Handlers) Status(c *gin.Context) {
	if h == nil || h.bridge == nil {
		c.JSON(http.StatusServiceUnavailable, statusResponse(nil, "", nil, plusvendor.QueryStatus{
			State:  "unavailable",
			Errors: []plusvendor.QueryIssue{{Component: "bridge", Kind: "unavailable", Message: "usage bridge is unavailable"}},
		}))
		return
	}
	dbPath := strings.TrimSpace(h.bridge.DBPath())
	store, closeStore, err := openUsageStore(dbPath)
	if err != nil {
		c.JSON(http.StatusOK, statusResponse(h.bridge, dbPath, nil, plusvendor.QueryStatus{
			State:  "error",
			Errors: []plusvendor.QueryIssue{{Component: "store", Kind: "open_error", Message: err.Error()}},
		}))
		return
	}
	defer closeStore()
	events, dead, err := store.Counts(c.Request.Context())
	status := plusvendor.QueryStatus{State: "ok", HasData: events > 0}
	if err != nil {
		status.State = "error"
		status.Errors = []plusvendor.QueryIssue{{Component: "store", Kind: "count_error", Message: err.Error()}}
	}
	if status.State == "ok" && !status.HasData {
		status.State = "empty"
	}
	c.JSON(http.StatusOK, statusResponse(h.bridge, dbPath, &storeCounts{events: events, deadLetters: dead}, status))
}

func (h *Handlers) DashboardSummary(c *gin.Context) {
	if h == nil || h.bridge == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "usage bridge is unavailable", "status": plusvendor.QueryStatus{State: "unavailable"}})
		return
	}
	todayStartMS, ok := requiredPositiveInt64(c, "today_start_ms")
	if !ok {
		return
	}
	nowMS, ok := optionalNonNegativeInt64(c, "now_ms")
	if !ok {
		return
	}
	if nowMS == 0 {
		nowMS = time.Now().UnixMilli()
	}
	if nowMS < todayStartMS {
		badRequest(c, "now_ms must be greater than or equal to today_start_ms")
		return
	}
	topModels, ok := optionalBoundedInt(c, "top_models", 0, 100)
	if !ok {
		return
	}
	recentFailures, ok := optionalBoundedInt(c, "recent_failures", 0, 100)
	if !ok {
		return
	}

	dbPath := strings.TrimSpace(h.bridge.DBPath())
	store, closeStore, err := openUsageStore(dbPath)
	if err != nil {
		serverError(c, err)
		return
	}
	defer closeStore()
	if collector := h.bridge.Collector(); collector != nil {
		_ = collector.Flush(c.Request.Context())
	}
	svc := plusvendor.NewUsageService(store)
	summary, err := svc.DashboardSummary(c.Request.Context(), plusvendor.DashboardSummaryQuery{
		TodayStartMS:   todayStartMS,
		NowMS:          nowMS,
		TopModels:      topModels,
		RecentFailures: recentFailures,
	})
	if err != nil {
		serverError(c, err)
		return
	}
	c.JSON(http.StatusOK, dashboardSummaryResponse(summary, h.monitoringAuthMetadata(c.Request.Context()), todayStartMS, nowMS, time.Now().UnixMilli()))
}

func (h *Handlers) MonitoringAccounts(c *gin.Context) {
	h.monitoringAnalytics(c, "accounts", false)
}

func (h *Handlers) MonitoringKeys(c *gin.Context) {
	h.monitoringAnalytics(c, "keys", false)
}

func (h *Handlers) MonitoringRealtime(c *gin.Context) {
	h.monitoringAnalytics(c, "realtime", true)
}

func (h *Handlers) MonitoringSelectors(c *gin.Context) {
	if h == nil || h.bridge == nil {
		monitoringUnavailable(c)
		return
	}
	query, ok := parseMonitoringQuery(c, false)
	if !ok {
		return
	}
	store, closeStore, err := openUsageStore(strings.TrimSpace(h.bridge.DBPath()))
	if err != nil {
		serverError(c, err)
		return
	}
	defer closeStore()
	if collector := h.bridge.Collector(); collector != nil {
		_ = collector.Flush(c.Request.Context())
	}
	svc := plusvendor.NewUsageService(store)
	selectors, err := svc.MonitoringSelectors(c.Request.Context(), query.Filter)
	if err != nil {
		serverError(c, err)
		return
	}
	c.JSON(http.StatusOK, monitoringSelectorsResponse(selectors, h.monitoringAuthMetadata(c.Request.Context()), time.Now().UnixMilli()))
}

func (h *Handlers) MonitoringAnalyticsAPI(c *gin.Context) {
	if h == nil || h.bridge == nil {
		monitoringUnavailable(c)
		return
	}
	req, filter, ok := parseMonitoringAnalyticsAPIRequest(c)
	if !ok {
		return
	}
	store, closeStore, err := openUsageStore(strings.TrimSpace(h.bridge.DBPath()))
	if err != nil {
		serverError(c, err)
		return
	}
	defer closeStore()
	if collector := h.bridge.Collector(); collector != nil {
		_ = collector.Flush(c.Request.Context())
	}
	svc := plusvendor.NewUsageService(store)
	analytics, err := svc.MonitoringAnalytics(c.Request.Context(), filter)
	if err != nil {
		serverError(c, err)
		return
	}
	var selectors *plusstore.MonitoringSelectors
	if req.Include.FilterOptions || req.Include.FilterSelectors {
		value, err := svc.MonitoringSelectors(c.Request.Context(), filter)
		if err != nil {
			serverError(c, err)
			return
		}
		selectors = &value
	}
	var page *plusstore.MonitoringEventPage
	if req.Include.EventsPage != nil {
		query := plusstore.AnalyticsEventPageQuery{
			Filter: filter,
			Limit:  req.Include.EventsPage.Limit,
		}
		if req.Include.EventsPage.BeforeMS != nil && req.Include.EventsPage.BeforeID != nil {
			query.Cursor = &plusstore.AnalyticsEventCursor{TimestampMS: *req.Include.EventsPage.BeforeMS, ID: *req.Include.EventsPage.BeforeID}
		}
		value, err := svc.MonitoringEventsPage(c.Request.Context(), query)
		if err != nil {
			serverError(c, err)
			return
		}
		page = &value
	}
	var failures *plusstore.MonitoringEventPage
	if req.Include.RecentFailures > 0 {
		failed := true
		failureFilter := filter
		failureFilter.Failed = &failed
		value, err := svc.MonitoringEventsPage(c.Request.Context(), plusstore.AnalyticsEventPageQuery{
			Filter: failureFilter,
			Limit:  req.Include.RecentFailures,
		})
		if err != nil {
			serverError(c, err)
			return
		}
		failures = &value
	}
	var drilldownPreview *plusstore.MonitoringEventPage
	if req.Include.DrilldownPreview != nil && req.Include.DrilldownPreview.FromMS > 0 && req.Include.DrilldownPreview.ToMS > req.Include.DrilldownPreview.FromMS {
		previewFilter := filter
		previewFilter.FromMS = req.Include.DrilldownPreview.FromMS
		previewFilter.ToMS = req.Include.DrilldownPreview.ToMS
		value, err := svc.MonitoringEventsPage(c.Request.Context(), plusstore.AnalyticsEventPageQuery{
			Filter: previewFilter,
			Limit:  req.Include.DrilldownPreview.Limit,
		})
		if err != nil {
			serverError(c, err)
			return
		}
		drilldownPreview = &value
	}
	c.JSON(http.StatusOK, monitoringAnalyticsAPIResponse(req, analytics, selectors, page, failures, drilldownPreview, h.monitoringAuthMetadata(c.Request.Context()), time.Now().UnixMilli()))
}

func (h *Handlers) MonitoringHeaderSnapshots(c *gin.Context) {
	if h == nil || h.bridge == nil {
		monitoringUnavailable(c)
		return
	}
	limit, ok := optionalBoundedInt(c, "limit", 0, 1000)
	if !ok {
		return
	}
	if limit <= 0 {
		limit = 100
	}
	days, ok := optionalBoundedInt(c, "days", 0, 365)
	if !ok {
		return
	}
	if days <= 0 {
		days = 7
	}
	nowMS := time.Now().UnixMilli()
	fromMS := nowMS - int64(days)*24*60*60*1000
	store, closeStore, err := openUsageStore(strings.TrimSpace(h.bridge.DBPath()))
	if err != nil {
		serverError(c, err)
		return
	}
	defer closeStore()
	if collector := h.bridge.Collector(); collector != nil {
		_ = collector.Flush(c.Request.Context())
	}
	svc := plusvendor.NewUsageService(store)
	page, err := svc.MonitoringEventsPage(c.Request.Context(), plusstore.AnalyticsEventPageQuery{
		Filter: plusstore.AnalyticsFilter{FromMS: fromMS, ToMS: nowMS},
		Limit:  limit,
	})
	if err != nil {
		serverError(c, err)
		return
	}
	c.JSON(http.StatusOK, monitoringHeaderSnapshotsResponse(page.Events, h.monitoringAuthMetadata(c.Request.Context()), fromMS, nowMS, time.Now().UnixMilli()))
}

func (h *Handlers) monitoringAnalytics(c *gin.Context, view string, defaultEvents bool) {
	if h == nil || h.bridge == nil {
		monitoringUnavailable(c)
		return
	}
	query, ok := parseMonitoringQuery(c, defaultEvents)
	if !ok {
		return
	}
	store, closeStore, err := openUsageStore(strings.TrimSpace(h.bridge.DBPath()))
	if err != nil {
		serverError(c, err)
		return
	}
	defer closeStore()
	if collector := h.bridge.Collector(); collector != nil {
		_ = collector.Flush(c.Request.Context())
	}
	svc := plusvendor.NewUsageService(store)
	analytics, err := svc.MonitoringAnalytics(c.Request.Context(), query.Filter)
	if err != nil {
		serverError(c, err)
		return
	}
	var selectors *plusstore.MonitoringSelectors
	if query.IncludeSelectors {
		value, err := svc.MonitoringSelectors(c.Request.Context(), query.Filter)
		if err != nil {
			serverError(c, err)
			return
		}
		selectors = &value
	}
	var page *plusstore.MonitoringEventPage
	if query.IncludeEvents {
		value, err := svc.MonitoringEventsPage(c.Request.Context(), plusstore.AnalyticsEventPageQuery{
			Filter: query.Filter,
			Limit:  query.Limit,
			Cursor: query.Cursor,
		})
		if err != nil {
			serverError(c, err)
			return
		}
		page = &value
	}
	c.JSON(http.StatusOK, monitoringAnalyticsResponse(view, analytics, selectors, page, h.monitoringAuthMetadata(c.Request.Context()), time.Now().UnixMilli()))
}

type storeCounts struct {
	events      int64
	deadLetters int64
}

func statusResponse(bridge *Bridge, dbPath string, counts *storeCounts, status plusvendor.QueryStatus) gin.H {
	var stats CollectorStats
	if bridge != nil && bridge.Collector() != nil {
		stats = bridge.Collector().Stats()
	}
	dbSize := int64(0)
	if dbPath != "" {
		if info, err := os.Stat(dbPath); err == nil {
			dbSize = info.Size()
		}
	}
	var events int64
	var deadLetters int64
	if counts != nil {
		events = counts.events
		deadLetters = counts.deadLetters
	}
	return gin.H{
		"service":       "cpa-usage-integrated",
		"source":        "integrated",
		"status":        status,
		"events":        events,
		"deadLetters":   deadLetters,
		"dead_letters":  deadLetters,
		"dbPath":        dbPath,
		"db_path":       dbPath,
		"dbSizeBytes":   dbSize,
		"db_size_bytes": dbSize,
		"store": gin.H{
			"state":         status.State,
			"configured":    strings.TrimSpace(dbPath) != "",
			"db_path":       dbPath,
			"events":        events,
			"dead_letters":  deadLetters,
			"db_size_bytes": dbSize,
			"has_data":      events > 0,
			"partial":       status.Partial,
			"stale":         status.Stale,
			"errors":        status.Errors,
			"warnings":      status.Warnings,
		},
		"collector": gin.H{
			"collector":        "integrated",
			"mode":             "local-sqlite",
			"queueSize":        stats.QueueSize,
			"queue_size":       stats.QueueSize,
			"totalInserted":    stats.TotalInserted,
			"total_inserted":   stats.TotalInserted,
			"totalDropped":     stats.TotalDropped,
			"total_dropped":    stats.TotalDropped,
			"totalSkipped":     0,
			"total_skipped":    0,
			"lastInsertedAt":   stats.LastInsertedAt,
			"last_inserted_at": stats.LastInsertedAt,
			"lastError":        stats.LastError,
			"last_error":       stats.LastError,
		},
	}
}

func openUsageStore(dbPath string) (*plusstore.Store, func(), error) {
	if strings.TrimSpace(dbPath) == "" {
		return nil, func() {}, fmt.Errorf("usage store is unavailable")
	}
	store, err := plusstore.OpenStore(dbPath)
	if err != nil {
		return nil, func() {}, err
	}
	return store, func() { _ = store.Close() }, nil
}

func (h *Handlers) monitoringAuthMetadata(ctx context.Context) map[string]MonitoringAuthMetadata {
	if h == nil || h.authMetadataProvider == nil {
		return nil
	}
	return h.authMetadataProvider(ctx)
}

func dashboardSummaryResponse(summary plusvendor.DashboardSummary, metadataByAuthIndex map[string]MonitoringAuthMetadata, todayStartMS, nowMS, generatedAtMS int64) gin.H {
	today := aggregateJSON(summary.Aggregate)
	tokenMix := tokenMixJSON(summary.Aggregate)
	topModels := topModelsTodayJSON(summary.Models)
	trafficTimeline := dashboardTrafficTimelineJSON(todayStartMS, summary.Traffic)
	channelHealth := enrichDashboardChannelHealthStats(summary.ChannelHealth, metadataByAuthIndex)
	failureSources := enrichDashboardFailureSourceStats(summary.FailureSources, metadataByAuthIndex)
	resp := gin.H{
		"generated_at_ms": generatedAtMS,
		"status":          summary.Status,
		"query_status":    summary.Status,
		"data_status":     summary.Status,
		"window": gin.H{
			"today_start_ms":       todayStartMS,
			"now_ms":               nowMS,
			"rolling_30m_start_ms": nowMS - plusvendor.DashboardRollingWindowMS,
		},
		"today":                         today,
		"rolling_30m":                   rollingSummaryJSON(summary.Rolling),
		"top_models_today":              topModels,
		"model_cost_rank":               []gin.H{},
		"traffic_timeline":              trafficTimeline,
		"hourly_activity":               hourlyActivityJSON(trafficTimeline),
		"today_request_health_timeline": requestHealthTimelineJSON(todayStartMS, nowMS, summary.HealthTimeline),
		"token_mix":                     tokenMix,
		"provider_activity":             providerActivityJSON(summary.ProviderActivity),
		"channel_health":                channelHealthJSON(channelHealth, plusvendor.DashboardHealthRows),
		"failure_sources":               failureSourcesJSON(failureSources, plusvendor.DashboardHealthRows),
		"recent_failures":               recentFailuresJSON(summary.Failures),
		"fromMs":                        todayStartMS,
		"toMs":                          nowMS,
	}
	resp["topModels"] = modelStatsJSON(summary.ModelStats)
	resp["recentFailures"] = resp["recent_failures"]
	resp["tokenMix"] = tokenMix
	resp["providerActivity"] = resp["provider_activity"]
	return resp
}

func requiredPositiveInt64(c *gin.Context, name string) (int64, bool) {
	value, ok := parseInt64Query(c, name)
	if !ok {
		return 0, false
	}
	if value <= 0 {
		badRequest(c, name+" must be a positive integer")
		return 0, false
	}
	return value, true
}

func optionalNonNegativeInt64(c *gin.Context, name string) (int64, bool) {
	raw := strings.TrimSpace(c.Query(name))
	if raw == "" {
		return 0, true
	}
	value, ok := parseInt64Query(c, name)
	if !ok {
		return 0, false
	}
	if value < 0 {
		badRequest(c, name+" must be greater than or equal to 0")
		return 0, false
	}
	return value, true
}

func parseInt64Query(c *gin.Context, name string) (int64, bool) {
	raw := strings.TrimSpace(c.Query(name))
	if raw == "" {
		badRequest(c, name+" is required")
		return 0, false
	}
	value, err := strconv.ParseInt(raw, 10, 64)
	if err != nil {
		badRequest(c, name+" must be an integer")
		return 0, false
	}
	return value, true
}

func optionalBoundedInt(c *gin.Context, name string, minValue int, maxValue int) (int, bool) {
	raw := strings.TrimSpace(c.Query(name))
	if raw == "" {
		return 0, true
	}
	value, err := strconv.Atoi(raw)
	if err != nil {
		badRequest(c, name+" must be an integer")
		return 0, false
	}
	if value < minValue || value > maxValue {
		badRequest(c, fmt.Sprintf("%s must be between %d and %d", name, minValue, maxValue))
		return 0, false
	}
	return value, true
}

func badRequest(c *gin.Context, msg string) {
	c.JSON(http.StatusBadRequest, gin.H{"error": msg})
}

func serverError(c *gin.Context, err error) {
	c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error(), "status": plusvendor.QueryStatus{
		State:  "error",
		Errors: []plusvendor.QueryIssue{{Component: "store", Kind: "query_error", Message: err.Error()}},
	}})
}

func aggregateJSON(agg plusvendor.Aggregate) gin.H {
	successRate := 1.0
	if agg.TotalCalls > 0 {
		successRate = float64(agg.SuccessCalls) / float64(agg.TotalCalls)
	}
	resp := gin.H{
		"totalCalls":            agg.TotalCalls,
		"successCalls":          agg.SuccessCalls,
		"failureCalls":          agg.FailureCalls,
		"successRate":           successRate,
		"inputTokens":           agg.InputTokens,
		"outputTokens":          agg.OutputTokens,
		"reasoningTokens":       agg.ReasoningTokens,
		"cachedTokens":          agg.CachedTokens,
		"cacheReadTokens":       agg.CacheReadTokens,
		"cacheCreationTokens":   agg.CacheCreationTokens,
		"totalTokens":           agg.TotalTokens,
		"latencySamples":        agg.LatencySamples,
		"zeroTokenCalls":        agg.ZeroTokenCalls,
		"totalCost":             0,
		"total_calls":           agg.TotalCalls,
		"success_calls":         agg.SuccessCalls,
		"failure_calls":         agg.FailureCalls,
		"success_rate":          successRate,
		"input_tokens":          agg.InputTokens,
		"output_tokens":         agg.OutputTokens,
		"reasoning_tokens":      agg.ReasoningTokens,
		"cached_tokens":         agg.CachedTokens,
		"cache_read_tokens":     agg.CacheReadTokens,
		"cache_creation_tokens": agg.CacheCreationTokens,
		"total_tokens":          agg.TotalTokens,
		"zero_token_calls":      agg.ZeroTokenCalls,
		"total_cost":            0,
		"average_cost_per_call": 0,
		"average_latency_ms":    nil,
		"avgLatencyMs":          nil,
	}
	if agg.AvgLatencyMS != nil {
		resp["avgLatencyMs"] = *agg.AvgLatencyMS
		resp["average_latency_ms"] = *agg.AvgLatencyMS
	}
	return resp
}

func tokenMixJSON(agg plusvendor.Aggregate) []gin.H {
	totalInputTokens := maxInt64(agg.InputTokens, 0)
	cachedTokens := maxInt64(agg.CachedTokens, 0) + maxInt64(agg.CacheReadTokens, 0) + maxInt64(agg.CacheCreationTokens, 0)
	inputTokens := maxInt64(totalInputTokens-cachedTokens, 0)
	outputTokens := maxInt64(agg.OutputTokens, 0)
	reasoningTokens := maxInt64(agg.ReasoningTokens, 0)
	if agg.TotalTokens > 0 {
		overflow := inputTokens + cachedTokens + outputTokens + reasoningTokens - agg.TotalTokens
		if overflow > 0 {
			deduction := minInt64(minInt64(outputTokens, reasoningTokens), overflow)
			outputTokens -= deduction
			overflow -= deduction
		}
		if overflow > 0 {
			deduction := minInt64(outputTokens, overflow)
			outputTokens -= deduction
			overflow -= deduction
		}
		if overflow > 0 {
			inputTokens -= minInt64(inputTokens, overflow)
		}
	}
	total := inputTokens + cachedTokens + outputTokens + reasoningTokens
	return []gin.H{
		{"key": "input", "tokens": inputTokens, "share": rate(inputTokens, total)},
		{"key": "cached", "tokens": cachedTokens, "share": rate(cachedTokens, total)},
		{"key": "output", "tokens": outputTokens, "share": rate(outputTokens, total)},
		{"key": "reasoning", "tokens": reasoningTokens, "share": rate(reasoningTokens, total)},
	}
}

func rollingSummaryJSON(agg plusvendor.Aggregate) gin.H {
	return gin.H{
		"rpm":          float64(agg.TotalCalls) / plusvendor.DashboardRollingWindowMinutes,
		"tpm":          float64(agg.TotalTokens) / plusvendor.DashboardRollingWindowMinutes,
		"total_calls":  agg.TotalCalls,
		"total_tokens": agg.TotalTokens,
	}
}

func providerActivityJSON(stats []plusvendor.ProviderActivityStat) []gin.H {
	out := make([]gin.H, 0, len(stats))
	for _, stat := range stats {
		out = append(out, gin.H{
			"provider":      stat.Provider,
			"calls":         stat.Calls,
			"success_calls": stat.SuccessCalls,
			"failure_calls": stat.FailureCalls,
			"success_rate":  rate(stat.SuccessCalls, stat.Calls),
			"tokens":        stat.TotalTokens,
		})
	}
	return out
}

func topModelsTodayJSON(stats []plusvendor.ModelStat) []gin.H {
	out := make([]gin.H, 0, len(stats))
	for _, stat := range stats {
		out = append(out, gin.H{
			"model":        stat.Model,
			"calls":        stat.Calls,
			"tokens":       stat.TotalTokens,
			"cost":         0,
			"success_rate": rate(stat.SuccessCalls, stat.Calls),
		})
	}
	return out
}

func dashboardTrafficTimelineJSON(todayStartMS int64, points []plusvendor.TimelinePoint) []gin.H {
	pointByBucket := make(map[int64]plusvendor.TimelinePoint, len(points))
	for _, point := range points {
		pointByBucket[point.BucketMS] = point
	}
	out := make([]gin.H, 0, 24)
	var maxCalls int64
	var maxTokens int64
	for hour := 0; hour < 24; hour++ {
		bucketMS := todayStartMS + int64(hour)*plusvendor.DashboardHourWindowMS
		point := pointByBucket[bucketMS]
		maxCalls = maxInt64(maxCalls, point.Calls)
		maxTokens = maxInt64(maxTokens, point.Tokens)
		out = append(out, gin.H{
			"bucket_ms":    bucketMS,
			"calls":        point.Calls,
			"tokens":       point.Tokens,
			"success":      point.Success,
			"failure":      point.Failure,
			"calls_share":  0.0,
			"tokens_share": 0.0,
			"failure_rate": rate(point.Failure, point.Calls),
		})
	}
	for _, row := range out {
		row["calls_share"] = rate(row["calls"].(int64), maxCalls)
		row["tokens_share"] = rate(row["tokens"].(int64), maxTokens)
	}
	return out
}

func hourlyActivityJSON(traffic []gin.H) []gin.H {
	out := make([]gin.H, 0, len(traffic))
	for hour, point := range traffic {
		callsShare, _ := point["calls_share"].(float64)
		tokensShare, _ := point["tokens_share"].(float64)
		intensity := callsShare
		if tokensShare > intensity {
			intensity = tokensShare
		}
		out = append(out, gin.H{
			"hour_index": hour,
			"bucket_ms":  point["bucket_ms"],
			"calls":      point["calls"],
			"tokens":     point["tokens"],
			"intensity":  intensity,
		})
	}
	return out
}

func requestHealthTimelineJSON(todayStartMS, nowMS int64, points []plusvendor.TimelinePoint) gin.H {
	pointByBucket := make(map[int64]plusvendor.TimelinePoint, len(points))
	var maxCalls int64
	for _, point := range points {
		pointByBucket[point.BucketMS] = point
		maxCalls = maxInt64(maxCalls, point.Calls)
	}
	toMS := todayStartMS + int64(plusvendor.DashboardHealthTimelineBuckets)*plusvendor.DashboardHealthTimelineBucketMS
	rows := make([]gin.H, 0, plusvendor.DashboardHealthTimelineBuckets)
	var successCalls int64
	var failureCalls int64
	var totalCalls int64
	for index := 0; index < plusvendor.DashboardHealthTimelineBuckets; index++ {
		bucketMS := todayStartMS + int64(index)*plusvendor.DashboardHealthTimelineBucketMS
		point := pointByBucket[bucketMS]
		future := bucketMS > nowMS
		failureRate := rate(point.Failure, point.Calls)
		successRate := rate(point.Success, point.Calls)
		successCalls += point.Success
		failureCalls += point.Failure
		totalCalls += point.Calls
		rows = append(rows, gin.H{
			"bucket_ms":    bucketMS,
			"calls":        point.Calls,
			"tokens":       point.Tokens,
			"success":      point.Success,
			"failure":      point.Failure,
			"success_rate": successRate,
			"failure_rate": failureRate,
			"tone":         requestHealthTone(point.Calls, failureRate, future),
			"intensity":    rate(point.Calls, maxCalls),
			"future":       future,
		})
	}
	return gin.H{
		"from_ms":       todayStartMS,
		"to_ms":         toMS,
		"bucket_ms":     plusvendor.DashboardHealthTimelineBucketMS,
		"success_calls": successCalls,
		"failure_calls": failureCalls,
		"total_calls":   totalCalls,
		"success_rate":  rate(successCalls, totalCalls),
		"points":        rows,
	}
}

func requestHealthTone(calls int64, failureRate float64, future bool) string {
	switch {
	case future:
		return "future"
	case calls == 0:
		return "empty"
	case failureRate >= 0.1:
		return "bad"
	case failureRate > 0:
		return "warn"
	default:
		return "good"
	}
}

func channelHealthJSON(stats []plusvendor.ChannelHealthStat, limit int) []gin.H {
	rows := make([]gin.H, 0, len(stats))
	for _, stat := range stats {
		avgLatency := dashboardAverageLatency(stat.LatencySumMS, stat.LatencySamples)
		successRate := rate(stat.Calls-stat.Failures, stat.Calls)
		failureRate := rate(stat.Failures, stat.Calls)
		rows = append(rows, gin.H{
			"auth_index":               firstNonEmptyUsageString(stat.AuthIndex, "-"),
			"source":                   stat.Source,
			"source_hash":              stat.SourceHash,
			"api_key_hash":             stat.APIKeyHash,
			"account_snapshot":         stat.AccountSnapshot,
			"auth_label_snapshot":      stat.AuthLabelSnapshot,
			"auth_provider_snapshot":   stat.AuthProviderSnapshot,
			"auth_project_id_snapshot": stat.AuthProjectIDSnapshot,
			"calls":                    stat.Calls,
			"failures":                 stat.Failures,
			"failure_rate":             failureRate,
			"success_rate":             successRate,
			"tokens":                   stat.TotalTokens,
			"cost":                     0,
			"average_latency_ms":       avgLatency,
			"tone":                     dashboardHealthTone(successRate, stat.Failures, avgLatency),
		})
	}
	sort.SliceStable(rows, func(i, j int) bool {
		leftTone, _ := rows[i]["tone"].(string)
		rightTone, _ := rows[j]["tone"].(string)
		if dashboardToneSeverity(leftTone) != dashboardToneSeverity(rightTone) {
			return dashboardToneSeverity(leftTone) > dashboardToneSeverity(rightTone)
		}
		leftFailures, _ := rows[i]["failures"].(int64)
		rightFailures, _ := rows[j]["failures"].(int64)
		if leftFailures != rightFailures {
			return leftFailures > rightFailures
		}
		leftCalls, _ := rows[i]["calls"].(int64)
		rightCalls, _ := rows[j]["calls"].(int64)
		return leftCalls > rightCalls
	})
	if limit > 0 && len(rows) > limit {
		rows = rows[:limit]
	}
	return rows
}

func failureSourcesJSON(stats []plusvendor.FailureSourceStat, limit int) []gin.H {
	rows := make([]gin.H, 0, len(stats))
	for _, stat := range stats {
		if stat.Failures == 0 {
			continue
		}
		avgLatency := dashboardAverageLatency(stat.LatencySumMS, stat.LatencySamples)
		failureRate := rate(stat.Failures, stat.Calls)
		tone := "warn"
		if failureRate >= 0.5 || stat.Failures >= 5 {
			tone = "bad"
		}
		rows = append(rows, gin.H{
			"source":                   stat.Source,
			"source_hash":              stat.SourceHash,
			"api_key_hash":             stat.APIKeyHash,
			"auth_index":               firstNonEmptyUsageString(stat.AuthIndex, "-"),
			"account_snapshot":         stat.AccountSnapshot,
			"auth_label_snapshot":      stat.AuthLabelSnapshot,
			"auth_provider_snapshot":   stat.AuthProviderSnapshot,
			"auth_project_id_snapshot": stat.AuthProjectIDSnapshot,
			"calls":                    stat.Calls,
			"failures":                 stat.Failures,
			"failure_rate":             failureRate,
			"last_seen_ms":             stat.LastSeenMS,
			"average_latency_ms":       avgLatency,
			"tone":                     tone,
		})
	}
	sort.SliceStable(rows, func(i, j int) bool {
		leftFailures, _ := rows[i]["failures"].(int64)
		rightFailures, _ := rows[j]["failures"].(int64)
		if leftFailures != rightFailures {
			return leftFailures > rightFailures
		}
		leftLastSeen, _ := rows[i]["last_seen_ms"].(int64)
		rightLastSeen, _ := rows[j]["last_seen_ms"].(int64)
		return leftLastSeen > rightLastSeen
	})
	if limit > 0 && len(rows) > limit {
		rows = rows[:limit]
	}
	return rows
}

func enrichDashboardChannelHealthStats(stats []plusvendor.ChannelHealthStat, metadataByAuthIndex map[string]MonitoringAuthMetadata) []plusvendor.ChannelHealthStat {
	if len(stats) == 0 || len(metadataByAuthIndex) == 0 {
		return stats
	}
	out := make([]plusvendor.ChannelHealthStat, len(stats))
	copy(out, stats)
	for i, stat := range out {
		metadata, ok := metadataByAuthIndex[strings.TrimSpace(stat.AuthIndex)]
		if !ok {
			continue
		}
		if isMonitoringSnapshotFillable(stat.AccountSnapshot, metadata) {
			if accountName := strings.TrimSpace(metadata.AccountName); accountName != "" {
				stat.AccountSnapshot = accountName
				if stat.AuthLabelSnapshot == "" {
					stat.AuthLabelSnapshot = accountName
				}
			}
		}
		if isMonitoringSnapshotFillable(stat.AuthLabelSnapshot, metadata) {
			if authLabel := strings.TrimSpace(metadata.AuthLabel); authLabel != "" {
				stat.AuthLabelSnapshot = authLabel
			}
		}
		if isMonitoringSnapshotFillable(stat.AuthProviderSnapshot, metadata) {
			if provider := firstNonEmptyUsageString(metadata.Provider, metadata.AuthProvider, metadata.Protocol); provider != "" {
				stat.AuthProviderSnapshot = provider
			}
		}
		if isMonitoringSnapshotFillable(stat.AuthProjectIDSnapshot, metadata) {
			if projectID := strings.TrimSpace(metadata.ProjectID); projectID != "" {
				stat.AuthProjectIDSnapshot = projectID
			}
		}
		out[i] = stat
	}
	return out
}

func enrichDashboardFailureSourceStats(stats []plusvendor.FailureSourceStat, metadataByAuthIndex map[string]MonitoringAuthMetadata) []plusvendor.FailureSourceStat {
	if len(stats) == 0 || len(metadataByAuthIndex) == 0 {
		return stats
	}
	out := make([]plusvendor.FailureSourceStat, len(stats))
	copy(out, stats)
	for i, stat := range out {
		metadata, ok := metadataByAuthIndex[strings.TrimSpace(stat.AuthIndex)]
		if !ok {
			continue
		}
		if isMonitoringSnapshotFillable(stat.AccountSnapshot, metadata) {
			if accountName := strings.TrimSpace(metadata.AccountName); accountName != "" {
				stat.AccountSnapshot = accountName
				if stat.AuthLabelSnapshot == "" {
					stat.AuthLabelSnapshot = accountName
				}
			}
		}
		if isMonitoringSnapshotFillable(stat.AuthLabelSnapshot, metadata) {
			if authLabel := strings.TrimSpace(metadata.AuthLabel); authLabel != "" {
				stat.AuthLabelSnapshot = authLabel
			}
		}
		if isMonitoringSnapshotFillable(stat.AuthProviderSnapshot, metadata) {
			if provider := firstNonEmptyUsageString(metadata.Provider, metadata.AuthProvider, metadata.Protocol); provider != "" {
				stat.AuthProviderSnapshot = provider
			}
		}
		if isMonitoringSnapshotFillable(stat.AuthProjectIDSnapshot, metadata) {
			if projectID := strings.TrimSpace(metadata.ProjectID); projectID != "" {
				stat.AuthProjectIDSnapshot = projectID
			}
		}
		out[i] = stat
	}
	return out
}

func modelStatsJSON(stats []plusvendor.ModelStat) []gin.H {
	out := make([]gin.H, 0, len(stats))
	for _, stat := range stats {
		failureCalls := stat.Calls - stat.SuccessCalls
		successRate := rate(stat.SuccessCalls, stat.Calls)
		if stat.Calls == 0 {
			successRate = 1
		}
		out = append(out, gin.H{
			"model":                 stat.Model,
			"calls":                 stat.Calls,
			"successCalls":          stat.SuccessCalls,
			"failureCalls":          failureCalls,
			"successRate":           successRate,
			"inputTokens":           stat.InputTokens,
			"outputTokens":          stat.OutputTokens,
			"reasoningTokens":       stat.ReasoningTokens,
			"cachedTokens":          stat.CachedTokens,
			"cacheReadTokens":       stat.CacheReadTokens,
			"cacheCreationTokens":   stat.CacheCreationTokens,
			"totalTokens":           stat.TotalTokens,
			"cost":                  0,
			"success_calls":         stat.SuccessCalls,
			"failure_calls":         failureCalls,
			"success_rate":          successRate,
			"input_tokens":          stat.InputTokens,
			"output_tokens":         stat.OutputTokens,
			"reasoning_tokens":      stat.ReasoningTokens,
			"cached_tokens":         stat.CachedTokens,
			"cache_read_tokens":     stat.CacheReadTokens,
			"cache_creation_tokens": stat.CacheCreationTokens,
			"total_tokens":          stat.TotalTokens,
		})
	}
	return out
}

func recentFailuresJSON(rows []plusvendor.RecentFailure) []gin.H {
	out := make([]gin.H, 0, len(rows))
	for _, row := range rows {
		item := gin.H{
			"timestampMs":              row.TimestampMS,
			"timestamp_ms":             row.TimestampMS,
			"model":                    row.Model,
			"apiKeyHash":               row.APIKeyHash,
			"api_key_hash":             row.APIKeyHash,
			"source":                   row.Source,
			"sourceHash":               row.SourceHash,
			"source_hash":              row.SourceHash,
			"authIndex":                row.AuthIndex,
			"auth_index":               row.AuthIndex,
			"endpoint":                 row.Endpoint,
			"accountSnapshot":          row.AccountSnapshot,
			"account_snapshot":         row.AccountSnapshot,
			"authLabelSnapshot":        row.AuthLabelSnapshot,
			"auth_label_snapshot":      row.AuthLabelSnapshot,
			"authProviderSnapshot":     row.AuthProviderSnapshot,
			"auth_provider_snapshot":   row.AuthProviderSnapshot,
			"authProjectIDSnapshot":    row.AuthProjectIDSnapshot,
			"auth_project_id_snapshot": row.AuthProjectIDSnapshot,
			"failSummary":              row.FailSummary,
			"fail_summary":             row.FailSummary,
			"responseMetadata":         row.ResponseMetadata,
			"response_metadata":        row.ResponseMetadata,
			"headerQuotaPlanType":      row.HeaderQuotaPlanType,
			"header_quota_plan_type":   row.HeaderQuotaPlanType,
			"headerErrorKind":          row.HeaderErrorKind,
			"header_error_kind":        row.HeaderErrorKind,
			"headerErrorCode":          row.HeaderErrorCode,
			"header_error_code":        row.HeaderErrorCode,
			"headerTraceID":            row.HeaderTraceID,
			"header_trace_id":          row.HeaderTraceID,
		}
		if row.LatencyMS != nil {
			item["latencyMs"] = *row.LatencyMS
			item["duration_ms"] = *row.LatencyMS
		}
		if row.FailStatusCode != nil {
			item["failStatusCode"] = *row.FailStatusCode
			item["fail_status_code"] = *row.FailStatusCode
		}
		if row.HeaderQuotaRecoverAtMS != nil {
			item["headerQuotaRecoverAtMs"] = *row.HeaderQuotaRecoverAtMS
			item["header_quota_recover_at_ms"] = *row.HeaderQuotaRecoverAtMS
		}
		if row.HeaderQuotaUsedPercent != nil {
			item["headerQuotaUsedPercent"] = *row.HeaderQuotaUsedPercent
			item["header_quota_used_percent"] = *row.HeaderQuotaUsedPercent
		}
		out = append(out, item)
	}
	return out
}

func isMonitoringSnapshotFillable(current string, metadata MonitoringAuthMetadata) bool {
	current = strings.TrimSpace(current)
	if current == "" || current == "-" {
		return true
	}
	metadataProvider := strings.TrimSpace(metadata.AuthProvider)
	metadataSnapshotProvider := strings.TrimSpace(metadata.Provider)
	if metadataSnapshotProvider == "" {
		metadataSnapshotProvider = metadataProvider
	}
	if metadataSnapshotProvider != "" && strings.EqualFold(current, metadataSnapshotProvider) {
		return true
	}
	return isOpenCodeGoMonitoringPlaceholder(current)
}

func isOpenCodeGoMonitoringPlaceholder(value string) bool {
	value = strings.TrimSpace(value)
	if value == "" {
		return true
	}
	if strings.EqualFold(value, "opencode-go") {
		return true
	}
	if !strings.HasPrefix(strings.ToLower(value), "opencode-go:") {
		return false
	}
	parts := strings.Split(value, ":")
	return len(parts) >= 3 && strings.EqualFold(parts[0], "opencode-go")
}

func dashboardAverageLatency(sum, samples int64) any {
	if samples <= 0 {
		return nil
	}
	return float64(sum) / float64(samples)
}

func dashboardHealthTone(successRate float64, failures int64, averageLatencyMS any) string {
	latency, _ := averageLatencyMS.(float64)
	if successRate < 0.85 || failures >= 5 || latency >= 30000 {
		return "bad"
	}
	if successRate < 0.95 || failures > 0 || latency >= 15000 {
		return "warn"
	}
	return "good"
}

func dashboardToneSeverity(tone string) int {
	switch tone {
	case "bad":
		return 3
	case "warn":
		return 2
	default:
		return 1
	}
}

func rate(part, total int64) float64 {
	if total <= 0 {
		return 0
	}
	return float64(part) / float64(total)
}

func firstNonEmptyUsageString(values ...string) string {
	for _, value := range values {
		if trimmed := strings.TrimSpace(value); trimmed != "" {
			return trimmed
		}
	}
	return ""
}

func statusString(ok bool, trueValue, falseValue string) string {
	if ok {
		return trueValue
	}
	return falseValue
}

func maxInt64(a, b int64) int64 {
	if a > b {
		return a
	}
	return b
}

func minInt64(a, b int64) int64 {
	if a < b {
		return a
	}
	return b
}
