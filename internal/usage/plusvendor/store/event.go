package plusstore

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"
)

type Event struct {
	RequestID              string                  `json:"request_id,omitempty"`
	EventHash              string                  `json:"event_hash"`
	TimestampMS            int64                   `json:"timestamp_ms"`
	Timestamp              string                  `json:"timestamp"`
	Provider               string                  `json:"provider,omitempty"`
	ExecutorType           string                  `json:"executor_type,omitempty"`
	Model                  string                  `json:"model"`
	RequestedModel         string                  `json:"requested_model,omitempty"`
	ResolvedModel          string                  `json:"resolved_model,omitempty"`
	Endpoint               string                  `json:"endpoint,omitempty"`
	Method                 string                  `json:"method,omitempty"`
	Path                   string                  `json:"path,omitempty"`
	AuthType               string                  `json:"auth_type,omitempty"`
	AuthIndex              string                  `json:"auth_index,omitempty"`
	Source                 string                  `json:"source,omitempty"`
	SourceHash             string                  `json:"source_hash,omitempty"`
	APIKeyHash             string                  `json:"api_key_hash,omitempty"`
	AccountSnapshot        string                  `json:"account_snapshot,omitempty"`
	AuthLabelSnapshot      string                  `json:"auth_label_snapshot,omitempty"`
	AuthFileSnapshot       string                  `json:"auth_file_snapshot,omitempty"`
	AuthProviderSnapshot   string                  `json:"auth_provider_snapshot,omitempty"`
	AuthProjectIDSnapshot  string                  `json:"auth_project_id_snapshot,omitempty"`
	AuthSnapshotAtMS       int64                   `json:"auth_snapshot_at_ms,omitempty"`
	ReasoningEffort        string                  `json:"reasoning_effort,omitempty"`
	ServiceTier            string                  `json:"service_tier,omitempty"`
	ResponseServiceTier    string                  `json:"response_service_tier,omitempty"`
	InputTokens            int64                   `json:"input_tokens"`
	OutputTokens           int64                   `json:"output_tokens"`
	ReasoningTokens        int64                   `json:"reasoning_tokens"`
	CachedTokens           int64                   `json:"cached_tokens"`
	CacheTokens            int64                   `json:"cache_tokens"`
	CacheReadTokens        int64                   `json:"cache_read_tokens"`
	CacheCreationTokens    int64                   `json:"cache_creation_tokens"`
	TotalTokens            int64                   `json:"total_tokens"`
	LatencyMS              *int64                  `json:"latency_ms,omitempty"`
	TTFTMS                 *int64                  `json:"ttft_ms,omitempty"`
	Failed                 bool                    `json:"failed"`
	FailStatusCode         int                     `json:"fail_status_code,omitempty"`
	FailSummary            string                  `json:"fail_summary,omitempty"`
	FailBody               string                  `json:"-"`
	ResponseMetadata       *ResponseHeaderMetadata `json:"response_metadata,omitempty"`
	ResponseMetadataJSON   string                  `json:"-"`
	HeaderQuotaRecoverAtMS int64                   `json:"header_quota_recover_at_ms,omitempty"`
	HeaderQuotaUsedPercent *float64                `json:"header_quota_used_percent,omitempty"`
	HeaderQuotaPlanType    string                  `json:"header_quota_plan_type,omitempty"`
	HeaderErrorKind        string                  `json:"header_error_kind,omitempty"`
	HeaderErrorCode        string                  `json:"header_error_code,omitempty"`
	HeaderTraceID          string                  `json:"header_trace_id,omitempty"`
	RawJSON                string                  `json:"raw_json,omitempty"`
	CreatedAtMS            int64                   `json:"created_at_ms"`
}

const maxFailSummaryBytes = 4096

var authorizationHeaderRegex = regexp.MustCompile(`(?i)\b(authorization\s*[:=]\s*)(?:bearer\s+)?[^\s,"'{}]+`)
var bearerTokenRegex = regexp.MustCompile(`(?i)\bbearer\s+[A-Za-z0-9._~+/=-]{8,}`)
var apiKeyTokenRegex = regexp.MustCompile(`(sk-proj-[A-Za-z0-9-_]{6,}|sk-ant-[A-Za-z0-9-_]{6,}|sk-[A-Za-z0-9-_]{6,}|sess-[A-Za-z0-9-_]{6,}|ghp_[A-Za-z0-9]{6,}|github_pat_[A-Za-z0-9_]{20,}|AIza[0-9A-Za-z-_]{8,}|hf_[A-Za-z0-9]{6,}|pk_[A-Za-z0-9]{6,}|rk_[A-Za-z0-9]{6,})`)
var tokenFieldRegex = regexp.MustCompile(`(?i)\b(access_token|refresh_token|id_token)\b(\s*["']?\s*[:=]\s*["']?)[^"',\s&}]+`)
var apiKeyFieldRegex = regexp.MustCompile(`(?i)\b(api[-_ ]?key|x-api-key)\b(\s*["']?\s*[:=]\s*["']?)[^"',\s&}]+`)
var emailRegex = regexp.MustCompile(`([A-Za-z0-9._%+\-])([A-Za-z0-9._%+\-]*)(@[A-Za-z0-9.\-]+\.[A-Za-z]{2,})`)

func NormalizeRaw(raw []byte) (Event, error) {
	var rec map[string]any
	if err := json.Unmarshal(raw, &rec); err != nil {
		return Event{}, fmt.Errorf("normalize usage event: decode JSON: %w", err)
	}
	timestampMS, timestamp := readTimestamp(rec)
	input := readInt(rec, "input_tokens", "inputTokens", "prompt_tokens", "promptTokens")
	output := readInt(rec, "output_tokens", "outputTokens", "completion_tokens", "completionTokens")
	total := readInt(rec, "total_tokens", "totalTokens", "total")
	if total <= 0 {
		total = input + output
	}
	sourceRaw := readString(rec, "source", "api_key", "apiKey", "key", "account", "email")
	model := readString(rec, "model", "model_name", "modelName", "resolved_model", "resolvedModel")
	event := Event{
		RequestID:     readString(rec, "request_id", "requestId", "id"),
		TimestampMS:   timestampMS,
		Timestamp:     timestamp,
		Provider:      readString(rec, "provider", "type", "auth_type", "authType"),
		Model:         model,
		ResolvedModel: model,
		Endpoint:      firstNonEmptyString(readString(rec, "endpoint", "api", "request", "operation"), "-"),
		Source:        MaskSource(sourceRaw),
		SourceHash:    HashString(sourceRaw),
		APIKeyHash:    HashString(readString(rec, "api_key", "apiKey", "key")),
		InputTokens:   input,
		OutputTokens:  output,
		TotalTokens:   total,
		Failed:        readFailed(rec),
		RawJSON:       SafeRawJSON(string(raw)),
		CreatedAtMS:   time.Now().UnixMilli(),
	}
	if event.Model == "" {
		event.Model = "-"
	}
	AttachResponseHeaderMetadata(&event, ResponseHeaderMetadataFromRecord(rec, time.UnixMilli(timestampMS)))
	event.EventHash = BuildEventHash(event)
	return event, nil
}

func CompatibleCachedTokens(cachedTokens, cacheTokens, cacheReadTokens, cacheCreationTokens int64) int64 {
	cached := cachedTokens
	if cacheTokens > cached {
		cached = cacheTokens
	}
	fine := maxInt64(cacheReadTokens, 0) + maxInt64(cacheCreationTokens, 0)
	if cached <= fine {
		return 0
	}
	return cached - fine
}

func FailSummaryFromBody(body string) string {
	summary := strings.TrimSpace(body)
	if summary == "" {
		return ""
	}
	summary = authorizationHeaderRegex.ReplaceAllString(summary, `${1}[redacted]`)
	summary = bearerTokenRegex.ReplaceAllString(summary, `Bearer [redacted]`)
	summary = tokenFieldRegex.ReplaceAllString(summary, `${1}${2}[redacted]`)
	summary = apiKeyFieldRegex.ReplaceAllString(summary, `${1}${2}[redacted]`)
	summary = apiKeyTokenRegex.ReplaceAllString(summary, `[redacted]`)
	summary = emailRegex.ReplaceAllString(summary, `${1}***${3}`)
	return truncateUTF8Bytes(strings.TrimSpace(summary), maxFailSummaryBytes)
}

func SafeRawJSON(raw string) string {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		return ""
	}
	var payload any
	if err := json.Unmarshal([]byte(trimmed), &payload); err == nil {
		if redacted, err := json.Marshal(redactValue(payload)); err == nil {
			return string(redacted)
		}
	}
	return FailSummaryFromBody(trimmed)
}

func MaskSource(value string) string {
	trimmed := strings.TrimSpace(value)
	if trimmed == "" {
		return ""
	}
	if strings.Contains(trimmed, "@") {
		parts := strings.SplitN(trimmed, "@", 2)
		prefix := parts[0]
		if len(prefix) > 3 {
			prefix = prefix[:3]
		}
		return prefix + "***@" + parts[1]
	}
	if strings.HasPrefix(trimmed, "sk-") || strings.HasPrefix(trimmed, "AIza") || len(trimmed) >= 32 {
		if len(trimmed) <= 8 {
			return "m:****"
		}
		return "m:" + trimmed[:4] + "..." + trimmed[len(trimmed)-4:]
	}
	return trimmed
}

func HashString(value string) string {
	trimmed := strings.TrimSpace(value)
	if trimmed == "" {
		return ""
	}
	sum := sha256.Sum256([]byte(trimmed))
	return hex.EncodeToString(sum[:])
}

func BuildEventHash(event Event) string {
	parts := []string{
		event.RequestID,
		event.Timestamp,
		event.Endpoint,
		event.Model,
		event.AuthIndex,
		event.SourceHash,
		strconv.FormatInt(event.InputTokens, 10),
		strconv.FormatInt(event.OutputTokens, 10),
		strconv.FormatBool(event.Failed),
	}
	return HashString(strings.Join(parts, "|"))
}

func readTimestamp(rec map[string]any) (int64, string) {
	raw := first(rec, "timestamp", "time", "created_at", "createdAt")
	now := time.Now()
	switch value := raw.(type) {
	case float64:
		ms := int64(value)
		if ms < 10000000000 {
			ms *= 1000
		}
		return ms, time.UnixMilli(ms).UTC().Format(time.RFC3339Nano)
	case string:
		text := strings.TrimSpace(value)
		if n, err := strconv.ParseInt(text, 10, 64); err == nil {
			if n < 10000000000 {
				n *= 1000
			}
			return n, time.UnixMilli(n).UTC().Format(time.RFC3339Nano)
		}
		if parsed, err := time.Parse(time.RFC3339Nano, text); err == nil {
			return parsed.UnixMilli(), parsed.UTC().Format(time.RFC3339Nano)
		}
	}
	return now.UnixMilli(), now.UTC().Format(time.RFC3339Nano)
}

func readString(rec map[string]any, keys ...string) string {
	raw := first(rec, keys...)
	switch value := raw.(type) {
	case string:
		return strings.TrimSpace(value)
	case float64:
		return strconv.FormatFloat(value, 'f', -1, 64)
	case json.Number:
		return value.String()
	case nil:
		return ""
	default:
		return strings.TrimSpace(fmt.Sprint(value))
	}
}

func readInt(rec map[string]any, keys ...string) int64 {
	raw := first(rec, keys...)
	switch value := raw.(type) {
	case float64:
		return int64(value)
	case int64:
		return value
	case int:
		return int64(value)
	case json.Number:
		n, _ := value.Int64()
		return n
	case string:
		n, _ := strconv.ParseInt(strings.TrimSpace(value), 10, 64)
		return n
	default:
		return 0
	}
}

func first(rec map[string]any, keys ...string) any {
	for _, key := range keys {
		if value, ok := rec[key]; ok {
			return value
		}
	}
	return nil
}

func readFailed(rec map[string]any) bool {
	if value, ok := first(rec, "failed", "is_failed", "isFailed").(bool); ok {
		return value
	}
	if value, ok := first(rec, "success", "ok").(bool); ok {
		return !value
	}
	return readInt(rec, "status", "status_code", "statusCode") >= 400 || first(rec, "error", "error_message", "errorMessage") != nil
}

func truncateUTF8Bytes(value string, maxBytes int) string {
	if maxBytes <= 0 || len(value) <= maxBytes {
		return value
	}
	var builder strings.Builder
	for _, r := range value {
		size := utf8.RuneLen(r)
		if size < 0 {
			size = len(string(r))
		}
		if builder.Len()+size > maxBytes {
			break
		}
		builder.WriteRune(r)
	}
	return strings.TrimSpace(builder.String()) + "..."
}

func redactValue(value any) any {
	switch item := value.(type) {
	case map[string]any:
		out := make(map[string]any, len(item))
		for key, v := range item {
			if isSecretKey(key) {
				out[key] = "[redacted]"
			} else if isPIIKey(key) {
				out[key] = FailSummaryFromBody(fmt.Sprint(v))
			} else {
				out[key] = redactValue(v)
			}
		}
		return out
	case []any:
		out := make([]any, 0, len(item))
		for _, v := range item {
			out = append(out, redactValue(v))
		}
		return out
	default:
		return value
	}
}

func isSecretKey(key string) bool {
	normalized := strings.ToLower(strings.ReplaceAll(strings.TrimSpace(key), "-", "_"))
	return normalized == "api_key" ||
		normalized == "apikey" ||
		normalized == "authorization" ||
		normalized == "cookie" ||
		normalized == "set_cookie" ||
		normalized == "access_token" ||
		normalized == "refresh_token" ||
		normalized == "id_token" ||
		normalized == "token" ||
		strings.Contains(normalized, "secret")
}

func isPIIKey(key string) bool {
	normalized := strings.ToLower(strings.ReplaceAll(strings.TrimSpace(key), "-", "_"))
	return normalized == "email" || normalized == "account" || normalized == "user_email"
}

func maxInt64(left, right int64) int64 {
	if left > right {
		return left
	}
	return right
}
