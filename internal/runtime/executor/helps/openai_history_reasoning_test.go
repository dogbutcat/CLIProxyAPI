package helps

import (
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/registry"
	"github.com/tidwall/gjson"
)

func TestOpenAICompatHistoryReasoningStripsKnownThinkingModel(t *testing.T) {
	info := &registry.ModelInfo{
		ID:      "glm-5.2",
		OwnedBy: "zhipu",
		Type:    "openai",
		Thinking: &registry.ThinkingSupport{
			Levels: []string{"high", "max"},
		},
	}
	payload := []byte(`{"messages":[{"role":"user","content":"q","reasoning_content":"keep user field"},{"role":"assistant","content":"a","reasoning_content":"drop assistant field","tool_calls":[{"id":"call_1","type":"function","function":{"name":"lookup","arguments":"{}"}}]},{"role":"tool","tool_call_id":"call_1","content":"ok"},{"role":"assistant","content":"done","reasoning_content":""}]}`)

	if !shouldStripOpenAICompatHistoryReasoning("glm-5.2", info) {
		t.Fatal("known thinking model should strip history reasoning")
	}

	out := stripAssistantHistoryReasoningContent(payload)
	if !gjson.ValidBytes(out) {
		t.Fatalf("output is invalid JSON: %s", out)
	}
	if gjson.GetBytes(out, "messages.1.reasoning_content").Exists() {
		t.Fatalf("assistant reasoning_content was not stripped: %s", out)
	}
	if gjson.GetBytes(out, "messages.3.reasoning_content").Exists() {
		t.Fatalf("empty assistant reasoning_content was not stripped: %s", out)
	}
	if got := gjson.GetBytes(out, "messages.0.reasoning_content").String(); got != "keep user field" {
		t.Fatalf("user reasoning_content = %q; body=%s", got, out)
	}
	if got := gjson.GetBytes(out, "messages.1.content").String(); got != "a" {
		t.Fatalf("assistant content = %q; body=%s", got, out)
	}
	if got := gjson.GetBytes(out, "messages.1.tool_calls.0.id").String(); got != "call_1" {
		t.Fatalf("tool call id = %q; body=%s", got, out)
	}
	if got := gjson.GetBytes(out, "messages.2.tool_call_id").String(); got != "call_1" {
		t.Fatalf("tool_call_id = %q; body=%s", got, out)
	}
}

func TestOpenAICompatHistoryReasoningPreservesDeepSeekAndUnknown(t *testing.T) {
	payload := []byte(`{"messages":[{"role":"assistant","content":"a","reasoning_content":"required"}]}`)
	deepSeekInfo := &registry.ModelInfo{
		ID:      "alias-reasoner",
		OwnedBy: "DeepSeek",
		Type:    "openai",
		Thinking: &registry.ThinkingSupport{
			Levels: []string{"high", "max"},
		},
	}

	tests := []struct {
		name  string
		model string
		info  *registry.ModelInfo
	}{
		{name: "model name", model: "deepseek-reasoner", info: &registry.ModelInfo{ID: "deepseek-reasoner", Thinking: &registry.ThinkingSupport{Levels: []string{"high"}}}},
		{name: "model info owner", model: "alias-reasoner", info: deepSeekInfo},
		{name: "unknown", model: "unknown-reasoning-model", info: nil},
		{name: "user defined", model: "user-openai", info: &registry.ModelInfo{ID: "user-openai", UserDefined: true, Thinking: &registry.ThinkingSupport{Levels: []string{"high"}}}},
		{name: "no thinking metadata", model: "known-text-model", info: &registry.ModelInfo{ID: "known-text-model", Type: "openai"}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if shouldStripOpenAICompatHistoryReasoning(tt.model, tt.info) {
				t.Fatal("payload should be preserved")
			}
			out := payload
			if shouldStripOpenAICompatHistoryReasoning(tt.model, tt.info) {
				out = stripAssistantHistoryReasoningContent(payload)
			}
			if string(out) != string(payload) {
				t.Fatalf("payload changed: %s", out)
			}
		})
	}
}

func TestOpenAICompatHistoryReasoningLeavesNonChatPayloadsUnchanged(t *testing.T) {
	inputs := [][]byte{
		[]byte(`{"input":[{"role":"assistant","reasoning_content":"keep"}]}`),
		[]byte(`{"messages":{"role":"assistant","reasoning_content":"keep"}}`),
		[]byte(`{"messages":[{"role":"user","reasoning_content":"keep"}]}`),
		[]byte(`{"messages":[{"role":"assistant","content":"no reasoning"}]}`),
		[]byte(`{"messages":[`),
	}

	for _, input := range inputs {
		out := stripAssistantHistoryReasoningContent(input)
		if string(out) != string(input) {
			t.Fatalf("payload changed: got %s want %s", out, input)
		}
	}
}
