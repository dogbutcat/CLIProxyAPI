package oagmsg

// StreamEventType categorizes a StreamDelta into one of the canonical event kinds.
// All protocol-specific SSE events are mapped to these types by protocol parsers.
type StreamEventType int

const (
	// EventStart marks the beginning of a stream: carries response ID, model, created timestamp.
	EventStart StreamEventType = iota
	// EventTextDelta carries a text content fragment.
	EventTextDelta
	// EventThinkingDelta carries a reasoning/thinking content fragment.
	EventThinkingDelta
	// EventToolStart marks the beginning of a tool call: carries ToolCallID, ToolName, ToolIndex.
	EventToolStart
	// EventToolDelta carries a fragment of tool call arguments (JSON partial).
	EventToolDelta
	// EventToolDone marks the end of a tool call: may carry complete ToolArgs.
	EventToolDone
	// EventImageDelta carries a partial image generation result.
	EventImageDelta
	// EventUsage carries token usage statistics.
	EventUsage
	// EventDone marks the end of the stream: carries FinishReason.
	EventDone
	// EventError carries an error event from the upstream provider.
	EventError
	// EventPing is a keep-alive signal with no payload.
	EventPing
)

// StreamDelta is the protocol-agnostic superset of all SSE streaming event fields.
// Each protocol parser populates only the fields relevant to the event type;
// serializers read only the fields relevant to their target format.
//
// This is a flat struct by design (not an interface hierarchy) to minimize
// heap allocations and interface dispatch overhead in the SSE hot path.
type StreamDelta struct {
	// Type identifies which event kind this delta represents.
	Type StreamEventType

	// --- EventStart fields ---

	// ID is the response/message identifier (e.g., OpenAI "chatcmpl-xxx", Anthropic message ID).
	ID string
	// Model is the model name from the upstream response.
	Model string
	// Created is the Unix timestamp of response creation.
	Created int64

	// --- EventTextDelta / EventThinkingDelta fields ---

	// Content holds a text or thinking content fragment.
	Content string
	// Signature holds an opaque thinking signature for Anthropic round-trip fidelity.
	Signature string
	// BlockIndex is the Anthropic content_block index for block-level tracking.
	BlockIndex int

	// --- EventToolStart / EventToolDelta / EventToolDone fields ---

	// ToolIndex is the positional index of the tool call within the response.
	ToolIndex int
	// ToolCallID is the unique identifier for this tool call (Anthropic id / Codex call_id).
	ToolCallID string
	// ToolName is the function/tool name being invoked.
	ToolName string
	// ToolArgs holds a JSON fragment (delta) or complete JSON string (done).
	ToolArgs string
	// ToolType distinguishes tool call variants: "function" (standard) or "custom" (Codex custom_tool_call).
	ToolType string
	// toolNamespace preserves Responses namespace metadata internally without expanding the public SDK surface.
	toolNamespace string

	// --- EventImageDelta fields ---

	// ImageData holds base64-encoded partial image data.
	ImageData string
	// ImageFormat is the image MIME subtype: "png", "jpg", "webp".
	ImageFormat string
	// ImageItemID is the Codex output item ID used for image deduplication.
	ImageItemID string

	// --- EventDone fields ---

	// FinishReason indicates why the stream ended: "stop", "length", "tool_calls", "content_filter".
	FinishReason string
	// NativeFinishReason preserves the upstream provider's original finish reason string
	// (e.g., Gemini "STOP", Codex "max_output_tokens") before canonical normalization.
	NativeFinishReason string
	// StopSequence is the Anthropic-specific stop sequence that triggered completion.
	StopSequence string

	// --- EventUsage fields ---

	// Usage holds token consumption statistics when available.
	Usage *UnifiedUsage

	// --- EventError fields ---

	// ErrorType is the error classification string from the upstream provider.
	ErrorType string
	// ErrorMessage is the human-readable error description.
	ErrorMessage string

	// --- Passthrough ---

	// Extra holds protocol-specific fields not covered by the superset.
	// Used for round-trip fidelity of provider-specific extensions.
	Extra map[string]any
}
