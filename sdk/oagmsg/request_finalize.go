package oagmsg

import (
	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

// FinalizeOpenAIChatRequest enforces OpenAI chat-completions wire constraints.
//
// Fork guard: cpa/main currently lets configured payload rules carry
// stream_options into non-stream OpenAI-compatible requests. Remove this note
// after upstream enforces the same request constraint at the shared boundary, so
// future rebases do not drop this fork-owned protection silently.
func FinalizeOpenAIChatRequest(body []byte, stream bool) []byte {
	if stream || len(body) == 0 {
		return body
	}
	return deleteJSONPathIfExists(body, "stream_options")
}

// FinalizeAntigravityRequest enforces Antigravity generateContent wire constraints.
//
// Fork guard: cpa/main currently allows stream_options to survive in the nested
// request body for non-stream Antigravity generate requests. Remove this note
// after upstream enforces the same Antigravity request constraint.
func FinalizeAntigravityRequest(body []byte, stream bool) []byte {
	if stream || len(body) == 0 {
		return body
	}
	body = deleteJSONPathIfExists(body, "request.stream_options")
	return deleteJSONPathIfExists(body, "stream_options")
}

func deleteJSONPathIfExists(body []byte, path string) []byte {
	if !gjson.GetBytes(body, path).Exists() {
		return body
	}
	updated, err := sjson.DeleteBytes(body, path)
	if err != nil {
		return body
	}
	return updated
}

func finalizeRequestForTarget(target Format, body []byte, stream bool) []byte {
	switch resolveFormat(target) {
	case FormatOpenAI:
		body = FinalizeOpenAIChatRequest(body, stream)
	case FormatAntigravity:
		body = FinalizeAntigravityRequest(body, stream)
	}
	return finalizeCodexRequestForTarget(target, body)
}
