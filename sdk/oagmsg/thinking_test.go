package oagmsg

import (
	"encoding/json"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/thinking"
	"github.com/tidwall/gjson"
)

func TestExtractThinkingUsesInternalThinkingConfig(t *testing.T) {
	cases := []struct {
		name    string
		root    string
		extract func(gjson.Result) *thinking.ThinkingConfig
		mode    thinking.ThinkingMode
		budget  int
		level   thinking.ThinkingLevel
	}{
		{
			name:    "Anthropic budget",
			root:    `{"thinking":{"type":"enabled","budget_tokens":16000}}`,
			extract: ExtractAnthropicThinking,
			mode:    thinking.ModeBudget,
			budget:  16000,
		},
		{
			name:    "OpenAI level",
			root:    `{"reasoning_effort":"high"}`,
			extract: ExtractOpenAIThinking,
			mode:    thinking.ModeLevel,
			level:   thinking.LevelHigh,
		},
		{
			name:    "Responses none",
			root:    `{"reasoning":{"effort":"none"}}`,
			extract: ExtractInteractionsThinking,
			mode:    thinking.ModeNone,
		},
		{
			name:    "Gemini auto budget",
			root:    `{"generationConfig":{"thinkingConfig":{"thinkingBudget":-1}}}`,
			extract: ExtractGeminiThinking,
			mode:    thinking.ModeAuto,
			budget:  -1,
		},
	}
	for _, tt := range cases {
		t.Run(tt.name, func(t *testing.T) {
			config := tt.extract(gjson.Parse(tt.root))
			if config == nil {
				t.Fatal("missing thinking config")
			}
			if config.Mode != tt.mode || config.Budget != tt.budget || config.Level != tt.level {
				t.Fatalf("config = %#v", *config)
			}
		})
	}
}

func TestSetThinkingStoresCanonicalConfig(t *testing.T) {
	req := &UnifiedRequest{}
	config := &thinking.ThinkingConfig{Mode: thinking.ModeBudget, Budget: 24576}
	req.SetThinking(config)
	if req.Thinking != config {
		t.Fatal("request did not store the internal thinking config pointer")
	}
	if req.ReasoningEffort != "high" {
		t.Fatalf("ReasoningEffort = %q, want high", req.ReasoningEffort)
	}
}

func TestApplyThinkingUsesExistingConversions(t *testing.T) {
	config := &thinking.ThinkingConfig{Mode: thinking.ModeLevel, Level: thinking.LevelHigh}

	anthropic := map[string]any{}
	ApplyAnthropicThinking(config, anthropic)
	if anthropic["thinking"].(map[string]any)["type"] != "adaptive" {
		t.Fatalf("Anthropic thinking = %#v", anthropic)
	}
	if anthropic["output_config"].(map[string]any)["effort"] != "high" {
		t.Fatalf("Anthropic output_config = %#v", anthropic)
	}

	gemini := map[string]any{}
	ApplyGeminiThinking(&thinking.ThinkingConfig{Mode: thinking.ModeBudget, Budget: 8192}, gemini)
	if gemini["thinkingConfig"].(map[string]any)["thinkingBudget"] != 8192 {
		t.Fatalf("Gemini thinking = %#v", gemini)
	}

	openAI := map[string]any{}
	ApplyOpenAIThinking(&thinking.ThinkingConfig{Mode: thinking.ModeBudget, Budget: 24576}, openAI)
	if openAI["reasoning_effort"] != "high" {
		t.Fatalf("OpenAI thinking = %#v", openAI)
	}
}

func TestParseSerializeRequestCarriesCanonicalThinking(t *testing.T) {
	raw := []byte(`{"model":"gpt-5","input":[{"type":"message","role":"user","content":[{"type":"input_text","text":"hi"}]}],"reasoning":{"effort":"high"}}`)
	req, err := (&InteractionsHandler{}).ParseRequest(raw)
	if err != nil {
		t.Fatal(err)
	}
	if req.Thinking == nil || req.Thinking.Mode != thinking.ModeLevel || req.Thinking.Level != thinking.LevelHigh {
		t.Fatalf("thinking = %#v", req.Thinking)
	}
	out, err := (&OpenAIHandler{}).SerializeRequest(req)
	if err != nil {
		t.Fatal(err)
	}
	var decoded map[string]any
	if err := json.Unmarshal(out, &decoded); err != nil {
		t.Fatal(err)
	}
	if decoded["reasoning_effort"] != "high" {
		t.Fatalf("serialized request = %s", out)
	}
}
