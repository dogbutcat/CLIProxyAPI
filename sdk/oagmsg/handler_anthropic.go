package oagmsg

import (
	"encoding/json"
	"strings"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/signature"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/util"
	"github.com/tidwall/gjson"
)

// AnthropicHandler implements ProtocolHandler for the Anthropic /v1/messages format.
// Aligned with oag_server handlers/anthropic.py AnthropicHandler.
type AnthropicHandler struct{}

func (h *AnthropicHandler) Format() Format { return FormatAnthropic }

// ParseRequest parses Anthropic /v1/messages JSON into a UnifiedRequest.
func (h *AnthropicHandler) ParseRequest(rawJSON []byte) (*UnifiedRequest, error) {
	if err := validateJSONObject(rawJSON); err != nil {
		return nil, err
	}

	root := gjson.ParseBytes(rawJSON)

	msgs, err := h.ParseMessages(rawJSON)
	if err != nil {
		return nil, err
	}

	req := &UnifiedRequest{
		Model:              root.Get("model").String(),
		Messages:           msgs,
		Stream:             root.Get("stream").Bool(),
		SourceFormat:       FormatAnthropic,
		anthropicWebSearch: newAnthropicWebSearchRequestMetadata(rawJSON),
	}

	if v := root.Get("temperature"); v.Exists() {
		t := v.Float()
		req.Temperature = &t
	}
	if v := root.Get("top_p"); v.Exists() {
		t := v.Float()
		req.TopP = &t
	}
	if v := root.Get("max_tokens"); v.Exists() {
		t := int(v.Int())
		req.MaxTokens = &t
	}
	if v := root.Get("stop_sequences"); v.Exists() && v.IsArray() {
		for _, s := range v.Array() {
			req.Stop = append(req.Stop, s.String())
		}
	}
	if v := root.Get("tools"); v.Exists() && v.IsArray() {
		for _, t := range v.Array() {
			var m map[string]any
			if err := json.Unmarshal([]byte(t.Raw), &m); err == nil {
				req.Tools = append(req.Tools, m)
			}
		}
	}
	if v := root.Get("tool_choice"); v.Exists() {
		if v.Type == gjson.String {
			req.ToolChoice = v.String()
		} else {
			var m map[string]any
			if err := json.Unmarshal([]byte(v.Raw), &m); err == nil {
				req.ToolChoice = m
			}
		}
	}
	if config := ExtractAnthropicThinking(root); config != nil {
		req.SetThinking(config)
	}

	return req, nil
}

// ParseMessages extracts messages from Anthropic /v1/messages JSON.
// System messages are extracted from the top-level "system" field.
func (h *AnthropicHandler) ParseMessages(rawJSON []byte) ([]OagMessage, error) {
	if err := validateJSONObject(rawJSON); err != nil {
		return nil, err
	}

	root := gjson.ParseBytes(rawJSON)
	var msgs []OagMessage

	// Parse system prompt (string or array of blocks)
	if sys := root.Get("system"); sys.Exists() {
		if sys.Type == gjson.String {
			msgs = append(msgs, SystemMsg(sys.String()))
		} else if sys.IsArray() {
			var blocks []ContentBlock
			for _, b := range sys.Array() {
				if b.Get("type").String() == "text" {
					tb := TextBlock{Text: b.Get("text").String()}
					if cc := parseCacheControl(b); cc != nil {
						tb.CacheControl = cc
					}
					blocks = append(blocks, tb)
				}
			}
			if len(blocks) > 0 {
				msgs = append(msgs, OagMessage{Role: "system", Content: blocks})
			}
		}
	}

	// Parse messages array
	messagesResult := root.Get("messages")
	if !messagesResult.Exists() || !messagesResult.IsArray() {
		return msgs, nil
	}

	for _, msg := range messagesResult.Array() {
		role := msg.Get("role").String()
		content := msg.Get("content")
		if role == "system" {
			if reminderText, ok := anthropicMessageSystemReminderText(content); ok {
				msgs = append(msgs, OagMessage{Role: "user", Content: []ContentBlock{TextBlock{Text: reminderText}}})
			}
			continue
		}

		var blocks []ContentBlock

		if content.Type == gjson.String {
			blocks = append(blocks, TextBlock{Text: content.String()})
		} else if content.IsArray() {
			for _, b := range content.Array() {
				parsed := h.parseContentBlock(b)
				if parsed != nil {
					blocks = append(blocks, parsed)
				}
			}
		}

		if len(blocks) == 0 {
			continue
		}
		msgs = append(msgs, OagMessage{Role: role, Content: blocks})
	}

	return groupAnthropicMessagesWithToolAlignment(msgs), nil
}

func groupAnthropicMessagesWithToolAlignment(msgs []OagMessage) []OagMessage {
	grouped := make([]OagMessage, 0, len(msgs))
	var pendingToolUseIDs []string
	var pendingSystemReminders []OagMessage

	appendMessage := func(msg OagMessage) {
		if len(grouped) > 0 && grouped[len(grouped)-1].Role == msg.Role {
			grouped[len(grouped)-1].Content = append(grouped[len(grouped)-1].Content, msg.Content...)
			return
		}
		grouped = append(grouped, msg)
	}

	for _, msg := range msgs {
		if isAnthropicSystemReminderMessage(msg) && len(pendingToolUseIDs) > 0 {
			pendingSystemReminders = append(pendingSystemReminders, msg)
			continue
		}
		precedingToolUseIDs := pendingToolUseIDs
		pendingToolUseIDs = nil
		if msg.Role == "user" && len(precedingToolUseIDs) > 0 {
			msg.Content = alignAnthropicToolResults(msg.Content, precedingToolUseIDs)
		}
		if len(pendingSystemReminders) > 0 {
			if msg.Role == "user" && len(precedingToolUseIDs) > 0 {
				msg.Content = insertAnthropicSystemRemindersAfterLeadingToolResults(msg.Content, pendingSystemReminders)
			} else {
				for _, reminder := range pendingSystemReminders {
					appendMessage(reminder)
				}
			}
			pendingSystemReminders = nil
		}
		if msg.Role == "assistant" {
			pendingToolUseIDs = anthropicToolUseIDs(msg.Content)
		}
		appendMessage(msg)
	}
	for _, reminder := range pendingSystemReminders {
		appendMessage(reminder)
	}
	return grouped
}

func alignAnthropicToolResults(blocks []ContentBlock, toolUseIDs []string) []ContentBlock {
	if len(blocks) == 0 || len(toolUseIDs) == 0 {
		return blocks
	}
	toolResults := make([]ToolResultBlock, 0, len(toolUseIDs))
	otherBlocks := make([]ContentBlock, 0, len(blocks))
	for _, block := range blocks {
		if toolResult, ok := block.(ToolResultBlock); ok {
			toolResults = append(toolResults, toolResult)
			continue
		}
		otherBlocks = append(otherBlocks, block)
	}
	if len(toolResults) != len(toolUseIDs) {
		return blocks
	}

	ordered := make([]ContentBlock, 0, len(blocks))
	used := make([]bool, len(toolResults))
	for _, toolUseID := range toolUseIDs {
		match := -1
		for i, toolResult := range toolResults {
			if !used[i] && toolUseID != "" && toolResult.ToolUseID == toolUseID {
				match = i
				break
			}
		}
		if match < 0 {
			return blocks
		}
		used[match] = true
		ordered = append(ordered, toolResults[match])
	}
	return append(ordered, otherBlocks...)
}

func anthropicToolUseIDs(blocks []ContentBlock) []string {
	ids := make([]string, 0)
	for _, block := range blocks {
		toolUse, ok := block.(ToolUseBlock)
		if !ok || toolUse.ID == "" {
			continue
		}
		ids = append(ids, toolUse.ID)
	}
	return ids
}

func isAnthropicSystemReminderMessage(msg OagMessage) bool {
	if msg.Role != "user" || len(msg.Content) != 1 {
		return false
	}
	text, ok := msg.Content[0].(TextBlock)
	if !ok {
		return false
	}
	return strings.HasPrefix(text.Text, "<system-reminder>\n") && strings.HasSuffix(text.Text, "\n</system-reminder>")
}

func insertAnthropicSystemRemindersAfterLeadingToolResults(blocks []ContentBlock, reminders []OagMessage) []ContentBlock {
	if len(reminders) == 0 {
		return blocks
	}
	insertAt := 0
	for insertAt < len(blocks) {
		if _, ok := blocks[insertAt].(ToolResultBlock); !ok {
			break
		}
		insertAt++
	}
	reminderBlocks := make([]ContentBlock, 0, len(reminders))
	for _, reminder := range reminders {
		reminderBlocks = append(reminderBlocks, reminder.Content...)
	}
	out := make([]ContentBlock, 0, len(blocks)+len(reminderBlocks))
	out = append(out, blocks[:insertAt]...)
	out = append(out, reminderBlocks...)
	out = append(out, blocks[insertAt:]...)
	return out
}

func anthropicMessageSystemReminderText(content gjson.Result) (string, bool) {
	parts := anthropicSystemTextParts(content)
	if len(parts) == 0 {
		return "", false
	}
	text := strings.Join(parts, "\n")
	if strings.TrimSpace(text) == "" {
		return "", false
	}
	return "<system-reminder>\n" + text + "\n</system-reminder>", true
}

func anthropicSystemTextParts(content gjson.Result) []string {
	if !content.Exists() {
		return nil
	}
	if content.Type == gjson.String {
		text := content.String()
		if text == "" || util.IsClaudeCodeAttributionSystemText(text) {
			return nil
		}
		return []string{text}
	}
	if !content.IsArray() {
		return nil
	}
	parts := make([]string, 0)
	content.ForEach(func(_, item gjson.Result) bool {
		if item.Get("type").String() != "text" {
			return true
		}
		text := item.Get("text").String()
		if text == "" || util.IsClaudeCodeAttributionSystemText(text) {
			return true
		}
		parts = append(parts, text)
		return true
	})
	return parts
}

// parseContentBlock parses a single Anthropic content block.
func (h *AnthropicHandler) parseContentBlock(block gjson.Result) ContentBlock {
	blkType := block.Get("type").String()
	cacheCtrl := parseCacheControl(block)

	switch blkType {
	case "text":
		tb := TextBlock{Text: block.Get("text").String()}
		if cacheCtrl != nil {
			tb.CacheControl = cacheCtrl
		}
		return tb

	case "image":
		src := block.Get("source")
		if src.Get("type").String() == "base64" {
			ib := ImageBlock{
				MediaType: src.Get("media_type").String(),
				Data:      src.Get("data").String(),
			}
			if cacheCtrl != nil {
				ib.CacheControl = cacheCtrl
			}
			return ib
		}
		if src.Get("type").String() == "url" {
			ib := ImageBlock{URL: src.Get("url").String()}
			if cacheCtrl != nil {
				ib.CacheControl = cacheCtrl
			}
			return ib
		}
		return rawBlock(block)

	case "document":
		src := block.Get("source")
		origin := &claudeDocumentSource{
			sourceType: src.Get("type").String(),
			mediaType:  src.Get("media_type").String(),
			data:       src.Get("data").String(),
			base64:     src.Get("base64").String(),
		}
		if src.Get("type").String() == "base64" {
			fb := FileBlock{
				Filename:             block.Get("title").String(),
				MediaType:            src.Get("media_type").String(),
				Data:                 src.Get("data").String(),
				claudeDocumentSource: origin,
			}
			if cacheCtrl != nil {
				fb.CacheControl = cacheCtrl
			}
			return fb
		}
		rb := rawBlock(block)
		rb.claudeDocumentSource = origin
		return rb

	case "tool_use":
		var input map[string]any
		inputRaw := block.Get("input")
		if inputRaw.Exists() {
			if err := json.Unmarshal([]byte(inputRaw.Raw), &input); err != nil {
				input = map[string]any{}
			}
		} else {
			input = map[string]any{}
		}
		return ToolUseBlock{
			ID:        block.Get("id").String(),
			Name:      block.Get("name").String(),
			Input:     input,
			Signature: block.Get("signature").String(),
		}

	case "thinking":
		return ThinkingBlock{
			Thinking:         block.Get("thinking").String(),
			Signature:        block.Get("signature").String(),
			signaturePresent: block.Get("signature").Exists(),
		}

	case "redacted_thinking":
		return ThinkingBlock{
			Redacted: true,
		}

	case "tool_result":
		tr := ToolResultBlock{
			ToolUseID: block.Get("tool_use_id").String(),
			IsError:   block.Get("is_error").Bool(),
		}
		if cacheCtrl != nil {
			tr.CacheControl = cacheCtrl
		}
		contentResult := block.Get("content")
		if contentResult.Type == gjson.String {
			tr.Content = contentResult.String()
		} else if contentResult.IsArray() {
			var contentBlocks []any
			for _, cb := range contentResult.Array() {
				var m map[string]any
				if err := json.Unmarshal([]byte(cb.Raw), &m); err == nil {
					contentBlocks = append(contentBlocks, m)
				}
			}
			tr.Content = contentBlocks
		}
		return tr

	default:
		return rawBlock(block)
	}
}

// SerializeMessages converts OagMessages to Anthropic /v1/messages format.
// Returns the messages array JSON. System messages are NOT included - use
// SerializeRequest for the full payload with system separation.
func (h *AnthropicHandler) SerializeMessages(msgs []OagMessage) ([]byte, error) {
	var result []any

	for _, msg := range groupConsecutiveRoleMessages(msgs) {
		if msg.Role == "system" {
			continue // system handled separately
		}
		result = append(result, h.serializeOneMessage(msg))
	}

	return json.Marshal(result)
}

func groupConsecutiveRoleMessages(msgs []OagMessage) []OagMessage {
	grouped := make([]OagMessage, 0, len(msgs))
	for _, msg := range msgs {
		if len(grouped) > 0 && grouped[len(grouped)-1].Role == msg.Role {
			grouped[len(grouped)-1].Content = append(grouped[len(grouped)-1].Content, msg.Content...)
			continue
		}
		grouped = append(grouped, msg)
	}
	return grouped
}

// serializeOneMessage serializes a single OagMessage to Anthropic format.
func (h *AnthropicHandler) serializeOneMessage(msg OagMessage) map[string]any {
	return h.serializeOneMessageForRequest(nil, msg)
}

func (h *AnthropicHandler) serializeOneMessageForRequest(req *UnifiedRequest, msg OagMessage) map[string]any {
	out := map[string]any{"role": msg.Role}

	var contentBlocks []any
	for _, b := range msg.Content {
		switch block := b.(type) {
		case TextBlock:
			cb := map[string]any{"type": "text", "text": block.Text}
			if block.CacheControl != nil {
				cb["cache_control"] = block.CacheControl
			}
			contentBlocks = append(contentBlocks, cb)

		case ImageBlock:
			if block.Data != "" {
				cb := map[string]any{
					"type": "image",
					"source": map[string]any{
						"type":       "base64",
						"media_type": block.MediaType,
						"data":       block.Data,
					},
				}
				if block.CacheControl != nil {
					cb["cache_control"] = block.CacheControl
				}
				contentBlocks = append(contentBlocks, cb)
			} else if block.URL != "" {
				cb := map[string]any{
					"type": "image",
					"source": map[string]any{
						"type": "url",
						"url":  block.URL,
					},
				}
				if block.CacheControl != nil {
					cb["cache_control"] = block.CacheControl
				}
				contentBlocks = append(contentBlocks, cb)
			}

		case FileBlock:
			if block.Data != "" {
				cb := map[string]any{
					"type": "document",
					"source": map[string]any{
						"type":       "base64",
						"media_type": block.MediaType,
						"data":       block.Data,
					},
				}
				if block.Filename != "" {
					cb["title"] = block.Filename
				}
				if block.CacheControl != nil {
					cb["cache_control"] = block.CacheControl
				}
				contentBlocks = append(contentBlocks, cb)
			}

		case ThinkingBlock:
			block, keep := requestThinkingForTarget(req, FormatAnthropic, msg.Role, block, signature.SignatureBlockKindClaudeThinking)
			if !keep {
				continue
			}
			if block.Redacted {
				contentBlocks = append(contentBlocks, map[string]any{"type": "redacted_thinking"})
			} else {
				cb := map[string]any{"type": "thinking", "thinking": block.Thinking}
				if block.hasSignatureField() {
					cb["signature"] = block.Signature
				}
				contentBlocks = append(contentBlocks, cb)
			}

		case AudioBlock:
			// Anthropic does not natively support audio input; pass as raw
			contentBlocks = append(contentBlocks, map[string]any{"type": "input_audio", "data": block.Data, "format": block.Format})

		case ToolUseBlock:
			contentBlock := map[string]any{
				"type":  "tool_use",
				"id":    sanitizeClaudeToolID(block.ID),
				"name":  block.Name,
				"input": block.Input,
			}
			if block.Signature != "" {
				contentBlock["signature"] = block.Signature
			}
			contentBlocks = append(contentBlocks, contentBlock)

		case CustomToolUseBlock:
			contentBlock := map[string]any{
				"type": "tool_use",
				"id":   sanitizeClaudeToolID(block.ID),
				"name": block.Name,
				"input": map[string]any{
					"input": block.RawInput(),
				},
			}
			if block.Signature != "" {
				contentBlock["signature"] = block.Signature
			}
			contentBlocks = append(contentBlocks, contentBlock)

		case ToolResultBlock:
			cb := map[string]any{
				"type":        "tool_result",
				"tool_use_id": sanitizeClaudeToolID(block.ToolUseID),
				"content":     block.Content,
			}
			if block.IsError {
				cb["is_error"] = true
			}
			if block.CacheControl != nil {
				cb["cache_control"] = block.CacheControl
			}
			contentBlocks = append(contentBlocks, cb)

		case CustomToolResultBlock:
			cb := map[string]any{
				"type":        "tool_result",
				"tool_use_id": sanitizeClaudeToolID(block.ToolUseID),
				"content":     claudeCustomToolResultContent(block),
			}
			if block.IsError {
				cb["is_error"] = true
			}
			if block.CacheControl != nil {
				cb["cache_control"] = block.CacheControl
			}
			contentBlocks = append(contentBlocks, cb)

		case RawBlock:
			contentBlocks = append(contentBlocks, block.RawData)
		}
	}

	// Simplify: single text block -> string content
	if len(contentBlocks) == 1 {
		if cb, ok := contentBlocks[0].(map[string]any); ok {
			if cb["type"] == "text" && cb["cache_control"] == nil {
				out["content"] = cb["text"]
				return out
			}
		}
	}

	out["content"] = contentBlocks
	return out
}

func claudeCustomToolResultContent(block CustomToolResultBlock) any {
	parts, ok := block.rawOutput.([]any)
	if !ok {
		return block.OutputString()
	}
	var converted []any
	hasImageOrFile := false
	for _, part := range parts {
		contentPart, ok := responsesContentPartToClaudeValue(part)
		if !ok {
			continue
		}
		if contentType := stringValue(contentPart["type"]); contentType == "image" || contentType == "document" {
			hasImageOrFile = true
		}
		converted = append(converted, contentPart)
	}
	if len(converted) == 0 {
		if raw, err := json.Marshal(parts); err == nil {
			return string(raw)
		}
		return block.OutputString()
	}
	if len(converted) == 1 && !hasImageOrFile {
		if textPart, ok := converted[0].(map[string]any); ok && textPart["type"] == "text" {
			return stringValue(textPart["text"])
		}
	}
	return converted
}

func responsesContentPartToClaudeValue(part any) (map[string]any, bool) {
	partMap, ok := part.(map[string]any)
	if !ok {
		return nil, false
	}
	switch stringValue(partMap["type"]) {
	case "input_text", "output_text":
		text, ok := partMap["text"].(string)
		if !ok {
			return nil, false
		}
		return map[string]any{"type": "text", "text": text}, true
	case "input_image":
		url := firstNonEmptyString(stringValue(partMap["image_url"]), stringValue(partMap["url"]))
		if url == "" {
			return nil, false
		}
		if strings.HasPrefix(url, "data:") {
			mediaType, data, ok := splitBase64DataURL(url)
			if !ok {
				return nil, false
			}
			return map[string]any{
				"type": "image",
				"source": map[string]any{
					"type":       "base64",
					"media_type": mediaType,
					"data":       data,
				},
			}, true
		}
		return map[string]any{
			"type": "image",
			"source": map[string]any{
				"type": "url",
				"url":  url,
			},
		}, true
	case "input_file":
		fileData := stringValue(partMap["file_data"])
		if fileData == "" {
			return nil, false
		}
		mediaType := "application/octet-stream"
		data := fileData
		if strings.HasPrefix(fileData, "data:") {
			if parsedMediaType, parsedData, ok := splitBase64DataURL(fileData); ok {
				mediaType = parsedMediaType
				data = parsedData
			}
		}
		return map[string]any{
			"type": "document",
			"source": map[string]any{
				"type":       "base64",
				"media_type": mediaType,
				"data":       data,
			},
		}, true
	default:
		return nil, false
	}
}

func splitBase64DataURL(dataURL string) (mediaType string, data string, ok bool) {
	trimmed := strings.TrimPrefix(dataURL, "data:")
	mediaAndData := strings.SplitN(trimmed, ";base64,", 2)
	if len(mediaAndData) != 2 || mediaAndData[1] == "" {
		return "", "", false
	}
	mediaType = "application/octet-stream"
	if mediaAndData[0] != "" {
		mediaType = mediaAndData[0]
	}
	return mediaType, mediaAndData[1], true
}

func firstNonEmptyString(values ...string) string {
	for _, value := range values {
		if value != "" {
			return value
		}
	}
	return ""
}

// SerializeRequest converts a UnifiedRequest to Anthropic /v1/messages JSON.
func (h *AnthropicHandler) SerializeRequest(req *UnifiedRequest) ([]byte, error) {
	out := map[string]any{
		"model": req.Model,
	}

	// Note: stream is intentionally NOT written to the translated body.
	// The caller controls streaming via transport; preserveUnknownFields
	// restores stream from the original JSON when needed.
	if req.Temperature != nil {
		out["temperature"] = *req.Temperature
	}
	if req.TopP != nil {
		out["top_p"] = *req.TopP
	}
	if req.MaxTokens != nil {
		out["max_tokens"] = *req.MaxTokens
	} else {
		// Claude API requires max_tokens; default matches upstream translator.
		out["max_tokens"] = 32000
	}
	if len(req.Stop) > 0 {
		out["stop_sequences"] = req.Stop
	}
	if speed, ok := anthropicSpeedForRequest(req); ok {
		out["speed"] = speed
	}
	normalizedTools, includedToolNames := anthropicRequestTools(req)
	if len(normalizedTools) > 0 {
		out["tools"] = normalizedTools
	}
	if req.ToolChoice != nil {
		if choice := normalizeAnthropicToolChoiceForRequest(req.ToolChoice, req.SourceFormat, includedToolNames); choice != nil {
			out["tool_choice"] = choice
		}
	}

	// Separate system messages
	var systemBlocks []any
	var messages []any

	messagesForSerialization := groupConsecutiveRoleMessages(req.Messages)
	if shouldDedupeAnthropicToolResults(req) {
		messagesForSerialization = dedupeAnthropicToolResults(messagesForSerialization)
	}
	for _, msg := range messagesForSerialization {
		if msg.Role == "system" {
			for _, b := range msg.Content {
				if tb, ok := b.(TextBlock); ok {
					sb := map[string]any{"type": "text", "text": tb.Text}
					if tb.CacheControl != nil {
						sb["cache_control"] = tb.CacheControl
					}
					systemBlocks = append(systemBlocks, sb)
				}
			}
			continue
		}
		serialized := h.serializeOneMessageForRequest(req, msg)
		if req != nil && !anthropicSerializedMessageHasContent(serialized) {
			continue
		}
		messages = append(messages, serialized)
	}

	if len(systemBlocks) == 1 {
		if sb, ok := systemBlocks[0].(map[string]any); ok {
			if sb["cache_control"] == nil {
				out["system"] = sb["text"]
			} else {
				out["system"] = systemBlocks
			}
		}
	} else if len(systemBlocks) > 1 {
		out["system"] = systemBlocks
	}

	out["messages"] = messages
	if req.Thinking != nil {
		ApplyAnthropicThinking(req.Thinking, out)
	} else if req.ReasoningEffort != "" {
		if config := thinkingFromLevel(req.ReasoningEffort); config != nil {
			ApplyAnthropicThinking(config, out)
		}
	}
	return json.Marshal(out)
}

func anthropicSpeedForRequest(req *UnifiedRequest) (string, bool) {
	if req == nil {
		return "", false
	}
	if req.responsesServiceTier == "priority" {
		return "fast", true
	}
	return "", false
}

func shouldDedupeAnthropicToolResults(req *UnifiedRequest) bool {
	if req == nil {
		return false
	}
	switch resolveFormat(req.SourceFormat) {
	case FormatOpenAI, FormatOpenAIResponse, FormatCodex:
		return true
	default:
		return false
	}
}

func dedupeAnthropicToolResults(messages []OagMessage) []OagMessage {
	lastByID := make(map[string]ContentBlock)
	for _, msg := range messages {
		for _, block := range msg.Content {
			key, ok := anthropicToolResultDedupeKey(block)
			if ok {
				lastByID[key] = block
			}
		}
	}
	if len(lastByID) == 0 {
		return messages
	}

	emitted := make(map[string]struct{}, len(lastByID))
	out := make([]OagMessage, 0, len(messages))
	for _, msg := range messages {
		nextContent := make([]ContentBlock, 0, len(msg.Content))
		for _, block := range msg.Content {
			key, ok := anthropicToolResultDedupeKey(block)
			if !ok {
				nextContent = append(nextContent, block)
				continue
			}
			if _, seen := emitted[key]; seen {
				continue
			}
			emitted[key] = struct{}{}
			nextContent = append(nextContent, lastByID[key])
		}
		msg.Content = nextContent
		out = append(out, msg)
	}
	return out
}

func anthropicToolResultDedupeKey(block ContentBlock) (string, bool) {
	var rawID string
	switch b := block.(type) {
	case ToolResultBlock:
		rawID = b.ToolUseID
	case CustomToolResultBlock:
		rawID = b.ToolUseID
	default:
		return "", false
	}
	rawID = strings.TrimSpace(rawID)
	if rawID == "" {
		return "", false
	}
	return sanitizeClaudeToolID(rawID), true
}

func anthropicSerializedMessageHasContent(message map[string]any) bool {
	content, ok := message["content"]
	if !ok {
		return false
	}
	switch value := content.(type) {
	case string:
		return true
	case []any:
		return len(value) > 0
	default:
		return true
	}
}

func anthropicRequestTools(req *UnifiedRequest) ([]map[string]any, map[string]struct{}) {
	if len(req.Tools) == 0 {
		return nil, nil
	}
	if req.SourceFormat != FormatOpenAIResponse && req.SourceFormat != FormatCodex {
		normalized := make([]map[string]any, 0, len(req.Tools))
		included := make(map[string]struct{}, len(req.Tools))
		for _, tool := range req.Tools {
			normalizedTool := NormalizeToolToAnthropic(tool)
			normalized = append(normalized, normalizedTool)
			if name := stringValue(normalizedTool["name"]); name != "" {
				included[name] = struct{}{}
			}
		}
		return normalized, included
	}
	descriptors := req.responsesTools.descriptors
	if len(descriptors) == 0 {
		return nil, nil
	}
	normalized := make([]map[string]any, 0, len(descriptors))
	included := make(map[string]struct{}, len(descriptors))
	winners := eligibleResponsesAnthropicToolWinners(descriptors)
	for _, descriptor := range descriptors {
		winner, ok := winners[descriptor.name]
		if !ok || winner.order != descriptor.order {
			continue
		}
		normalizedTool, ok := normalizeResponsesToolToAnthropic(descriptor)
		if !ok {
			continue
		}
		normalized = append(normalized, normalizedTool)
		if name := stringValue(normalizedTool["name"]); name != "" {
			included[name] = struct{}{}
		}
	}
	return normalized, included
}

// ParseResponse parses a non-streaming Anthropic messages API response.
func (h *AnthropicHandler) ParseResponse(rawJSON []byte) (*UnifiedResponse, error) {
	if err := validateJSONObject(rawJSON); err != nil {
		return nil, err
	}

	root := gjson.ParseBytes(rawJSON)
	resp := &UnifiedResponse{
		ID:           root.Get("id").String(),
		Model:        root.Get("model").String(),
		FinishReason: mapAnthropicFinishReason(root.Get("stop_reason").String()),
	}

	// Extract text from content blocks
	var textParts []string
	if content := root.Get("content"); content.Exists() && content.IsArray() {
		for _, block := range content.Array() {
			switch block.Get("type").String() {
			case "text":
				textParts = append(textParts, block.Get("text").String())
			case "thinking":
				resp.ThinkingContent = block.Get("thinking").String()
				resp.ThinkingSignature = block.Get("signature").String()
			case "redacted_thinking":
				// Preserve the opaque data for round-trip; store in signature field
				if data := block.Get("data").String(); data != "" {
					resp.ThinkingSignature = data
				}
			case "tool_use":
				var tcMap map[string]any
				if err := json.Unmarshal([]byte(block.Raw), &tcMap); err == nil {
					resp.ToolCalls = appendResponseToolCall(resp.ToolCalls, tcMap)
				}
			}
		}
	}
	resp.Content = strings.Join(textParts, "")

	// Usage
	if usage := root.Get("usage"); usage.Exists() {
		resp.Usage = claudeUsage(usage)
	}

	normalizeResponseToolCallFinish(resp)
	return resp, nil
}

func (h *AnthropicHandler) FormatResponse(resp *UnifiedResponse, model string) ([]byte, error) {
	m := model
	if resp.preferResponseModel {
		m = resp.Model
	} else if m == "" {
		m = resp.Model
	}

	var content []any
	if len(resp.responseContent) > 0 {
		for _, block := range resp.responseContent {
			if serialized := anthropicResponseContentBlock(block); serialized != nil {
				content = append(content, serialized)
			}
		}
	} else if resp.ThinkingContent != "" {
		thinkingBlock := map[string]any{"type": "thinking", "thinking": resp.ThinkingContent}
		if resp.ThinkingSignature != "" {
			thinkingBlock["signature"] = resp.ThinkingSignature
		}
		content = append(content, thinkingBlock)
	}
	if len(resp.responseContent) == 0 {
		content = append(content, map[string]any{"type": "text", "text": resp.Content})
		for _, call := range resp.ToolCalls {
			content = append(content, NormalizeToolCallToAnthropic(call))
		}
	}

	out := map[string]any{
		"id":            resp.ID,
		"type":          "message",
		"role":          "assistant",
		"model":         m,
		"content":       content,
		"stop_reason":   anthropicStopReason(resp.FinishReason),
		"stop_sequence": nil,
	}
	if resp.Usage != nil {
		usageMap := map[string]any{}
		if usageHasPrompt(resp.Usage) {
			usageMap["input_tokens"] = resp.Usage.PromptTokens
		}
		if usageHasCompletion(resp.Usage) {
			usageMap["output_tokens"] = resp.Usage.CompletionTokens
		}
		if usageHasCacheCreation(resp.Usage) {
			usageMap["cache_creation_input_tokens"] = resp.Usage.CacheCreationInputTokens
		}
		cacheRead := resp.Usage.CacheReadInputTokens
		if !usageHasCacheRead(resp.Usage) {
			cacheRead = resp.Usage.CachedTokens
		}
		if usageHasCacheRead(resp.Usage) || usageHasCached(resp.Usage) {
			usageMap["cache_read_input_tokens"] = cacheRead
		}
		if usageHasReasoning(resp.Usage) {
			usageMap["thinking_tokens"] = resp.Usage.ReasoningTokens
		}
		if resp.Usage.serverToolUseWebSearchRequests > 0 {
			usageMap["server_tool_use"] = map[string]any{
				"web_search_requests": resp.Usage.serverToolUseWebSearchRequests,
			}
		}
		out["usage"] = usageMap
	}

	return json.Marshal(out)
}

func anthropicResponseContentBlock(block ContentBlock) map[string]any {
	switch b := block.(type) {
	case ThinkingBlock:
		if b.Thinking == "" && b.Signature == "" {
			return nil
		}
		out := map[string]any{"type": "thinking", "thinking": b.Thinking}
		if b.Signature != "" {
			out["signature"] = b.Signature
		}
		return out
	case TextBlock:
		if b.Text == "" {
			return nil
		}
		return map[string]any{"type": "text", "text": b.Text}
	case ToolUseBlock:
		return map[string]any{
			"type":  "tool_use",
			"id":    sanitizeClaudeToolID(b.ID),
			"name":  b.Name,
			"input": b.Input,
		}
	case CustomToolUseBlock:
		return map[string]any{
			"type":  "tool_use",
			"id":    sanitizeClaudeToolID(b.ID),
			"name":  b.Name,
			"input": map[string]any{"input": b.Input},
		}
	case WebSearchInvocationBlock:
		input := b.Input
		if input == nil {
			input = codexWebSearchInput(b.Query)
		}
		return map[string]any{
			"type":  "server_tool_use",
			"id":    sanitizeClaudeToolID(b.ID),
			"name":  "web_search",
			"input": input,
		}
	case WebSearchResultSetBlock:
		content := make([]any, 0, len(b.Results))
		for _, result := range b.Results {
			url := strings.TrimSpace(result.URL)
			if url == "" {
				continue
			}
			title := result.Title
			if !b.noTitleFallback {
				title = strings.TrimSpace(result.Title)
				if title == "" {
					title = url
				}
			}
			item := map[string]any{
				"type":     "web_search_result",
				"url":      url,
				"page_age": nil,
			}
			if title != "" || result.titlePresent || !b.noTitleFallback {
				item["title"] = title
			}
			content = append(content, item)
		}
		return map[string]any{
			"type":        "web_search_tool_result",
			"tool_use_id": sanitizeClaudeToolID(b.ToolUseID),
			"content":     content,
		}
	case WebSearchAnnotationBlock:
		if b.Text == "" {
			return nil
		}
		out := map[string]any{"type": "text", "text": b.Text}
		if len(b.Citations) > 0 {
			citations := make([]any, 0, len(b.Citations))
			for _, citation := range b.Citations {
				citations = append(citations, anthropicWebSearchCitation(citation, b.Text))
			}
			out["citations"] = citations
		}
		return out
	default:
		return nil
	}
}

func anthropicWebSearchCitation(citation WebSearchCitation, citedText string) map[string]any {
	return map[string]any{
		"type":       "web_search_result_location",
		"cited_text": citedText,
		"url":        citation.URL,
		"title":      citation.Title,
	}
}

// FormatError formats a UnifiedError into Anthropic error JSON.
func (h *AnthropicHandler) FormatError(err *UnifiedError) ([]byte, error) {
	out := map[string]any{
		"type": "error",
		"error": map[string]any{
			"type":    err.ErrorType,
			"message": err.Message,
		},
	}
	return json.Marshal(out)
}

// HasToolsDefined checks if tools are defined in the raw Anthropic JSON.
func (h *AnthropicHandler) HasToolsDefined(rawJSON []byte) bool {
	tools := gjson.GetBytes(rawJSON, "tools")
	return tools.Exists() && tools.IsArray() && len(tools.Array()) > 0
}

// anthropicStopReason maps OpenAI finish_reason to Anthropic stop_reason.
func anthropicStopReason(finishReason string) string {
	switch finishReason {
	case "stop":
		return "end_turn"
	case "length":
		return "max_tokens"
	case "tool_calls":
		return "tool_use"
	default:
		return finishReason
	}
}

// Ensure compile-time interface compliance.
var _ ProtocolHandler = (*AnthropicHandler)(nil)
var _ = time.Now // suppress unused import
