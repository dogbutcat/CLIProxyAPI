package oagmsg

import (
	"testing"

	"github.com/tidwall/gjson"
)

func TestTokenCount_Claude(t *testing.T) {
	h := &AnthropicHandler{}
	out := h.FormatTokenCount(42)
	root := gjson.ParseBytes(out)
	if root.Get("input_tokens").Int() != 42 {
		t.Errorf("input_tokens = %d, want 42", root.Get("input_tokens").Int())
	}
}

func TestTokenCount_Gemini(t *testing.T) {
	h := &GeminiHandler{}
	out := h.FormatTokenCount(100)
	root := gjson.ParseBytes(out)
	if root.Get("totalTokens").Int() != 100 {
		t.Errorf("totalTokens = %d, want 100", root.Get("totalTokens").Int())
	}
	details := root.Get("promptTokensDetails")
	if !details.IsArray() || len(details.Array()) != 1 {
		t.Fatal("promptTokensDetails should have 1 element")
	}
	if details.Array()[0].Get("modality").String() != "TEXT" {
		t.Error("modality should be TEXT")
	}
	if details.Array()[0].Get("tokenCount").Int() != 100 {
		t.Errorf("tokenCount = %d, want 100", details.Array()[0].Get("tokenCount").Int())
	}
}

func TestTokenCount_Antigravity_InheritsGemini(t *testing.T) {
	h := &AntigravityHandler{}
	out := h.FormatTokenCount(55)
	root := gjson.ParseBytes(out)
	if root.Get("totalTokens").Int() != 55 {
		t.Errorf("totalTokens = %d, want 55 (inherited from GeminiHandler)", root.Get("totalTokens").Int())
	}
}

func TestTokenCount_OpenAI_Nil(t *testing.T) {
	h := &OpenAIHandler{}
	out := FormatTokenCount(h, 10)
	if out != nil {
		t.Errorf("OpenAI TokenCount should be nil, got %s", out)
	}
}

func TestTokenCount_Interactions_Nil(t *testing.T) {
	h := &InteractionsHandler{}
	out := FormatTokenCount(h, 10)
	if out != nil {
		t.Errorf("Interactions TokenCount should be nil, got %s", out)
	}
}

func TestTokenCount_Codex_Nil(t *testing.T) {
	h := &CodexHandler{}
	out := FormatTokenCount(h, 10)
	if out != nil {
		t.Errorf("Codex TokenCount should be nil, got %s", out)
	}
}

func TestTokenCount_FormatTokenCountHelper(t *testing.T) {
	// Test the convenience function with a handler that implements the interface.
	h := &AnthropicHandler{}
	out := FormatTokenCount(h, 99)
	if out == nil {
		t.Fatal("FormatTokenCount should return non-nil for Anthropic")
	}
	root := gjson.ParseBytes(out)
	if root.Get("input_tokens").Int() != 99 {
		t.Errorf("input_tokens = %d, want 99", root.Get("input_tokens").Int())
	}
}
