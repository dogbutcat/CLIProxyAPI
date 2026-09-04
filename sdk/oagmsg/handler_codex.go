package oagmsg

import (
	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

// CodexHandler is a Responses-family handler with Codex request and stream semantics.
type CodexHandler struct {
	InteractionsHandler
}

func (h *CodexHandler) Format() Format { return FormatCodex }

// ParseRequest parses a Codex request and records the concrete source format.
func (h *CodexHandler) ParseRequest(rawJSON []byte) (*UnifiedRequest, error) {
	if err := validateJSONObject(rawJSON); err != nil {
		return nil, err
	}
	sourceInstructions := codexSourceInstructionsFromRaw(rawJSON)
	finalized := FinalizeCodexRequest(rawJSON)
	req, err := h.InteractionsHandler.ParseRequest(finalized)
	if err != nil {
		return nil, err
	}
	req.SourceFormat = FormatCodex
	req.codexSourceInstructions = sourceInstructions
	if sourceInstructions.present {
		req.Messages = removeCodexSyntheticInstructionsMessage(req.Messages, gjson.GetBytes(finalized, "instructions"))
	}
	if config := ExtractCodexThinking(gjson.ParseBytes(finalized)); config != nil {
		req.SetThinking(config)
	}
	return req, nil
}

// SerializeRequest keeps system messages as developer input items to match the
// Codex request contract instead of collapsing them into instructions.
func (h *CodexHandler) SerializeRequest(req *UnifiedRequest) ([]byte, error) {
	body, err := h.InteractionsHandler.serializeRequest(req, false, true)
	if err != nil {
		return nil, err
	}
	metadata := buildRequestToolMetadataFromIndex(req.responsesTools)
	if len(metadata.toolNameForward) == 0 {
		metadata = buildRequestToolMetadataFromRequests(body)
	}
	body, err = applyCodexRequestToolMetadata(body, metadata)
	if err != nil {
		return nil, err
	}
	body = applyCodexSourceInstructionsForRequest(req, body)
	return FinalizeCodexRequest(body), nil
}

var _ ProtocolHandler = (*CodexHandler)(nil)

func codexSourceInstructionsFromRaw(rawJSON []byte) codexSourceInstructionsMetadata {
	instructions := gjson.GetBytes(rawJSON, "instructions")
	if !instructions.Exists() {
		return codexSourceInstructionsMetadata{}
	}
	return codexSourceInstructionsMetadata{
		present: true,
		raw:     []byte(instructions.Raw),
	}
}

func removeCodexSyntheticInstructionsMessage(messages []OagMessage, instructions gjson.Result) []OagMessage {
	if len(messages) == 0 || !isCodexSyntheticInstructionsMessage(messages[0], instructions) {
		return messages
	}
	return messages[1:]
}

func isCodexSyntheticInstructionsMessage(message OagMessage, instructions gjson.Result) bool {
	if message.Role != "system" || message.Name != "" || len(message.Content) != 1 {
		return false
	}
	if _, ok := message.Content[0].(TextBlock); !ok {
		return false
	}
	return message.GetText() == instructions.String()
}

func applyCodexSourceInstructionsForRequest(req *UnifiedRequest, body []byte) []byte {
	if req == nil || req.SourceFormat != FormatCodex {
		return body
	}
	if req.codexSourceInstructions.present {
		if updated, err := sjson.SetRawBytes(body, "instructions", req.codexSourceInstructions.raw); err == nil {
			return updated
		}
		return body
	}
	if gjson.GetBytes(body, "instructions").Type == gjson.String && gjson.GetBytes(body, "instructions").String() == "" {
		if updated, err := sjson.DeleteBytes(body, "instructions"); err == nil {
			return updated
		}
	}
	return body
}
