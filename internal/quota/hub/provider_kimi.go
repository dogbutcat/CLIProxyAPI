package hub

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"math"
	"math/big"
	"net/http"
	"strings"
	"time"
)

const (
	kimiManualQueryProvider     = "kimi"
	kimiManualQueryHost         = "api.kimi.com"
	kimiManualQueryPath         = "/coding/v1/usages"
	kimiUnixMillisecondsCutover = 1e11
	kimiMaxDurationWholeSeconds = int64(math.MaxInt64) / int64(time.Second)
)

var errKimiQuotaResponseInvalid = errors.New("quota hub: invalid Kimi quota response")

func kimiManualQueryAdapter() manualQueryAdapter {
	return manualQueryAdapter{
		provider: kimiManualQueryProvider,
		match:    matchKimiManualQuery,
		observe:  observeKimiManualQuery,
	}
}

func matchKimiManualQuery(query manualQueryMetadata) bool {
	if query.Method != http.MethodGet || !strings.EqualFold(query.Scheme, "https") ||
		!strings.EqualFold(query.Host, kimiManualQueryHost) ||
		!strings.EqualFold(query.Hostname, kimiManualQueryHost) || query.Port != "" ||
		query.Path != kimiManualQueryPath || query.RawQuery != "" {
		return false
	}
	return query.EscapedPath == "" || query.EscapedPath == kimiManualQueryPath
}

func observeKimiManualQuery(metadata manualResponseMetadata, body io.Reader) (Observation, error) {
	var payload map[string]json.RawMessage
	decoder := json.NewDecoder(body)
	if err := decoder.Decode(&payload); err != nil {
		return Observation{}, errKimiQuotaResponseInvalid
	}
	if err := requireKimiJSONEnd(decoder); err != nil {
		return Observation{}, errKimiQuotaResponseInvalid
	}

	usageRaw, exists := payload["usage"]
	if !exists {
		return Observation{}, errKimiQuotaResponseInvalid
	}
	var usage map[string]json.RawMessage
	if !kimiDecodeJSONObject(usageRaw, &usage) {
		return Observation{}, errKimiQuotaResponseInvalid
	}

	limit, err := parseRequiredKimiNumber(usage, "limit")
	if err != nil || limit.Sign() <= 0 {
		return Observation{}, errKimiQuotaResponseInvalid
	}
	used, usedPresent, err := parseOptionalKimiNumber(usage, "used")
	if err != nil {
		return Observation{}, errKimiQuotaResponseInvalid
	}
	remaining, remainingPresent, err := parseOptionalKimiNumber(usage, "remaining")
	if err != nil || (!usedPresent && !remainingPresent) {
		return Observation{}, errKimiQuotaResponseInvalid
	}

	switch {
	case usedPresent && remainingPresent:
		if new(big.Rat).Add(used, remaining).Cmp(limit) != 0 {
			return Observation{}, errKimiQuotaResponseInvalid
		}
	case usedPresent:
		remaining = new(big.Rat).Sub(limit, used)
	case remainingPresent:
		used = new(big.Rat).Sub(limit, remaining)
	}
	if used.Sign() < 0 || used.Cmp(limit) > 0 || remaining.Sign() < 0 || remaining.Cmp(limit) > 0 {
		return Observation{}, errKimiQuotaResponseInvalid
	}

	scoreRatio := new(big.Rat).Quo(new(big.Rat).Set(remaining), limit)
	score, _ := new(big.Rat).Mul(scoreRatio, big.NewRat(100, 1)).Float64()
	if math.IsNaN(score) || math.IsInf(score, 0) || score < 0 || score > 100 {
		return Observation{}, errKimiQuotaResponseInvalid
	}
	observation := Observation{Score: &score, Completeness: ScoreOnly}
	exhausted := remaining.Sign() == 0 || used.Cmp(limit) == 0
	if !exhausted {
		observation.Completeness = AuthoritativeSnapshot
		observation.Mutations = []Mutation{{Scope: ScopeAuth, Outcome: Healthy}}
		return observation, nil
	}

	resetAt, ok := kimiFutureReset(usage, metadata.CompletedAt)
	if !ok {
		return observation, nil
	}
	observation.Completeness = ExhaustionEvidence
	observation.Mutations = []Mutation{{Scope: ScopeAuth, Outcome: Exhausted, ResetAt: resetAt}}
	return observation, nil
}

func parseRequiredKimiNumber(payload map[string]json.RawMessage, key string) (*big.Rat, error) {
	value, present, err := parseOptionalKimiNumber(payload, key)
	if err != nil || !present {
		return nil, errKimiQuotaResponseInvalid
	}
	return value, nil
}

func parseOptionalKimiNumber(payload map[string]json.RawMessage, key string) (*big.Rat, bool, error) {
	raw, exists := payload[key]
	if !exists {
		return nil, false, nil
	}
	value, ok := parseKimiNumber(raw)
	if !ok {
		return nil, false, errKimiQuotaResponseInvalid
	}
	return value, true, nil
}

func parseKimiNumber(raw json.RawMessage) (*big.Rat, bool) {
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) == 0 || bytes.Equal(trimmed, []byte("null")) {
		return nil, false
	}

	lexeme := string(trimmed)
	if trimmed[0] == '"' {
		var numericString string
		if err := json.Unmarshal(trimmed, &numericString); err != nil {
			return nil, false
		}
		lexeme = strings.TrimSpace(numericString)
		if lexeme == "" {
			return nil, false
		}
	}

	var jsonNumber json.Number
	if err := json.Unmarshal([]byte(lexeme), &jsonNumber); err != nil || jsonNumber.String() != lexeme {
		return nil, false
	}
	floatValue, err := jsonNumber.Float64()
	if err != nil || math.IsNaN(floatValue) || math.IsInf(floatValue, 0) {
		return nil, false
	}
	exact, ok := new(big.Rat).SetString(lexeme)
	if !ok {
		return nil, false
	}
	return exact, true
}

func kimiFutureReset(payload map[string]json.RawMessage, completedAt time.Time) (time.Time, bool) {
	for _, key := range []string{"reset_at", "resetAt", "reset_time", "resetTime"} {
		raw, exists := payload[key]
		if !exists {
			continue
		}
		resetAt, ok := parseKimiAbsoluteReset(raw)
		if !ok {
			continue
		}
		return resetAt, resetAt.After(completedAt)
	}

	for _, key := range []string{"reset_in", "resetIn", "ttl"} {
		raw, exists := payload[key]
		if !exists {
			continue
		}
		seconds, ok := parseKimiNumber(raw)
		if !ok || seconds.Sign() <= 0 {
			continue
		}
		secondsFloat, _ := seconds.Float64()
		duration, ok := kimiDurationFromSeconds(secondsFloat)
		if !ok {
			continue
		}
		resetAt := completedAt.Add(duration)
		return resetAt, resetAt.After(completedAt)
	}
	return time.Time{}, false
}

func parseKimiAbsoluteReset(raw json.RawMessage) (time.Time, bool) {
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) == 0 || bytes.Equal(trimmed, []byte("null")) {
		return time.Time{}, false
	}
	if trimmed[0] == '"' {
		var value string
		if err := json.Unmarshal(trimmed, &value); err != nil {
			return time.Time{}, false
		}
		value = strings.TrimSpace(value)
		if value == "" {
			return time.Time{}, false
		}
		if resetAt, err := time.Parse(time.RFC3339Nano, normalizeKimiTimestampPrecision(value)); err == nil {
			return resetAt, true
		}
	}

	numeric, ok := parseKimiNumber(trimmed)
	if !ok || numeric.Sign() <= 0 {
		return time.Time{}, false
	}
	value, _ := numeric.Float64()
	if value >= kimiUnixMillisecondsCutover {
		value /= float64(time.Second / time.Millisecond)
	}
	if math.IsNaN(value) || math.IsInf(value, 0) || value <= 0 || value >= float64(math.MaxInt64) {
		return time.Time{}, false
	}
	wholeSeconds, fractionalSeconds := math.Modf(value)
	return time.Unix(int64(wholeSeconds), int64(fractionalSeconds*float64(time.Second))).UTC(), true
}

func normalizeKimiTimestampPrecision(value string) string {
	fractionStart := strings.IndexByte(value, '.')
	if fractionStart < 0 {
		return value
	}
	fractionEnd := fractionStart + 1
	for fractionEnd < len(value) && value[fractionEnd] >= '0' && value[fractionEnd] <= '9' {
		fractionEnd++
	}
	if fractionEnd-(fractionStart+1) <= 6 {
		return value
	}
	return value[:fractionStart+7] + value[fractionEnd:]
}

func kimiDurationFromSeconds(seconds float64) (time.Duration, bool) {
	if math.IsNaN(seconds) || math.IsInf(seconds, 0) || seconds <= 0 {
		return 0, false
	}
	wholeSeconds, fractionalSeconds := math.Modf(seconds)
	if wholeSeconds > float64(kimiMaxDurationWholeSeconds) {
		return 0, false
	}
	wholeDuration := time.Duration(int64(wholeSeconds)) * time.Second
	fractionalDuration := time.Duration(fractionalSeconds * float64(time.Second))
	if fractionalDuration > time.Duration(math.MaxInt64)-wholeDuration {
		return 0, false
	}
	return wholeDuration + fractionalDuration, true
}

func kimiDecodeJSONObject(raw json.RawMessage, destination any) bool {
	trimmed := bytes.TrimSpace(raw)
	return len(trimmed) >= 2 && trimmed[0] == '{' && trimmed[len(trimmed)-1] == '}' &&
		json.Unmarshal(trimmed, destination) == nil
}

func requireKimiJSONEnd(decoder *json.Decoder) error {
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		return errKimiQuotaResponseInvalid
	}
	return nil
}
