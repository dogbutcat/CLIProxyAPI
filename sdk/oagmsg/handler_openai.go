package oagmsg

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/signature"
	"github.com/tidwall/gjson"
)

// OpenAIHandler implements ProtocolHandler for the OpenAI /v1/chat/completions format.
// Aligned with oag_server handlers/openai.py OpenAIHandler.
type OpenAIHandler struct{}

func (h *OpenAIHandler) Format() Format { return FormatOpenAI }

// ParseRequest parses OpenAI chat completions JSON into a UnifiedRequest.
func (h *OpenAIHandler) ParseRequest(rawJSON []byte) (*UnifiedRequest, error) {
	if err := validateJSONObject(rawJSON); err != nil {
		return nil, err
	}

	root := gjson.ParseBytes(rawJSON)

	msgs, err := h.ParseMessages(rawJSON)
	if err != nil {
		return nil, err
	}

	req := &UnifiedRequest{
		Model:        root.Get("model").String(),
		Messages:     msgs,
		Stream:       root.Get("stream").Bool(),
		SourceFormat: FormatOpenAI,
	}

	if v := root.Get("temperature"); v.Exists() {
		t := v.Float()
		req.Temperature = &t
	}
	if v := root.Get("top_p"); v.Exists() {
		t := v.Float()
		req.TopP = &t
	}
	if v := root.Get("max_completion_tokens"); v.Exists() {
		t := int(v.Int())
		req.MaxTokens = &t
	} else if v := root.Get("max_tokens"); v.Exists() {
		t := int(v.Int())
		req.MaxTokens = &t
	}
	if v := root.Get("stop"); v.Exists() {
		if v.IsArray() {
			for _, s := range v.Array() {
				req.Stop = append(req.Stop, s.String())
			}
		} else {
			req.Stop = []string{v.String()}
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
	if config := ExtractOpenAIThinking(root); config != nil {
		req.SetThinking(config)
	}
	if v := root.Get("response_format"); v.Exists() {
		var m map[string]any
		if err := json.Unmarshal([]byte(v.Raw), &m); err == nil {
			req.ResponseFormat = m
		}
	}

	return req, nil
}

func openAIResponseFormatFromResponsesTextFormat(textFormat gjson.Result) map[string]any {
	formatType := textFormat.Get("type").String()
	switch formatType {
	case "text", "json_object":
		return map[string]any{"type": formatType}
	case "json_schema":
		jsonSchema := map[string]any{}
		if nested := textFormat.Get("json_schema"); nested.Exists() && nested.IsObject() {
			_ = json.Unmarshal([]byte(nested.Raw), &jsonSchema)
		}
		for _, field := range []string{"name", "description", "strict"} {
			if value := textFormat.Get(field); value.Exists() {
				jsonSchema[field] = value.Value()
			}
		}
		if schema := textFormat.Get("schema"); schema.Exists() {
			jsonSchema["schema"] = jsonValueFromRaw(schema.Raw)
		}
		return map[string]any{
			"type":        "json_schema",
			"json_schema": jsonSchema,
		}
	default:
		return nil
	}
}

func responsesTextFormatFromOpenAIResponseFormat(responseFormat map[string]any, flattenJSONSchema bool) map[string]any {
	formatType, _ := responseFormat["type"].(string)
	switch formatType {
	case "text", "json_object":
		return map[string]any{"type": formatType}
	case "json_schema":
		if !flattenJSONSchema {
			return responseFormat
		}
		format := map[string]any{"type": "json_schema"}
		jsonSchema, _ := responseFormat["json_schema"].(map[string]any)
		for _, field := range []string{"name", "description", "strict", "schema"} {
			if value, ok := jsonSchema[field]; ok {
				format[field] = value
			}
		}
		return format
	default:
		return nil
	}
}

func jsonValueFromRaw(raw string) any {
	var value any
	if err := json.Unmarshal([]byte(raw), &value); err != nil {
		return nil
	}
	return value
}

// ParseMessages extracts messages from OpenAI chat completions JSON.
func (h *OpenAIHandler) ParseMessages(rawJSON []byte) ([]OagMessage, error) {
	if err := validateJSONObject(rawJSON); err != nil {
		return nil, err
	}

	root := gjson.ParseBytes(rawJSON)
	messagesResult := root.Get("messages")
	if !messagesResult.Exists() || !messagesResult.IsArray() {
		return nil, nil
	}

	var msgs []OagMessage
	for _, msg := range messagesResult.Array() {
		parsed := h.parseOneMessage(msg)
		msgs = append(msgs, parsed...)
	}
	return msgs, nil
}

// parseOneMessage parses a single OpenAI message JSON into one or more OagMessages.
func (h *OpenAIHandler) parseOneMessage(msg gjson.Result) []OagMessage {
	role := msg.Get("role").String()
	content := msg.Get("content")
	msgName := msg.Get("name").String()

	// OpenAI role="tool" -> user message with ToolResultBlock
	if role == "tool" {
		contentStr := ""
		if content.Exists() {
			contentStr = content.String()
		}
		cacheCtrl := parseCacheControl(msg)
		return []OagMessage{{
			Role: "user",
			Name: msgName,
			Content: []ContentBlock{
				ToolResultBlock{
					ToolUseID:    msg.Get("tool_call_id").String(),
					Content:      contentStr,
					CacheControl: cacheCtrl,
				},
			},
		}}
	}

	// assistant with tool_calls -> ToolUseBlock(s)
	if role == "assistant" && msg.Get("tool_calls").Exists() {
		var blocks []ContentBlock
		if reasoning := msg.Get("reasoning_content"); reasoning.Exists() && reasoning.Type == gjson.String && reasoning.String() != "" {
			blocks = append(blocks, ThinkingBlock{
				Thinking:         reasoning.String(),
				signaturePresent: true,
			})
		}

		// Parse text content
		if content.Exists() && content.String() != "" {
			if content.Type == gjson.String {
				blocks = append(blocks, TextBlock{Text: content.String()})
			}
		}

		// Parse tool_calls
		for _, tc := range msg.Get("tool_calls").Array() {
			funcObj := tc.Get("function")
			var inputObj map[string]any
			argsStr := funcObj.Get("arguments").String()
			if argsStr != "" {
				if err := json.Unmarshal([]byte(argsStr), &inputObj); err != nil {
					inputObj = map[string]any{}
				}
			} else {
				inputObj = map[string]any{}
			}
			blocks = append(blocks, ToolUseBlock{
				ID:    tc.Get("id").String(),
				Name:  funcObj.Get("name").String(),
				Input: inputObj,
			})
		}
		return []OagMessage{{Role: "assistant", Name: msgName, Content: blocks}}
	}

	// system / user / assistant / developer (text/image/file/audio)
	roleMap := map[string]string{
		"system": "system", "user": "user", "assistant": "assistant", "developer": "system",
	}
	oagRole := roleMap[role]
	if oagRole == "" {
		oagRole = "user"
	}

	var blocks []ContentBlock
	if role == "assistant" {
		if reasoning := msg.Get("reasoning_content"); reasoning.Exists() && reasoning.Type == gjson.String && reasoning.String() != "" {
			blocks = append(blocks, ThinkingBlock{
				Thinking:         reasoning.String(),
				signaturePresent: true,
			})
		}
	}

	if !content.Exists() || content.Type == gjson.Null {
		if len(blocks) == 0 {
			blocks = append(blocks, TextBlock{Text: ""})
		}
		return []OagMessage{{Role: oagRole, Name: msgName, Content: blocks}}
	}

	// String content
	if content.Type == gjson.String {
		blocks = append(blocks, TextBlock{Text: content.String()})
		return []OagMessage{{Role: oagRole, Name: msgName, Content: blocks}}
	}

	// Array content (multimodal blocks)
	if content.IsArray() {
		for _, block := range content.Array() {
			blkType := block.Get("type").String()
			parsed := h.parseContentBlock(blkType, block)
			if parsed != nil {
				blocks = append(blocks, parsed)
			}
		}
	}

	if len(blocks) == 0 {
		blocks = append(blocks, TextBlock{Text: ""})
	}
	return []OagMessage{{Role: oagRole, Name: msgName, Content: blocks}}
}

// parseContentBlock parses a single OpenAI content block.
func (h *OpenAIHandler) parseContentBlock(blkType string, block gjson.Result) ContentBlock {
	cacheCtrl := parseCacheControl(block)

	switch blkType {
	case "text":
		tb := TextBlock{Text: block.Get("text").String()}
		if cacheCtrl != nil {
			tb.CacheControl = cacheCtrl
		}
		return tb

	case "image_url":
		url := block.Get("image_url.url").String()
		if strings.HasPrefix(url, "data:") {
			meta, data, _ := strings.Cut(url[5:], ",")
			mime := strings.Split(meta, ";")[0]
			ib := ImageBlock{MediaType: mime, Data: data}
			if cacheCtrl != nil {
				ib.CacheControl = cacheCtrl
			}
			return ib
		}
		if url != "" {
			ib := ImageBlock{URL: url}
			if cacheCtrl != nil {
				ib.CacheControl = cacheCtrl
			}
			return ib
		}
		return nil

	case "video_url":
		videoURL := block.Get("video_url.url").String()
		if fb := parseFileData("", videoURL); fb != nil {
			return fb
		}
		return nil

	case "file", "input_file", "document":
		fileObj := block.Get("file")
		filename := fileObj.Get("filename").String()
		if filename == "" {
			filename = block.Get("filename").String()
		}
		rawData := fileObj.Get("file_data").String()
		if rawData == "" {
			rawData = block.Get("file_data").String()
		}
		if rawData == "" {
			rawData = block.Get("data").String()
		}

		fb := parseFileData(filename, rawData)
		if fb != nil {
			if cacheCtrl != nil {
				if fbVal, ok := fb.(FileBlock); ok {
					fbVal.CacheControl = cacheCtrl
					fb = fbVal
				}
			}
			return fb
		}
		// URL-based file
		fileURL := fileObj.Get("file_url").String()
		if fileURL == "" {
			fileURL = block.Get("file_url").String()
		}
		if fileURL == "" {
			fileURL = block.Get("url").String()
		}
		if fileURL != "" {
			return FileBlock{Filename: filename, URL: fileURL}
		}
		return rawBlock(block)

	case "input_audio":
		audioData := block.Get("input_audio.data").String()
		audioFormat := block.Get("input_audio.format").String()
		if audioData != "" {
			return AudioBlock{Data: audioData, Format: audioFormat}
		}
		return rawBlock(block)

	default:
		return rawBlock(block)
	}
}

// SerializeMessages converts OagMessages to OpenAI chat completions messages JSON.
func (h *OpenAIHandler) SerializeMessages(msgs []OagMessage) ([]byte, error) {
	var result []any

	for _, msg := range msgs {
		serialized := h.serializeOneMessage(msg)
		result = append(result, serialized...)
	}

	return json.Marshal(result)
}

// serializeOneMessage serializes a single OagMessage to OpenAI format.
// Returns a slice because ToolResultBlocks split into separate role="tool" messages.
func (h *OpenAIHandler) serializeOneMessage(msg OagMessage) []any {
	return h.serializeOneMessageWithTextBlocks(msg, false)
}

func (h *OpenAIHandler) serializeOneMessageWithTextBlocks(msg OagMessage, preserveTextBlocks bool) []any {
	return h.serializeOneMessageForRequest(nil, msg, preserveTextBlocks)
}

func (h *OpenAIHandler) serializeOneMessageForRequest(req *UnifiedRequest, msg OagMessage, preserveTextBlocks bool) []any {
	toolResults := openAIToolResultMessages(msg)
	otherBlocks := filterNonToolResults(msg.Content)

	var results []any

	// ToolResults -> separate role="tool" messages
	for _, toolMsg := range toolResults {
		results = append(results, toolMsg)
	}

	if len(otherBlocks) == 0 && len(toolResults) > 0 {
		return results
	}

	// Build the main message
	out := map[string]any{"role": msg.Role}
	if msg.Name != "" {
		out["name"] = msg.Name
	}

	// Extract tool calls from blocks
	var contentBlocks []any
	var toolCalls []any
	var reasoningParts []string

	for _, b := range otherBlocks {
		switch block := b.(type) {
		case TextBlock:
			contentBlocks = append(contentBlocks, map[string]any{
				"type": "text",
				"text": block.Text,
			})
		case ImageBlock:
			if block.Data != "" {
				url := fmt.Sprintf("data:%s;base64,%s", block.MediaType, block.Data)
				contentBlocks = append(contentBlocks, map[string]any{
					"type":      "image_url",
					"image_url": map[string]any{"url": url},
				})
			} else if block.URL != "" {
				contentBlocks = append(contentBlocks, map[string]any{
					"type":      "image_url",
					"image_url": map[string]any{"url": block.URL},
				})
			}
		case FileBlock:
			if block.Data != "" {
				fileData := fmt.Sprintf("data:%s;base64,%s", block.MediaType, block.Data)
				contentBlocks = append(contentBlocks, map[string]any{
					"type": "file",
					"file": map[string]any{
						"filename":  block.Filename,
						"file_data": fileData,
					},
				})
			} else if block.URL != "" {
				contentBlocks = append(contentBlocks, map[string]any{
					"type": "file",
					"file": map[string]any{
						"filename": block.Filename,
						"file_url": block.URL,
					},
				})
			}
		case ToolUseBlock:
			inputJSON, _ := json.Marshal(block.Input)
			toolCalls = append(toolCalls, map[string]any{
				"id":   block.ID,
				"type": "function",
				"function": map[string]any{
					"name":      block.Name,
					"arguments": string(inputJSON),
				},
			})
		case CustomToolUseBlock:
			inputJSON, _ := json.Marshal(map[string]any{"input": block.Input})
			toolCalls = append(toolCalls, map[string]any{
				"id":   block.ID,
				"type": "function",
				"function": map[string]any{
					"name":      block.Name,
					"arguments": string(inputJSON),
				},
			})
		case ThinkingBlock:
			block, keep := requestThinkingForTarget(req, FormatOpenAI, msg.Role, block, signature.SignatureBlockKindGPTReasoning)
			if keep && !block.Redacted && block.Thinking != "" {
				reasoningParts = append(reasoningParts, block.Thinking)
			}
		case AudioBlock:
			cb := map[string]any{"type": "input_audio"}
			audio := map[string]any{"data": block.Data}
			if block.Format != "" {
				audio["format"] = block.Format
			}
			cb["input_audio"] = audio
			contentBlocks = append(contentBlocks, cb)
		case RawBlock:
			contentBlocks = append(contentBlocks, block.RawData)
		}
	}

	// Set tool_calls for assistant messages
	if len(toolCalls) > 0 {
		out["tool_calls"] = toolCalls
	}
	if len(reasoningParts) > 0 {
		out["reasoning_content"] = strings.Join(reasoningParts, "\n")
	}

	// Simplify content: single text block -> string
	if !preserveTextBlocks && len(contentBlocks) == 1 {
		if tb, ok := contentBlocks[0].(map[string]any); ok {
			if tb["type"] == "text" {
				out["content"] = tb["text"]
				results = append(results, out)
				return results
			}
		}
	}

	if len(contentBlocks) > 0 {
		out["content"] = contentBlocks
	} else if len(toolCalls) == 0 {
		out["content"] = ""
	}

	results = append(results, out)
	return results
}

func openAIToolResultMessages(msg OagMessage) []map[string]any {
	var results []map[string]any
	for _, block := range msg.Content {
		var toolMsg map[string]any
		switch tr := block.(type) {
		case ToolResultBlock:
			toolMsg = map[string]any{
				"role":         "tool",
				"tool_call_id": tr.ToolUseID,
				"content":      normalizeOpenAIToolResultContent(tr.Content),
			}
		case CustomToolResultBlock:
			toolMsg = map[string]any{
				"role":         "tool",
				"tool_call_id": tr.ToolUseID,
				"content":      normalizeOpenAICustomToolResultContent(tr),
			}
		}
		if toolMsg == nil {
			continue
		}
		if msg.Name != "" {
			toolMsg["name"] = msg.Name
		}
		results = append(results, toolMsg)
	}
	return results
}

func normalizeOpenAICustomToolResultContent(result CustomToolResultBlock) any {
	if content, ok := normalizeOpenAICustomToolResultContentFromRaw(result.rawOutputJSON); ok {
		return content
	}
	rawOutput := result.rawOutput
	if rawOutput == nil {
		return result.OutputString()
	}
	if text, ok := rawOutput.(string); ok {
		var decoded any
		if json.Unmarshal([]byte(text), &decoded) == nil && openAIToolResultContentHasImage(decoded) {
			return normalizeOpenAIToolResultContent(decoded)
		}
		return result.OutputString()
	}
	if openAIToolResultContentHasImage(rawOutput) {
		return normalizeOpenAIToolResultContent(rawOutput)
	}
	return result.OutputString()
}

func normalizeOpenAICustomToolResultContentFromRaw(rawOutput string) (any, bool) {
	rawOutput = strings.TrimSpace(rawOutput)
	if rawOutput == "" {
		return nil, false
	}
	output := gjson.Parse(rawOutput)
	structuredContent := output
	if output.Type == gjson.String {
		if !gjson.Valid(output.String()) {
			return nil, false
		}
		structuredContent = gjson.Parse(output.String())
	}
	if !openAIToolResultGJSONHasImage(structuredContent) {
		return nil, false
	}
	var parts []any
	for _, item := range structuredContent.Array() {
		parts = append(parts, openAIToolResultGJSONContentPart(item))
	}
	return parts, true
}

func openAIToolResultGJSONHasImage(content gjson.Result) bool {
	if !content.IsArray() {
		return false
	}
	for _, item := range content.Array() {
		if _, ok := openAIToolResultGJSONImage(item); ok {
			return true
		}
	}
	return false
}

func openAIToolResultGJSONContentPart(item gjson.Result) any {
	switch item.Get("type").String() {
	case "text", "input_text", "output_text":
		return map[string]any{"type": "text", "text": item.Get("text").String()}
	case "image_url", "input_image":
		if image, ok := openAIToolResultGJSONImage(item); ok {
			return map[string]any{"type": "image_url", "image_url": image}
		}
	}
	return map[string]any{"type": "text", "text": item.Raw}
}

func openAIToolResultGJSONImage(item gjson.Result) (map[string]any, bool) {
	imageURL := ""
	if url := item.Get("image_url"); url.Exists() {
		imageURL = url.String()
		if imageURL == "" {
			imageURL = url.Get("url").String()
		}
	}
	if imageURL == "" {
		return nil, false
	}
	image := map[string]any{"url": imageURL}
	if detail := normalizeOpenAIImageDetail(item.Get("detail").Value()); detail != "" {
		image["detail"] = detail
	}
	return image, true
}

func openAIToolResultContentHasImage(content any) bool {
	items, ok := content.([]any)
	if !ok {
		return false
	}
	for _, item := range items {
		if value, okMap := item.(map[string]any); okMap && openAIToolResultImageURL(value) != "" {
			return true
		}
	}
	return false
}

func normalizeOpenAIToolResultContent(content any) any {
	items, ok := content.([]any)
	if !ok {
		if content == nil {
			return ""
		}
		return content
	}

	if !openAIToolResultContentHasImage(content) {
		encoded, err := json.Marshal(items)
		if err != nil {
			return ""
		}
		return string(encoded)
	}

	parts := make([]any, 0, len(items))
	for _, item := range items {
		value, okMap := item.(map[string]any)
		if !okMap {
			parts = append(parts, map[string]any{"type": "text", "text": fmt.Sprint(item)})
			continue
		}
		if imageURL := openAIToolResultImageURL(value); imageURL != "" {
			image := map[string]any{"url": imageURL}
			if detail := normalizeOpenAIImageDetail(value["detail"]); detail != "" {
				image["detail"] = detail
			}
			parts = append(parts, map[string]any{"type": "image_url", "image_url": image})
			continue
		}
		if text, okText := value["text"].(string); okText {
			parts = append(parts, map[string]any{"type": "text", "text": text})
			continue
		}
		encoded, _ := json.Marshal(value)
		parts = append(parts, map[string]any{"type": "text", "text": string(encoded)})
	}
	return parts
}

func openAIToolResultImageURL(value map[string]any) string {
	typeName, _ := value["type"].(string)
	switch typeName {
	case "image":
		source, _ := value["source"].(map[string]any)
		if source == nil {
			return ""
		}
		if sourceType, _ := source["type"].(string); sourceType == "base64" {
			mediaType, _ := source["media_type"].(string)
			data, _ := source["data"].(string)
			if mediaType != "" && data != "" {
				return "data:" + mediaType + ";base64," + data
			}
		}
		if url, _ := source["url"].(string); url != "" {
			return url
		}
	case "input_image", "output_image":
		url, _ := value["image_url"].(string)
		return url
	case "image_url":
		if image, ok := value["image_url"].(map[string]any); ok {
			url, _ := image["url"].(string)
			return url
		}
		url, _ := value["image_url"].(string)
		return url
	}
	return ""
}

func normalizeOpenAIImageDetail(value any) string {
	detail, _ := value.(string)
	switch strings.ToLower(strings.TrimSpace(detail)) {
	case "auto", "low", "high":
		return strings.ToLower(strings.TrimSpace(detail))
	case "original":
		return "high"
	default:
		return ""
	}
}

// SerializeRequest converts a UnifiedRequest to OpenAI chat completions JSON.
func (h *OpenAIHandler) SerializeRequest(req *UnifiedRequest) ([]byte, error) {
	out := map[string]any{
		"model": req.Model,
	}

	if req.Stream {
		out["stream"] = true
	}
	if req.Temperature != nil {
		out["temperature"] = *req.Temperature
	}
	if req.TopP != nil {
		out["top_p"] = *req.TopP
	}
	if req.MaxTokens != nil {
		out["max_tokens"] = *req.MaxTokens
	}
	if len(req.Stop) > 0 {
		out["stop"] = req.Stop
	}
	normalizedTools := openAIRequestTools(req)
	if len(normalizedTools) > 0 {
		out["tools"] = normalizedTools
		if req.ToolChoice != nil {
			if req.SourceFormat == FormatOpenAIResponse || req.SourceFormat == FormatCodex {
				out["tool_choice"] = normalizeResponsesToolChoiceToOpenAI(req.ToolChoice)
			} else {
				out["tool_choice"] = NormalizeToolChoiceToOpenAI(req.ToolChoice)
			}
		}
		if (req.SourceFormat == FormatOpenAIResponse || req.SourceFormat == FormatCodex) && req.responsesParallelToolCalls != nil {
			out["parallel_tool_calls"] = *req.responsesParallelToolCalls
		}
	}
	if req.Thinking != nil {
		ApplyOpenAIThinking(req.Thinking, out)
	} else if req.ReasoningEffort != "" {
		out["reasoning_effort"] = req.ReasoningEffort
	}
	if len(req.ResponseFormat) > 0 {
		out["response_format"] = req.ResponseFormat
	}

	// Serialize messages
	var messages []any
	preserveTextBlocks := req.SourceFormat == FormatOpenAIResponse || req.SourceFormat == FormatCodex
	messagesForSerialization := openAIChatMessagesForSerialization(req)
	for _, msg := range messagesForSerialization {
		messages = append(messages, h.serializeOneMessageForRequest(req, msg, preserveTextBlocks)...)
	}
	out["messages"] = messages

	return json.Marshal(out)
}

func openAIChatMessagesForSerialization(req *UnifiedRequest) []OagMessage {
	if req == nil || (req.SourceFormat != FormatOpenAIResponse && req.SourceFormat != FormatCodex) {
		return req.Messages
	}
	return coalesceOpenAIChatAssistantMessages(req.Messages)
}

func coalesceOpenAIChatAssistantMessages(messages []OagMessage) []OagMessage {
	if len(messages) <= 1 {
		return messages
	}
	coalesced := make([]OagMessage, 0, len(messages))
	for _, msg := range messages {
		if msg.Role == "assistant" && len(coalesced) > 0 && coalesced[len(coalesced)-1].Role == "assistant" {
			coalesced[len(coalesced)-1].Content = append(coalesced[len(coalesced)-1].Content, msg.Content...)
			continue
		}
		coalesced = append(coalesced, msg)
	}
	return coalesced
}

func openAIRequestTools(req *UnifiedRequest) []map[string]any {
	if len(req.Tools) == 0 {
		return nil
	}
	if req.SourceFormat != FormatOpenAIResponse && req.SourceFormat != FormatCodex {
		normalized := make([]map[string]any, 0, len(req.Tools))
		for _, tool := range req.Tools {
			normalized = append(normalized, NormalizeToolToOpenAI(tool))
		}
		return normalized
	}
	descriptors := openAIChatToolDescriptors(req.responsesTools)
	if len(descriptors) == 0 {
		return nil
	}
	normalized := make([]map[string]any, 0, len(descriptors))
	for _, descriptor := range descriptors {
		if descriptor.name == "" {
			continue
		}
		switch descriptor.toolType {
		case "function", "custom":
			if normalizedTool, ok := normalizeResponsesToolToOpenAI(descriptor); ok {
				normalized = append(normalized, normalizedTool)
			}
		}
	}
	return normalized
}

// ParseResponse parses an OpenAI chat completions non-streaming response.
func (h *OpenAIHandler) ParseResponse(rawJSON []byte) (*UnifiedResponse, error) {
	if err := validateJSONObject(rawJSON); err != nil {
		return nil, err
	}

	root := gjson.ParseBytes(rawJSON)
	resp := &UnifiedResponse{
		ID:      root.Get("id").String(),
		Model:   root.Get("model").String(),
		Created: root.Get("created").Int(),
	}

	choice := root.Get("choices.0")
	if choice.Exists() {
		resp.FinishReason = choice.Get("finish_reason").String()
		resp.Content = choice.Get("message.content").String()
		if reasoning := choice.Get("message.reasoning_content"); reasoning.Exists() {
			resp.ThinkingContent = reasoning.String()
		}

		// Tool calls
		if tcs := choice.Get("message.tool_calls"); tcs.Exists() && tcs.IsArray() {
			for _, tc := range tcs.Array() {
				var tcMap map[string]any
				if err := json.Unmarshal([]byte(tc.Raw), &tcMap); err == nil {
					resp.ToolCalls = appendResponseToolCall(resp.ToolCalls, tcMap)
				}
			}
		}
	}

	// Usage
	if usage := root.Get("usage"); usage.Exists() {
		resp.Usage = openAIUsage(usage)
	}

	normalizeResponseToolCallFinish(resp)
	return resp, nil
}

// FormatResponse formats a UnifiedResponse into OpenAI chat completions response JSON.
func (h *OpenAIHandler) FormatResponse(resp *UnifiedResponse, model string) ([]byte, error) {
	m := model
	if m == "" {
		m = resp.Model
	}
	choice := map[string]any{
		"index":         0,
		"finish_reason": resp.FinishReason,
		"message": map[string]any{
			"role":    "assistant",
			"content": resp.Content,
		},
	}
	if resp.ThinkingContent != "" {
		choice["message"].(map[string]any)["reasoning_content"] = resp.ThinkingContent
	}
	if len(resp.ToolCalls) > 0 {
		normalized := make([]map[string]any, 0, len(resp.ToolCalls))
		for _, call := range resp.ToolCalls {
			normalized = append(normalized, NormalizeToolCallToOpenAI(call))
		}
		choice["message"].(map[string]any)["tool_calls"] = normalized
	}

	out := map[string]any{
		"id":      resp.ID,
		"object":  "chat.completion",
		"created": resp.Created,
		"model":   m,
		"choices": []any{choice},
	}
	if resp.Usage != nil {
		usageMap := map[string]any{}
		if usageHasPrompt(resp.Usage) {
			usageMap["prompt_tokens"] = usagePromptForTarget(resp.Usage, FormatOpenAI)
		}
		if usageHasCompletion(resp.Usage) {
			usageMap["completion_tokens"] = resp.Usage.CompletionTokens
		}
		if total, ok := usageTotalForTarget(resp.Usage, FormatOpenAI); ok {
			usageMap["total_tokens"] = total
		}
		promptDetails := map[string]any{}
		if cached, ok := usageCachedForTarget(resp.Usage, FormatOpenAI); ok {
			promptDetails["cached_tokens"] = cached
		}
		if usageHasCacheCreation(resp.Usage) {
			promptDetails["cached_creation_tokens"] = resp.Usage.CacheCreationInputTokens
		}
		if len(promptDetails) > 0 {
			usageMap["prompt_tokens_details"] = promptDetails
		}
		if usageHasReasoning(resp.Usage) {
			usageMap["completion_tokens_details"] = map[string]any{
				"reasoning_tokens": resp.Usage.ReasoningTokens,
			}
		}
		out["usage"] = usageMap
	}
	return json.Marshal(out)
}

// FormatError formats a UnifiedError into OpenAI error JSON.
func (h *OpenAIHandler) FormatError(err *UnifiedError) ([]byte, error) {
	out := map[string]any{
		"error": map[string]any{
			"message": err.Message,
			"type":    err.ErrorType,
			"code":    err.StatusCode,
		},
	}
	return json.Marshal(out)
}

// HasToolsDefined checks if tools are defined in the raw JSON.
func (h *OpenAIHandler) HasToolsDefined(rawJSON []byte) bool {
	tools := gjson.GetBytes(rawJSON, "tools")
	return tools.Exists() && tools.IsArray() && len(tools.Array()) > 0
}

// ----------------------------------------------------------------
// Helpers
// ----------------------------------------------------------------

// parseCacheControl extracts cache_control from a block if present.
func parseCacheControl(block gjson.Result) map[string]any {
	cc := block.Get("cache_control")
	if !cc.Exists() {
		return nil
	}
	var m map[string]any
	if err := json.Unmarshal([]byte(cc.Raw), &m); err != nil {
		return nil
	}
	return m
}

// parseFileData parses OpenAI file_data (data:mime;base64,payload) into a FileBlock.
// Returns nil if the data cannot be parsed.
func parseFileData(filename, rawData string) ContentBlock {
	if rawData == "" {
		return nil
	}

	const dataPrefix = "data:"
	if !strings.HasPrefix(rawData, dataPrefix) {
		// Raw base64 without data: URI - cannot determine MIME type reliably
		return nil
	}

	metaAndPayload := rawData[len(dataPrefix):]
	metadata, payload, found := strings.Cut(metaAndPayload, ",")
	if !found || payload == "" {
		return nil
	}

	fields := strings.Split(metadata, ";")
	mimeType := strings.TrimSpace(fields[0])
	if mimeType == "" {
		return nil
	}

	isBase64 := false
	for _, field := range fields[1:] {
		if strings.EqualFold(strings.TrimSpace(field), "base64") {
			isBase64 = true
			break
		}
	}
	if !isBase64 {
		return nil
	}

	return FileBlock{
		Filename:  filename,
		MediaType: mimeType,
		Data:      payload,
	}
}

// rawBlock creates a RawBlock from a gjson.Result.
func rawBlock(block gjson.Result) RawBlock {
	var m map[string]any
	if err := json.Unmarshal([]byte(block.Raw), &m); err != nil {
		m = map[string]any{"raw": block.String()}
	}
	return RawBlock{RawData: m}
}

// filterNonToolResults returns blocks that are not tool result blocks.
func filterNonToolResults(blocks []ContentBlock) []ContentBlock {
	var result []ContentBlock
	for _, b := range blocks {
		if _, ok := b.(ToolResultBlock); ok {
			continue
		}
		if _, ok := b.(CustomToolResultBlock); !ok {
			result = append(result, b)
		}
	}
	return result
}
