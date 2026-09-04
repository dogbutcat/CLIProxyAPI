package hub

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"math"
	"net/http"
	"strings"
	"time"
)

const (
	codexManualQueryProvider     = "codex"
	codexManualQueryHost         = "chatgpt.com"
	codexManualQueryPath         = "/backend-api/wham/usage"
	codexServerDateSkewTolerance = 5 * time.Minute
	codexMaxDurationWholeSeconds = int64(math.MaxInt64) / int64(time.Second)
)

var errCodexQuotaResponseInvalid = errors.New("quota hub: invalid Codex quota response")

type codexUsagePayload struct {
	RateLimit json.RawMessage `json:"rate_limit"`
}

type codexRateLimitPayload struct {
	Allowed         json.RawMessage `json:"allowed"`
	LimitReached    json.RawMessage `json:"limit_reached"`
	PrimaryWindow   json.RawMessage `json:"primary_window"`
	SecondaryWindow json.RawMessage `json:"secondary_window"`
}

type codexWindowPayload struct {
	UsedPercent       json.RawMessage `json:"used_percent"`
	ResetAt           json.RawMessage `json:"reset_at"`
	ResetAfterSeconds json.RawMessage `json:"reset_after_seconds"`
}

type codexCoreWindow struct {
	usedPercent       float64
	resetAt           json.RawMessage
	resetAfterSeconds json.RawMessage
}

func codexManualQueryAdapter() manualQueryAdapter {
	return manualQueryAdapter{
		provider: codexManualQueryProvider,
		match:    matchCodexManualQuery,
		observe:  observeCodexManualQuery,
	}
}

func matchCodexManualQuery(query manualQueryMetadata) bool {
	if query.Method != http.MethodGet || !strings.EqualFold(query.Scheme, "https") ||
		!strings.EqualFold(query.Host, codexManualQueryHost) ||
		!strings.EqualFold(query.Hostname, codexManualQueryHost) || query.Port != "" ||
		query.Path != codexManualQueryPath || query.RawQuery != "" {
		return false
	}
	return query.EscapedPath == "" || query.EscapedPath == codexManualQueryPath
}

func observeCodexManualQuery(metadata manualResponseMetadata, body io.Reader) (Observation, error) {
	var payload codexUsagePayload
	decoder := json.NewDecoder(body)
	if err := decoder.Decode(&payload); err != nil {
		return Observation{}, errCodexQuotaResponseInvalid
	}
	if err := requireCodexJSONEnd(decoder); err != nil {
		return Observation{}, errCodexQuotaResponseInvalid
	}

	var rateLimit codexRateLimitPayload
	if !codexDecodeJSONObject(payload.RateLimit, &rateLimit) {
		return Observation{}, errCodexQuotaResponseInvalid
	}

	primary, primaryPresent, err := parseCodexCoreWindow(rateLimit.PrimaryWindow)
	if err != nil {
		return Observation{}, err
	}
	secondary, secondaryPresent, err := parseCodexCoreWindow(rateLimit.SecondaryWindow)
	if err != nil {
		return Observation{}, err
	}
	if !primaryPresent && !secondaryPresent {
		return Observation{}, errCodexQuotaResponseInvalid
	}

	windows := make([]codexCoreWindow, 0, 2)
	if primaryPresent {
		windows = append(windows, primary)
	}
	if secondaryPresent {
		windows = append(windows, secondary)
	}
	maxUsedPercent := windows[0].usedPercent
	for _, window := range windows[1:] {
		maxUsedPercent = max(maxUsedPercent, window.usedPercent)
	}
	score := 100 - maxUsedPercent
	observation := Observation{
		Score:        &score,
		Completeness: ScoreOnly,
	}

	allowed, allowedPresent, err := parseCodexOptionalBool(rateLimit.Allowed)
	if err != nil {
		return Observation{}, err
	}
	limitReached, limitReachedPresent, err := parseCodexOptionalBool(rateLimit.LimitReached)
	if err != nil {
		return Observation{}, err
	}
	if !primaryPresent || !allowedPresent || !limitReachedPresent {
		return observation, nil
	}

	globalExhaustion := !allowed || limitReached
	exhausted := globalExhaustion
	for _, window := range windows {
		if window.usedPercent == 100 {
			exhausted = true
		}
	}
	if !exhausted {
		observation.Completeness = AuthoritativeSnapshot
		observation.Mutations = []Mutation{{Scope: ScopeAuth, Outcome: Healthy}}
		return observation, nil
	}

	var latestReset time.Time
	for _, window := range windows {
		if !globalExhaustion && window.usedPercent != 100 {
			continue
		}
		if resetAt, ok := codexWindowReset(metadata, window); ok && resetAt.After(latestReset) {
			latestReset = resetAt
		}
	}
	if latestReset.IsZero() {
		return observation, nil
	}
	observation.Completeness = ExhaustionEvidence
	observation.Mutations = []Mutation{{Scope: ScopeAuth, Outcome: Exhausted, ResetAt: latestReset}}
	return observation, nil
}

func parseCodexCoreWindow(raw json.RawMessage) (codexCoreWindow, bool, error) {
	if codexJSONMissingOrNull(raw) {
		return codexCoreWindow{}, false, nil
	}
	var payload codexWindowPayload
	if !codexDecodeJSONObject(raw, &payload) {
		return codexCoreWindow{}, false, errCodexQuotaResponseInvalid
	}
	usedPercent, ok := parseCodexNumber(payload.UsedPercent)
	if !ok || usedPercent < 0 || usedPercent > 100 {
		return codexCoreWindow{}, false, errCodexQuotaResponseInvalid
	}
	return codexCoreWindow{
		usedPercent:       usedPercent,
		resetAt:           payload.ResetAt,
		resetAfterSeconds: payload.ResetAfterSeconds,
	}, true, nil
}

func parseCodexOptionalBool(raw json.RawMessage) (bool, bool, error) {
	if codexJSONMissingOrNull(raw) {
		return false, false, nil
	}
	var value bool
	if err := json.Unmarshal(raw, &value); err != nil {
		return false, false, errCodexQuotaResponseInvalid
	}
	return value, true, nil
}

func codexWindowReset(metadata manualResponseMetadata, window codexCoreWindow) (time.Time, bool) {
	if resetAt, ok := parseCodexUnixTime(window.resetAt); ok && resetAt.After(metadata.CompletedAt) {
		return resetAt, true
	}
	resetAfterSeconds, ok := parseCodexNumber(window.resetAfterSeconds)
	if !ok {
		return time.Time{}, false
	}
	resetAfter, ok := codexDurationFromSeconds(resetAfterSeconds)
	if !ok {
		return time.Time{}, false
	}
	resetAt := codexRelativeResetAnchor(metadata).Add(resetAfter)
	return resetAt, resetAt.After(metadata.CompletedAt)
}

func codexRelativeResetAnchor(metadata manualResponseMetadata) time.Time {
	if metadata.ServerDate.IsZero() || metadata.CompletedAt.IsZero() {
		return metadata.CompletedAt
	}
	skew := metadata.ServerDate.Sub(metadata.CompletedAt)
	if skew < -codexServerDateSkewTolerance || skew > codexServerDateSkewTolerance {
		return metadata.CompletedAt
	}
	return metadata.ServerDate
}

func codexDurationFromSeconds(seconds float64) (time.Duration, bool) {
	if math.IsNaN(seconds) || math.IsInf(seconds, 0) || seconds <= 0 {
		return 0, false
	}
	wholeSeconds, fractionalSeconds := math.Modf(seconds)
	if wholeSeconds > float64(codexMaxDurationWholeSeconds) {
		return 0, false
	}
	wholeDuration := time.Duration(int64(wholeSeconds)) * time.Second
	fractionalDuration := time.Duration(fractionalSeconds * float64(time.Second))
	if fractionalDuration > time.Duration(math.MaxInt64)-wholeDuration {
		return 0, false
	}
	return wholeDuration + fractionalDuration, true
}

func parseCodexUnixTime(raw json.RawMessage) (time.Time, bool) {
	seconds, ok := parseCodexNumber(raw)
	if !ok || seconds <= 0 || seconds >= float64(math.MaxInt64) {
		return time.Time{}, false
	}
	wholeSeconds, fractionalSeconds := math.Modf(seconds)
	return time.Unix(int64(wholeSeconds), int64(fractionalSeconds*float64(time.Second))).UTC(), true
}

func parseCodexNumber(raw json.RawMessage) (float64, bool) {
	if codexJSONMissingOrNull(raw) {
		return 0, false
	}
	var value float64
	if err := json.Unmarshal(raw, &value); err != nil || math.IsNaN(value) || math.IsInf(value, 0) {
		return 0, false
	}
	return value, true
}

func codexDecodeJSONObject(raw json.RawMessage, destination any) bool {
	trimmed := bytes.TrimSpace(raw)
	return len(trimmed) >= 2 && trimmed[0] == '{' && trimmed[len(trimmed)-1] == '}' &&
		json.Unmarshal(trimmed, destination) == nil
}

func codexJSONMissingOrNull(raw json.RawMessage) bool {
	trimmed := bytes.TrimSpace(raw)
	return len(trimmed) == 0 || bytes.Equal(trimmed, []byte("null"))
}

func requireCodexJSONEnd(decoder *json.Decoder) error {
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		return errCodexQuotaResponseInvalid
	}
	return nil
}
