package hub

import (
	"context"
	"errors"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
)

func TestClaudeManualQueryMatcherIsExact(t *testing.T) {
	valid := manualQueryMetadata{
		Provider:    claudeManualQueryProvider,
		Method:      http.MethodGet,
		Scheme:      "https",
		Host:        claudeManualQueryHost,
		Hostname:    claudeManualQueryHost,
		Path:        claudeManualQueryPath,
		EscapedPath: claudeManualQueryPath,
	}
	if adapter, ok := activeManualAdapterTable().match(valid); !ok || adapter.provider != claudeManualQueryProvider {
		t.Fatalf("active table match = (%q, %v), want Claude", adapter.provider, ok)
	}

	tests := []struct {
		name   string
		mutate func(*manualQueryMetadata)
	}{
		{name: "provider", mutate: func(query *manualQueryMetadata) { query.Provider = "codex" }},
		{name: "method", mutate: func(query *manualQueryMetadata) { query.Method = http.MethodPost }},
		{name: "scheme", mutate: func(query *manualQueryMetadata) { query.Scheme = "http" }},
		{name: "host", mutate: func(query *manualQueryMetadata) { query.Hostname = "console.anthropic.com" }},
		{name: "port", mutate: func(query *manualQueryMetadata) {
			query.Host = claudeManualQueryHost + ":443"
			query.Port = "443"
		}},
		{name: "path", mutate: func(query *manualQueryMetadata) { query.Path = claudeManualQueryPath + "/" }},
		{name: "escaped path", mutate: func(query *manualQueryMetadata) { query.EscapedPath = "/api/oauth/%75sage" }},
		{name: "query", mutate: func(query *manualQueryMetadata) { query.RawQuery = "include=limits" }},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			query := valid
			tt.mutate(&query)
			if _, ok := activeManualAdapterTable().match(query); ok {
				t.Fatal("active table matched non-exact Claude query")
			}
		})
	}
}

func TestClaudeExcludedEndpointsDoNotAllocateTicket(t *testing.T) {
	tests := []struct {
		name   string
		method string
		rawURL string
	}{
		{name: "profile", method: http.MethodGet, rawURL: "https://api.anthropic.com/api/oauth/profile"},
		{name: "query", method: http.MethodGet, rawURL: "https://api.anthropic.com/api/oauth/usage?detail=true"},
		{name: "userinfo", method: http.MethodGet, rawURL: "https://user:password@api.anthropic.com/api/oauth/usage"},
		{name: "port", method: http.MethodGet, rawURL: "https://api.anthropic.com:443/api/oauth/usage"},
		{name: "escaped path", method: http.MethodGet, rawURL: "https://api.anthropic.com/api/oauth/%75sage"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			manager := auth.NewManager(nil, nil, nil)
			resolved := registerManualTestAuth(t, manager, &auth.Auth{
				ID:       "claude-excluded-" + strings.ReplaceAll(tt.name, " ", "-"),
				Provider: claudeManualQueryProvider,
				Status:   auth.StatusActive,
			})
			queryURL, err := url.Parse(tt.rawURL)
			if err != nil {
				t.Fatalf("url.Parse() error = %v", err)
			}
			if completion := BeginManualQuery(context.Background(), manager, resolved, tt.method, queryURL); completion != nil {
				t.Fatal("excluded Claude endpoint returned a completion")
			}
			ticket, ok := manager.IssueQuotaObservationTicketForAuth(resolved)
			if !ok || ticket.StartOrder != 1 {
				t.Fatalf("ticket after excluded endpoint = (%+v, %v), want first ticket", ticket, ok)
			}
		})
	}
}

func TestClaudeFixtureProjections(t *testing.T) {
	completedAt := time.Date(2026, time.January, 10, 12, 0, 0, 0, time.UTC)
	tests := []struct {
		name         string
		completeness Completeness
		score        float64
		outcome      Outcome
		resetAt      time.Time
	}{
		{name: "healthy", completeness: AuthoritativeSnapshot, score: 60, outcome: Healthy},
		{name: "partial", completeness: ScoreOnly, score: 55},
		{name: "exhausted_single", completeness: ExhaustionEvidence, score: 0, outcome: Exhausted, resetAt: time.Date(2026, time.January, 10, 13, 0, 0, 0, time.UTC)},
		{name: "exhausted_multiple", completeness: ExhaustionEvidence, score: 0, outcome: Exhausted, resetAt: time.Date(2026, time.January, 10, 15, 0, 0, 0, time.UTC)},
		{name: "display_only", completeness: AuthoritativeSnapshot, score: 70, outcome: Healthy},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			observation, err := observeClaudeManualQuery(manualResponseMetadata{CompletedAt: completedAt}, strings.NewReader(readClaudeFixture(t, tt.name+".json")))
			if err != nil {
				t.Fatalf("observeClaudeManualQuery() error = %v", err)
			}
			assertClaudeObservation(t, observation, tt.completeness, tt.score, tt.outcome, tt.resetAt)
		})
	}
}

func TestClaudeMalformedUtilizationFixtureIsRejected(t *testing.T) {
	_, err := observeClaudeManualQuery(manualResponseMetadata{
		CompletedAt: time.Date(2026, time.January, 10, 12, 0, 0, 0, time.UTC),
	}, strings.NewReader(readClaudeFixture(t, "malformed_utilization.json")))
	if !errors.Is(err, errClaudeQuotaResponseInvalid) {
		t.Fatalf("observeClaudeManualQuery() error = %v, want invalid response", err)
	}
}

func TestClaudePartialAndInvalidWindows(t *testing.T) {
	completedAt := time.Date(2026, time.January, 10, 12, 0, 0, 0, time.UTC)
	tests := []struct {
		name      string
		body      string
		wantError bool
		score     float64
	}{
		{name: "five hour only", body: `{"five_hour":{"utilization":25}}`, score: 75},
		{name: "seven day only", body: `{"seven_day":{"utilization":35}}`, score: 65},
		{name: "null with valid", body: `{"five_hour":null,"seven_day":{"utilization":20}}`, wantError: true},
		{name: "single exhausted stays score only", body: `{"five_hour":{"utilization":100,"resets_at":"2026-01-10T13:00:00Z"}}`, score: 0},
		{name: "no windows", body: `{}`, wantError: true},
		{name: "top level null", body: `null`, wantError: true},
		{name: "null windows", body: `{"five_hour":null,"seven_day":null}`, wantError: true},
		{name: "non object", body: `{"five_hour":[],"seven_day":{"utilization":20}}`, wantError: true},
		{name: "missing utilization", body: `{"five_hour":{},"seven_day":{"utilization":20}}`, wantError: true},
		{name: "null utilization", body: `{"five_hour":{"utilization":null},"seven_day":{"utilization":20}}`, wantError: true},
		{name: "string utilization", body: `{"five_hour":{"utilization":"20"},"seven_day":{"utilization":20}}`, wantError: true},
		{name: "negative utilization", body: `{"five_hour":{"utilization":-1},"seven_day":{"utilization":20}}`, wantError: true},
		{name: "overflow utilization", body: `{"five_hour":{"utilization":101},"seven_day":{"utilization":20}}`, wantError: true},
		{name: "camel case only", body: `{"fiveHour":{"utilization":20},"sevenDay":{"utilization":30}}`, wantError: true},
		{name: "case variant top level", body: `{"FIVE_HOUR":{"utilization":20},"seven_day":{"utilization":30}}`, score: 70},
		{name: "case variant utilization", body: `{"five_hour":{"Utilization":20},"seven_day":{"utilization":30}}`, wantError: true},
		{name: "trailing JSON", body: `{"five_hour":{"utilization":20}}{}`, wantError: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			observation, err := observeClaudeManualQuery(manualResponseMetadata{CompletedAt: completedAt}, strings.NewReader(tt.body))
			if tt.wantError {
				if !errors.Is(err, errClaudeQuotaResponseInvalid) {
					t.Fatalf("error = %v, want invalid response", err)
				}
				return
			}
			if err != nil {
				t.Fatalf("observeClaudeManualQuery() error = %v", err)
			}
			assertClaudeObservation(t, observation, ScoreOnly, tt.score, 0, time.Time{})
		})
	}
}

func TestClaudeFractionalUtilizationAndExactRange(t *testing.T) {
	completedAt := time.Date(2026, time.January, 10, 12, 0, 0, 0, time.UTC)
	tests := []struct {
		name      string
		body      string
		wantError bool
		score     float64
	}{
		{
			name:  "fractional score",
			body:  `{"five_hour":{"utilization":12.5},"seven_day":{"utilization":33.25}}`,
			score: 66.75,
		},
		{
			name:  "precision below maximum remains healthy",
			body:  `{"five_hour":{"utilization":99.999999999999999},"seven_day":{"utilization":20}}`,
			score: 0,
		},
		{
			name:      "precision above maximum",
			body:      `{"five_hour":{"utilization":100.000000000000001},"seven_day":{"utilization":20}}`,
			wantError: true,
		},
		{
			name:      "precision below minimum",
			body:      `{"five_hour":{"utilization":-0.0000000000000001},"seven_day":{"utilization":20}}`,
			wantError: true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			observation, err := observeClaudeManualQuery(manualResponseMetadata{CompletedAt: completedAt}, strings.NewReader(tt.body))
			if tt.wantError {
				if !errors.Is(err, errClaudeQuotaResponseInvalid) {
					t.Fatalf("error = %v, want invalid response", err)
				}
				return
			}
			if err != nil {
				t.Fatalf("observeClaudeManualQuery() error = %v", err)
			}
			assertClaudeObservation(t, observation, AuthoritativeSnapshot, tt.score, Healthy, time.Time{})
		})
	}
}

func TestClaudeExhaustionRequiresEveryBlockingFutureReset(t *testing.T) {
	completedAt := time.Date(2026, time.January, 10, 12, 0, 0, 0, time.UTC)
	tests := []struct {
		name string
		body string
	}{
		{
			name: "missing reset",
			body: `{"five_hour":{"utilization":100},` +
				`"seven_day":{"utilization":40,"resets_at":"not-actionable"}}`,
		},
		{
			name: "past reset",
			body: `{"five_hour":{"utilization":100,"resets_at":"2026-01-10T11:59:59Z"},` +
				`"seven_day":{"utilization":40}}`,
		},
		{
			name: "malformed reset",
			body: `{"five_hour":{"utilization":100,"resets_at":"tomorrow"},` +
				`"seven_day":{"utilization":40}}`,
		},
		{
			name: "wrong reset type",
			body: `{"five_hour":{"utilization":100,"resets_at":1768042800},` +
				`"seven_day":{"utilization":40}}`,
		},
		{
			name: "one of multiple exhausted resets invalid",
			body: `{"five_hour":{"utilization":100,"resets_at":"2026-01-10T13:00:00Z"},` +
				`"seven_day":{"utilization":100,"resets_at":null}}`,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			observation, err := observeClaudeManualQuery(manualResponseMetadata{CompletedAt: completedAt}, strings.NewReader(tt.body))
			if err != nil {
				t.Fatalf("observeClaudeManualQuery() error = %v", err)
			}
			assertClaudeObservation(t, observation, ScoreOnly, 0, 0, time.Time{})
		})
	}
}

func TestClaudeHealthySnapshotDoesNotRequireReset(t *testing.T) {
	observation, err := observeClaudeManualQuery(manualResponseMetadata{
		CompletedAt: time.Date(2026, time.January, 10, 12, 0, 0, 0, time.UTC),
	}, strings.NewReader(`{"five_hour":{"utilization":10,"resets_at":false},"seven_day":{"utilization":20,"resets_at":"invalid"}}`))
	if err != nil {
		t.Fatalf("observeClaudeManualQuery() error = %v", err)
	}
	assertClaudeObservation(t, observation, AuthoritativeSnapshot, 80, Healthy, time.Time{})
}

func readClaudeFixture(t *testing.T, name string) string {
	t.Helper()
	content, err := os.ReadFile(filepath.Join("testdata", "claude", name))
	if err != nil {
		t.Fatalf("read Claude fixture: %v", err)
	}
	return string(content)
}

func assertClaudeObservation(
	t *testing.T,
	observation Observation,
	completeness Completeness,
	score float64,
	outcome Outcome,
	resetAt time.Time,
) {
	t.Helper()
	if observation.Completeness != completeness || observation.Score == nil || *observation.Score != score {
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
