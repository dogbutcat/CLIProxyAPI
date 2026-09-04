package hub

import (
	"context"
	"errors"
	"io"
	"math"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
)

func TestKimiManualQueryMatcherIsExact(t *testing.T) {
	valid := manualQueryMetadata{
		Provider:    kimiManualQueryProvider,
		Method:      http.MethodGet,
		Scheme:      "https",
		Host:        kimiManualQueryHost,
		Hostname:    kimiManualQueryHost,
		Path:        kimiManualQueryPath,
		EscapedPath: kimiManualQueryPath,
	}
	if adapter, ok := activeManualAdapterTable().match(valid); !ok || adapter.provider != kimiManualQueryProvider {
		t.Fatalf("active table match = (%q, %v), want Kimi", adapter.provider, ok)
	}

	tests := []struct {
		name   string
		mutate func(*manualQueryMetadata)
	}{
		{name: "provider", mutate: func(query *manualQueryMetadata) { query.Provider = "codex" }},
		{name: "method", mutate: func(query *manualQueryMetadata) { query.Method = http.MethodPost }},
		{name: "scheme", mutate: func(query *manualQueryMetadata) { query.Scheme = "http" }},
		{name: "host", mutate: func(query *manualQueryMetadata) { query.Host = "usage.kimi.com" }},
		{name: "hostname", mutate: func(query *manualQueryMetadata) { query.Hostname = "usage.kimi.com" }},
		{name: "port", mutate: func(query *manualQueryMetadata) {
			query.Host = kimiManualQueryHost + ":443"
			query.Port = "443"
		}},
		{name: "path", mutate: func(query *manualQueryMetadata) { query.Path = kimiManualQueryPath + "/" }},
		{name: "escaped path", mutate: func(query *manualQueryMetadata) { query.EscapedPath = "/coding/v1/%75sages" }},
		{name: "query", mutate: func(query *manualQueryMetadata) { query.RawQuery = "scope=all" }},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			query := valid
			tt.mutate(&query)
			if _, ok := activeManualAdapterTable().match(query); ok {
				t.Fatal("active table matched non-exact Kimi query")
			}
		})
	}
}

func TestKimiExcludedEndpointsDoNotAllocateTicket(t *testing.T) {
	tests := []struct {
		name   string
		method string
		rawURL string
	}{
		{name: "wrong method", method: http.MethodPost, rawURL: "https://api.kimi.com/coding/v1/usages"},
		{name: "wrong endpoint", method: http.MethodGet, rawURL: "https://api.kimi.com/coding/v1/usage"},
		{name: "trailing slash", method: http.MethodGet, rawURL: "https://api.kimi.com/coding/v1/usages/"},
		{name: "query", method: http.MethodGet, rawURL: "https://api.kimi.com/coding/v1/usages?detail=true"},
		{name: "port", method: http.MethodGet, rawURL: "https://api.kimi.com:443/coding/v1/usages"},
		{name: "userinfo", method: http.MethodGet, rawURL: "https://user:password@api.kimi.com/coding/v1/usages"},
		{name: "escaped path", method: http.MethodGet, rawURL: "https://api.kimi.com/coding/v1/%75sages"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			manager := auth.NewManager(nil, nil, nil)
			resolved := registerManualTestAuth(t, manager, &auth.Auth{
				ID:       "kimi-excluded-" + strings.ReplaceAll(tt.name, " ", "-"),
				Provider: kimiManualQueryProvider,
				Status:   auth.StatusActive,
			})
			queryURL, err := url.Parse(tt.rawURL)
			if err != nil {
				t.Fatalf("url.Parse() error = %v", err)
			}
			if completion := BeginManualQuery(context.Background(), manager, resolved, tt.method, queryURL); completion != nil {
				t.Fatal("excluded Kimi endpoint returned a completion")
			}
			ticket, ok := manager.IssueQuotaObservationTicketForAuth(resolved)
			if !ok || ticket.StartOrder != 1 {
				t.Fatalf("ticket after excluded endpoint = (%+v, %v), want first ticket", ticket, ok)
			}
		})
	}
}

func TestKimiFixtureProjections(t *testing.T) {
	completedAt := time.Date(2026, time.January, 10, 12, 0, 0, 0, time.UTC)
	tests := []struct {
		name         string
		completeness Completeness
		score        float64
		outcome      Outcome
		resetAt      time.Time
	}{
		{name: "healthy", completeness: AuthoritativeSnapshot, score: 75, outcome: Healthy},
		{name: "used_only", completeness: AuthoritativeSnapshot, score: 75, outcome: Healthy},
		{name: "remaining_only", completeness: AuthoritativeSnapshot, score: 25, outcome: Healthy},
		{name: "both_consistent", completeness: AuthoritativeSnapshot, score: 200.0 / 3, outcome: Healthy},
		{name: "numeric_strings", completeness: AuthoritativeSnapshot, score: 75, outcome: Healthy},
		{name: "precision", completeness: AuthoritativeSnapshot, score: 1e-16, outcome: Healthy},
		{name: "limits_isolation", completeness: AuthoritativeSnapshot, score: 70, outcome: Healthy},
		{name: "exhausted_absolute", completeness: ExhaustionEvidence, score: 0, outcome: Exhausted, resetAt: completedAt.Add(time.Hour)},
		{name: "exhausted_relative", completeness: ExhaustionEvidence, score: 0, outcome: Exhausted, resetAt: completedAt.Add(time.Hour + 500*time.Millisecond)},
		{name: "exhausted_ttl", completeness: ExhaustionEvidence, score: 0, outcome: Exhausted, resetAt: completedAt.Add(30 * time.Minute)},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			observation, err := observeKimiManualQuery(
				manualResponseMetadata{CompletedAt: completedAt},
				strings.NewReader(readKimiFixture(t, tt.name+".json")),
			)
			if err != nil {
				t.Fatalf("observeKimiManualQuery() error = %v", err)
			}
			assertKimiObservation(t, observation, tt.completeness, tt.score, tt.outcome, tt.resetAt)
		})
	}
}

func TestKimiInvalidSummariesAreRejected(t *testing.T) {
	completedAt := time.Date(2026, time.January, 10, 12, 0, 0, 0, time.UTC)
	tests := []struct {
		name string
		body string
	}{
		{name: "missing usage", body: `{}`},
		{name: "top level null", body: `null`},
		{name: "null usage", body: `{"usage":null}`},
		{name: "array usage", body: `{"usage":[]}`},
		{name: "case variant usage", body: `{"Usage":{"limit":100,"used":20}}`},
		{name: "nested usage only", body: `{"scope":{"usage":{"limit":100,"used":20}}}`},
		{name: "nested summary only", body: `{"usage":{"summary":{"limit":100,"used":20}}}`},
		{name: "limits only", body: `{"limits":[{"limit":100,"used":20}]}`},
		{name: "missing limit", body: `{"usage":{"used":20}}`},
		{name: "null limit", body: `{"usage":{"limit":null,"used":20}}`},
		{name: "zero limit", body: `{"usage":{"limit":0,"used":0}}`},
		{name: "negative limit", body: `{"usage":{"limit":-1,"used":0}}`},
		{name: "missing amounts", body: `{"usage":{"limit":100}}`},
		{name: "null used is present invalid", body: `{"usage":{"limit":100,"used":null,"remaining":80}}`},
		{name: "null remaining is present invalid", body: `{"usage":{"limit":100,"used":20,"remaining":null}}`},
		{name: "inconsistent", body: `{"usage":{"limit":100,"used":20,"remaining":70}}`},
		{name: "float rounded inconsistency", body: `{"usage":{"limit":0.30000000000000004,"used":0.1,"remaining":0.2}}`},
		{name: "used below range", body: `{"usage":{"limit":100,"used":-1}}`},
		{name: "used above range", body: `{"usage":{"limit":100,"used":101}}`},
		{name: "remaining below range", body: `{"usage":{"limit":100,"remaining":-1}}`},
		{name: "remaining above range", body: `{"usage":{"limit":100,"remaining":101}}`},
		{name: "empty string", body: `{"usage":{"limit":"","used":0}}`},
		{name: "whitespace string", body: `{"usage":{"limit":"   ","used":0}}`},
		{name: "quoted nonnumeric", body: `{"usage":{"limit":"many","used":0}}`},
		{name: "quoted NaN", body: `{"usage":{"limit":"NaN","used":0}}`},
		{name: "quoted positive infinity", body: `{"usage":{"limit":"Infinity","used":0}}`},
		{name: "quoted negative infinity", body: `{"usage":{"limit":"-Infinity","used":0}}`},
		{name: "overflow number", body: `{"usage":{"limit":1e309,"used":0}}`},
		{name: "overflow numeric string", body: `{"usage":{"limit":"1e309","used":0}}`},
		{name: "leading plus string", body: `{"usage":{"limit":"+100","used":0}}`},
		{name: "leading zero string", body: `{"usage":{"limit":"0100","used":0}}`},
		{name: "hex string", body: `{"usage":{"limit":"0x64","used":0}}`},
		{name: "boolean amount", body: `{"usage":{"limit":100,"used":true}}`},
		{name: "object amount", body: `{"usage":{"limit":100,"used":{"value":20}}}`},
		{name: "trailing JSON", body: `{"usage":{"limit":100,"used":20}}{}`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := observeKimiManualQuery(manualResponseMetadata{CompletedAt: completedAt}, strings.NewReader(tt.body))
			if !errors.Is(err, errKimiQuotaResponseInvalid) {
				t.Fatalf("observeKimiManualQuery() error = %v, want invalid response", err)
			}
		})
	}
}

func TestKimiExactDecimalNormalizationAndExhaustion(t *testing.T) {
	completedAt := time.Date(2026, time.January, 10, 12, 0, 0, 0, time.UTC)
	tests := []struct {
		name         string
		body         string
		completeness Completeness
		score        float64
		outcome      Outcome
	}{
		{
			name:         "JSON exponent",
			body:         `{"usage":{"limit":1e2,"used":2.5e1,"remaining":7.5e1}}`,
			completeness: AuthoritativeSnapshot,
			score:        75,
			outcome:      Healthy,
		},
		{
			name:         "trimmed string exponent",
			body:         `{"usage":{"limit":" 1E2 ","used":" 2.5e1 ","remaining":" 75 "}}`,
			completeness: AuthoritativeSnapshot,
			score:        75,
			outcome:      Healthy,
		},
		{
			name:         "exact decimal sum",
			body:         `{"usage":{"limit":0.3,"used":0.1,"remaining":0.2}}`,
			completeness: AuthoritativeSnapshot,
			score:        200.0 / 3,
			outcome:      Healthy,
		},
		{
			name:         "near limit remains healthy",
			body:         `{"usage":{"limit":1,"used":0.999999999999999999,"remaining":0.000000000000000001}}`,
			completeness: AuthoritativeSnapshot,
			score:        1e-16,
			outcome:      Healthy,
		},
		{
			name:         "tiny finite exponent",
			body:         `{"usage":{"limit":"1e-400","remaining":"1e-400"}}`,
			completeness: AuthoritativeSnapshot,
			score:        100,
			outcome:      Healthy,
		},
		{
			name:         "exact remaining zero is exhausted",
			body:         `{"usage":{"limit":1,"remaining":0}}`,
			completeness: ScoreOnly,
			score:        0,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			observation, err := observeKimiManualQuery(manualResponseMetadata{CompletedAt: completedAt}, strings.NewReader(tt.body))
			if err != nil {
				t.Fatalf("observeKimiManualQuery() error = %v", err)
			}
			assertKimiObservation(t, observation, tt.completeness, tt.score, tt.outcome, time.Time{})
		})
	}
}

func TestKimiResetSelectionMatchesManagementPriority(t *testing.T) {
	completedAt := time.Date(2026, time.January, 10, 12, 0, 0, 0, time.UTC)
	tests := []struct {
		name         string
		body         string
		completeness Completeness
		resetAt      time.Time
	}{
		{
			name: "absolute key priority",
			body: `{"usage":{"limit":100,"used":100,` +
				`"reset_at":"2026-01-10T13:00:00Z","resetAt":"2026-01-10T14:00:00Z",` +
				`"reset_time":"2026-01-10T15:00:00Z","resetTime":"2026-01-10T16:00:00Z","reset_in":18000}}`,
			completeness: ExhaustionEvidence,
			resetAt:      completedAt.Add(time.Hour),
		},
		{
			name: "malformed absolute falls through",
			body: `{"usage":{"limit":100,"used":100,"reset_at":"bad",` +
				`"resetAt":"2026-01-10T14:00:00Z","reset_in":18000}}`,
			completeness: ExhaustionEvidence,
			resetAt:      completedAt.Add(2 * time.Hour),
		},
		{
			name: "past first parseable absolute retains priority",
			body: `{"usage":{"limit":100,"used":100,"reset_at":"2026-01-10T11:00:00Z",` +
				`"resetAt":"2026-01-10T14:00:00Z","reset_in":18000}}`,
			completeness: ScoreOnly,
		},
		{
			name: "relative key priority",
			body: `{"usage":{"limit":100,"used":100,"reset_at":"bad",` +
				`"reset_in":600,"resetIn":1200,"ttl":1800}}`,
			completeness: ExhaustionEvidence,
			resetAt:      completedAt.Add(10 * time.Minute),
		},
		{
			name:         "invalid relative falls through",
			body:         `{"usage":{"limit":100,"used":100,"reset_in":0,"resetIn":1200,"ttl":1800}}`,
			completeness: ExhaustionEvidence,
			resetAt:      completedAt.Add(20 * time.Minute),
		},
		{
			name:         "Unix seconds",
			body:         `{"usage":{"limit":100,"used":100,"resetAt":1768050000}}`,
			completeness: ExhaustionEvidence,
			resetAt:      completedAt.Add(time.Hour),
		},
		{
			name:         "Unix milliseconds string",
			body:         `{"usage":{"limit":100,"used":100,"resetAt":"1768050000000"}}`,
			completeness: ExhaustionEvidence,
			resetAt:      completedAt.Add(time.Hour),
		},
		{
			name:         "reset time snake case",
			body:         `{"usage":{"limit":100,"used":100,"reset_time":"2026-01-10T13:00:00Z"}}`,
			completeness: ExhaustionEvidence,
			resetAt:      completedAt.Add(time.Hour),
		},
		{
			name:         "reset time camel case",
			body:         `{"usage":{"limit":100,"used":100,"resetTime":"2026-01-10T13:00:00Z"}}`,
			completeness: ExhaustionEvidence,
			resetAt:      completedAt.Add(time.Hour),
		},
		{
			name:         "over precise RFC3339 fraction",
			body:         `{"usage":{"limit":100,"used":100,"resetAt":"2026-01-10T13:00:00.123456789123Z"}}`,
			completeness: ExhaustionEvidence,
			resetAt:      completedAt.Add(time.Hour + 123456*time.Microsecond),
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			observation, err := observeKimiManualQuery(manualResponseMetadata{CompletedAt: completedAt}, strings.NewReader(tt.body))
			if err != nil {
				t.Fatalf("observeKimiManualQuery() error = %v", err)
			}
			assertKimiObservation(t, observation, tt.completeness, 0, Exhausted, tt.resetAt)
		})
	}
}

func TestKimiRelativeResetUsesCompletionTime(t *testing.T) {
	completedAt := time.Date(2026, time.January, 10, 12, 0, 0, 0, time.UTC)
	observation, err := observeKimiManualQuery(manualResponseMetadata{
		CompletedAt: completedAt,
		ServerDate:  completedAt.Add(-10 * time.Minute),
	}, strings.NewReader(`{"usage":{"limit":100,"used":100,"resetIn":600}}`))
	if err != nil {
		t.Fatalf("observeKimiManualQuery() error = %v", err)
	}
	assertKimiObservation(t, observation, ExhaustionEvidence, 0, Exhausted, completedAt.Add(10*time.Minute))
}

func TestKimiExhaustionWithoutFutureResetIsScoreOnly(t *testing.T) {
	completedAt := time.Date(2026, time.January, 10, 12, 0, 0, 0, time.UTC)
	tests := []struct {
		name string
		body string
	}{
		{name: "missing", body: `{"usage":{"limit":100,"used":100}}`},
		{name: "past", body: `{"usage":{"limit":100,"used":100,"resetAt":"2026-01-10T11:59:59Z"}}`},
		{name: "malformed", body: `{"usage":{"limit":100,"used":100,"resetAt":"tomorrow"}}`},
		{name: "wrong type", body: `{"usage":{"limit":100,"used":100,"resetAt":true}}`},
		{name: "zero relative", body: `{"usage":{"limit":100,"used":100,"resetIn":0}}`},
		{name: "negative relative", body: `{"usage":{"limit":100,"used":100,"resetIn":-1}}`},
		{name: "overflow relative", body: `{"usage":{"limit":100,"used":100,"resetIn":"1e309"}}`},
		{name: "overflow absolute", body: `{"usage":{"limit":100,"used":100,"resetAt":"1e309"}}`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			observation, err := observeKimiManualQuery(manualResponseMetadata{CompletedAt: completedAt}, strings.NewReader(tt.body))
			if err != nil {
				t.Fatalf("observeKimiManualQuery() error = %v", err)
			}
			assertKimiObservation(t, observation, ScoreOnly, 0, 0, time.Time{})
		})
	}
}

func TestKimiHealthySummaryIgnoresResetAndDisplayOnlyShapes(t *testing.T) {
	body := `{"usage":{"limit":100,"used":20,"remaining":80,"resetAt":false},` +
		`"limits":[{"limit":1,"used":1,"resetAt":"2099-01-01T00:00:00Z"}],` +
		`"scope":{"usage":{"limit":1,"used":1,"resetAt":"2099-01-01T00:00:00Z"}}}`
	observation, err := observeKimiManualQuery(manualResponseMetadata{
		CompletedAt: time.Date(2026, time.January, 10, 12, 0, 0, 0, time.UTC),
	}, strings.NewReader(body))
	if err != nil {
		t.Fatalf("observeKimiManualQuery() error = %v", err)
	}
	assertKimiObservation(t, observation, AuthoritativeSnapshot, 80, Healthy, time.Time{})
}

func TestKimiObserverConsumesReaderSynchronously(t *testing.T) {
	reader := &kimiEOFTrackingReader{reader: strings.NewReader(`{"usage":{"limit":100,"used":25}}`)}
	observation, err := observeKimiManualQuery(manualResponseMetadata{
		CompletedAt: time.Date(2026, time.January, 10, 12, 0, 0, 0, time.UTC),
	}, reader)
	if err != nil {
		t.Fatalf("observeKimiManualQuery() error = %v", err)
	}
	if !reader.sawEOF {
		t.Fatal("observer returned before consuming the borrowed reader")
	}
	assertKimiObservation(t, observation, AuthoritativeSnapshot, 75, Healthy, time.Time{})
}

type kimiEOFTrackingReader struct {
	reader *strings.Reader
	sawEOF bool
}

func (reader *kimiEOFTrackingReader) Read(buffer []byte) (int, error) {
	read, err := reader.reader.Read(buffer)
	if errors.Is(err, io.EOF) {
		reader.sawEOF = true
	}
	return read, err
}

func readKimiFixture(t *testing.T, name string) string {
	t.Helper()
	content, err := os.ReadFile(filepath.Join("testdata", "kimi", name))
	if err != nil {
		t.Fatalf("read Kimi fixture: %v", err)
	}
	return string(content)
}

func assertKimiObservation(
	t *testing.T,
	observation Observation,
	completeness Completeness,
	score float64,
	outcome Outcome,
	resetAt time.Time,
) {
	t.Helper()
	if observation.Completeness != completeness || observation.Score == nil || !kimiScoresEqual(*observation.Score, score) {
		t.Fatalf("observation = %+v, want completeness %v score %v", observation, completeness, score)
	}
	if completeness == ScoreOnly {
		if len(observation.Mutations) != 0 {
			t.Fatalf("score-only mutations = %+v, want none", observation.Mutations)
		}
		return
	}
	if len(observation.Mutations) != 1 {
		t.Fatalf("mutations = %+v, want one", observation.Mutations)
	}
	mutation := observation.Mutations[0]
	if mutation.Scope != ScopeAuth || mutation.Outcome != outcome || !mutation.ResetAt.Equal(resetAt) {
		t.Fatalf("mutation = %+v, want auth outcome %v reset %v", mutation, outcome, resetAt)
	}
}

func kimiScoresEqual(got, want float64) bool {
	return got == want || math.Abs(got-want) <= 1e-12*math.Max(1, math.Abs(want))
}
