package oagmsg

import (
	"encoding/json"
	"strings"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/signature"
	"github.com/tidwall/gjson"
)

// GoogleInteractionsHandler implements the Google Interactions wire protocol.
// It is intentionally separate from the Codex and OpenAI Responses handlers:
// Google uses system_instruction, steps, and interaction.*/step.* stream events.
type GoogleInteractionsHandler struct{}

func (h *GoogleInteractionsHandler) Format() Format { return FormatInteractions }

func (h *GoogleInteractionsHandler) ParseRequest(rawJSON []byte) (*UnifiedRequest, error) {
	if err := validateJSONObject(rawJSON); err != nil {
		return nil, err
	}
	root := gjson.ParseBytes(rawJSON)
	messages, err := h.ParseMessages(rawJSON)
	if err != nil {
		return nil, err
	}
	req := &UnifiedRequest{
		Model:        root.Get("model").String(),
		Messages:     messages,
		Stream:       root.Get("stream").Bool(),
		SourceFormat: FormatInteractions,
	}
	config := root.Get("generation_config")
	if value := firstExisting(config, "temperature"); value.Exists() {
		v := value.Float()
		req.Temperature = &v
	}
	if value := firstExisting(config, "top_p", "topP"); value.Exists() {
		v := value.Float()
		req.TopP = &v
	}
	if value := firstExisting(config, "max_output_tokens", "maxOutputTokens"); value.Exists() {
		v := int(value.Int())
		req.MaxTokens = &v
	}
	if value := firstExisting(config, "stop_sequences", "stopSequences"); value.IsArray() {
		for _, stop := range value.Array() {
			req.Stop = append(req.Stop, stop.String())
		}
	}
	if tools := root.Get("tools"); tools.IsArray() {
		for _, tool := range tools.Array() {
			var value map[string]any
			if json.Unmarshal([]byte(tool.Raw), &value) == nil {
				req.Tools = append(req.Tools, value)
			}
		}
	}
	toolChoice := root.Get("tool_choice")
	if !toolChoice.Exists() {
		toolChoice = firstExisting(config, "tool_choice", "toolChoice")
	}
	if toolChoice.Exists() {
		req.ToolChoice = decodeJSONResult(toolChoice)
	}
	if thinkingConfig := ExtractInteractionsThinking(root); thinkingConfig != nil {
		req.SetThinking(thinkingConfig)
	}
	return req, nil
}

func (h *GoogleInteractionsHandler) ParseMessages(rawJSON []byte) ([]OagMessage, error) {
	if err := validateJSONObject(rawJSON); err != nil {
		return nil, err
	}
	root := gjson.ParseBytes(rawJSON)
	var messages []OagMessage
	if instruction := googleInteractionInstruction(root.Get("system_instruction")); instruction != "" {
		messages = append(messages, SystemMsg(instruction))
	}
	input := root.Get("input")
	if !input.Exists() {
		return messages, nil
	}
	if input.Type == gjson.String {
		return append(messages, UserTextMsg(input.String())), nil
	}
	if input.IsArray() {
		for _, item := range input.Array() {
			messages = append(messages, parseGoogleInteractionItem(item, "user")...)
		}
		return messages, nil
	}
	return append(messages, parseGoogleInteractionItem(input, "user")...), nil
}

func (h *GoogleInteractionsHandler) SerializeMessages(messages []OagMessage) ([]byte, error) {
	return json.Marshal(serializeGoogleInteractionMessages(messages))
}

func (h *GoogleInteractionsHandler) SerializeRequest(req *UnifiedRequest) ([]byte, error) {
	out := map[string]any{
		"model": req.Model,
		"input": serializeGoogleInteractionMessagesForRequest(req, req.Messages),
	}
	if req.Stream {
		out["stream"] = true
	}
	var instructions []string
	for _, message := range req.Messages {
		if message.Role == "system" {
			instructions = append(instructions, message.GetText())
		}
	}
	if instruction := joinNonEmpty(instructions, "\n\n"); instruction != "" {
		out["system_instruction"] = instruction
	}
	generationConfig := make(map[string]any)
	if req.Temperature != nil {
		generationConfig["temperature"] = *req.Temperature
	}
	if req.TopP != nil {
		generationConfig["top_p"] = *req.TopP
	}
	if req.MaxTokens != nil {
		generationConfig["max_output_tokens"] = *req.MaxTokens
	}
	if len(req.Stop) > 0 {
		generationConfig["stop_sequences"] = req.Stop
	}
	if effort := ThinkingEffort(req.Thinking); effort != "" {
		generationConfig["thinking_level"] = effort
	}
	if req.ToolChoice != nil {
		generationConfig["tool_choice"] = req.ToolChoice
	}
	if len(generationConfig) > 0 {
		out["generation_config"] = generationConfig
	}
	if len(req.Tools) > 0 {
		normalized := make([]map[string]any, 0, len(req.Tools))
		for _, tool := range req.Tools {
			normalized = append(normalized, NormalizeToolToInteractions(tool))
		}
		out["tools"] = normalized
	}
	return json.Marshal(out)
}

func (h *GoogleInteractionsHandler) ParseResponse(rawJSON []byte) (*UnifiedResponse, error) {
	if err := validateJSONObject(rawJSON); err != nil {
		return nil, err
	}
	root := gjson.ParseBytes(rawJSON)
	interaction := root
	if nested := root.Get("interaction"); nested.Exists() {
		interaction = nested
	}
	resp := &UnifiedResponse{
		ID:           interaction.Get("id").String(),
		Model:        interaction.Get("model").String(),
		FinishReason: googleInteractionFinishReason(interaction),
		Created:      parseGoogleInteractionTime(interaction.Get("created")),
	}
	var text []string
	for _, step := range interaction.Get("steps").Array() {
		switch step.Get("type").String() {
		case "thought":
			resp.ThinkingContent += googleInteractionContentText(step.Get("content"))
			resp.ThinkingSignature = firstExisting(step, "signature", "thought_signature", "thoughtSignature").String()
		case "function_call":
			call := map[string]any{
				"type":      "function_call",
				"call_id":   firstExisting(step, "call_id", "id").String(),
				"name":      step.Get("name").String(),
				"arguments": decodeJSONResult(step.Get("arguments")),
			}
			resp.ToolCalls = append(resp.ToolCalls, call)
		case "model_output", "":
			text = append(text, googleInteractionContentText(step.Get("content")))
		}
	}
	resp.Content = strings.Join(text, "")
	if usage := interaction.Get("usage"); usage.Exists() {
		resp.Usage = googleInteractionUsage(usage)
	}
	return resp, nil
}

func (h *GoogleInteractionsHandler) FormatResponse(resp *UnifiedResponse, model string) ([]byte, error) {
	if model == "" {
		model = resp.Model
	}
	steps := make([]any, 0, 2+len(resp.ToolCalls))
	if resp.ThinkingContent != "" || resp.ThinkingSignature != "" {
		step := map[string]any{"type": "thought", "content": []any{map[string]any{"type": "text", "text": resp.ThinkingContent}}}
		if resp.ThinkingSignature != "" {
			step["signature"] = resp.ThinkingSignature
		}
		steps = append(steps, step)
	}
	if resp.Content != "" || len(resp.ToolCalls) == 0 {
		steps = append(steps, map[string]any{
			"type":    "model_output",
			"content": []any{map[string]any{"type": "text", "text": resp.Content}},
		})
	}
	for _, call := range resp.ToolCalls {
		steps = append(steps, googleInteractionToolCall(call))
	}
	out := map[string]any{
		"id":     resp.ID,
		"object": "interaction",
		"status": "completed",
		"model":  model,
		"steps":  steps,
	}
	if resp.Usage != nil {
		usageMap := map[string]any{}
		if usageHasPrompt(resp.Usage) {
			usageMap["input_tokens"] = resp.Usage.PromptTokens
		}
		if usageHasCompletion(resp.Usage) {
			usageMap["output_tokens"] = resp.Usage.CompletionTokens
		}
		if total, ok := usageTotalForTarget(resp.Usage, FormatInteractions); ok {
			usageMap["total_tokens"] = total
		}
		if cached, ok := usageCachedForTarget(resp.Usage, FormatInteractions); ok {
			usageMap["cached_tokens"] = cached
			usageMap["total_cached_tokens"] = cached
		}
		if usageHasReasoning(resp.Usage) {
			usageMap["reasoning_tokens"] = resp.Usage.ReasoningTokens
			usageMap["total_thought_tokens"] = resp.Usage.ReasoningTokens
		}
		out["usage"] = usageMap
	}
	return json.Marshal(out)
}

func (h *GoogleInteractionsHandler) FormatError(err *UnifiedError) ([]byte, error) {
	return json.Marshal(map[string]any{
		"status": "failed",
		"error": map[string]any{
			"message": err.Message,
			"type":    err.ErrorType,
			"code":    err.StatusCode,
		},
	})
}

func (h *GoogleInteractionsHandler) HasToolsDefined(rawJSON []byte) bool {
	tools := gjson.GetBytes(rawJSON, "tools")
	return tools.IsArray() && len(tools.Array()) > 0
}

func parseGoogleInteractionItem(item gjson.Result, defaultRole string) []OagMessage {
	if item.Type == gjson.String {
		return []OagMessage{{Role: defaultRole, Content: []ContentBlock{TextBlock{Text: item.String()}}}}
	}
	if steps := item.Get("steps"); steps.IsArray() {
		role := googleInteractionRole(item.Get("role").String(), defaultRole)
		var messages []OagMessage
		for _, step := range steps.Array() {
			messages = append(messages, parseGoogleInteractionItem(step, role)...)
		}
		return messages
	}
	switch item.Get("type").String() {
	case "function_call":
		return []OagMessage{{Role: "assistant", Content: []ContentBlock{ToolUseBlock{
			ID: item.Get("call_id").String(), Name: item.Get("name").String(), Input: googleInteractionArguments(item.Get("arguments")),
		}}}}
	case "function_result":
		return []OagMessage{{Role: "user", Content: []ContentBlock{ToolResultBlock{
			ToolUseID: item.Get("call_id").String(), Content: decodeJSONResult(item.Get("result")), IsError: item.Get("is_error").Bool(),
		}}}}
	case "thought":
		sig := firstExisting(item, "signature", "thought_signature", "thoughtSignature")
		return []OagMessage{{Role: "assistant", Content: []ContentBlock{ThinkingBlock{
			Thinking:         googleInteractionContentText(item.Get("content")),
			Signature:        sig.String(),
			signaturePresent: sig.Exists(),
		}}}}
	case "model_output":
		return []OagMessage{{Role: "assistant", Content: parseGoogleInteractionContent(item.Get("content"))}}
	default:
		role := googleInteractionRole(item.Get("role").String(), defaultRole)
		content := item.Get("content")
		if !content.Exists() && item.Get("text").Exists() {
			content = item
		}
		return []OagMessage{{Role: role, Content: parseGoogleInteractionContent(content)}}
	}
}

func parseGoogleInteractionContent(content gjson.Result) []ContentBlock {
	if !content.Exists() {
		return nil
	}
	if content.Type == gjson.String {
		return []ContentBlock{TextBlock{Text: content.String()}}
	}
	parts := content.Array()
	if content.IsObject() {
		parts = []gjson.Result{content}
	}
	blocks := make([]ContentBlock, 0, len(parts))
	for _, part := range parts {
		switch part.Get("type").String() {
		case "", "text":
			if text := part.Get("text"); text.Exists() {
				blocks = append(blocks, TextBlock{Text: text.String()})
			} else {
				blocks = append(blocks, rawBlock(part))
			}
		case "image":
			blocks = append(blocks, ImageBlock{MediaType: googleInteractionMediaType(part), Data: part.Get("data").String(), URL: part.Get("url").String()})
		case "audio":
			blocks = append(blocks, AudioBlock{Data: part.Get("data").String(), Format: mediaSubtype(googleInteractionMediaType(part))})
		case "video", "document":
			blocks = append(blocks, FileBlock{MediaType: googleInteractionMediaType(part), Data: part.Get("data").String(), URL: firstExisting(part, "file_uri", "fileUri", "url").String()})
		default:
			blocks = append(blocks, rawBlock(part))
		}
	}
	return blocks
}

func serializeGoogleInteractionMessages(messages []OagMessage) []any {
	return serializeGoogleInteractionMessagesForRequest(nil, messages)
}

func serializeGoogleInteractionMessagesForRequest(req *UnifiedRequest, messages []OagMessage) []any {
	var steps []any
	for _, message := range messages {
		if message.Role == "system" {
			continue
		}
		stepType := "user_input"
		if message.Role == "assistant" {
			stepType = "model_output"
		}
		var content []any
		flushContent := func() {
			if len(content) > 0 {
				steps = append(steps, map[string]any{"type": stepType, "content": content})
				content = nil
			}
		}
		for _, block := range message.Content {
			switch value := block.(type) {
			case TextBlock:
				content = append(content, map[string]any{"type": "text", "text": value.Text})
			case ImageBlock:
				content = append(content, googleInteractionMedia("image", value.MediaType, value.Data, value.URL))
			case AudioBlock:
				content = append(content, googleInteractionMedia("audio", audioMediaType(value.Format), value.Data, ""))
			case FileBlock:
				content = append(content, googleInteractionMedia("document", value.MediaType, value.Data, value.URL))
			case ThinkingBlock:
				value, keep := requestThinkingForTarget(req, FormatInteractions, message.Role, value, signature.SignatureBlockKindGeminiModelPart)
				if !keep {
					continue
				}
				flushContent()
				step := map[string]any{"type": "thought", "content": []any{map[string]any{"type": "text", "text": value.Thinking}}}
				if value.Signature != "" {
					step["signature"] = value.Signature
				}
				steps = append(steps, step)
			case ToolUseBlock:
				flushContent()
				step := map[string]any{"type": "function_call", "name": value.Name, "call_id": value.ID, "arguments": value.Input}
				if value.Signature != "" {
					step["signature"] = value.Signature
				}
				steps = append(steps, step)
			case ToolResultBlock:
				flushContent()
				steps = append(steps, map[string]any{"type": "function_result", "call_id": value.ToolUseID, "result": value.Content})
			case RawBlock:
				content = append(content, value.RawData)
			}
		}
		flushContent()
	}
	return steps
}

func googleInteractionInstruction(value gjson.Result) string {
	if value.Type == gjson.String {
		return value.String()
	}
	if text := value.Get("text"); text.Exists() {
		return text.String()
	}
	var parts []string
	for _, part := range value.Get("parts").Array() {
		if text := part.Get("text").String(); text != "" {
			parts = append(parts, text)
		}
	}
	return strings.Join(parts, "\n")
}

func googleInteractionContentText(content gjson.Result) string {
	if content.Type == gjson.String {
		return content.String()
	}
	var parts []string
	if content.IsObject() {
		content = gjson.Parse("[" + content.Raw + "]")
	}
	for _, part := range content.Array() {
		if text := part.Get("text").String(); text != "" {
			parts = append(parts, text)
		}
	}
	return strings.Join(parts, "")
}

func googleInteractionUsage(usage gjson.Result) *UnifiedUsage {
	return googleInteractionUsageFromResult(usage)
}

func googleInteractionToolCall(call map[string]any) map[string]any {
	name, _ := call["name"].(string)
	callID, _ := call["call_id"].(string)
	if callID == "" {
		callID, _ = call["id"].(string)
	}
	arguments := call["arguments"]
	if function, ok := call["function"].(map[string]any); ok {
		if value, ok := function["name"].(string); ok {
			name = value
		}
		arguments = function["arguments"]
	}
	if value, ok := arguments.(string); ok {
		var decoded any
		if json.Unmarshal([]byte(value), &decoded) == nil {
			arguments = decoded
		}
	}
	return map[string]any{"type": "function_call", "name": name, "call_id": callID, "arguments": arguments}
}

func googleInteractionArguments(value gjson.Result) map[string]any {
	var arguments map[string]any
	if value.Type == gjson.String {
		_ = json.Unmarshal([]byte(value.String()), &arguments)
	} else if value.Exists() {
		_ = json.Unmarshal([]byte(value.Raw), &arguments)
	}
	if arguments == nil {
		arguments = map[string]any{}
	}
	return arguments
}

func decodeJSONResult(value gjson.Result) any {
	if !value.Exists() {
		return nil
	}
	if value.Type == gjson.String {
		return value.String()
	}
	var decoded any
	if json.Unmarshal([]byte(value.Raw), &decoded) == nil {
		return decoded
	}
	return value.String()
}

func firstExisting(root gjson.Result, paths ...string) gjson.Result {
	for _, path := range paths {
		if value := root.Get(path); value.Exists() {
			return value
		}
	}
	return gjson.Result{}
}

func googleInteractionRole(role, fallback string) string {
	switch role {
	case "model", "assistant":
		return "assistant"
	case "system", "developer":
		return "system"
	case "user":
		return "user"
	default:
		return fallback
	}
}

func googleInteractionFinishReason(interaction gjson.Result) string {
	if interaction.Get("status").String() == "incomplete" {
		switch interaction.Get("incomplete_details.reason").String() {
		case "max_tokens", "max_output_tokens":
			return "length"
		case "content_filter":
			return "content_filter"
		}
	}
	return "stop"
}

func parseGoogleInteractionTime(value gjson.Result) int64 {
	if value.Type == gjson.Number {
		return value.Int()
	}
	parsed, err := time.Parse(time.RFC3339, value.String())
	if err != nil {
		return 0
	}
	return parsed.Unix()
}

func googleInteractionMediaType(part gjson.Result) string {
	return firstExisting(part, "mime_type", "mimeType").String()
}

func mediaSubtype(mediaType string) string {
	if index := strings.IndexByte(mediaType, '/'); index >= 0 {
		return mediaType[index+1:]
	}
	return mediaType
}

func audioMediaType(format string) string {
	switch strings.ToLower(format) {
	case "wav":
		return "audio/wav"
	case "mp3":
		return "audio/mpeg"
	case "flac":
		return "audio/flac"
	case "opus":
		return "audio/opus"
	default:
		return "audio/" + format
	}
}

func googleInteractionMedia(contentType, mediaType, data, uri string) map[string]any {
	out := map[string]any{"type": contentType}
	if mediaType != "" {
		out["mime_type"] = mediaType
	}
	if data != "" {
		out["data"] = data
	}
	if uri != "" {
		out["file_uri"] = uri
	}
	return out
}

var _ ProtocolHandler = (*GoogleInteractionsHandler)(nil)
