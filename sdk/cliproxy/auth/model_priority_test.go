package auth

import (
	"context"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/registry"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/executor"
)

func TestFillFirstSelectorPick_ModelSpecificPriority(t *testing.T) {
	t.Parallel()

	selector := &FillFirstSelector{}
	auths := []*Auth{
		{ID: "openai-first", Attributes: map[string]string{
			AttributeModelPriorities: `{"gpt-visible":10,"claude-visible":1}`,
		}},
		{ID: "anthropic-first", Attributes: map[string]string{
			AttributeModelPriorities: `{"gpt-visible":1,"claude-visible":10}`,
		}},
	}

	openAI, errOpenAI := selector.Pick(context.Background(), "opencode-go", "gpt-visible(high)", cliproxyexecutor.Options{}, auths)
	if errOpenAI != nil || openAI == nil || openAI.ID != "openai-first" {
		t.Fatalf("Pick(gpt-visible) = %+v, %v; want openai-first", openAI, errOpenAI)
	}
	claude, errClaude := selector.Pick(context.Background(), "opencode-go", "claude-visible", cliproxyexecutor.Options{}, auths)
	if errClaude != nil || claude == nil || claude.ID != "anthropic-first" {
		t.Fatalf("Pick(claude-visible) = %+v, %v; want anthropic-first", claude, errClaude)
	}
}

func TestSchedulerPick_ModelSpecificPriority(t *testing.T) {
	modelRegistry := registry.GetGlobalRegistry()
	for _, authID := range []string{"openai-first", "anthropic-first"} {
		modelRegistry.RegisterClient(authID, "opencode-go", []*registry.ModelInfo{{ID: "gpt-visible"}, {ID: "claude-visible"}})
	}
	t.Cleanup(func() {
		modelRegistry.UnregisterClient("openai-first")
		modelRegistry.UnregisterClient("anthropic-first")
	})
	scheduler := newSchedulerForTest(
		&FillFirstSelector{},
		&Auth{ID: "openai-first", Provider: "opencode-go", Attributes: map[string]string{
			AttributeModelPriorities: `{"gpt-visible":10,"claude-visible":1}`,
		}},
		&Auth{ID: "anthropic-first", Provider: "opencode-go", Attributes: map[string]string{
			AttributeModelPriorities: `{"gpt-visible":1,"claude-visible":10}`,
		}},
	)

	openAI, errOpenAI := scheduler.pickSingle(context.Background(), "opencode-go", "gpt-visible", cliproxyexecutor.Options{}, nil)
	if errOpenAI != nil || openAI == nil || openAI.ID != "openai-first" {
		t.Fatalf("pickSingle(gpt-visible) = %+v, %v; want openai-first", openAI, errOpenAI)
	}
	claude, errClaude := scheduler.pickSingle(context.Background(), "opencode-go", "claude-visible", cliproxyexecutor.Options{}, nil)
	if errClaude != nil || claude == nil || claude.ID != "anthropic-first" {
		t.Fatalf("pickSingle(claude-visible) = %+v, %v; want anthropic-first", claude, errClaude)
	}
}

func TestSchedulerAuthCandidates_ModelSpecificPriority(t *testing.T) {
	auths := []*Auth{
		{ID: "openai-first", Provider: "opencode-go", Attributes: map[string]string{
			AttributeModelPriorities: `{"gpt-visible":10,"claude-visible":1}`,
		}},
		{ID: "anthropic-first", Provider: "opencode-go", Attributes: map[string]string{
			AttributeModelPriorities: `{"gpt-visible":1,"claude-visible":10}`,
		}},
	}

	openAI := schedulerAuthCandidates(auths, "gpt-visible(high)")
	if len(openAI) != 2 || openAI[0].Priority != 10 || openAI[1].Priority != 1 {
		t.Fatalf("schedulerAuthCandidates(gpt-visible) priorities = %#v, want [10 1]", openAI)
	}
	claude := schedulerAuthCandidates(auths, "claude-visible")
	if len(claude) != 2 || claude[0].Priority != 1 || claude[1].Priority != 10 {
		t.Fatalf("schedulerAuthCandidates(claude-visible) priorities = %#v, want [1 10]", claude)
	}
}
