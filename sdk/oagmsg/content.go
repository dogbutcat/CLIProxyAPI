package oagmsg

// ContentBlock is the discriminated union interface for all message content types.
//
// Cross-protocol mapping:
//
//	TextBlock          OpenAI text        | Anthropic text       | Gemini {text}
//	ImageBlock         OpenAI image_url   | Anthropic image      | Gemini inlineData(image/*)
//	FileBlock          OpenAI file        | Anthropic document   | Gemini inlineData(application/*)
//	ToolUseBlock       OpenAI tool_calls  | Anthropic tool_use   | Gemini functionCall
//	ToolResultBlock    OpenAI role=tool   | Anthropic tool_result| Gemini functionResponse
//	CustomToolUseBlock OpenAI custom_tool_call freeform input
//	CustomToolResultBlock OpenAI custom_tool_call_output
//	WebSearchInvocationBlock ordered web search invocation phase
//	WebSearchResultSetBlock  ordered web search result-set phase
//	WebSearchAnnotationBlock ordered web search citation/annotation phase
//	RawBlock           unrecognized block passthrough
type ContentBlock interface {
	// blockType returns the canonical block type identifier.
	// This is a sealed interface marker - only types in this package implement it.
	blockType() string
}

// ----------------------------------------------------------------
// TextBlock - pure text content
// ----------------------------------------------------------------

// TextBlock represents plain text content.
type TextBlock struct {
	Text         string         `json:"text"`
	CacheControl map[string]any `json:"cache_control,omitempty"` // {"type": "ephemeral"} for Anthropic/OpenAI
}

func (TextBlock) blockType() string { return "text" }

// ----------------------------------------------------------------
// ImageBlock - image content (base64 or URL)
// ----------------------------------------------------------------

// ImageBlock represents image content. Data and URL are mutually exclusive.
type ImageBlock struct {
	MediaType    string         `json:"media_type"` // e.g. "image/png"
	Data         string         `json:"data,omitempty"`
	URL          string         `json:"url,omitempty"`
	CacheControl map[string]any `json:"cache_control,omitempty"`
}

func (ImageBlock) blockType() string { return "image" }

// ----------------------------------------------------------------
// FileBlock - document/file content (PDF, code, spreadsheets, etc.)
// ----------------------------------------------------------------

// FileBlock represents file/document content.
//
// Cross-protocol mapping:
//
//	OpenAI:    {"type": "file", "file": {"filename": "...", "file_data": "data:...;base64,..."}}
//	Anthropic: {"type": "document", "source": {"type": "base64", "media_type": "...", "data": "..."}}
//	Gemini:    {"inlineData": {"mimeType": "application/pdf", "data": "..."}}
//	Codex:     {"type": "input_file", "file_data": "data:...;base64,...", "filename": "..."}
type FileBlock struct {
	Filename     string         `json:"filename,omitempty"`
	MediaType    string         `json:"media_type,omitempty"` // e.g. "application/pdf", "text/plain"
	Data         string         `json:"data,omitempty"`       // raw base64 encoded content
	URL          string         `json:"url,omitempty"`        // HTTP URL or file URI
	CacheControl map[string]any `json:"cache_control,omitempty"`

	claudeDocumentSource *claudeDocumentSource
}

func (FileBlock) blockType() string { return "file" }

type claudeDocumentSource struct {
	sourceType string
	mediaType  string
	data       string
	base64     string
}

// ----------------------------------------------------------------
// ToolUseBlock - assistant requests a tool call
// ----------------------------------------------------------------

// ToolUseBlock represents a tool call request from the assistant.
// Input is the parsed dict, not OpenAI's JSON string. No "type": "function" wrapper.
type ToolUseBlock struct {
	ID        string         `json:"id"`
	Name      string         `json:"name"`
	Input     map[string]any `json:"input"`
	Signature string         `json:"signature,omitempty"`
}

func (ToolUseBlock) blockType() string { return "tool_use" }

// ----------------------------------------------------------------
// CustomToolUseBlock - assistant requests a custom freeform tool call
// ----------------------------------------------------------------

// CustomToolUseBlock represents a custom tool call whose input is freeform
// text, not parsed JSON arguments. Input is kept exactly as received.
type CustomToolUseBlock struct {
	ID        string `json:"id"`
	Name      string `json:"name"`
	Input     string `json:"input"`
	Signature string `json:"signature,omitempty"`
}

func (CustomToolUseBlock) blockType() string { return "custom_tool_use" }

// RawInput returns the freeform tool input without JSON interpretation.
func (c CustomToolUseBlock) RawInput() string {
	return c.Input
}

// ----------------------------------------------------------------
// ToolResultBlock - tool execution result
// ----------------------------------------------------------------

// ToolResultBlock represents the result of a tool call. Appears in user message
// content blocks, linked to ToolUseBlock via ToolUseID.
//
// Content can be a plain string or structured content (Anthropic format).
type ToolResultBlock struct {
	ToolUseID    string         `json:"tool_use_id"`
	Content      any            `json:"content"` // string or []any (Anthropic structured blocks)
	IsError      bool           `json:"is_error,omitempty"`
	CacheControl map[string]any `json:"cache_control,omitempty"`
}

func (ToolResultBlock) blockType() string { return "tool_result" }

// ContentString returns the Content as a string, handling both string and list formats.
func (t ToolResultBlock) ContentString() string {
	if s, ok := t.Content.(string); ok {
		return s
	}
	return ""
}

// ----------------------------------------------------------------
// CustomToolResultBlock - custom freeform tool execution result
// ----------------------------------------------------------------

// CustomToolResultBlock represents the result of a custom tool call, linked to
// CustomToolUseBlock via ToolUseID.
type CustomToolResultBlock struct {
	ToolUseID     string         `json:"tool_use_id"`
	Output        string         `json:"output"`
	IsError       bool           `json:"is_error,omitempty"`
	CacheControl  map[string]any `json:"cache_control,omitempty"`
	rawOutput     any
	rawOutputJSON string
}

func (CustomToolResultBlock) blockType() string { return "custom_tool_result" }

// OutputString returns the custom tool output.
func (c CustomToolResultBlock) OutputString() string {
	return c.Output
}

// ----------------------------------------------------------------
// ThinkingBlock - model reasoning/thinking content (request layer)
// ----------------------------------------------------------------

// ThinkingBlock represents thinking or redacted_thinking content blocks in requests.
// Response-layer thinking is handled separately via UnifiedResponse.ThinkingContent.
//
// Cross-protocol mapping:
//
//	Anthropic: {"type": "thinking", "thinking": "...", "signature": "..."}
//	           {"type": "redacted_thinking", "data": "..."}
//	OpenAI:    skipped (thinking not expressed in request content)
//	Codex:     {"type": "reasoning", "content": [{"type": "summary_text", "text": "..."}]}
//	Gemini:    {"text": "...", "thought": true}
type ThinkingBlock struct {
	Thinking  string `json:"thinking"`
	Signature string `json:"signature,omitempty"`
	Redacted  bool   `json:"redacted,omitempty"`

	signaturePresent bool
}

func (ThinkingBlock) blockType() string { return "thinking" }

func (t ThinkingBlock) hasSignatureField() bool {
	return t.signaturePresent || t.Signature != ""
}

// ----------------------------------------------------------------
// AudioBlock - audio input content
// ----------------------------------------------------------------

// AudioBlock represents audio input content.
//
// Cross-protocol mapping:
//
//	OpenAI:    {"type": "input_audio", "input_audio": {"data": "...", "format": "wav"}}
//	Codex:     {"type": "input_audio", "data": "...", "format": "wav"}
//	Anthropic: not supported (falls back to RawBlock)
//	Gemini:    {"inlineData": {"mimeType": "audio/wav", "data": "..."}}
type AudioBlock struct {
	Data   string `json:"data"`             // base64-encoded audio data
	Format string `json:"format,omitempty"` // "wav", "mp3", "pcm16", etc.
}

func (AudioBlock) blockType() string { return "audio" }

// ----------------------------------------------------------------
// RawBlock - unrecognized block passthrough
// ----------------------------------------------------------------

// RawBlock catches any block type not explicitly modeled above.
// Inbound: stores original dict. Outbound: serializer outputs RawData directly.
type RawBlock struct {
	RawData map[string]any `json:"raw_data"`

	claudeDocumentSource *claudeDocumentSource
}

func (RawBlock) blockType() string { return "raw" }
