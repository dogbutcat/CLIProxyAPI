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
	claudeManualQueryProvider = "claude"
	claudeManualQueryHost     = "api.anthropic.com"
	claudeManualQueryPath     = "/api/oauth/usage"
)

var errClaudeQuotaResponseInvalid = errors.New("quota hub: invalid Claude quota response")

type claudeGeneralWindow struct {
	utilization float64
	atLimit     bool
	resetsAt    json.RawMessage
}

func claudeManualQueryAdapter() manualQueryAdapter {
	return manualQueryAdapter{
		provider: claudeManualQueryProvider,
		match:    matchClaudeManualQuery,
		observe:  observeClaudeManualQuery,
	}
}

func matchClaudeManualQuery(query manualQueryMetadata) bool {
	if query.Method != http.MethodGet || !strings.EqualFold(query.Scheme, "https") ||
		!strings.EqualFold(query.Host, claudeManualQueryHost) ||
		!strings.EqualFold(query.Hostname, claudeManualQueryHost) || query.Port != "" ||
		query.Path != claudeManualQueryPath || query.RawQuery != "" {
		return false
	}
	return query.EscapedPath == "" || query.EscapedPath == claudeManualQueryPath
}

func observeClaudeManualQuery(metadata manualResponseMetadata, body io.Reader) (Observation, error) {
	var payload map[string]json.RawMessage
	decoder := json.NewDecoder(body)
	if err := decoder.Decode(&payload); err != nil {
		return Observation{}, errClaudeQuotaResponseInvalid
	}
	if err := requireClaudeJSONEnd(decoder); err != nil {
		return Observation{}, errClaudeQuotaResponseInvalid
	}

	fiveHourRaw, fiveHourExists := payload["five_hour"]
	fiveHour, fiveHourPresent, err := parseClaudeGeneralWindow(fiveHourRaw, fiveHourExists)
	if err != nil {
		return Observation{}, err
	}
	sevenDayRaw, sevenDayExists := payload["seven_day"]
	sevenDay, sevenDayPresent, err := parseClaudeGeneralWindow(sevenDayRaw, sevenDayExists)
	if err != nil {
		return Observation{}, err
	}
	if !fiveHourPresent && !sevenDayPresent {
		return Observation{}, errClaudeQuotaResponseInvalid
	}

	windows := make([]claudeGeneralWindow, 0, 2)
	if fiveHourPresent {
		windows = append(windows, fiveHour)
	}
	if sevenDayPresent {
		windows = append(windows, sevenDay)
	}
	maxUtilization := windows[0].utilization
	for _, window := range windows[1:] {
		maxUtilization = max(maxUtilization, window.utilization)
	}
	score := 100 - maxUtilization
	observation := Observation{
		Score:        &score,
		Completeness: ScoreOnly,
	}

	if !fiveHourPresent || !sevenDayPresent {
		return observation, nil
	}
	if !fiveHour.atLimit && !sevenDay.atLimit {
		observation.Completeness = AuthoritativeSnapshot
		observation.Mutations = []Mutation{{Scope: ScopeAuth, Outcome: Healthy}}
		return observation, nil
	}

	var latestReset time.Time
	for _, window := range windows {
		if !window.atLimit {
			continue
		}
		resetAt, ok := parseClaudeFutureReset(window.resetsAt, metadata.CompletedAt)
		if !ok {
			return observation, nil
		}
		if resetAt.After(latestReset) {
			latestReset = resetAt
		}
	}
	observation.Completeness = ExhaustionEvidence
	observation.Mutations = []Mutation{{Scope: ScopeAuth, Outcome: Exhausted, ResetAt: latestReset}}
	return observation, nil
}

func parseClaudeGeneralWindow(raw json.RawMessage, exists bool) (claudeGeneralWindow, bool, error) {
	if !exists {
		return claudeGeneralWindow{}, false, nil
	}
	if claudeJSONMissingOrNull(raw) {
		return claudeGeneralWindow{}, false, errClaudeQuotaResponseInvalid
	}
	var payload map[string]json.RawMessage
	if !claudeDecodeJSONObject(raw, &payload) {
		return claudeGeneralWindow{}, false, errClaudeQuotaResponseInvalid
	}
	utilization, atLimit, ok := parseClaudeNumber(payload["utilization"])
	if !ok || utilization < 0 || utilization > 100 {
		return claudeGeneralWindow{}, false, errClaudeQuotaResponseInvalid
	}
	return claudeGeneralWindow{
		utilization: utilization,
		atLimit:     atLimit,
		resetsAt:    payload["resets_at"],
	}, true, nil
}

func parseClaudeFutureReset(raw json.RawMessage, completedAt time.Time) (time.Time, bool) {
	if claudeJSONMissingOrNull(raw) {
		return time.Time{}, false
	}
	var value string
	if err := json.Unmarshal(raw, &value); err != nil {
		return time.Time{}, false
	}
	resetAt, err := time.Parse(time.RFC3339, value)
	if err != nil || !resetAt.After(completedAt) {
		return time.Time{}, false
	}
	return resetAt, true
}

func parseClaudeNumber(raw json.RawMessage) (float64, bool, bool) {
	if claudeJSONMissingOrNull(raw) {
		return 0, false, false
	}
	trimmed := bytes.TrimSpace(raw)
	exact, ok := new(big.Rat).SetString(string(trimmed))
	if !ok || exact.Sign() < 0 || exact.Cmp(big.NewRat(100, 1)) > 0 {
		return 0, false, false
	}
	var jsonNumber json.Number
	if err := json.Unmarshal(trimmed, &jsonNumber); err != nil || jsonNumber.String() != string(trimmed) {
		return 0, false, false
	}
	value, _ := exact.Float64()
	if math.IsNaN(value) || math.IsInf(value, 0) {
		return 0, false, false
	}
	return value, exact.Cmp(big.NewRat(100, 1)) == 0, true
}

func claudeDecodeJSONObject(raw json.RawMessage, destination any) bool {
	trimmed := bytes.TrimSpace(raw)
	return len(trimmed) >= 2 && trimmed[0] == '{' && trimmed[len(trimmed)-1] == '}' &&
		json.Unmarshal(trimmed, destination) == nil
}

func claudeJSONMissingOrNull(raw json.RawMessage) bool {
	trimmed := bytes.TrimSpace(raw)
	return len(trimmed) == 0 || bytes.Equal(trimmed, []byte("null"))
}

func requireClaudeJSONEnd(decoder *json.Decoder) error {
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		return errClaudeQuotaResponseInvalid
	}
	return nil
}
