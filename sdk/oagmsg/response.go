package oagmsg

import (
	"fmt"
	"time"
)

// UnifiedUsage holds token usage statistics.
type UnifiedUsage struct {
	PromptTokens     int `json:"prompt_tokens"`
	CompletionTokens int `json:"completion_tokens"`
	TotalTokens      int `json:"total_tokens"`
	// CachedTokens is the number of prompt tokens served from cache.
	// Maps to OpenAI's prompt_tokens_details.cached_tokens.
	CachedTokens int `json:"cached_tokens,omitempty"`
	// ReasoningTokens is the number of output tokens spent on reasoning/thinking.
	ReasoningTokens int `json:"reasoning_tokens,omitempty"`
	// CacheCreationInputTokens is the number of tokens written to cache (Anthropic-specific).
	CacheCreationInputTokens int `json:"cache_creation_input_tokens,omitempty"`
	// CacheReadInputTokens is the number of tokens read from cache (Anthropic-specific).
	CacheReadInputTokens int `json:"cache_read_input_tokens,omitempty"`

	usagePresence usagePresence
	usageOrigin   Format
	// serverToolUseWebSearchRequests is Anthropic usage metadata for native
	// server-side web_search requests.
	serverToolUseWebSearchRequests int
}

type usagePresence struct {
	Prompt        bool
	Completion    bool
	Total         bool
	DerivedTotal  bool
	Cached        bool
	CacheCreation bool
	CacheRead     bool
	Reasoning     bool
}

// UnifiedResponse is the protocol-agnostic non-streaming response.
// Produced by adapters, consumed by ProtocolHandler.FormatResponse().
//
// Aligned with oag_server models.py UnifiedResponse (L284-297).
type UnifiedResponse struct {
	ID           string           `json:"id"`
	Model        string           `json:"model"`
	Content      string           `json:"content"`
	FinishReason string           `json:"finish_reason"`
	Usage        *UnifiedUsage    `json:"usage,omitempty"`
	Created      int64            `json:"created"`
	ToolCalls    []map[string]any `json:"tool_calls,omitempty"` // model-initiated tool calls
	// ThinkingContent holds model reasoning/thinking text from explicit non-stream responses.
	ThinkingContent string `json:"thinking_content,omitempty"`
	// ThinkingSignature holds opaque reasoning signatures for round-trip fidelity.
	ThinkingSignature         string           `json:"thinking_signature,omitempty"`
	responsesOutput           []map[string]any // Responses-only raw output sidecar.
	responseContent           []ContentBlock   // Ordered response blocks for provider-specific content.
	preferResponseModel       bool
	skipUnknownResponseFields bool
}

// NewUnifiedResponse creates a UnifiedResponse with sensible defaults.
func NewUnifiedResponse() *UnifiedResponse {
	return &UnifiedResponse{
		ID:           fmt.Sprintf("chatcmpl-%d", time.Now().UnixNano()),
		FinishReason: "stop",
		Created:      time.Now().Unix(),
	}
}

// UnifiedError is the protocol-agnostic error representation.
// Consumed by ProtocolHandler.FormatError() to produce protocol-specific error JSON.
//
// Aligned with oag_server models.py UnifiedError (L324-328).
type UnifiedError struct {
	StatusCode int    `json:"status_code"`
	Message    string `json:"message"`
	ErrorType  string `json:"error_type"`
}
