package oagmsg

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/cache"
	sigcompat "github.com/router-for-me/CLIProxyAPI/v7/internal/signature"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/thinking"
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

func cacheGeminiResponsesTextSignatures(modelName, messageID, text string, signatures []string) bool {
	if messageID == "" || text == "" || len(signatures) == 0 {
		return false
	}
	textHash := fmt.Sprintf("%x", sha256.Sum256([]byte(text)))
	items := make([][]byte, 0, len(signatures))
	for _, signature := range signatures {
		normalized, ok := compatibleGeminiResponsesCarrierSignature(signature, geminiResponsesCarrierText)
		if !ok {
			return false
		}
		item := []byte(`{"type":"thought_signature","targetKind":"text"}`)
		item, _ = sjson.SetBytes(item, "thoughtSignature", normalized)
		item, _ = sjson.SetBytes(item, "targetHash", textHash)
		items = append(items, item)
	}
	return cache.CacheAntigravityReasoningReplayItems(
		thinking.ParseSuffix(modelName).ModelName,
		geminiResponsesTextReplaySessionKey(messageID),
		items,
	)
}

func restoreGeminiResponsesTextSignaturesForRequest(modelName string, rawJSON []byte) []byte {
	input := gjson.GetBytes(rawJSON, "input")
	if !input.IsArray() {
		return rawJSON
	}
	restored, changed := restoreGeminiResponsesTextSignatureItems(modelName, input.Array())
	if !changed {
		return rawJSON
	}
	updated, err := sjson.SetRawBytes(rawJSON, "input", rawGJSONResultArray(restored))
	if err != nil {
		return rawJSON
	}
	return updated
}

func restoreGeminiResponsesTextSignatureItems(modelName string, items []gjson.Result) ([]gjson.Result, bool) {
	restored := make([]gjson.Result, 0, len(items))
	skip := make(map[int]bool)
	changed := false
	for index, item := range items {
		if skip[index] {
			changed = true
			continue
		}
		restored = append(restored, item)
		text, ok := openAIResponsesAssistantVisibleText(item)
		messageID := strings.TrimSpace(item.Get("id").String())
		if !ok || messageID == "" {
			continue
		}
		cached, found := cache.GetAntigravityReasoningReplayItems(
			thinking.ParseSuffix(modelName).ModelName,
			geminiResponsesTextReplaySessionKey(messageID),
		)
		if !found {
			continue
		}
		textHash := fmt.Sprintf("%x", sha256.Sum256([]byte(text)))
		replayed := make(map[string]bool)
		for _, raw := range cached {
			entry := gjson.ParseBytes(raw)
			if entry.Get("targetHash").String() != textHash {
				continue
			}
			signature := strings.TrimSpace(entry.Get("thoughtSignature").String())
			if signature == "" {
				continue
			}
			carrier := []byte(`{"type":"reasoning","summary":[]}`)
			carrier, _ = sjson.SetBytes(carrier, "encrypted_content", encodeGeminiResponsesCarrier(signature, geminiResponsesCarrierPrevious, geminiResponsesCarrierText))
			restored = append(restored, gjson.ParseBytes(carrier))
			replayed[signature] = true
			changed = true
		}
		if len(replayed) == 0 {
			continue
		}
		for adjacent := index + 1; adjacent < len(items) && isOpenAIResponsesDetachedCarrier(items[adjacent]); adjacent++ {
			signature, direction, target, _, valid := decodeGeminiResponsesCarrier(items[adjacent].Get("encrypted_content").String())
			if valid && direction == geminiResponsesCarrierPrevious && target == geminiResponsesCarrierText && replayed[signature] {
				skip[adjacent] = true
				changed = true
			}
		}
	}
	return restored, changed
}

func geminiResponsesTextReplaySessionKey(messageID string) string {
	return "gemini-responses-text:" + strings.TrimSpace(messageID)
}

func openAIResponsesAssistantVisibleText(item gjson.Result) (string, bool) {
	itemType := strings.TrimSpace(item.Get("type").String())
	if itemType == "" && item.Get("role").Exists() {
		itemType = "message"
	}
	if itemType != "message" {
		return "", false
	}
	role := strings.ToLower(strings.TrimSpace(item.Get("role").String()))
	if role != "" && role != "assistant" && role != "model" {
		return "", false
	}
	content := item.Get("content")
	if content.Type == gjson.String {
		text := content.String()
		return text, text != ""
	}
	if !content.IsArray() {
		return "", false
	}
	var builder strings.Builder
	for _, part := range content.Array() {
		switch part.Get("type").String() {
		case "output_text", "text":
			builder.WriteString(part.Get("text").String())
		}
	}
	if builder.Len() == 0 {
		return "", false
	}
	return builder.String(), true
}

func isOpenAIResponsesDetachedCarrier(item gjson.Result) bool {
	if item.Get("type").String() != "reasoning" {
		return false
	}
	if strings.TrimSpace(item.Get("encrypted_content").String()) == "" {
		return false
	}
	if summary := item.Get("summary"); summary.Exists() && summary.IsArray() && len(summary.Array()) > 0 {
		return false
	}
	return true
}

func rawGJSONResultArray(items []gjson.Result) []byte {
	var builder strings.Builder
	builder.WriteByte('[')
	for index, item := range items {
		if index > 0 {
			builder.WriteByte(',')
		}
		raw := item.Raw
		if raw == "" {
			raw = "null"
		}
		builder.WriteString(raw)
	}
	builder.WriteByte(']')
	return []byte(builder.String())
}

func geminiMessagesForSerialization(req *UnifiedRequest) []OagMessage {
	if req == nil {
		return nil
	}
	switch resolveFormat(req.SourceFormat) {
	case FormatOpenAIResponse, FormatCodex:
		return applyGeminiResponsesTextCarriers(req.Messages)
	default:
		return req.Messages
	}
}

func applyGeminiResponsesTextCarriers(messages []OagMessage) []OagMessage {
	if len(messages) == 0 {
		return messages
	}
	updated := cloneOagMessages(messages)
	skip := make([]bool, len(updated))
	changed := false
	for index, msg := range updated {
		signature, direction, targetKind, ok := geminiTextCarrierFromMessage(msg)
		if !ok || targetKind != geminiResponsesCarrierText {
			continue
		}
		switch direction {
		case geminiResponsesCarrierPrevious:
			if applyGeminiCarrierToPreviousText(updated, index, signature) {
				skip[index] = true
				changed = true
			}
		case geminiResponsesCarrierNext:
			if applyGeminiCarrierToNextText(updated, index, signature) {
				skip[index] = true
				changed = true
			}
		}
	}
	if !changed {
		return messages
	}
	out := make([]OagMessage, 0, len(updated))
	for index, msg := range updated {
		if !skip[index] {
			out = append(out, msg)
		}
	}
	return out
}

func cloneOagMessages(messages []OagMessage) []OagMessage {
	out := make([]OagMessage, len(messages))
	copy(out, messages)
	for index := range out {
		out[index].Content = append([]ContentBlock(nil), messages[index].Content...)
	}
	return out
}

func geminiTextCarrierFromMessage(msg OagMessage) (signature, direction, targetKind string, ok bool) {
	if !strings.EqualFold(msg.Role, "assistant") || len(msg.Content) != 1 {
		return "", "", "", false
	}
	block, ok := msg.Content[0].(ThinkingBlock)
	if !ok || strings.TrimSpace(block.Thinking) != "" {
		return "", "", "", false
	}
	signature, direction, targetKind, marked, valid := decodeGeminiResponsesCarrier(block.Signature)
	if !marked || !valid || signature == "" {
		return "", "", "", false
	}
	return signature, direction, targetKind, true
}

func applyGeminiCarrierToPreviousText(messages []OagMessage, carrierIndex int, signature string) bool {
	for index := carrierIndex - 1; index >= 0; index-- {
		if !strings.EqualFold(messages[index].Role, "assistant") {
			return false
		}
		if applyGeminiSignatureToLastTextBlock(&messages[index], signature) {
			return true
		}
		return false
	}
	return false
}

func applyGeminiCarrierToNextText(messages []OagMessage, carrierIndex int, signature string) bool {
	for index := carrierIndex + 1; index < len(messages); index++ {
		if !strings.EqualFold(messages[index].Role, "assistant") {
			return false
		}
		if applyGeminiSignatureToFirstTextBlock(&messages[index], signature) {
			return true
		}
		return false
	}
	return false
}

func applyGeminiSignatureToLastTextBlock(msg *OagMessage, signature string) bool {
	for index := len(msg.Content) - 1; index >= 0; index-- {
		if applyGeminiSignatureToTextBlock(msg, index, signature) {
			return true
		}
	}
	return false
}

func applyGeminiSignatureToFirstTextBlock(msg *OagMessage, signature string) bool {
	for index := range msg.Content {
		if applyGeminiSignatureToTextBlock(msg, index, signature) {
			return true
		}
	}
	return false
}

func applyGeminiSignatureToTextBlock(msg *OagMessage, index int, signature string) bool {
	switch block := msg.Content[index].(type) {
	case TextBlock:
		if block.Text == "" {
			return false
		}
		raw := map[string]any{"text": block.Text, "thoughtSignature": signature}
		msg.Content[index] = RawBlock{RawData: raw}
		return true
	case RawBlock:
		text, ok := block.RawData["text"].(string)
		if !ok || text == "" {
			return false
		}
		if _, exists := block.RawData["thoughtSignature"]; exists {
			return false
		}
		raw := make(map[string]any, len(block.RawData)+1)
		for key, value := range block.RawData {
			raw[key] = value
		}
		raw["thoughtSignature"] = signature
		msg.Content[index] = RawBlock{RawData: raw}
		return true
	default:
		return false
	}
}

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

func geminiResponseSignatureOutputItems(modelName string, rawJSON []byte, responseID string) []map[string]any {
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
		modelName:  modelName,
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
	modelName  string
	nextIndex  int
	output     []map[string]any
	seen       map[string]bool
	msgIndex   int
	detachedID int

	reasoningText      strings.Builder
	reasoningSignature string
	messageText        strings.Builder
	messageSignature   string
	lastMessageID      string
	lastMessageText    string
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
		if b.cacheTrailingTextSignatures(signature) {
			return
		}
		b.appendDetached(signature, geminiResponsesCarrierPrevious, geminiResponsesCarrierText)
		return
	}
	switch b.lastSemanticKind {
	case geminiResponsesCarrierFunction:
		b.appendDetached(signature, geminiResponsesCarrierPrevious, geminiResponsesCarrierFunction)
	case geminiResponsesCarrierText:
		if b.cacheTrailingTextSignatures(signature) {
			return
		}
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
		if b.cacheTrailingTextSignatures(b.pendingSignatures...) {
			b.pendingSignatures = nil
			return
		}
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
	messageID := fmt.Sprintf("msg_%s_%d", b.responseID, b.msgIndex)
	messageText := b.messageText.String()
	item := []byte(`{"id":"","type":"message","status":"completed","role":"assistant","content":[{"type":"output_text","text":"","annotations":[],"logprobs":[]}]}`)
	item, _ = sjson.SetBytes(item, "id", messageID)
	item, _ = sjson.SetBytes(item, "content.0.text", messageText)
	b.output = append(b.output, markedResponsesOutputItem(item))
	b.lastMessageID = messageID
	b.lastMessageText = messageText
	b.messageText.Reset()
	b.messageSignature = ""
	b.msgIndex++
	b.nextIndex++
	b.lastSemanticKind = geminiResponsesCarrierText
}

func (b *geminiSignatureOutputBuilder) cacheTrailingTextSignatures(signatures ...string) bool {
	if !cacheGeminiResponsesTextSignatures(b.modelName, b.lastMessageID, b.lastMessageText, signatures) {
		return false
	}
	for _, signature := range signatures {
		normalized, ok := compatibleGeminiResponsesCarrierSignature(signature, geminiResponsesCarrierText)
		if ok {
			b.seen[normalized] = true
		}
	}
	return true
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
