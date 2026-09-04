package oagmsg

import "strconv"

// TokenCountFormatter is an optional interface that ProtocolHandlers can implement
// to format token count responses in their protocol's native format.
//
// Token count format is determined by the CLIENT format, not the UPSTREAM format.
// For example, a Claude client always receives {"input_tokens": N} regardless of
// which upstream provider was used.
//
// Handlers that do not implement this interface (OpenAI, Interactions, Codex)
// return nil for token count (the proxy does not send token count to those clients).
type TokenCountFormatter interface {
	// FormatTokenCount formats a token count integer into protocol-specific JSON.
	// Returns the formatted JSON bytes.
	FormatTokenCount(count int64) []byte
}

// FormatTokenCount is a convenience function that checks if a handler implements
// TokenCountFormatter and returns the formatted token count, or nil if not supported.
func FormatTokenCount(h ProtocolHandler, count int64) []byte {
	if f, ok := h.(TokenCountFormatter); ok {
		return f.FormatTokenCount(count)
	}
	return nil
}

// FormatTokenCount implements TokenCountFormatter for the Anthropic wire format.
// Output: {"input_tokens": N}
func (h *AnthropicHandler) FormatTokenCount(count int64) []byte {
	out := make([]byte, 0, 32)
	out = append(out, `{"input_tokens":`...)
	out = strconv.AppendInt(out, count, 10)
	out = append(out, '}')
	return out
}

// FormatTokenCount implements TokenCountFormatter for the Gemini wire format.
// Output: {"totalTokens": N, "promptTokensDetails": [{"modality":"TEXT","tokenCount": N}]}
func (h *GeminiHandler) FormatTokenCount(count int64) []byte {
	out := make([]byte, 0, 96)
	out = append(out, `{"totalTokens":`...)
	out = strconv.AppendInt(out, count, 10)
	out = append(out, `,"promptTokensDetails":[{"modality":"TEXT","tokenCount":`...)
	out = strconv.AppendInt(out, count, 10)
	out = append(out, `}]}`...)
	return out
}

// AntigravityHandler inherits FormatTokenCount from GeminiHandler via embedding.
// No explicit implementation needed — Go's embedding propagates the method.

// Compile-time checks: only Claude and Gemini (+ Antigravity via embedding) implement TokenCountFormatter.
var (
	_ TokenCountFormatter = (*AnthropicHandler)(nil)
	_ TokenCountFormatter = (*GeminiHandler)(nil)
	_ TokenCountFormatter = (*AntigravityHandler)(nil)
)
