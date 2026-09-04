package auth

import (
	"context"
	"testing"

	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/executor"
)

func TestSeqRandomStartSelectorPick_ContinuesSequentiallyAfterInitialPick(t *testing.T) {
	selector := &SeqRandomStartSelector{}
	auths := []*Auth{
		{ID: "auth-a", Provider: "gemini"},
		{ID: "auth-b", Provider: "gemini"},
		{ID: "auth-c", Provider: "gemini"},
	}

	first, errPick := selector.Pick(context.Background(), "gemini", "", cliproxyexecutor.Options{}, auths)
	if errPick != nil {
		t.Fatalf("Pick() first error = %v", errPick)
	}
	second, errPick := selector.Pick(context.Background(), "gemini", "", cliproxyexecutor.Options{}, auths)
	if errPick != nil {
		t.Fatalf("Pick() second error = %v", errPick)
	}
	if first == nil || second == nil {
		t.Fatal("Pick() returned nil auth")
	}

	wantNext := map[string]string{
		"auth-a": "auth-b",
		"auth-b": "auth-c",
		"auth-c": "auth-a",
	}
	if second.ID != wantNext[first.ID] {
		t.Fatalf("second auth.ID = %q after %q, want %q", second.ID, first.ID, wantNext[first.ID])
	}
}

func TestSeqRandomStartSelectorPick_RandomizesInitialStartWithoutQuotaScores(t *testing.T) {
	const pools = 128
	starts := make(map[string]int)
	auths := []*Auth{
		{ID: "auth-a", Provider: "gemini"},
		{ID: "auth-b", Provider: "gemini"},
		{ID: "auth-c", Provider: "gemini"},
	}

	for index := 0; index < pools; index++ {
		selector := &SeqRandomStartSelector{}
		got, errPick := selector.Pick(context.Background(), "gemini", "", cliproxyexecutor.Options{}, auths)
		if errPick != nil {
			t.Fatalf("Pick() pool #%d error = %v", index, errPick)
		}
		if got == nil {
			t.Fatalf("Pick() pool #%d auth = nil", index)
		}
		starts[got.ID]++
	}

	if len(starts) < 2 {
		t.Fatalf("initial starts = %v, want random starts across more than one index", starts)
	}
}

func TestSeqRandomStartSelectorPick_WeightedInitialStart(t *testing.T) {
	const picks = 1000
	counts := make(map[string]int)
	for index := 0; index < picks; index++ {
		selector := &SeqRandomStartSelector{}
		selector.setQuotaScoreLookup(func(authID string) (float64, bool) {
			if authID == "auth-high" {
				return 100, true
			}
			return 1, true
		})
		got, errPick := selector.Pick(context.Background(), "gemini", "", cliproxyexecutor.Options{}, []*Auth{
			{ID: "auth-low-a", Provider: "gemini"},
			{ID: "auth-high", Provider: "gemini"},
			{ID: "auth-low-b", Provider: "gemini"},
		})
		if errPick != nil {
			t.Fatalf("Pick() error = %v", errPick)
		}
		counts[got.ID]++
	}

	if counts["auth-high"] < 900 {
		t.Fatalf("weighted initial picks for auth-high = %d/%d, want broad majority; counts=%v", counts["auth-high"], picks, counts)
	}
}

func TestSeqRandomStartSelectorPick_QuotaScoresOnlySeedInitialStart(t *testing.T) {
	selector := &SeqRandomStartSelector{}
	scores := map[string]float64{
		"auth-a": 0,
		"auth-b": 1,
		"auth-c": 0,
	}
	selector.setQuotaScoreLookup(func(authID string) (float64, bool) {
		score, ok := scores[authID]
		return score, ok
	})
	auths := []*Auth{
		{ID: "auth-a", Provider: "gemini"},
		{ID: "auth-b", Provider: "gemini"},
		{ID: "auth-c", Provider: "gemini"},
	}

	first, errPick := selector.Pick(context.Background(), "gemini", "", cliproxyexecutor.Options{}, auths)
	if errPick != nil {
		t.Fatalf("Pick() first error = %v", errPick)
	}
	if first == nil || first.ID != "auth-b" {
		t.Fatalf("first auth = %#v, want quota-weighted seed auth-b", first)
	}
	scores["auth-b"] = 100

	second, errPick := selector.Pick(context.Background(), "gemini", "", cliproxyexecutor.Options{}, auths)
	if errPick != nil {
		t.Fatalf("Pick() second error = %v", errPick)
	}
	if second == nil || second.ID != "auth-c" {
		t.Fatalf("second auth = %#v, want sequential auth-c", second)
	}
}

func TestSeqRandomStartSelectorPick_PoolChangesContinueFromStableSuccessor(t *testing.T) {
	selector := &SeqRandomStartSelector{}
	scores := map[string]float64{
		"auth-a": 0,
		"auth-b": 1,
		"auth-c": 0,
		"auth-d": 0,
	}
	selector.setQuotaScoreLookup(func(authID string) (float64, bool) {
		score, ok := scores[authID]
		return score, ok
	})
	authA := &Auth{ID: "auth-a", Provider: "gemini"}
	authB := &Auth{ID: "auth-b", Provider: "gemini"}
	authC := &Auth{ID: "auth-c", Provider: "gemini"}
	authD := &Auth{ID: "auth-d", Provider: "gemini"}
	auths := []*Auth{authD, authB, authA, authC}

	first, errPick := selector.Pick(context.Background(), "gemini", "", cliproxyexecutor.Options{}, auths)
	if errPick != nil {
		t.Fatalf("Pick() first error = %v", errPick)
	}
	if first == nil || first.ID != "auth-b" {
		t.Fatalf("first auth = %#v, want quota-weighted seed auth-b", first)
	}

	scores["auth-a"] = 1000
	authC.Disabled = true
	second, errPick := selector.Pick(context.Background(), "gemini", "", cliproxyexecutor.Options{}, auths)
	if errPick != nil {
		t.Fatalf("Pick() second error = %v", errPick)
	}
	if second == nil || second.ID != "auth-d" {
		t.Fatalf("second auth = %#v, want successor auth-d after auth-c became unavailable", second)
	}

	authC.Disabled = false
	third, errPick := selector.Pick(context.Background(), "gemini", "", cliproxyexecutor.Options{}, auths)
	if errPick != nil {
		t.Fatalf("Pick() third error = %v", errPick)
	}
	if third == nil || third.ID != "auth-a" {
		t.Fatalf("third auth = %#v, want wraparound successor auth-a after auth-d", third)
	}
}

func TestSeqRandomStartSelectorPick_SkipsUnavailableAndQuotaExceeded(t *testing.T) {
	selector := &SeqRandomStartSelector{}
	available := &Auth{ID: "auth-available", Provider: "gemini"}
	got, errPick := selector.Pick(context.Background(), "gemini", "", cliproxyexecutor.Options{}, []*Auth{
		{ID: "auth-disabled", Provider: "gemini", Disabled: true},
		{ID: "auth-quota", Provider: "gemini", Quota: QuotaState{Exceeded: true}},
		available,
	})
	if errPick != nil {
		t.Fatalf("Pick() error = %v", errPick)
	}
	if got == nil || got.ID != available.ID {
		t.Fatalf("Pick() auth = %v, want %s", got, available.ID)
	}
}

func TestSeqRandomStartSelectorFindNextIndex(t *testing.T) {
	auths := []*Auth{
		{ID: "auth-a"},
		{ID: "auth-c"},
		{ID: "auth-e"},
	}
	tests := []struct {
		name   string
		lastID string
		want   int
	}{
		{name: "known advances", lastID: "auth-a", want: 1},
		{name: "missing between", lastID: "auth-b", want: 1},
		{name: "missing after all wraps", lastID: "auth-z", want: 0},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := seqFindNextIndex(auths, tt.lastID); got != tt.want {
				t.Fatalf("seqFindNextIndex() = %d, want %d", got, tt.want)
			}
		})
	}
}
