package oagmsg

// Format identifies the wire protocol format of an API request or response.
// Each Format maps to a specific ProtocolHandler in the HandlerRegistry.
type Format string

// FromString converts an arbitrary protocol identifier to a Format.
func FromString(value string) Format {
	return Format(value)
}

const (
	// FormatOpenAI is the OpenAI /v1/chat/completions wire format.
	FormatOpenAI Format = "openai"

	// FormatOpenAIResponse is the OpenAI Responses API wire format.
	FormatOpenAIResponse Format = "openai-response"

	// FormatAnthropic is the Anthropic /v1/messages wire format.
	FormatAnthropic Format = "claude"

	// FormatClaude is an alias for FormatAnthropic, matching the SDK translator format name.
	FormatClaude Format = FormatAnthropic

	// FormatGemini is the Google Gemini generateContent wire format.
	FormatGemini Format = "gemini"

	// FormatInteractions is the Google Interactions wire format (interaction.*/step.* events).
	FormatInteractions Format = "interactions"

	// FormatInteractionsSteps is a deprecated compatibility name for Google Interactions.
	FormatInteractionsSteps Format = "interactions-steps"

	// FormatCodex is the Codex variant of the Responses API wire format.
	FormatCodex Format = "codex"

	// FormatAntigravity is the Antigravity provider wire format.
	// Structurally identical to Gemini with an envelope wrapper:
	// Request: {"project":"", "request":{GEMINI_BODY}, "model":""}
	// Response: {"response":{GEMINI_BODY}} or raw Gemini body.
	FormatAntigravity Format = "antigravity"
)

// InteractionsMode selects which Interactions SSE event variant the serializer outputs.
type InteractionsMode int

const (
	// InteractionsModeCodex outputs Codex/Responses API events (response.*, default).
	InteractionsModeCodex InteractionsMode = iota
	// InteractionsModeSteps outputs interaction.*/step.* lifecycle events.
	InteractionsModeSteps
	// InteractionsModeResponsesAPI outputs full OpenAI Responses API lifecycle events
	// including content_part and output_item envelope events.
	InteractionsModeResponsesAPI
)

// standardFormats is the whitelist of formats handled by oagmsg handlers.
var standardFormats = map[Format]bool{
	FormatOpenAI:            true,
	FormatOpenAIResponse:    true,
	FormatAnthropic:         true,
	FormatGemini:            true,
	FormatInteractions:      true,
	FormatInteractionsSteps: true,
	FormatCodex:             true,
	FormatAntigravity:       true,
}

// IsStandardFormat returns true if f is a standard protocol format handled by oagmsg.
func IsStandardFormat(f Format) bool {
	return standardFormats[f]
}

// resolveFormat maps aliases to their canonical handler format.
func resolveFormat(f Format) Format {
	return f
}

// String returns the string representation of the Format.
func (f Format) String() string {
	return string(f)
}
