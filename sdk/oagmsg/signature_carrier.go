package oagmsg

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"strings"

	sigcompat "github.com/router-for-me/CLIProxyAPI/v7/internal/signature"
	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

const (
	geminiResponsesCarrierPrefix     = "cpa-gemini-responses-carrier-v1:"
	geminiResponsesCarrierNext       = "next"
	geminiResponsesCarrierPrevious   = "previous"
	geminiResponsesCarrierStandalone = "standalone"
	geminiResponsesCarrierText       = "text"
	geminiResponsesCarrierFunction   = "function"
	geminiResponsesCarrierAny        = "any"

	oagmsgResponsesOutputItemMarker = "_oagmsg_responses_output_item"
)

func encodeGeminiResponsesCarrier(rawSignature, direction, targetKind string) string {
	rawSignature = strings.TrimSpace(rawSignature)
	if rawSignature == "" {
		return ""
	}
	return geminiResponsesCarrierPrefix + direction + ":" + targetKind + ":" + base64.RawStdEncoding.EncodeToString([]byte(rawSignature))
}

func decodeGeminiResponsesCarrier(rawSignature string) (signatureValue, direction, targetKind string, marked, ok bool) {
	rawSignature = strings.TrimSpace(rawSignature)
	if !strings.HasPrefix(rawSignature, geminiResponsesCarrierPrefix) {
		return rawSignature, "", "", false, true
	}
	marked = true
	if len(rawSignature) > (sigcompat.MaxGeminiThoughtSignatureLen*4/3)+1024 {
		return "", "", "", true, false
	}
	fields := strings.SplitN(strings.TrimPrefix(rawSignature, geminiResponsesCarrierPrefix), ":", 3)
	if len(fields) != 3 {
		return "", "", "", true, false
	}
	direction, targetKind = fields[0], fields[1]
	switch direction {
	case geminiResponsesCarrierNext, geminiResponsesCarrierPrevious, geminiResponsesCarrierStandalone:
	default:
		return "", "", "", true, false
	}
	switch targetKind {
	case geminiResponsesCarrierText, geminiResponsesCarrierFunction, geminiResponsesCarrierAny:
	default:
		return "", "", "", true, false
	}
	decoded, err := base64.RawStdEncoding.DecodeString(fields[2])
	if err != nil || len(decoded) == 0 || strings.HasPrefix(string(decoded), geminiResponsesCarrierPrefix) {
		return "", "", "", true, false
	}
	normalized, compatible := compatibleGeminiResponsesCarrierSignature(string(decoded), targetKind)
	if !compatible {
		return "", "", "", true, false
	}
	return normalized, direction, targetKind, true, true
}

func compatibleGeminiResponsesCarrierSignature(rawSignature, targetKind string) (string, bool) {
	rawSignature = strings.TrimSpace(rawSignature)
	if rawSignature == "" ||
		strings.HasPrefix(rawSignature, geminiResponsesCarrierPrefix) ||
		sigcompat.IsGeminiThoughtSignatureBypass(sigcompat.SignaturePayloadWithoutProviderPrefix(rawSignature)) {
		return "", false
	}
	if provider, _, ok := sigcompat.SplitSignatureProviderPrefix(rawSignature); ok && provider != sigcompat.SignatureProviderGemini {
		return "", false
	}
	blockKind := sigcompat.SignatureBlockKindGeminiModelPart
	if targetKind == geminiResponsesCarrierFunction {
		blockKind = sigcompat.SignatureBlockKindGeminiFunctionCall
	}
	normalized, compatible := sigcompat.CompatibleSignatureForProviderBlock(sigcompat.SignatureProviderGemini, rawSignature, blockKind)
	if compatible && normalized != "" {
		return normalized, true
	}
	return sigcompat.SignaturePayloadWithoutProviderPrefix(rawSignature), true
}

func geminiPartSignature(part gjson.Result) string {
	signature := strings.TrimSpace(part.Get("thoughtSignature").String())
	if signature == "" {
		signature = strings.TrimSpace(part.Get("thought_signature").String())
	}
	return signature
}

func rawResponsesOutputItems(calls []map[string]any) ([]any, bool) {
	var output []any
	hasRaw := false
	for _, call := range calls {
		if len(call) == 1 {
			raw, ok := call[oagmsgResponsesOutputItemMarker].(string)
			if !ok || raw == "" {
				continue
			}
			var rawMap map[string]any
			if err := json.Unmarshal([]byte(raw), &rawMap); err == nil && isBlankResponseToolCall(rawMap) {
				continue
			}
			var item any
			if err := json.Unmarshal([]byte(raw), &item); err != nil {
				continue
			}
			output = append(output, item)
			hasRaw = true
			continue
		}
		if isBlankResponseToolCall(call) {
			continue
		}
		if tool, ok := normalizeResponsesToolCallWithoutMarker(call); ok {
			output = append(output, tool)
		}
	}
	return output, hasRaw
}

func normalizeResponsesToolCallWithoutMarker(call map[string]any) (map[string]any, bool) {
	stripped := stripResponsesOutputItemMarker(call)
	if len(stripped) == 0 {
		return nil, false
	}
	return NormalizeToolCallToInteractions(stripped), true
}

func stripResponsesOutputItemMarker(call map[string]any) map[string]any {
	if _, ok := call[oagmsgResponsesOutputItemMarker]; !ok {
		return call
	}
	stripped := make(map[string]any, len(call)-1)
	for key, value := range call {
		if key != oagmsgResponsesOutputItemMarker {
			stripped[key] = value
		}
	}
	return stripped
}

func markedResponsesOutputItem(itemJSON []byte) map[string]any {
	return map[string]any{oagmsgResponsesOutputItemMarker: string(itemJSON)}
}

func geminiResponseSignatureOutputItems(rawJSON []byte, responseID string) []map[string]any {
	root := gjson.ParseBytes(rawJSON)
	if nested := root.Get("response"); nested.Exists() && nested.Get("candidates").Exists() {
		root = nested
	}
	parts := root.Get("candidates.0.content.parts")
	if !parts.Exists() || !parts.IsArray() {
		return nil
	}

	builder := &geminiSignatureOutputBuilder{
		responseID: strings.TrimPrefix(responseID, "resp_"),
		seen:       make(map[string]bool),
	}
	for _, part := range parts.Array() {
		builder.acceptPart(part)
	}
	builder.flushReasoning()
	builder.flushMessage()
	builder.flushPendingTerminalSignatures()
	if !builder.requiresCarrier {
		return nil
	}
	return builder.output
}

type geminiSignatureOutputBuilder struct {
	responseID string
	nextIndex  int
	output     []map[string]any
	seen       map[string]bool
	msgIndex   int
	detachedID int

	reasoningText      strings.Builder
	reasoningSignature string
	messageText        strings.Builder
	messageSignature   string
	lastSemanticKind   string
	pendingSignatures  []string
	requiresCarrier    bool
}

func (b *geminiSignatureOutputBuilder) acceptPart(part gjson.Result) {
	signature := geminiPartSignature(part)
	if part.Get("thought").Bool() {
		b.flushMessage()
		if signature != "" && len(b.pendingSignatures) > 0 && b.pendingSignatures[0] != signature {
			b.flushPendingSignatures(geminiResponsesCarrierStandalone, geminiResponsesCarrierAny)
		}
		if signature != "" && b.reasoningSignature != "" && signature != b.reasoningSignature {
			b.flushReasoning()
		}
		if signature != "" {
			b.reasoningSignature = signature
		} else if pending := b.popPendingSignature(); pending != "" {
			b.reasoningSignature = pending
		}
		if text := part.Get("text"); text.Exists() {
			b.reasoningText.WriteString(text.String())
		}
		return
	}
	if fc := part.Get("functionCall"); fc.Exists() {
		if signature == "" {
			signature = b.popPendingSignature()
		}
		b.flushReasoning()
		b.flushMessage()
		if signature != "" {
			b.appendDetached(signature, geminiResponsesCarrierNext, geminiResponsesCarrierFunction)
		}
		b.appendFunction(fc)
		b.lastSemanticKind = geminiResponsesCarrierFunction
		return
	}
	if text := part.Get("text"); text.Exists() && text.String() != "" {
		if b.reasoningText.Len() > 0 && b.reasoningSignature == "" && signature != "" {
			b.reasoningSignature = signature
			signature = ""
		}
		if signature != "" && len(b.pendingSignatures) > 0 && b.pendingSignatures[0] != signature {
			b.flushPendingSignatures(geminiResponsesCarrierStandalone, geminiResponsesCarrierAny)
		}
		if signature == "" {
			signature = b.popPendingSignature()
		}
		b.flushReasoning()
		if signature != "" {
			if b.messageSignature != "" && b.messageSignature != signature {
				b.flushMessage()
			}
			b.messageSignature = signature
		} else if b.messageSignature != "" {
			b.flushMessage()
		}
		b.messageText.WriteString(text.String())
		return
	}
	if signature != "" {
		b.acceptTerminalSignature(signature)
	}
}

func (b *geminiSignatureOutputBuilder) acceptTerminalSignature(signature string) {
	if b.reasoningText.Len() > 0 {
		if b.reasoningSignature == "" {
			b.reasoningSignature = signature
			return
		}
		b.flushReasoning()
		b.appendDetached(signature, geminiResponsesCarrierPrevious, geminiResponsesCarrierText)
		return
	}
	if b.messageText.Len() > 0 {
		b.flushMessage()
		b.appendDetached(signature, geminiResponsesCarrierPrevious, geminiResponsesCarrierText)
		return
	}
	switch b.lastSemanticKind {
	case geminiResponsesCarrierFunction:
		b.appendDetached(signature, geminiResponsesCarrierPrevious, geminiResponsesCarrierFunction)
	case geminiResponsesCarrierText:
		b.appendDetached(signature, geminiResponsesCarrierPrevious, geminiResponsesCarrierText)
	default:
		b.pushPendingSignature(signature)
	}
}

func (b *geminiSignatureOutputBuilder) pushPendingSignature(signature string) {
	signature = strings.TrimSpace(signature)
	if signature != "" {
		b.pendingSignatures = append(b.pendingSignatures, signature)
	}
}

func (b *geminiSignatureOutputBuilder) popPendingSignature() string {
	if len(b.pendingSignatures) == 0 {
		return ""
	}
	signature := b.pendingSignatures[0]
	b.pendingSignatures = b.pendingSignatures[1:]
	return signature
}

func (b *geminiSignatureOutputBuilder) flushPendingSignatures(direction, targetKind string) {
	pending := b.pendingSignatures
	b.pendingSignatures = nil
	for _, signature := range pending {
		b.appendDetached(signature, direction, targetKind)
	}
}

func (b *geminiSignatureOutputBuilder) flushPendingTerminalSignatures() {
	switch b.lastSemanticKind {
	case geminiResponsesCarrierFunction:
		b.flushPendingSignatures(geminiResponsesCarrierPrevious, geminiResponsesCarrierFunction)
	case geminiResponsesCarrierText:
		b.flushPendingSignatures(geminiResponsesCarrierPrevious, geminiResponsesCarrierText)
	default:
		b.flushPendingSignatures(geminiResponsesCarrierStandalone, geminiResponsesCarrierAny)
	}
}

func (b *geminiSignatureOutputBuilder) flushReasoning() {
	if b.reasoningText.Len() == 0 && b.reasoningSignature == "" {
		return
	}
	item := []byte(`{"id":"","type":"reasoning","encrypted_content":"","summary":[]}`)
	item, _ = sjson.SetBytes(item, "id", fmt.Sprintf("rs_%s_%d", b.responseID, b.nextIndex))
	if b.reasoningSignature != "" {
		if normalized, compatible := compatibleGeminiResponsesCarrierSignature(b.reasoningSignature, geminiResponsesCarrierText); compatible {
			b.reasoningSignature = normalized
			item, _ = sjson.SetBytes(item, "encrypted_content", encodeGeminiResponsesCarrier(b.reasoningSignature, geminiResponsesCarrierStandalone, geminiResponsesCarrierText))
			b.requiresCarrier = true
		} else {
			b.reasoningSignature = ""
		}
	}
	if b.reasoningText.Len() > 0 {
		item, _ = sjson.SetBytes(item, "summary.0.type", "summary_text")
		item, _ = sjson.SetBytes(item, "summary.0.text", b.reasoningText.String())
	}
	b.output = append(b.output, markedResponsesOutputItem(item))
	b.seen[b.reasoningSignature] = true
	b.reasoningText.Reset()
	b.reasoningSignature = ""
	b.nextIndex++
	b.lastSemanticKind = geminiResponsesCarrierText
}

func (b *geminiSignatureOutputBuilder) flushMessage() {
	if b.messageText.Len() == 0 {
		return
	}
	if b.messageSignature != "" {
		b.appendDetached(b.messageSignature, geminiResponsesCarrierNext, geminiResponsesCarrierText)
	}
	item := []byte(`{"id":"","type":"message","status":"completed","role":"assistant","content":[{"type":"output_text","text":"","annotations":[],"logprobs":[]}]}`)
	item, _ = sjson.SetBytes(item, "id", fmt.Sprintf("msg_%s_%d", b.responseID, b.msgIndex))
	item, _ = sjson.SetBytes(item, "content.0.text", b.messageText.String())
	b.output = append(b.output, markedResponsesOutputItem(item))
	b.messageText.Reset()
	b.messageSignature = ""
	b.msgIndex++
	b.nextIndex++
	b.lastSemanticKind = geminiResponsesCarrierText
}

func (b *geminiSignatureOutputBuilder) appendDetached(signature, direction, targetKind string) {
	signature = strings.TrimSpace(signature)
	if signature == "" || b.seen[signature] {
		return
	}
	normalized, compatible := compatibleGeminiResponsesCarrierSignature(signature, targetKind)
	if !compatible {
		return
	}
	signature = normalized
	if b.seen[signature] {
		return
	}
	b.requiresCarrier = true
	item := []byte(`{"id":"","type":"reasoning","encrypted_content":"","summary":[]}`)
	placement := "before"
	if direction == geminiResponsesCarrierPrevious {
		placement = "after"
	}
	item, _ = sjson.SetBytes(item, "id", fmt.Sprintf("rs_%s_detached_%s_%d", b.responseID, placement, b.detachedID))
	item, _ = sjson.SetBytes(item, "encrypted_content", encodeGeminiResponsesCarrier(signature, direction, targetKind))
	b.output = append(b.output, markedResponsesOutputItem(item))
	b.seen[signature] = true
	b.detachedID++
	b.nextIndex++
}

func (b *geminiSignatureOutputBuilder) appendFunction(fc gjson.Result) {
	name := fc.Get("name").String()
	callID := fc.Get("id").String()
	if callID == "" {
		callID = fmt.Sprintf("call_%s_%d", b.responseID, b.nextIndex)
	}
	args := fc.Get("args").Raw
	if args == "" {
		args = "{}"
	}
	item := []byte(`{"id":"","type":"function_call","status":"completed","call_id":"","name":"","arguments":""}`)
	item, _ = sjson.SetBytes(item, "id", fmt.Sprintf("fc_%s", callID))
	item, _ = sjson.SetBytes(item, "call_id", callID)
	item, _ = sjson.SetBytes(item, "name", name)
	item, _ = sjson.SetBytes(item, "arguments", args)
	b.output = append(b.output, markedResponsesOutputItem(item))
	b.nextIndex++
}
