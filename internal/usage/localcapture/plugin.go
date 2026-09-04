package localcapture

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"regexp"
	"strings"
	"time"

	internallogging "github.com/router-for-me/CLIProxyAPI/v7/internal/logging"
	usagebridge "github.com/router-for-me/CLIProxyAPI/v7/internal/usage"
	coreusage "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/usage"
)

type EventRecorder interface {
	RecordEvent(usagebridge.Event)
}

type Plugin struct {
	recorder EventRecorder
}

func New(recorder EventRecorder) *Plugin {
	return &Plugin{recorder: recorder}
}

func (p *Plugin) HandleUsage(ctx context.Context, record coreusage.Record) {
	if p == nil || p.recorder == nil {
		return
	}
	p.recorder.RecordEvent(convertRecord(ctx, record))
}

func convertRecord(ctx context.Context, record coreusage.Record) usagebridge.Event {
	now := time.Now()
	requestedAt := record.RequestedAt
	if requestedAt.IsZero() {
		requestedAt = now
	}

	model := normalizeOrDefault(record.Model, "unknown")
	requestedModel := firstNonEmpty(record.Alias, coreusage.RequestedModelAliasFromContext(ctx), model)
	provider := normalizeOrDefault(record.Provider, "unknown")
	executorType := normalizeOrDefault(record.ExecutorType, "unknown")
	authType := normalizeOrDefault(record.AuthType, "unknown")
	endpoint := normalizeOrDefault(internallogging.GetEndpoint(ctx), "-")
	method, path := parseEndpoint(endpoint)

	detail := coreusage.EnsureTokenBreakdownForProvider(record.Detail, record.Provider, record.ExecutorType)
	totalTokens := detail.TotalTokens
	if totalTokens == 0 {
		totalTokens = detail.TokenBreakdown.TotalTokens
	}

	failed := record.Failed
	status := internallogging.GetResponseStatus(ctx)
	if !failed && status >= http.StatusBadRequest {
		failed = true
	}
	failStatusCode := record.Fail.StatusCode
	failBody := sanitizeText(record.Fail.Body)
	if failed {
		if failStatusCode <= 0 {
			failStatusCode = status
		}
		if failStatusCode <= 0 {
			failStatusCode = http.StatusInternalServerError
		}
	} else {
		failStatusCode = http.StatusOK
		failBody = ""
	}

	headers := coreusage.SanitizeResponseHeaders(record.ResponseHeaders)
	headerTraceID := firstHeader(headers, "X-Oai-Request-Id", "X-Request-Id", "X-Upstream-Request-Id")
	metadataJSON := responseMetadataJSON(headers)

	event := usagebridge.Event{
		RequestID:             strings.TrimSpace(internallogging.GetRequestID(ctx)),
		TimestampMS:           requestedAt.UnixMilli(),
		Timestamp:             requestedAt.UTC().Format(time.RFC3339Nano),
		Provider:              provider,
		ExecutorType:          executorType,
		Model:                 requestedModel,
		RequestedModel:        requestedModel,
		ResolvedModel:         model,
		Endpoint:              endpoint,
		Method:                method,
		Path:                  path,
		AuthType:              authType,
		AuthIndex:             strings.TrimSpace(record.AuthIndex),
		AuthProviderSnapshot:  provider,
		AuthProjectIDSnapshot: projectSnapshot(record.Source, provider),
		Source:                maskSource(record.Source),
		SourceHash:            hashString(record.Source),
		APIKeyHash:            hashString(record.APIKey),
		ReasoningEffort:       firstNonEmpty(record.ReasoningEffort, coreusage.ReasoningEffortFromContext(ctx)),
		ServiceTier:           firstNonEmpty(record.ServiceTier, record.RequestServiceTier, coreusage.ServiceTierFromContext(ctx)),
		ResponseServiceTier:   strings.TrimSpace(record.ResponseServiceTier),
		InputTokens:           detail.InputTokens,
		OutputTokens:          detail.OutputTokens,
		ReasoningTokens:       detail.ReasoningTokens,
		CachedTokens:          detail.CachedTokens,
		CacheTokens:           detail.CachedTokens,
		CacheReadTokens:       detail.CacheReadTokens,
		CacheCreationTokens:   detail.CacheCreationTokens,
		TotalTokens:           totalTokens,
		LatencyMS:             durationMS(record.Latency),
		TTFTMS:                durationMS(record.TTFT),
		Failed:                failed,
		FailStatusCode:        failStatusCode,
		FailSummary:           failBody,
		FailBody:              failBody,
		ResponseHeaders:       headers,
		HeaderTraceID:         headerTraceID,
		ResponseMetadataJSON:  metadataJSON,
		CreatedAtMS:           now.UnixMilli(),
	}
	if record.AuthID != "" {
		event.AuthLabelSnapshot = strings.TrimSpace(record.AuthID)
	}
	event.EventHash = buildEventHash(event)
	return event
}

func durationMS(duration time.Duration) *int64 {
	if duration <= 0 {
		return nil
	}
	ms := duration.Milliseconds()
	return &ms
}

func normalizeOrDefault(value, fallback string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return fallback
	}
	return value
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if trimmed := strings.TrimSpace(value); trimmed != "" {
			return trimmed
		}
	}
	return ""
}

func parseEndpoint(endpoint string) (string, string) {
	parts := strings.SplitN(strings.TrimSpace(endpoint), " ", 2)
	if len(parts) != 2 {
		return "", strings.TrimSpace(endpoint)
	}
	return strings.ToUpper(strings.TrimSpace(parts[0])), strings.TrimSpace(parts[1])
}

func projectSnapshot(source, provider string) string {
	if !strings.EqualFold(strings.TrimSpace(provider), "vertex") {
		return ""
	}
	return strings.TrimSpace(source)
}

func hashString(value string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return ""
	}
	sum := sha256.Sum256([]byte(value))
	return hex.EncodeToString(sum[:])
}

func maskSource(value string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return ""
	}
	if at := strings.LastIndex(value, "@"); at > 0 {
		local := value[:at]
		domain := value[at+1:]
		if local == "" || domain == "" {
			return hashString(value)
		}
		return local[:1] + "***@" + domain
	}
	return hashString(value)
}

var secretTextPatterns = []*regexp.Regexp{
	regexp.MustCompile(`(?i)bearer\s+[A-Za-z0-9._~+/=-]+`),
	regexp.MustCompile(`(?i)(api[_-]?key|authorization|cookie)\s*[:=]\s*[^,\s]+`),
}

func sanitizeText(value string) string {
	value = strings.TrimSpace(value)
	for _, pattern := range secretTextPatterns {
		value = pattern.ReplaceAllString(value, "$1 [redacted]")
	}
	return value
}

func firstHeader(headers http.Header, names ...string) string {
	for _, name := range names {
		if value := strings.TrimSpace(headers.Get(name)); value != "" {
			return value
		}
	}
	return ""
}

func responseMetadataJSON(headers http.Header) string {
	if len(headers) == 0 {
		return ""
	}
	raw, err := json.Marshal(headers)
	if err != nil {
		return ""
	}
	return string(raw)
}

func buildEventHash(event usagebridge.Event) string {
	parts := []string{
		event.RequestID,
		event.Provider,
		event.ExecutorType,
		event.RequestedModel,
		event.ResolvedModel,
		event.AuthType,
		event.AuthIndex,
		event.APIKeyHash,
		event.SourceHash,
		event.Endpoint,
		event.Timestamp,
	}
	return hashString(strings.Join(parts, "\x00"))
}
