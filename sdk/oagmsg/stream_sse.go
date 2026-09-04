package oagmsg

// SSE formatting utilities for stream serializers.
// Vendored from internal/translator/common/bytes.go to avoid importing internal/.

// appendSSEEvent formats a complete SSE event line: "event: <event>\ndata: <payload>\n\n".
// Used by Anthropic serializer which requires the event: prefix.
func appendSSEEvent(event string, payload []byte) []byte {
	// Pre-allocate: "event: " + event + "\n" + "data: " + payload + "\n\n"
	out := make([]byte, 0, 7+len(event)+1+6+len(payload)+2)
	out = append(out, "event: "...)
	out = append(out, event...)
	out = append(out, '\n')
	out = append(out, "data: "...)
	out = append(out, payload...)
	out = append(out, '\n', '\n')
	return out
}

// formatDataLine formats a simple SSE data line: "data: <payload>\n\n".
// Used by OpenAI/Codex/Gemini serializers which only use the data: prefix.
func formatDataLine(payload []byte) []byte {
	// Pre-allocate: "data: " + payload + "\n\n"
	out := make([]byte, 0, 6+len(payload)+2)
	out = append(out, "data: "...)
	out = append(out, payload...)
	out = append(out, '\n', '\n')
	return out
}
