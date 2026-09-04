package plusstore

import (
	"encoding/json"
	"math"
	"strconv"
	"strings"
	"time"
)

type ResponseHeaderMetadata struct {
	Quota    *HeaderQuotaMetadata `json:"quota,omitempty"`
	Errors   *HeaderErrorMetadata `json:"errors,omitempty"`
	Trace    *HeaderTraceMetadata `json:"trace,omitempty"`
	Response *HeaderResponseShape `json:"response,omitempty"`
}

type HeaderQuotaMetadata struct {
	PlanType    string   `json:"plan_type,omitempty"`
	RecoverAtMS int64    `json:"recover_at_ms,omitempty"`
	UsedPercent *float64 `json:"used_percent,omitempty"`
}

type HeaderErrorMetadata struct {
	Kind string `json:"kind,omitempty"`
	Code string `json:"code,omitempty"`
}

type HeaderTraceMetadata struct {
	PrimaryTraceID string `json:"primary_trace_id,omitempty"`
	RequestID      string `json:"request_id,omitempty"`
}

type HeaderResponseShape struct {
	ContentType   string `json:"content_type,omitempty"`
	ContentLength *int64 `json:"content_length,omitempty"`
}

type ResponseHeaderDerived struct {
	MetadataJSON     string
	QuotaRecoverAtMS int64
	QuotaUsedPercent *float64
	QuotaPlanType    string
	ErrorKind        string
	ErrorCode        string
	TraceID          string
}

func ResponseHeaderMetadataFromRecord(rec map[string]any, base time.Time) *ResponseHeaderMetadata {
	if rec == nil {
		return nil
	}
	if raw := first(rec, "response_metadata", "responseMetadata"); raw != nil {
		if metadata := responseHeaderMetadataFromAny(raw); metadata != nil {
			return metadata
		}
	}
	return ParseResponseHeaderMetadata(first(rec, "response_headers", "responseHeaders", "headers"), base)
}

func ParseResponseHeaderMetadata(raw any, base time.Time) *ResponseHeaderMetadata {
	headers := normalizeResponseHeaders(raw)
	if len(headers) == 0 {
		return nil
	}
	metadata := &ResponseHeaderMetadata{
		Quota:    parseQuotaHeaders(headers, base),
		Errors:   parseErrorHeaders(headers),
		Trace:    parseTraceHeaders(headers),
		Response: parseResponseHeaders(headers),
	}
	if metadata.isEmpty() {
		return nil
	}
	return metadata
}

func ResponseHeaderMetadataFromJSON(raw string) *ResponseHeaderMetadata {
	if strings.TrimSpace(raw) == "" {
		return nil
	}
	var metadata ResponseHeaderMetadata
	if err := json.Unmarshal([]byte(raw), &metadata); err == nil && !metadata.isEmpty() {
		return &metadata
	}
	var headers map[string]any
	if err := json.Unmarshal([]byte(raw), &headers); err != nil {
		return nil
	}
	return ParseResponseHeaderMetadata(headers, time.Time{})
}

func DeriveResponseHeaderMetadata(metadata *ResponseHeaderMetadata) ResponseHeaderDerived {
	if metadata == nil || metadata.isEmpty() {
		return ResponseHeaderDerived{}
	}
	raw, err := json.Marshal(metadata)
	if err != nil {
		return ResponseHeaderDerived{}
	}
	derived := ResponseHeaderDerived{MetadataJSON: string(raw)}
	if metadata.Quota != nil {
		derived.QuotaRecoverAtMS = metadata.Quota.RecoverAtMS
		derived.QuotaUsedPercent = metadata.Quota.UsedPercent
		derived.QuotaPlanType = metadata.Quota.PlanType
	}
	if metadata.Errors != nil {
		derived.ErrorKind = metadata.Errors.Kind
		derived.ErrorCode = metadata.Errors.Code
	}
	if metadata.Trace != nil {
		derived.TraceID = firstNonEmptyString(metadata.Trace.PrimaryTraceID, metadata.Trace.RequestID)
	}
	return derived
}

func AttachResponseHeaderMetadata(event *Event, metadata *ResponseHeaderMetadata) {
	if event == nil || metadata == nil || metadata.isEmpty() {
		return
	}
	derived := DeriveResponseHeaderMetadata(metadata)
	event.ResponseMetadata = metadata
	event.ResponseMetadataJSON = derived.MetadataJSON
	event.HeaderQuotaRecoverAtMS = derived.QuotaRecoverAtMS
	event.HeaderQuotaUsedPercent = derived.QuotaUsedPercent
	event.HeaderQuotaPlanType = derived.QuotaPlanType
	event.HeaderErrorKind = derived.ErrorKind
	event.HeaderErrorCode = derived.ErrorCode
	event.HeaderTraceID = derived.TraceID
}

func normalizeResponseHeaders(raw any) map[string][]string {
	rec, ok := raw.(map[string]any)
	if !ok {
		return nil
	}
	out := map[string][]string{}
	for key, value := range rec {
		normalizedKey := strings.ToLower(strings.TrimSpace(key))
		if !isResponseHeaderAllowed(normalizedKey) {
			continue
		}
		if scalar := scalarHeaderValue(value); scalar != "" {
			out[normalizedKey] = append(out[normalizedKey], scalar)
		}
	}
	return out
}

func isResponseHeaderAllowed(key string) bool {
	if key == "set-cookie" ||
		strings.Contains(key, "token") ||
		strings.Contains(key, "secret") ||
		(strings.Contains(key, "authorization") && key != "x-openai-authorization-error") {
		return false
	}
	switch key {
	case "x-codex-plan-type",
		"x-codex-primary-used-percent",
		"x-codex-primary-reset-at",
		"retry-after",
		"x-openai-authorization-error",
		"x-openai-ide-error-code",
		"x-openai-ide-root-error-code",
		"x-ratelimit-bypass",
		"x-oai-request-id",
		"x-request-id",
		"content-type",
		"content-length":
		return true
	default:
		return false
	}
}

func scalarHeaderValue(raw any) string {
	switch value := raw.(type) {
	case string:
		return strings.TrimSpace(value)
	case []any:
		if len(value) > 0 {
			return scalarHeaderValue(value[0])
		}
		return ""
	case []string:
		if len(value) > 0 {
			return strings.TrimSpace(value[0])
		}
		return ""
	case float64:
		if value == math.Trunc(value) {
			return strconv.FormatInt(int64(value), 10)
		}
		return strconv.FormatFloat(value, 'f', -1, 64)
	case int:
		return strconv.Itoa(value)
	case int64:
		return strconv.FormatInt(value, 10)
	case bool:
		return strconv.FormatBool(value)
	default:
		return ""
	}
}

func parseQuotaHeaders(headers map[string][]string, base time.Time) *HeaderQuotaMetadata {
	quota := &HeaderQuotaMetadata{PlanType: normalizeHeaderValue(headerFirst(headers, "x-codex-plan-type"))}
	if value, ok := parseFloatHeader(headerFirst(headers, "x-codex-primary-used-percent")); ok {
		quota.UsedPercent = &value
	}
	if at, ok := parseHeaderTime(headerFirst(headers, "x-codex-primary-reset-at"), base); ok {
		quota.RecoverAtMS = at.UnixMilli()
	}
	if seconds, ok := parseFloatHeader(headerFirst(headers, "retry-after")); ok && !base.IsZero() && quota.RecoverAtMS == 0 {
		quota.RecoverAtMS = base.Add(time.Duration(seconds * float64(time.Second))).UnixMilli()
	}
	if quota.isEmpty() {
		return nil
	}
	return quota
}

func parseErrorHeaders(headers map[string][]string) *HeaderErrorMetadata {
	errMeta := &HeaderErrorMetadata{
		Code: firstNonEmptyString(
			headerFirst(headers, "x-openai-ide-root-error-code"),
			headerFirst(headers, "x-openai-ide-error-code"),
			headerFirst(headers, "x-openai-authorization-error"),
			headerFirst(headers, "x-ratelimit-bypass"),
		),
	}
	switch strings.ToLower(errMeta.Code) {
	case "token_revoked", "token_invalidated", "account_deactivated", "401":
		errMeta.Kind = "auth"
	case "identity_edge_internal_error":
		errMeta.Kind = "identity"
	case "":
	default:
		errMeta.Kind = "rate_limit"
	}
	if errMeta.isEmpty() {
		return nil
	}
	return errMeta
}

func parseTraceHeaders(headers map[string][]string) *HeaderTraceMetadata {
	trace := &HeaderTraceMetadata{
		PrimaryTraceID: firstNonEmptyString(headerFirst(headers, "x-oai-request-id"), headerFirst(headers, "x-request-id")),
		RequestID:      headerFirst(headers, "x-request-id"),
	}
	if trace.isEmpty() {
		return nil
	}
	return trace
}

func parseResponseHeaders(headers map[string][]string) *HeaderResponseShape {
	response := &HeaderResponseShape{ContentType: normalizeHeaderValue(headerFirst(headers, "content-type"))}
	if value, ok := parseIntHeader(headerFirst(headers, "content-length")); ok {
		response.ContentLength = &value
	}
	if response.isEmpty() {
		return nil
	}
	return response
}

func headerFirst(headers map[string][]string, keys ...string) string {
	for _, key := range keys {
		for _, value := range headers[strings.ToLower(key)] {
			if strings.TrimSpace(value) != "" {
				return strings.TrimSpace(value)
			}
		}
	}
	return ""
}

func responseHeaderMetadataFromAny(raw any) *ResponseHeaderMetadata {
	data, err := json.Marshal(raw)
	if err != nil {
		return nil
	}
	var metadata ResponseHeaderMetadata
	if err := json.Unmarshal(data, &metadata); err != nil || metadata.isEmpty() {
		return nil
	}
	return &metadata
}

func normalizeHeaderValue(value string) string {
	return truncateUTF8Bytes(FailSummaryFromBody(strings.TrimSpace(value)), 1024)
}

func parseFloatHeader(value string) (float64, bool) {
	text := strings.TrimSpace(strings.TrimSuffix(value, "%"))
	if text == "" {
		return 0, false
	}
	parsed, err := strconv.ParseFloat(text, 64)
	if err != nil || math.IsNaN(parsed) || math.IsInf(parsed, 0) {
		return 0, false
	}
	return parsed, true
}

func parseIntHeader(value string) (int64, bool) {
	parsed, ok := parseFloatHeader(value)
	return int64(parsed), ok
}

func parseHeaderTime(value string, base time.Time) (time.Time, bool) {
	text := strings.TrimSpace(value)
	if text == "" {
		return time.Time{}, false
	}
	for _, layout := range []string{time.RFC3339Nano, time.RFC3339, time.RFC1123, time.RFC1123Z} {
		if parsed, err := time.Parse(layout, text); err == nil {
			return parsed, true
		}
	}
	n, err := strconv.ParseFloat(text, 64)
	if err != nil || n <= 0 {
		return time.Time{}, false
	}
	if n > 1_000_000_000_000 {
		return time.UnixMilli(int64(n)), true
	}
	if n > 1_000_000_000 {
		return time.Unix(int64(n), 0), true
	}
	if !base.IsZero() {
		return base.Add(time.Duration(n * float64(time.Second))), true
	}
	return time.Time{}, false
}

func firstNonEmptyString(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return strings.TrimSpace(value)
		}
	}
	return ""
}

func (m *ResponseHeaderMetadata) isEmpty() bool {
	return m == nil || (m.Quota == nil && m.Errors == nil && m.Trace == nil && m.Response == nil)
}

func (q *HeaderQuotaMetadata) isEmpty() bool {
	return q == nil || (q.PlanType == "" && q.RecoverAtMS == 0 && q.UsedPercent == nil)
}

func (e *HeaderErrorMetadata) isEmpty() bool {
	return e == nil || (e.Kind == "" && e.Code == "")
}

func (t *HeaderTraceMetadata) isEmpty() bool {
	return t == nil || (t.PrimaryTraceID == "" && t.RequestID == "")
}

func (r *HeaderResponseShape) isEmpty() bool {
	return r == nil || (r.ContentType == "" && r.ContentLength == nil)
}
