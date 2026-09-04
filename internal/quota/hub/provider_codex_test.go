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

func TestCodexManualQueryMatcherIsExact(t *testing.T) {
	valid := manualQueryMetadata{
		Provider:    "codex",
		Method:      http.MethodGet,
		Scheme:      "https",
		Host:        "chatgpt.com",
		Hostname:    "chatgpt.com",
		Path:        codexManualQueryPath,
		EscapedPath: codexManualQueryPath,
	}
	if adapter, ok := activeManualAdapterTable().match(valid); !ok || adapter.provider != codexManualQueryProvider {
		t.Fatalf("active table match = (%q, %v), want Codex", adapter.provider, ok)
	}

	tests := []struct {
		name   string
		mutate func(*manualQueryMetadata)
	}{
		{name: "provider", mutate: func(query *manualQueryMetadata) { query.Provider = "claude" }},
		{name: "method", mutate: func(query *manualQueryMetadata) { query.Method = http.MethodPost }},
		{name: "scheme", mutate: func(query *manualQueryMetadata) { query.Scheme = "http" }},
		{name: "host", mutate: func(query *manualQueryMetadata) { query.Hostname = "example.com" }},
		{name: "port", mutate: func(query *manualQueryMetadata) { query.Port = "443" }},
		{name: "path", mutate: func(query *manualQueryMetadata) { query.Path = "/backend-api/wham/usage/" }},
		{name: "escaped path", mutate: func(query *manualQueryMetadata) { query.EscapedPath = "/backend-api/wham%2Fusage" }},
		{name: "query", mutate: func(query *manualQueryMetadata) { query.RawQuery = "credits=true" }},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			query := valid
			tt.mutate(&query)
			if _, ok := activeManualAdapterTable().match(query); ok {
				t.Fatal("active table matched non-exact Codex query")
			}
		})
	}
}

func TestCodexBeginManualQueryRejectsUserinfoAndResetCredits(t *testing.T) {
	manager := auth.NewManager(nil, nil, nil)
	resolved := registerManualTestAuth(t, manager, &auth.Auth{
		ID:       "codex-excluded-endpoints",
		Provider: codexManualQueryProvider,
		Status:   auth.StatusActive,
	})
	tests := []struct {
		name   string
		method string
		rawURL string
	}{
		{
			name:   "userinfo",
			method: http.MethodGet,
			rawURL: "https://user:password@chatgpt.com/backend-api/wham/usage",
		},
		{
			name:   "reset credit listing",
			method: http.MethodGet,
			rawURL: "https://chatgpt.com/backend-api/wham/rate-limit-reset-credits",
		},
		{
			name:   "reset credit consumption",
			method: http.MethodPost,
			rawURL: "https://chatgpt.com/backend-api/wham/rate-limit-reset-credits/consume",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			queryURL, err := url.Parse(tt.rawURL)
			if err != nil {
				t.Fatalf("url.Parse() error = %v", err)
			}
			if completion := BeginManualQuery(context.Background(), manager, resolved, tt.method, queryURL); completion != nil {
				t.Fatal("excluded Codex endpoint returned a completion")
			}
		})
	}
}

func TestCodexFixtureProjections(t *testing.T) {
	completedAt := time.Date(2026, time.January, 10, 12, 0, 0, 0, time.UTC)
	serverDate := completedAt.Add(-time.Minute)
	tests := []struct {
		name         string
		completeness Completeness
		score        float64
		outcome      Outcome
		resetAt      time.Time
	}{
		{name: "healthy", completeness: AuthoritativeSnapshot, score: 60, outcome: Healthy},
		{name: "exhausted", completeness: ExhaustionEvidence, score: 0, outcome: Exhausted, resetAt: time.Date(2026, time.January, 10, 14, 0, 0, 0, time.UTC)},
		{name: "partial", completeness: ScoreOnly, score: 55},
		{name: "contradictory", completeness: ExhaustionEvidence, score: 0, outcome: Exhausted, resetAt: serverDate.Add(90 * time.Minute)},
		{name: "ignored_fields", completeness: AuthoritativeSnapshot, score: 90, outcome: Healthy},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			observation, err := observeCodexManualQuery(manualResponseMetadata{
				CompletedAt: completedAt,
				ServerDate:  serverDate,
			}, strings.NewReader(readCodexFixture(t, tt.name+".json")))
			if err != nil {
				t.Fatalf("observeCodexManualQuery() error = %v", err)
			}
			assertCodexObservation(t, observation, tt.completeness, tt.score, tt.outcome, tt.resetAt)
		})
	}
}

func TestCodexMalformedFixtureIsRejected(t *testing.T) {
	_, err := observeCodexManualQuery(manualResponseMetadata{
		CompletedAt: time.Date(2026, time.January, 10, 12, 0, 0, 0, time.UTC),
	}, strings.NewReader(readCodexFixture(t, "malformed.json")))
	if !errors.Is(err, errCodexQuotaResponseInvalid) {
		t.Fatalf("observeCodexManualQuery() error = %v, want invalid response", err)
	}
}

func TestCodexIncompleteAndInvalidEvidence(t *testing.T) {
	completedAt := time.Date(2026, time.January, 10, 12, 0, 0, 0, time.UTC)
	tests := []struct {
		name         string
		body         string
		wantError    bool
		completeness Completeness
		score        float64
	}{
		{name: "secondary only", body: `{"rate_limit":{"allowed":true,"limit_reached":false,"secondary_window":{"used_percent":30}}}`, completeness: ScoreOnly, score: 70},
		{name: "secondary only exhausted", body: `{"rate_limit":{"allowed":true,"limit_reached":false,"secondary_window":{"used_percent":100,"reset_after_seconds":600}}}`, completeness: ScoreOnly, score: 0},
		{name: "missing booleans", body: `{"rate_limit":{"primary_window":{"used_percent":20}}}`, completeness: ScoreOnly, score: 80},
		{name: "null boolean", body: `{"rate_limit":{"allowed":null,"limit_reached":false,"primary_window":{"used_percent":20}}}`, completeness: ScoreOnly, score: 80},
		{name: "exhaustion without reset", body: `{"rate_limit":{"allowed":false,"limit_reached":false,"primary_window":{"used_percent":70}}}`, completeness: ScoreOnly, score: 30},
		{name: "past reset", body: `{"rate_limit":{"allowed":false,"limit_reached":false,"primary_window":{"used_percent":70,"reset_at":1}}}`, completeness: ScoreOnly, score: 30},
		{name: "overflow reset at", body: `{"rate_limit":{"allowed":false,"limit_reached":false,"primary_window":{"used_percent":70,"reset_at":1e40}}}`, completeness: ScoreOnly, score: 30},
		{name: "overflow relative reset", body: `{"rate_limit":{"allowed":false,"limit_reached":false,"primary_window":{"used_percent":70,"reset_after_seconds":1e40}}}`, completeness: ScoreOnly, score: 30},
		{name: "no windows", body: `{"rate_limit":{"allowed":true,"limit_reached":false}}`, wantError: true},
		{name: "invalid primary", body: `{"rate_limit":{"primary_window":{"used_percent":101},"secondary_window":{"used_percent":30}}}`, wantError: true},
		{name: "invalid secondary", body: `{"rate_limit":{"primary_window":{"used_percent":20},"secondary_window":{"used_percent":"30"}}}`, wantError: true},
		{name: "invalid boolean", body: `{"rate_limit":{"allowed":"true","limit_reached":false,"primary_window":{"used_percent":20}}}`, wantError: true},
		{name: "camel case only", body: `{"rateLimit":{"allowed":true,"limitReached":false,"primaryWindow":{"usedPercent":20}}}`, wantError: true},
		{name: "trailing JSON", body: `{"rate_limit":{"primary_window":{"used_percent":20}}}{}`, wantError: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			observation, err := observeCodexManualQuery(manualResponseMetadata{CompletedAt: completedAt}, strings.NewReader(tt.body))
			if tt.wantError {
				if !errors.Is(err, errCodexQuotaResponseInvalid) {
					t.Fatalf("error = %v, want invalid response", err)
				}
				return
			}
			if err != nil {
				t.Fatalf("observeCodexManualQuery() error = %v", err)
			}
			assertCodexObservation(t, observation, tt.completeness, tt.score, 0, time.Time{})
		})
	}
}

func TestCodexExhaustionResetSelection(t *testing.T) {
	completedAt := time.Date(2026, time.January, 10, 12, 0, 0, 0, time.UTC)
	serverDate := completedAt.Add(-2 * time.Minute)
	tests := []struct {
		name    string
		body    string
		score   float64
		resetAt time.Time
	}{
		{
			name: "global exhaustion uses latest present window",
			body: `{"rate_limit":{"allowed":false,"limit_reached":false,` +
				`"primary_window":{"used_percent":70,"reset_after_seconds":600},` +
				`"secondary_window":{"used_percent":80,"reset_after_seconds":1200}}}`,
			score:   20,
			resetAt: serverDate.Add(20 * time.Minute),
		},
		{
			name: "limit reached overrides allowed",
			body: `{"rate_limit":{"allowed":true,"limit_reached":true,` +
				`"primary_window":{"used_percent":30,"reset_after_seconds":600}}}`,
			score:   70,
			resetAt: serverDate.Add(10 * time.Minute),
		},
		{
			name: "window exhaustion ignores non-blocking reset",
			body: `{"rate_limit":{"allowed":true,"limit_reached":false,` +
				`"primary_window":{"used_percent":100,"reset_after_seconds":600},` +
				`"secondary_window":{"used_percent":80,"reset_after_seconds":1200}}}`,
			resetAt: serverDate.Add(10 * time.Minute),
		},
		{
			name: "secondary exhaustion ignores later primary reset",
			body: `{"rate_limit":{"allowed":true,"limit_reached":false,` +
				`"primary_window":{"used_percent":80,"reset_after_seconds":1200},` +
				`"secondary_window":{"used_percent":100,"reset_after_seconds":600}}}`,
			resetAt: serverDate.Add(10 * time.Minute),
		},
		{
			name: "absolute reset preferred per window",
			body: `{"rate_limit":{"allowed":false,"limit_reached":false,` +
				`"primary_window":{"used_percent":100,"reset_at":1768048200,"reset_after_seconds":3600}}}`,
			resetAt: time.Date(2026, time.January, 10, 12, 30, 0, 0, time.UTC),
		},
		{
			name: "fractional absolute reset",
			body: `{"rate_limit":{"allowed":false,"limit_reached":false,` +
				`"primary_window":{"used_percent":100,"reset_at":1768048200.5}}}`,
			resetAt: time.Date(2026, time.January, 10, 12, 30, 0, 500_000_000, time.UTC),
		},
		{
			name: "fractional relative reset",
			body: `{"rate_limit":{"allowed":false,"limit_reached":false,` +
				`"primary_window":{"used_percent":100,"reset_after_seconds":180.5}}}`,
			resetAt: serverDate.Add(180*time.Second + 500*time.Millisecond),
		},
		{
			name: "completion fallback without server date",
			body: `{"rate_limit":{"allowed":false,"limit_reached":false,` +
				`"primary_window":{"used_percent":100,"reset_after_seconds":600}}}`,
			resetAt: completedAt.Add(10 * time.Minute),
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			metadata := manualResponseMetadata{CompletedAt: completedAt, ServerDate: serverDate}
			if tt.name == "completion fallback without server date" {
				metadata.ServerDate = time.Time{}
			}
			observation, err := observeCodexManualQuery(metadata, strings.NewReader(tt.body))
			if err != nil {
				t.Fatalf("observeCodexManualQuery() error = %v", err)
			}
			if observation.Score == nil || *observation.Score != tt.score ||
				observation.Completeness != ExhaustionEvidence || len(observation.Mutations) != 1 ||
				observation.Mutations[0].Scope != ScopeAuth || observation.Mutations[0].Outcome != Exhausted ||
				!observation.Mutations[0].ResetAt.Equal(tt.resetAt) {
				t.Fatalf("observation = %+v, want score %v auth exhaustion reset %v", observation, tt.score, tt.resetAt)
			}
		})
	}
}

func TestCodexRelativeResetRejectsSkewedServerDate(t *testing.T) {
	completedAt := time.Date(2026, time.January, 10, 12, 0, 0, 0, time.UTC)
	body := `{"rate_limit":{"allowed":false,"limit_reached":false,` +
		`"primary_window":{"used_percent":100,"reset_after_seconds":600}}}`
	tests := []struct {
		name       string
		serverDate time.Time
	}{
		{name: "future", serverDate: completedAt.Add(24 * time.Hour)},
		{name: "stale", serverDate: completedAt.Add(-24 * time.Hour)},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			observation, err := observeCodexManualQuery(manualResponseMetadata{
				CompletedAt: completedAt,
				ServerDate:  tt.serverDate,
			}, strings.NewReader(body))
			if err != nil {
				t.Fatalf("observeCodexManualQuery() error = %v", err)
			}
			wantReset := completedAt.Add(10 * time.Minute)
			if observation.Completeness != ExhaustionEvidence || len(observation.Mutations) != 1 ||
				!observation.Mutations[0].ResetAt.Equal(wantReset) {
				t.Fatalf("observation = %+v, want completion-anchored reset %v", observation, wantReset)
			}
		})
	}
}

func TestCodexDurationFromSecondsBoundaries(t *testing.T) {
	tests := []struct {
		name        string
		seconds     float64
		want        time.Duration
		wantPresent bool
	}{
		{name: "fractional", seconds: 0.25, want: 250 * time.Millisecond, wantPresent: true},
		{
			name:        "largest whole second",
			seconds:     float64(codexMaxDurationWholeSeconds),
			want:        time.Duration(codexMaxDurationWholeSeconds) * time.Second,
			wantPresent: true,
		},
		{name: "whole second overflow", seconds: float64(codexMaxDurationWholeSeconds + 1)},
		{name: "fractional overflow", seconds: float64(codexMaxDurationWholeSeconds) + 0.9},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, present := codexDurationFromSeconds(tt.seconds)
			if present != tt.wantPresent || got != tt.want {
				t.Fatalf("codexDurationFromSeconds(%v) = (%v, %v), want (%v, %v)", tt.seconds, got, present, tt.want, tt.wantPresent)
			}
		})
	}
}

func readCodexFixture(t *testing.T, name string) string {
	t.Helper()
	data, err := os.ReadFile(filepath.Join("testdata", "codex", name))
	if err != nil {
		t.Fatalf("read fixture %q: %v", name, err)
	}
	return string(data)
}

func assertCodexObservation(
	t *testing.T,
	observation Observation,
	completeness Completeness,
	score float64,
	outcome Outcome,
	resetAt time.Time,
) {
	t.Helper()
	if observation.Score == nil || *observation.Score != score || observation.Completeness != completeness {
		t.Fatalf("observation score/completeness = %+v, want score %.2f completeness %d", observation, score, completeness)
	}
	if completeness == ScoreOnly {
		if len(observation.Mutations) != 0 {
			t.Fatalf("score-only observation mutations = %+v", observation.Mutations)
		}
		return
	}
	if len(observation.Mutations) != 1 {
		t.Fatalf("observation mutations = %+v, want one", observation.Mutations)
	}
	mutation := observation.Mutations[0]
	if mutation.Scope != ScopeAuth || mutation.Outcome != outcome || !mutation.ResetAt.Equal(resetAt) {
		t.Fatalf("mutation = %+v, want outcome %d reset %v", mutation, outcome, resetAt)
	}
}
