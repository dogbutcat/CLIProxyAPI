package hub

import (
	"io"
	"math"
	"reflect"
	"testing"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/quota"
	"github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
)

func TestCanonicalTypeEnums(t *testing.T) {
	for _, scope := range []ScopeKind{ScopeAuth, ScopeModel} {
		if !scope.Valid() {
			t.Fatalf("ScopeKind(%d).Valid() = false", scope)
		}
	}
	if ScopeKind(0).Valid() || ScopeKind(255).Valid() {
		t.Fatal("unknown ScopeKind accepted")
	}
	if !ScopeAuth.Active() || ScopeModel.Active() {
		t.Fatal("only ScopeAuth must be active")
	}

	for _, source := range []SourceKind{ManagementManual, OpenCodeManual, OpenCodeScheduled} {
		if !source.Valid() {
			t.Fatalf("SourceKind(%d).Valid() = false", source)
		}
	}
	if SourceKind(0).Valid() || SourceKind(255).Valid() {
		t.Fatal("unknown SourceKind accepted")
	}

	for _, completeness := range []Completeness{ScoreOnly, ExhaustionEvidence, AuthoritativeSnapshot} {
		if !completeness.Valid() {
			t.Fatalf("Completeness(%d).Valid() = false", completeness)
		}
	}
	if Completeness(0).Valid() || Completeness(255).Valid() {
		t.Fatal("unknown Completeness accepted")
	}

	for _, outcome := range []Outcome{Healthy, Exhausted} {
		if !outcome.Valid() {
			t.Fatalf("Outcome(%d).Valid() = false", outcome)
		}
	}
	if Outcome(0).Valid() || Outcome(255).Valid() {
		t.Fatal("unknown Outcome accepted")
	}
}

func TestObservationBatchTypeValidation(t *testing.T) {
	now := time.Now()
	valid := validObservationBatch(now)
	tests := []struct {
		name   string
		mutate func(*ObservationBatch)
	}{
		{name: "missing auth ID", mutate: func(batch *ObservationBatch) { batch.AuthID = "" }},
		{name: "missing provider", mutate: func(batch *ObservationBatch) { batch.Provider = "" }},
		{name: "ticket auth mismatch", mutate: func(batch *ObservationBatch) { batch.Ticket.AuthID = "other" }},
		{name: "ticket provider mismatch", mutate: func(batch *ObservationBatch) { batch.Ticket.Provider = "claude" }},
		{name: "invalid ticket", mutate: func(batch *ObservationBatch) { batch.Ticket.StartOrder = 0 }},
		{name: "invalid source", mutate: func(batch *ObservationBatch) { batch.Metadata.Source = 0 }},
		{name: "missing completion time", mutate: func(batch *ObservationBatch) { batch.Metadata.CompletedAt = time.Time{} }},
		{name: "invalid completeness", mutate: func(batch *ObservationBatch) { batch.Completeness = 0 }},
		{name: "invalid score", mutate: func(batch *ObservationBatch) { score := math.NaN(); batch.Score = &score }},
		{name: "missing mutation", mutate: func(batch *ObservationBatch) { batch.Mutations = nil }},
		{name: "missing scope", mutate: func(batch *ObservationBatch) { batch.Mutations[0].Scope = 0 }},
		{name: "reserved model scope", mutate: func(batch *ObservationBatch) {
			batch.Mutations[0].Scope = ScopeModel
			batch.Mutations[0].Model = "gpt-5"
		}},
		{name: "auth scope model", mutate: func(batch *ObservationBatch) { batch.Mutations[0].Model = "gpt-5" }},
		{name: "invalid outcome", mutate: func(batch *ObservationBatch) { batch.Mutations[0].Outcome = 0 }},
		{name: "healthy evidence", mutate: func(batch *ObservationBatch) {
			batch.Mutations[0].Outcome = Healthy
			batch.Mutations[0].ResetAt = time.Time{}
		}},
		{name: "expired reset", mutate: func(batch *ObservationBatch) { batch.Mutations[0].ResetAt = now }},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			batch := cloneObservationBatch(valid)
			tt.mutate(&batch)
			if err := batch.Validate(); err == nil {
				t.Fatal("Validate() accepted invalid batch")
			}
		})
	}

	if err := valid.Validate(); err != nil {
		t.Fatalf("Validate() error = %v", err)
	}

	healthy := cloneObservationBatch(valid)
	healthy.Completeness = AuthoritativeSnapshot
	healthy.Mutations[0].Outcome = Healthy
	healthy.Mutations[0].ResetAt = time.Time{}
	if err := healthy.Validate(); err != nil {
		t.Fatalf("Validate() rejected healthy authoritative snapshot: %v", err)
	}

	scoreOnly := cloneObservationBatch(valid)
	scoreOnly.Completeness = ScoreOnly
	scoreOnly.Mutations = nil
	if err := scoreOnly.Validate(); err != nil {
		t.Fatalf("Validate() rejected score-only observation: %v", err)
	}
}

func TestObservationBatchTypeMapsToAuthOwnedBatch(t *testing.T) {
	now := time.Now()
	batch := validObservationBatch(now)
	mapped, err := batch.ToAuthBatch()
	if err != nil {
		t.Fatalf("ToAuthBatch() error = %v", err)
	}
	if mapped.Ticket != batch.Ticket {
		t.Fatalf("mapped ticket = %+v, want %+v", mapped.Ticket, batch.Ticket)
	}
	if mapped.Completeness != auth.QuotaObservationCompletenessExhaustionEvidence {
		t.Fatalf("mapped completeness = %d", mapped.Completeness)
	}
	if mapped.Score == nil || *mapped.Score != *batch.Score {
		t.Fatalf("mapped score = %v, want %v", mapped.Score, batch.Score)
	}
	if len(mapped.Mutations) != 1 {
		t.Fatalf("mapped mutation count = %d", len(mapped.Mutations))
	}
	mutation := mapped.Mutations[0]
	if mutation.Scope != auth.QuotaObservationScopeAuth || mutation.Model != "" ||
		mutation.Outcome != auth.QuotaObservationOutcomeExhausted ||
		!mutation.ResetAt.Equal(batch.Mutations[0].ResetAt) {
		t.Fatalf("mapped mutation = %+v", mutation)
	}

	*mapped.Score = 1
	mapped.Mutations[0].ResetAt = now.Add(48 * time.Hour)
	if *batch.Score == 1 || batch.Mutations[0].ResetAt.Equal(mapped.Mutations[0].ResetAt) {
		t.Fatal("ToAuthBatch() returned aliases into canonical batch")
	}
}

func TestManualAdapterTableDispatchAndImmutableCopies(t *testing.T) {
	table := newManualAdapterTable(
		manualTestAdapter("codex", "chatgpt.com"),
		manualTestAdapter("claude", "api.anthropic.com"),
	)
	if len(table.entries) != 2 {
		t.Fatalf("entry count = %d, want 2", len(table.entries))
	}

	query := manualQueryMetadata{
		Provider: " CODEX ",
		Method:   "GET",
		Scheme:   "https",
		Host:     "chatgpt.com",
		Hostname: "chatgpt.com",
		Path:     "/backend-api/wham/usage",
	}
	got, ok := table.match(query)
	if !ok || got.provider != "codex" {
		t.Fatalf("match() = (%v, %v), want codex adapter", got.provider, ok)
	}
	query.Provider = ""
	if _, ok := table.match(query); ok {
		t.Fatal("match() accepted missing provider")
	}
	query.Provider = "claude"
	if _, ok := table.match(query); ok {
		t.Fatal("match() dispatched across provider boundary")
	}

	copyTable := newManualAdapterTable(table.entries...)
	copyTable.entries[0].provider = "mutated"
	if got := table.entries[0].provider; got != "codex" {
		t.Fatalf("clone mutation changed source table: %q", got)
	}

	firstProductionTable := activeManualAdapterTable()
	if len(firstProductionTable.entries) > 0 {
		firstProductionTable.entries[0].provider = "mutated"
		if activeManualAdapterTable().entries[0].provider == "mutated" {
			t.Fatal("activeManualAdapterTable() returned shared production storage")
		}
	}
}

func TestManualAdapterTableTypeExcludesMutableAdapterObjects(t *testing.T) {
	metadataType := reflect.TypeOf(manualQueryMetadata{})
	for index := 0; index < metadataType.NumField(); index++ {
		field := metadataType.Field(index)
		if field.Type.Kind() != reflect.String {
			t.Fatalf("manualQueryMetadata field %q has mutable kind %s", field.Name, field.Type.Kind())
		}
	}

	adapterType := reflect.TypeOf(manualQueryAdapter{})
	for index := 0; index < adapterType.NumField(); index++ {
		field := adapterType.Field(index)
		switch field.Type.Kind() {
		case reflect.String, reflect.Func:
		default:
			t.Fatalf("manualQueryAdapter field %q has mutable-object kind %s", field.Name, field.Type.Kind())
		}
	}
	if adapterType.NumMethod() != 0 || reflect.TypeOf(manualAdapterTable{}).NumMethod() != 0 {
		t.Fatal("manual adapter types expose methods outside package hub")
	}
}

func TestOpenCodeResultAdapterTypeContract(t *testing.T) {
	completedAt := time.Now()
	metadata := openCodeResultMetadata{
		Source:              OpenCodeScheduled,
		CompletedAt:         completedAt,
		ThresholdConfigured: true,
		Threshold:           5,
	}
	result := &quota.PollResult{EntryName: "auth-1", Timestamp: completedAt}
	called := false
	adapter := openCodeResultAdapter{observe: func(gotMetadata openCodeResultMetadata, gotResult *quota.PollResult) (Observation, error) {
		called = true
		if gotMetadata != metadata {
			t.Fatalf("metadata = %+v, want %+v", gotMetadata, metadata)
		}
		if gotResult != result {
			t.Fatal("typed poll result pointer was not preserved")
		}
		return Observation{Completeness: ScoreOnly}, nil
	}}
	observation, available, err := adapter.observeResult(metadata, result)
	if err != nil || !available || !called || observation.Completeness != ScoreOnly {
		t.Fatalf("observeResult() = (%+v, %v, %v), called=%v", observation, available, err, called)
	}

	if _, available, err = (openCodeResultAdapter{}).observeResult(metadata, result); err != nil || available {
		t.Fatalf("empty observeResult() = (available=%v, err=%v)", available, err)
	}
}

func manualTestAdapter(provider, host string) manualQueryAdapter {
	return manualQueryAdapter{
		provider: provider,
		match: func(query manualQueryMetadata) bool {
			return query.Hostname == host
		},
		observe: func(manualResponseMetadata, io.Reader) (Observation, error) {
			return Observation{}, nil
		},
	}
}

func validObservationBatch(now time.Time) ObservationBatch {
	score := 12.5
	return ObservationBatch{
		AuthID:   "auth-1",
		Provider: "opencode-go",
		Ticket: auth.QuotaObservationTicket{
			AuthID:     "auth-1",
			Provider:   "opencode-go",
			Generation: 1,
			Revision:   2,
			StartOrder: 3,
		},
		Metadata: ObservationMetadata{
			Source:      OpenCodeScheduled,
			CompletedAt: now,
			ServerDate:  now.Add(-time.Second),
		},
		Observation: Observation{
			Score:        &score,
			Completeness: ExhaustionEvidence,
			Mutations: []Mutation{{
				Scope:   ScopeAuth,
				Outcome: Exhausted,
				ResetAt: now.Add(time.Hour),
			}},
		},
	}
}

func cloneObservationBatch(batch ObservationBatch) ObservationBatch {
	clone := batch
	if batch.Score != nil {
		score := *batch.Score
		clone.Score = &score
	}
	clone.Mutations = append([]Mutation(nil), batch.Mutations...)
	return clone
}
