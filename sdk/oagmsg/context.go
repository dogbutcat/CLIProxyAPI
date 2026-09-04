package oagmsg

import (
	"crypto/sha256"
	"strings"
)

// TranslationContext carries cross-phase metadata through the oagmsg translation lifecycle.
// It is OPTIONAL — all existing APIs work without a context (nil-safe).
//
// Lifecycle:
//
//	Phase 1 (Request):  Caller creates context, oagmsg populates ToolNameForward/Reverse during SerializeRequest.
//	Phase 2 (Response): oagmsg uses ToolNameReverse to restore tool names in non-streaming responses.
//	Phase 3 (Stream):   StreamSession uses context for tool name restore, SawToolCall tracking, image dedup.
//
// The context is owned by the caller (executor) and passed into oagmsg via builder options
// or session options. oagmsg reads and writes fields but never creates or destroys the context.
type TranslationContext struct {
	// --- Populated by caller / request phase ---

	// OriginalRequestJSON is the raw client request JSON before translation.
	// Used by tool name mapping to build forward/reverse maps.
	OriginalRequestJSON []byte

	// translatedRequestJSON is the upstream request JSON after translation.
	// Response metadata prefers OriginalRequestJSON and falls back to this value
	// when the original request is unavailable or not valid JSON.
	translatedRequestJSON []byte

	// ToolNameForward maps original tool names to sanitized/shortened names.
	// Populated during request serialization when tool names are transformed.
	// Example: "mcp__memory__create_entities" → "cr_ent"
	ToolNameForward map[string]string

	// ToolNameReverse maps sanitized/shortened names back to originals.
	// Populated alongside ToolNameForward; consumed during response translation.
	// Example: "cr_ent" → "mcp__memory__create_entities"
	ToolNameReverse map[string]string

	// IsStreaming records whether the request had stream=true.
	IsStreaming bool

	// ModelName is the resolved model name for this translation session.
	ModelName string

	// SourceFormat is the client-facing protocol format.
	SourceFormat Format

	// TargetFormat is the upstream provider protocol format.
	TargetFormat Format

	// --- Accumulated during response / stream phase ---

	// SawToolCall is set to true once any tool call event is observed during
	// streaming. Used to override finish_reason to "tool_calls" at stream end.
	SawToolCall bool

	// imageHashes tracks sha256 hashes of image data by item ID for deduplication.
	// Codex may send the same image data multiple times via partial_image and output_item.done.
	imageHashes map[string][32]byte

	responseTools           toolDescriptorIndex
	responsesModel          string
	responsesModelSet       bool
	responsesModelNoRuntime bool
}

// RestoreToolName looks up a sanitized tool name in the reverse map.
// Returns the original name if found, otherwise returns the input unchanged.
func (c *TranslationContext) RestoreToolName(sanitized string) string {
	if c == nil || c.ToolNameReverse == nil {
		return sanitized
	}
	if original, ok := c.ToolNameReverse[sanitized]; ok {
		return original
	}
	if original, ok := c.ToolNameReverse[strings.ToLower(strings.TrimSpace(sanitized))]; ok {
		return original
	}
	return sanitized
}

// IsImageDuplicate checks if the given image data for itemID has already been seen.
// Returns true if duplicate. On first sight, stores the hash and returns false.
// Safe to call with nil context (always returns false).
func (c *TranslationContext) IsImageDuplicate(itemID string, imageData string) bool {
	if c == nil || itemID == "" {
		return false
	}
	if c.imageHashes == nil {
		c.imageHashes = make(map[string][32]byte)
	}
	hash := sha256.Sum256([]byte(imageData))
	if last, ok := c.imageHashes[itemID]; ok && last == hash {
		return true
	}
	c.imageHashes[itemID] = hash
	return false
}

// EffectiveFinishReason returns the correct finish_reason based on accumulated state.
// Incomplete or filtered upstream endings must win over tool_calls so Responses
// serializers can emit response.incomplete instead of finalizing partial tools.
func (c *TranslationContext) EffectiveFinishReason(upstreamReason string) string {
	if _, incomplete := responsesIncompleteReasonForFinishReason(upstreamReason); incomplete {
		return upstreamReason
	}
	if c != nil && c.SawToolCall {
		return "tool_calls"
	}
	return upstreamReason
}
