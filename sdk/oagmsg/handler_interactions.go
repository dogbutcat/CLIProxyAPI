package oagmsg

import (
	"encoding/json"
	"strings"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/signature"
	"github.com/tidwall/gjson"
)

// InteractionsHandler implements ProtocolHandler for the OpenAI Responses API
// (interactions/responses) format. Also serves as the base for CodexHandler.
//
// The Responses API uses a flat input[] array of type-tagged items instead of
// the chat completions messages[] array.
//
// Mode selects the SSE serializer strategy:
//   - InteractionsModeCodex (default): outputs response.* events
//   - InteractionsModeSteps: outputs interaction.*/step.* lifecycle events
type InteractionsHandler struct {
	Mode InteractionsMode
}

func (h *InteractionsHandler) Format() Format { return FormatOpenAIResponse }

// ParseRequest parses Responses API JSON into a UnifiedRequest.
func (h *InteractionsHandler) ParseRequest(rawJSON []byte) (*UnifiedRequest, error) {
	if err := validateJSONObject(rawJSON); err != nil {
		return nil, err
	}

	root := gjson.ParseBytes(rawJSON)
	toolIndex := buildToolDescriptorIndex(rawJSON)

	msgs, err := h.parseMessages(rawJSON, toolIndex)
	if err != nil {
		return nil, err
	}

	req := &UnifiedRequest{
		Model:          root.Get("model").String(),
		Messages:       msgs,
		SourceFormat:   FormatOpenAIResponse,
		responsesTools: toolIndex,
	}
	if v := root.Get("parallel_tool_calls"); v.Exists() {
		parallelToolCalls := v.Bool()
		req.responsesParallelToolCalls = &parallelToolCalls
	}
	if v := root.Get("service_tier"); v.Type == gjson.String {
		req.responsesServiceTier = strings.TrimSpace(v.String())
	}

	if v := root.Get("temperature"); v.Exists() {
		t := v.Float()
		req.Temperature = &t
	}
	if v := root.Get("top_p"); v.Exists() {
		t := v.Float()
		req.TopP = &t
	}
	if v := root.Get("max_output_tokens"); v.Exists() {
		t := int(v.Int())
		req.MaxTokens = &t
	}
	if tools := responsesRequestTools(toolIndex); len(tools) > 0 {
		req.Tools = append(req.Tools, tools...)
	} else if v := root.Get("tools"); v.Exists() && v.IsArray() {
		req.Tools = append(req.Tools, rawToolMaps(v)...)
	}
	if config := ExtractInteractionsThinking(root); config != nil {
		req.SetThinking(config)
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
	if textFormat := root.Get("text.format"); textFormat.Exists() {
		req.ResponseFormat = openAIResponseFormatFromResponsesTextFormat(textFormat)
	} else if responseFormat := root.Get("response_format"); responseFormat.Exists() {
		var m map[string]any
		if err := json.Unmarshal([]byte(responseFormat.Raw), &m); err == nil {
			req.ResponseFormat = m
		}
	}

	return req, nil
}

// ParseMessages extracts messages from Responses API input[] array.
func (h *InteractionsHandler) ParseMessages(rawJSON []byte) ([]OagMessage, error) {
	return h.parseMessages(rawJSON, buildToolDescriptorIndex(rawJSON))
}

func (h *InteractionsHandler) parseMessages(rawJSON []byte, toolIndex toolDescriptorIndex) ([]OagMessage, error) {
	if err := validateJSONObject(rawJSON); err != nil {
		return nil, err
	}

	root := gjson.ParseBytes(rawJSON)

	// Check for both "input" and "messages" fields
	inputField := root.Get("input")
	if !inputField.Exists() {
		inputField = root.Get("messages")
	}
	var msgs []OagMessage

	// System instructions
	if instructions := root.Get("instructions"); instructions.Exists() {
		msgs = append(msgs, SystemMsg(instructions.String()))
	}

	// Responses API accepts a plain string input as a single user turn.
	if inputField.Type == gjson.String {
		if text := inputField.String(); text != "" {
			msgs = append(msgs, UserTextMsg(text))
		}
		return msgs, nil
	}

	if !inputField.Exists() || !inputField.IsArray() {
		// Also try instructions as system message
		if len(msgs) > 0 {
			return msgs, nil
		}
		return nil, nil
	}

	for _, item := range inputField.Array() {
		itemType := responsesInputItemType(item)
		parsed := h.parseInputItem(itemType, item, toolIndex)
		if parsed != nil {
			msgs = append(msgs, *parsed)
		}
	}

	return msgs, nil
}

func responsesInputItemType(item gjson.Result) string {
	itemType := strings.TrimSpace(item.Get("type").String())
	if itemType == "" && item.Get("role").Exists() && item.Get("content").Exists() {
		return "message"
	}
	return itemType
}

// parseInputItem parses a single Responses API input item.
func (h *InteractionsHandler) parseInputItem(itemType string, item gjson.Result, toolIndex toolDescriptorIndex) *OagMessage {
	switch itemType {
	case "message":
		role := item.Get("role").String()
		oagRole := role
		if oagRole == "developer" {
			oagRole = "system"
		}
		var blocks []ContentBlock
		itemCacheControl := parseCacheControl(item)
		contentResult := item.Get("content")
		if contentResult.Type == gjson.String {
			if contentResult.String() != "" {
				blocks = append(blocks, TextBlock{Text: contentResult.String()})
			}
		} else if contentResult.IsArray() {
			for _, cb := range contentResult.Array() {
				cbType := cb.Get("type").String()
				cacheCtrl := parseCacheControl(cb)
				switch cbType {
				case "input_text":
					blocks = append(blocks, withParsedCacheControl(TextBlock{Text: cb.Get("text").String()}, cacheCtrl))
				case "input_image":
					if image := parseResponsesImagePart(cb); image != nil {
						blocks = append(blocks, withParsedCacheControl(image, cacheCtrl))
					}
				case "input_file":
					if file := parseResponsesFilePart(cb); file != nil {
						blocks = append(blocks, withParsedCacheControl(file, cacheCtrl))
					}
				case "output_text":
					blocks = append(blocks, withParsedCacheControl(TextBlock{Text: cb.Get("text").String()}, cacheCtrl))
				case "input_audio":
					audioData := cb.Get("data").String()
					audioFormat := cb.Get("format").String()
					if audioData != "" {
						blocks = append(blocks, AudioBlock{Data: audioData, Format: audioFormat})
					}
				default:
					blocks = append(blocks, rawBlock(cb))
				}
			}
		}
		if len(blocks) == 0 {
			return nil
		}
		blocks = applyItemCacheControl(blocks, itemCacheControl)
		return &OagMessage{Role: oagRole, Content: blocks}

	case "function_call":
		var input map[string]any
		argsStr := item.Get("arguments").String()
		if argsStr != "" {
			if err := json.Unmarshal([]byte(argsStr), &input); err != nil {
				input = map[string]any{}
			}
		} else {
			input = map[string]any{}
		}
		return &OagMessage{
			Role: "assistant",
			Content: []ContentBlock{
				ToolUseBlock{
					ID:    item.Get("call_id").String(),
					Name:  resolveResponsesHistoryToolName(item, toolIndex),
					Input: input,
				},
			},
		}

	case "function_call_output":
		cacheCtrl := parseCacheControl(item)
		return &OagMessage{
			Role: "user",
			Content: []ContentBlock{
				ToolResultBlock{
					ToolUseID:    item.Get("call_id").String(),
					Content:      decodeJSONResult(item.Get("output")),
					CacheControl: cacheCtrl,
				},
			},
		}

	case "custom_tool_call":
		return &OagMessage{
			Role: "assistant",
			Content: []ContentBlock{
				CustomToolUseBlock{
					ID:    item.Get("call_id").String(),
					Name:  resolveResponsesHistoryToolName(item, toolIndex),
					Input: item.Get("input").String(),
				},
			},
		}

	case "custom_tool_call_output":
		output := item.Get("output")
		cacheCtrl := parseCacheControl(item)
		return &OagMessage{
			Role: "user",
			Content: []ContentBlock{
				CustomToolResultBlock{
					ToolUseID:     item.Get("call_id").String(),
					Output:        responsesToolOutputText(output),
					rawOutput:     decodeJSONResult(output),
					rawOutputJSON: output.Raw,
					CacheControl:  cacheCtrl,
				},
			},
		}

	case "reasoning":
		// Parse reasoning as ThinkingBlock for round-trip fidelity
		thinkingText := ""
		if summary := item.Get("content"); summary.IsArray() {
			for _, part := range summary.Array() {
				if part.Get("type").String() == "summary_text" {
					thinkingText = part.Get("text").String()
					break
				}
			}
		}
		if thinkingText == "" {
			thinkingText = item.Get("summary.0.text").String()
		}
		encrypted := item.Get("encrypted_content")
		return &OagMessage{Role: "assistant", Content: []ContentBlock{ThinkingBlock{
			Thinking:         thinkingText,
			Signature:        encrypted.String(),
			signaturePresent: encrypted.Exists(),
		}}}

	default:
		return nil
	}
}

func rawToolMaps(tools gjson.Result) []map[string]any {
	var out []map[string]any
	for _, tool := range tools.Array() {
		var m map[string]any
		if err := json.Unmarshal([]byte(tool.Raw), &m); err == nil {
			out = append(out, m)
		}
	}
	return out
}

func responsesToolOutputText(output gjson.Result) string {
	if output.Type == gjson.String {
		return output.String()
	}
	if output.IsArray() {
		var b strings.Builder
		output.ForEach(func(_, part gjson.Result) bool {
			if part.Type == gjson.String {
				b.WriteString(part.String())
				return true
			}
			if text := part.Get("text"); text.Exists() {
				b.WriteString(text.String())
			}
			return true
		})
		return b.String()
	}
	if output.Exists() {
		return output.Raw
	}
	return ""
}

func resolveResponsesHistoryToolName(item gjson.Result, index toolDescriptorIndex) string {
	name := item.Get("name").String()
	if namespace := item.Get("namespace").String(); namespace != "" {
		name = qualifyToolDescriptorName(namespace, name)
	}
	if descriptor, ok := index.lookup(name); ok {
		return descriptor.name
	}
	return name
}

func parseResponsesImagePart(part gjson.Result) ContentBlock {
	imageURL := ""
	if url := part.Get("image_url"); url.Exists() && url.String() != "" {
		imageURL = url.String()
	} else if url := part.Get("url"); url.Exists() && url.String() != "" {
		imageURL = url.String()
	}
	if imageURL != "" {
		if strings.HasPrefix(imageURL, "data:") {
			mediaType, data, ok := splitBase64DataURL(imageURL)
			if !ok {
				return nil
			}
			return ImageBlock{MediaType: mediaType, Data: data}
		}
		return ImageBlock{URL: imageURL}
	}
	if data := part.Get("image_data"); data.Exists() {
		return ImageBlock{Data: data.String()}
	}
	return nil
}

func parseResponsesFilePart(part gjson.Result) ContentBlock {
	filename := part.Get("filename").String()
	fileData := part.Get("file_data").String()
	if fileData == "" {
		return nil
	}
	if fb := parseFileData(filename, fileData); fb != nil {
		return fb
	}
	return FileBlock{
		Filename:  filename,
		MediaType: "application/octet-stream",
		Data:      fileData,
	}
}

func applyItemCacheControl(blocks []ContentBlock, cacheControl map[string]any) []ContentBlock {
	if cacheControl == nil || len(blocks) == 0 {
		return blocks
	}
	last := len(blocks) - 1
	if blockHasCacheControl(blocks[last]) {
		return blocks
	}
	blocks[last] = withParsedCacheControl(blocks[last], cacheControl)
	return blocks
}

func blockHasCacheControl(block ContentBlock) bool {
	switch b := block.(type) {
	case TextBlock:
		return b.CacheControl != nil
	case ImageBlock:
		return b.CacheControl != nil
	case FileBlock:
		return b.CacheControl != nil
	case ToolResultBlock:
		return b.CacheControl != nil
	case CustomToolResultBlock:
		return b.CacheControl != nil
	case RawBlock:
		_, ok := b.RawData["cache_control"]
		return ok
	default:
		return false
	}
}

func withParsedCacheControl(block ContentBlock, cacheControl map[string]any) ContentBlock {
	if cacheControl == nil {
		return block
	}
	switch b := block.(type) {
	case TextBlock:
		b.CacheControl = cacheControl
		return b
	case ImageBlock:
		b.CacheControl = cacheControl
		return b
	case FileBlock:
		b.CacheControl = cacheControl
		return b
	case ToolResultBlock:
		b.CacheControl = cacheControl
		return b
	case CustomToolResultBlock:
		b.CacheControl = cacheControl
		return b
	case RawBlock:
		raw := cloneToolMap(b.RawData)
		raw["cache_control"] = cacheControl
		b.RawData = raw
		return b
	default:
		return block
	}
}

// SerializeMessages converts OagMessages to Responses API input items JSON.
func (h *InteractionsHandler) SerializeMessages(msgs []OagMessage) ([]byte, error) {
	var items []any

	for _, msg := range msgs {
		if msg.Role == "system" {
			continue // Handled as instructions
		}
		items = append(items, h.serializeOneItem(msg, false)...)
	}

	return json.Marshal(items)
}

// serializeOneItem serializes a single OagMessage to Responses API items.
func (h *InteractionsHandler) serializeOneItem(msg OagMessage, codexClaudeDocuments bool) []any {
	return h.serializeOneItemForRequest(nil, msg, codexClaudeDocuments)
}

func (h *InteractionsHandler) serializeOneItemForRequest(req *UnifiedRequest, msg OagMessage, codexClaudeDocuments bool) []any {
	if codexClaudeDocuments && messageHasClaudeDocumentOrigin(msg) {
		return h.serializeOneItemWithClaudeDocumentGrouping(req, msg)
	}

	var items []any

	for _, b := range msg.Content {
		switch block := b.(type) {
		case TextBlock:
			contentType := "input_text"
			if msg.Role == "assistant" {
				contentType = "output_text"
			}
			items = append(items, map[string]any{
				"type": "message",
				"role": interactionsRole(msg.Role),
				"content": []any{
					map[string]any{
						"type": contentType,
						"text": block.Text,
					},
				},
			})

		case ToolUseBlock:
			argsJSON, _ := json.Marshal(block.Input)
			items = append(items, map[string]any{
				"type":      "function_call",
				"call_id":   block.ID,
				"name":      block.Name,
				"arguments": string(argsJSON),
			})

		case CustomToolUseBlock:
			items = append(items, map[string]any{
				"type":    "custom_tool_call",
				"call_id": block.ID,
				"name":    block.Name,
				"input":   block.Input,
			})

		case ToolResultBlock:
			items = append(items, map[string]any{
				"type":    "function_call_output",
				"call_id": block.ToolUseID,
				"output":  block.ContentString(),
			})

		case CustomToolResultBlock:
			item := map[string]any{
				"type":    "custom_tool_call_output",
				"call_id": block.ToolUseID,
				"output":  block.OutputString(),
			}
			if block.rawOutput != nil {
				item["output"] = block.rawOutput
			}
			items = append(items, item)

		case ImageBlock:
			content := map[string]any{"type": "input_image"}
			if block.URL != "" {
				content["image_url"] = block.URL
			} else if block.Data != "" {
				content["image_data"] = block.Data
			}
			items = append(items, map[string]any{
				"type":    "message",
				"role":    interactionsRole(msg.Role),
				"content": []any{content},
			})

		case FileBlock:
			fileData := ""
			if block.Data != "" && block.MediaType != "" {
				fileData = "data:" + block.MediaType + ";base64," + block.Data
			}
			content := map[string]any{
				"type":      "input_file",
				"filename":  block.Filename,
				"file_data": fileData,
			}
			items = append(items, map[string]any{
				"type":    "message",
				"role":    interactionsRole(msg.Role),
				"content": []any{content},
			})

		case ThinkingBlock:
			target := FormatOpenAIResponse
			if codexClaudeDocuments {
				target = FormatCodex
			}
			block, keep := requestThinkingForTarget(req, target, msg.Role, block, signature.SignatureBlockKindGPTReasoning)
			if !keep {
				continue
			}
			if !block.Redacted && (block.Thinking != "" || block.hasSignatureField()) {
				reasoning := map[string]any{"type": "reasoning"}
				if block.Thinking != "" {
					reasoning["content"] = []any{
						map[string]any{"type": "summary_text", "text": block.Thinking},
					}
				}
				if block.hasSignatureField() {
					reasoning["encrypted_content"] = block.Signature
				}
				items = append(items, reasoning)
			}

		case AudioBlock:
			content := map[string]any{"type": "input_audio", "data": block.Data}
			if block.Format != "" {
				content["format"] = block.Format
			}
			items = append(items, map[string]any{
				"type":    "message",
				"role":    interactionsRole(msg.Role),
				"content": []any{content},
			})

		case RawBlock:
			items = append(items, block.RawData)
		}
	}

	return items
}

func messageHasClaudeDocumentOrigin(msg OagMessage) bool {
	for _, block := range msg.Content {
		switch b := block.(type) {
		case FileBlock:
			if b.claudeDocumentSource != nil {
				return true
			}
		case RawBlock:
			if b.claudeDocumentSource != nil {
				return true
			}
		}
	}
	return false
}

func (h *InteractionsHandler) serializeOneItemWithClaudeDocumentGrouping(req *UnifiedRequest, msg OagMessage) []any {
	var items []any
	var contentItems []any

	flushMessage := func() {
		if len(contentItems) == 0 {
			return
		}
		items = append(items, map[string]any{
			"type":    "message",
			"role":    interactionsRole(msg.Role),
			"content": contentItems,
		})
		contentItems = nil
	}

	for _, b := range msg.Content {
		switch block := b.(type) {
		case TextBlock:
			contentType := "input_text"
			if msg.Role == "assistant" {
				contentType = "output_text"
			}
			contentItems = append(contentItems, map[string]any{
				"type": contentType,
				"text": block.Text,
			})

		case FileBlock:
			if block.claudeDocumentSource == nil {
				flushMessage()
				items = append(items, h.serializeOneItem(OagMessage{Role: msg.Role, Content: []ContentBlock{block}}, false)...)
				continue
			}
			content, ok := codexClaudeDocumentInputFile(block)
			if ok {
				contentItems = append(contentItems, content)
			}

		case RawBlock:
			if block.claudeDocumentSource != nil {
				continue
			}
			flushMessage()
			items = append(items, h.serializeOneItem(OagMessage{Role: msg.Role, Content: []ContentBlock{block}}, false)...)

		default:
			flushMessage()
			items = append(items, h.serializeOneItemForRequest(req, OagMessage{Role: msg.Role, Content: []ContentBlock{block}}, true)...)
		}
	}
	flushMessage()

	return items
}

func codexClaudeDocumentInputFile(block FileBlock) (map[string]any, bool) {
	origin := block.claudeDocumentSource
	if origin == nil {
		return nil, false
	}
	if origin.sourceType != "base64" {
		return nil, false
	}
	mediaType := strings.TrimSpace(origin.mediaType)
	if !strings.EqualFold(mediaType, "application/pdf") {
		return nil, false
	}
	data := origin.data
	if data == "" {
		data = origin.base64
	}
	if data == "" {
		return nil, false
	}
	return map[string]any{
		"type":      "input_file",
		"filename":  "document.pdf",
		"file_data": "data:" + mediaType + ";base64," + data,
	}, true
}

// SerializeRequest converts a UnifiedRequest to Responses API JSON.
func (h *InteractionsHandler) SerializeRequest(req *UnifiedRequest) ([]byte, error) {
	return h.serializeRequest(req, true, false)
}

func (h *InteractionsHandler) serializeRequest(req *UnifiedRequest, systemAsInstructions bool, codexTarget bool) ([]byte, error) {
	out := map[string]any{
		"model": req.Model,
	}
	if req.Stream {
		out["stream"] = true
	}
	if !systemAsInstructions {
		out["instructions"] = ""
	}

	if req.Temperature != nil {
		out["temperature"] = *req.Temperature
	}
	if req.TopP != nil {
		out["top_p"] = *req.TopP
	}
	if req.MaxTokens != nil {
		out["max_output_tokens"] = *req.MaxTokens
	}
	if len(req.Tools) > 0 {
		normalized := make([]map[string]any, 0, len(req.Tools))
		for _, tool := range req.Tools {
			normalized = append(normalized, NormalizeToolToInteractions(tool))
		}
		out["tools"] = normalized
	}
	if req.ToolChoice != nil {
		out["tool_choice"] = NormalizeToolChoiceToInteractions(req.ToolChoice)
	}
	if req.Thinking != nil {
		ApplyInteractionsThinking(req.Thinking, out)
	} else if req.ReasoningEffort != "" {
		out["reasoning"] = map[string]any{"effort": req.ReasoningEffort}
	}

	// Instructions from system messages
	var instructions []string
	var inputItems []any

	for _, msg := range req.Messages {
		if msg.Role == "system" && systemAsInstructions {
			instructions = append(instructions, msg.GetText())
			continue
		}
		inputItems = append(inputItems, h.serializeOneItemForRequest(req, msg, codexTarget && req.SourceFormat == FormatAnthropic)...)
	}

	if len(instructions) > 0 {
		out["instructions"] = joinNonEmpty(instructions, "\n\n")
	}
	out["input"] = inputItems

	// Map response_format to Responses API text.format.
	if len(req.ResponseFormat) > 0 {
		flattenJSONSchema := req.SourceFormat != FormatOpenAI
		if format := responsesTextFormatFromOpenAIResponseFormat(req.ResponseFormat, flattenJSONSchema); len(format) > 0 {
			out["text"] = map[string]any{"format": format}
		}
	}

	return json.Marshal(out)
}

// ParseResponse parses a non-streaming Responses API response.
func (h *InteractionsHandler) ParseResponse(rawJSON []byte) (*UnifiedResponse, error) {
	if err := validateJSONObject(rawJSON); err != nil {
		return nil, err
	}

	root := gjson.ParseBytes(rawJSON)
	if nested := root.Get("response"); nested.Exists() && nested.IsObject() && strings.HasPrefix(root.Get("type").String(), "response.") {
		root = nested
	}
	resp := &UnifiedResponse{
		ID:    root.Get("id").String(),
		Model: root.Get("model").String(),
	}

	// Status → finish_reason mapping
	switch root.Get("status").String() {
	case "completed":
		resp.FinishReason = "stop"
	case "incomplete":
		reason := root.Get("incomplete_details.reason").String()
		switch reason {
		case "max_tokens", "max_output_tokens":
			resp.FinishReason = "length"
		case "content_filter":
			resp.FinishReason = "content_filter"
		default:
			resp.FinishReason = "stop"
		}
	case "failed":
		resp.FinishReason = "stop"
	}
	if resp.FinishReason == "" {
		resp.FinishReason = responsesFinishReason(root)
	}

	// Extract text from output array
	var textParts []string
	var orderedContent []ContentBlock
	var rawOutput []map[string]any
	hasRawWebSearchOutput := false
	webSearchState := &codexWebSearchState{noFallback: true}
	if output := root.Get("output"); output.Exists() && output.IsArray() {
		for _, item := range output.Array() {
			if item.Get("type").String() == "web_search_call" {
				hasRawWebSearchOutput = true
			}
			switch item.Get("type").String() {
			case "message":
				rawOutput = append(rawOutput, markedResponsesOutputItem([]byte(item.Raw)))
				for _, content := range item.Get("content").Array() {
					if content.Get("type").String() == "output_text" {
						text := content.Get("text").String()
						textParts = append(textParts, text)
						if text != "" {
							orderedContent = append(orderedContent, TextBlock{Text: text})
						}
					}
				}
			case "function_call", "custom_tool_call":
				var tcMap map[string]any
				if err := json.Unmarshal([]byte(item.Raw), &tcMap); err == nil {
					filtered := appendResponseToolCall(resp.ToolCalls, tcMap)
					if len(filtered) != len(resp.ToolCalls) {
						resp.ToolCalls = filtered
						rawOutput = append(rawOutput, tcMap)
						resp.FinishReason = "tool_calls"
						if block := responsesOutputToolUseBlock(item); block != nil {
							orderedContent = append(orderedContent, block)
						}
					}
				} else {
					rawOutput = append(rawOutput, markedResponsesOutputItem([]byte(item.Raw)))
				}
			case "image_generation_call":
				rawOutput = append(rawOutput, markedResponsesOutputItem([]byte(item.Raw)))
				// Extract generated image as base64 inline content
				b64 := item.Get("result").String()
				if b64 != "" {
					outputFormat := item.Get("output_format").String()
					if outputFormat == "" {
						outputFormat = "png"
					}
					mimeType := "image/" + outputFormat
					if outputFormat == "jpg" {
						mimeType = "image/jpeg"
					}
					imageURL := "data:" + mimeType + ";base64," + b64
					// Append as inline image content to the text output
					imageMarkdown := "\n![generated image](" + imageURL + ")"
					textParts = append(textParts, imageMarkdown)
					orderedContent = append(orderedContent, TextBlock{Text: imageMarkdown})
				}
			case "reasoning":
				rawOutput = append(rawOutput, markedResponsesOutputItem([]byte(item.Raw)))
				if summary := item.Get("summary.0.text"); summary.Exists() {
					resp.ThinkingContent = summary.String()
				} else if content := item.Get("content.0.text"); content.Exists() {
					resp.ThinkingContent = content.String()
				}
				if encrypted := item.Get("encrypted_content"); encrypted.Exists() {
					resp.ThinkingSignature = encrypted.String()
				}
				if resp.ThinkingContent != "" || resp.ThinkingSignature != "" {
					orderedContent = append(orderedContent, ThinkingBlock{
						Thinking:         resp.ThinkingContent,
						Signature:        resp.ThinkingSignature,
						signaturePresent: item.Get("encrypted_content").Exists(),
					})
				}
			case "web_search_call":
				rawOutput = append(rawOutput, markedResponsesOutputItem([]byte(item.Raw)))
				orderedContent = append(orderedContent, codexWebSearchBlocks(gjson.Result{}, item, webSearchState, false)...)
			default:
				rawOutput = append(rawOutput, markedResponsesOutputItem([]byte(item.Raw)))
			}
		}
	}
	resp.Content = strings.Join(textParts, "")
	if hasRawWebSearchOutput {
		resp.responseContent = orderedContent
		resp.responsesOutput = rawOutput
	}

	// Usage
	if usage := root.Get("usage"); usage.Exists() {
		resp.Usage = responsesUsage(usage, FormatOpenAIResponse)
	}

	normalizeResponseToolCallFinish(resp)
	return resp, nil
}

// FormatResponse formats a UnifiedResponse into Responses API response JSON.
func (h *InteractionsHandler) FormatResponse(resp *UnifiedResponse, model string) ([]byte, error) {
	m := model
	if m == "" {
		m = resp.Model
	}
	status, incompleteReason, incomplete := responsesStatusForFinishReason(resp.FinishReason)

	var output []any
	rawOutput, hasRawOutput := rawResponsesOutputItems(resp.ToolCalls)
	if len(resp.responsesOutput) > 0 {
		rawOutput, hasRawOutput = rawResponsesOutputItems(resp.responsesOutput)
	}
	if hasRawOutput {
		output = rawOutput
	} else if resp.ThinkingContent != "" || resp.ThinkingSignature != "" {
		reasoning := map[string]any{
			"type": "reasoning",
			"id":   resp.ID + "_reasoning",
		}
		if resp.ThinkingContent != "" {
			reasoning["summary"] = []any{map[string]any{"type": "summary_text", "text": resp.ThinkingContent}}
		}
		if resp.ThinkingSignature != "" {
			reasoning["encrypted_content"] = resp.ThinkingSignature
		}
		output = append(output, reasoning)
	}
	if !hasRawOutput {
		if resp.Content != "" || len(resp.ToolCalls) == 0 {
			output = append(output, map[string]any{
				"type":   "message",
				"role":   "assistant",
				"status": status,
				"content": []any{
					map[string]any{"type": "output_text", "text": resp.Content},
				},
			})
		}
		for _, call := range resp.ToolCalls {
			if isBlankResponseToolCall(call) {
				continue
			}
			if tool, ok := normalizeResponsesToolCallWithoutMarker(call); ok {
				ensureResponsesToolItemID(tool)
				tool["status"] = status
				output = append(output, tool)
			}
		}
	}
	applyResponsesOutputStatus(output, status)

	out := map[string]any{
		"id":     resp.ID,
		"object": "response",
		"model":  m,
		"output": output,
		"status": status,
	}
	if incomplete {
		out["incomplete_details"] = map[string]any{"reason": incompleteReason}
	}
	if resp.Usage != nil {
		usageMap := map[string]any{}
		if usageHasPrompt(resp.Usage) {
			usageMap["input_tokens"] = usagePromptForTarget(resp.Usage, FormatOpenAIResponse)
		}
		if usageHasCompletion(resp.Usage) {
			usageMap["output_tokens"] = resp.Usage.CompletionTokens
		}
		if total, ok := usageTotalForTarget(resp.Usage, FormatOpenAIResponse); ok {
			usageMap["total_tokens"] = total
		}
		inputDetails := map[string]any{}
		if cached, ok := responsesCachedTokensForUsage(resp.Usage); ok {
			inputDetails["cached_tokens"] = cached
		}
		if usageHasCacheCreation(resp.Usage) {
			inputDetails["cache_write_tokens"] = resp.Usage.CacheCreationInputTokens
		}
		if len(inputDetails) > 0 {
			usageMap["input_tokens_details"] = inputDetails
		}
		if usageHasReasoning(resp.Usage) {
			usageMap["output_tokens_details"] = map[string]any{"reasoning_tokens": resp.Usage.ReasoningTokens}
		}
		out["usage"] = usageMap
	}

	return json.Marshal(out)
}

// FormatError formats a UnifiedError into Responses API error JSON.
func (h *InteractionsHandler) FormatError(err *UnifiedError) ([]byte, error) {
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
func (h *InteractionsHandler) HasToolsDefined(rawJSON []byte) bool {
	tools := gjson.GetBytes(rawJSON, "tools")
	return tools.Exists() && tools.IsArray() && len(tools.Array()) > 0
}

// ----------------------------------------------------------------
// Helpers
// ----------------------------------------------------------------

func interactionsRole(role string) string {
	switch role {
	case "system":
		return "developer"
	default:
		return role
	}
}

func joinNonEmpty(parts []string, sep string) string {
	var nonEmpty []string
	for _, p := range parts {
		if p != "" {
			nonEmpty = append(nonEmpty, p)
		}
	}
	if len(nonEmpty) == 0 {
		return ""
	}
	result := nonEmpty[0]
	for _, p := range nonEmpty[1:] {
		result += sep + p
	}
	return result
}

func ensureResponsesToolItemID(tool map[string]any) {
	switch stringValue(tool["type"]) {
	case "function_call":
		if stringValue(tool["id"]) == "" {
			if callID := stringValue(tool["call_id"]); callID != "" {
				tool["id"] = "fc_" + callID
			}
		}
	case "custom_tool_call":
		if stringValue(tool["id"]) == "" {
			if callID := stringValue(tool["call_id"]); callID != "" {
				tool["id"] = "ctc_" + callID
			}
		}
	}
}

func responsesFinishReason(root gjson.Result) string {
	if reason := root.Get("stop_reason").String(); reason != "" {
		switch reason {
		case "stop", "completed", "end_turn", "tool_use", "tool_calls", "function_call":
			return "stop"
		case "max_tokens", "max_output_tokens":
			return "length"
		case "content_filter":
			return "content_filter"
		default:
			return "stop"
		}
	}
	if reason := root.Get("incomplete_details.reason").String(); reason != "" {
		switch reason {
		case "max_tokens", "max_output_tokens":
			return "length"
		case "content_filter":
			return "content_filter"
		default:
			return "stop"
		}
	}
	return "stop"
}

var _ ProtocolHandler = (*InteractionsHandler)(nil)
