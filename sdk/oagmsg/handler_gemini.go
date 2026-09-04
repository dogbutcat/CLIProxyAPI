package oagmsg

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/signature"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/util"
	"github.com/tidwall/gjson"
)

// GeminiHandler implements ProtocolHandler for the Google Gemini generateContent format.
// Aligned with oag_server handlers/gemini.py GeminiHandler.
type GeminiHandler struct{}

func (h *GeminiHandler) Format() Format { return FormatGemini }

type geminiRequestParseOptions struct {
	dropHiddenThoughtParts bool
}

// ParseRequest parses Gemini generateContent JSON into a UnifiedRequest.
func (h *GeminiHandler) ParseRequest(rawJSON []byte) (*UnifiedRequest, error) {
	if err := validateJSONObject(rawJSON); err != nil {
		return nil, err
	}

	root := util.ParseGJSONBytesNoCopy(rawJSON)
	return h.parseRequestFromRoot(root), nil
}

func (h *GeminiHandler) parseRequestFromRoot(root gjson.Result) *UnifiedRequest {
	return h.parseRequestFromRootWithOptions(root, geminiRequestParseOptions{dropHiddenThoughtParts: true})
}

func (h *GeminiHandler) parseRequestFromRootWithOptions(root gjson.Result, options geminiRequestParseOptions) *UnifiedRequest {
	req := &UnifiedRequest{
		Model:        root.Get("model").String(),
		Messages:     h.parseMessagesFromRootWithOptions(root, options),
		SourceFormat: FormatGemini,
	}

	// Parse generation config
	genConfig := root.Get("generationConfig")
	if genConfig.Exists() {
		if v := genConfig.Get("temperature"); v.Exists() {
			t := v.Float()
			req.Temperature = &t
		}
		if v := genConfig.Get("topP"); v.Exists() {
			t := v.Float()
			req.TopP = &t
		}
		if v := genConfig.Get("maxOutputTokens"); v.Exists() {
			t := int(v.Int())
			req.MaxTokens = &t
		}
		if v := genConfig.Get("stopSequences"); v.Exists() && v.IsArray() {
			for _, s := range v.Array() {
				req.Stop = append(req.Stop, s.String())
			}
		}
	}

	if config := ExtractGeminiThinking(root); config != nil {
		req.SetThinking(config)
	}

	// Parse tools
	if v := root.Get("tools"); v.Exists() && v.IsArray() {
		for _, t := range v.Array() {
			var m map[string]any
			if err := json.Unmarshal([]byte(t.Raw), &m); err == nil {
				req.Tools = append(req.Tools, m)
			}
		}
	}

	return req
}

// ParseMessages extracts messages from Gemini generateContent JSON.
func (h *GeminiHandler) ParseMessages(rawJSON []byte) ([]OagMessage, error) {
	if err := validateJSONObject(rawJSON); err != nil {
		return nil, err
	}

	root := util.ParseGJSONBytesNoCopy(rawJSON)
	return h.parseMessagesFromRoot(root), nil
}

func (h *GeminiHandler) parseMessagesFromRoot(root gjson.Result) []OagMessage {
	return h.parseMessagesFromRootWithOptions(root, geminiRequestParseOptions{dropHiddenThoughtParts: true})
}

func (h *GeminiHandler) parseMessagesFromRootWithOptions(root gjson.Result, options geminiRequestParseOptions) []OagMessage {
	var msgs []OagMessage
	toolIDs := newGeminiRequestToolIDState()

	// Parse systemInstruction
	if sys := root.Get("systemInstruction"); sys.Exists() {
		var blocks []ContentBlock
		for _, part := range sys.Get("parts").Array() {
			if options.dropHiddenThoughtParts && isGeminiHiddenThoughtPart(part) {
				continue
			}
			if text := part.Get("text"); text.Exists() {
				blocks = append(blocks, TextBlock{Text: text.String()})
			}
		}
		if len(blocks) > 0 {
			msgs = append(msgs, OagMessage{Role: "system", Content: blocks})
		}
	}

	// Parse contents array
	previousGeminiRole := ""
	for _, content := range root.Get("contents").Array() {
		role := normalizeGeminiRequestContentRole(content, previousGeminiRole)
		previousGeminiRole = role
		oagRole := geminiRoleToOag(role)

		var blocks []ContentBlock
		for _, part := range content.Get("parts").Array() {
			parsed := h.parsePartWithToolIDState(part, options, toolIDs)
			if parsed != nil {
				blocks = append(blocks, parsed)
			}
		}

		if len(blocks) > 0 {
			msgs = append(msgs, OagMessage{Role: oagRole, Content: blocks})
		}
	}

	return msgs
}

// parsePart parses a single Gemini part.
func (h *GeminiHandler) parsePart(part gjson.Result, options geminiRequestParseOptions) ContentBlock {
	return h.parsePartWithToolIDState(part, options, nil)
}

func (h *GeminiHandler) parsePartWithToolIDState(part gjson.Result, options geminiRequestParseOptions, toolIDs *geminiRequestToolIDState) ContentBlock {
	if options.dropHiddenThoughtParts && isGeminiHiddenThoughtPart(part) {
		return nil
	}
	if text := part.Get("text"); text.Exists() {
		if isGeminiHiddenThoughtPart(part) {
			return rawBlock(part)
		}
		return TextBlock{Text: text.String()}
	}

	// InlineData (image/audio/file by MIME type)
	if inline := part.Get("inlineData"); inline.Exists() {
		mime := inline.Get("mimeType").String()
		data := inline.Get("data").String()

		if strings.HasPrefix(mime, "image/") {
			return ImageBlock{MediaType: mime, Data: data}
		}
		return FileBlock{MediaType: mime, Data: data}
	}

	// functionCall -> ToolUseBlock
	if fc := part.Get("functionCall"); fc.Exists() {
		var args map[string]any
		if argsRaw := fc.Get("args"); argsRaw.Exists() {
			if err := json.Unmarshal([]byte(argsRaw.Raw), &args); err != nil {
				args = map[string]any{}
			}
		} else {
			args = map[string]any{}
		}
		id := firstExisting(fc, "id", "call_id", "callId").String()
		if toolIDs != nil {
			id = toolIDs.acceptFunctionCall(fc)
		}
		return ToolUseBlock{
			ID:        id,
			Name:      fc.Get("name").String(),
			Input:     args,
			Signature: firstExisting(part, "thoughtSignature", "thought_signature").String(),
		}
	}

	// functionResponse -> ToolResultBlock
	if fr := part.Get("functionResponse"); fr.Exists() {
		content := ""
		if resp := fr.Get("response"); resp.Exists() {
			content = resp.Raw
		}
		id := firstExisting(fr, "id", "call_id", "callId").String()
		if toolIDs != nil {
			id = toolIDs.acceptFunctionResponse(fr)
		}
		return ToolResultBlock{
			ToolUseID: id,
			Content:   content,
		}
	}

	return nil
}

type geminiRequestToolIDState struct {
	next   int
	byName map[string][]string
}

func newGeminiRequestToolIDState() *geminiRequestToolIDState {
	return &geminiRequestToolIDState{byName: make(map[string][]string)}
}

func (s *geminiRequestToolIDState) acceptFunctionCall(fc gjson.Result) string {
	name := fc.Get("name").String()
	id := firstExisting(fc, "id", "call_id", "callId").String()
	if id == "" {
		id = s.nextGeneratedID(name)
	}
	s.byName[name] = append(s.byName[name], id)
	return id
}

func (s *geminiRequestToolIDState) acceptFunctionResponse(fr gjson.Result) string {
	name := fr.Get("name").String()
	if id := firstExisting(fr, "id", "call_id", "callId").String(); id != "" {
		s.removeQueued(name, id)
		return id
	}
	if id, ok := s.popQueued(name); ok {
		return id
	}
	return s.nextGeneratedID(name)
}

func (s *geminiRequestToolIDState) nextGeneratedID(name string) string {
	s.next++
	safeName := strings.Map(func(r rune) rune {
		if r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '_' || r == '-' {
			return r
		}
		return '_'
	}, name)
	if safeName == "" {
		safeName = "tool"
	}
	return fmt.Sprintf("call_%s_%d", safeName, s.next)
}

func (s *geminiRequestToolIDState) popQueued(name string) (string, bool) {
	queue := s.byName[name]
	if len(queue) == 0 {
		return "", false
	}
	id := queue[0]
	s.byName[name] = queue[1:]
	return id, true
}

func (s *geminiRequestToolIDState) removeQueued(name, id string) {
	queue := s.byName[name]
	for index, queued := range queue {
		if queued == id {
			s.byName[name] = append(queue[:index], queue[index+1:]...)
			return
		}
	}
}

func isGeminiHiddenThoughtPart(part gjson.Result) bool {
	return part.Get("thought").Bool()
}

// SerializeMessages converts OagMessages to Gemini contents JSON.
// System messages are NOT included - use SerializeRequest for full payload.
func (h *GeminiHandler) SerializeMessages(msgs []OagMessage) ([]byte, error) {
	var contents []any
	for _, msg := range msgs {
		if msg.Role == "system" {
			continue
		}
		contents = append(contents, h.serializeOneContent(msg))
	}
	contents = normalizeGeminiRequestContents(contents)
	return json.Marshal(contents)
}

// serializeOneContent serializes a single OagMessage to Gemini content format.
func (h *GeminiHandler) serializeOneContent(msg OagMessage) map[string]any {
	return h.serializeOneContentWithToolNames(msg, nil)
}

func (h *GeminiHandler) serializeOneContentWithToolNames(msg OagMessage, toolNames map[string]string) map[string]any {
	return h.serializeOneContentForRequest(nil, msg, toolNames)
}

func (h *GeminiHandler) serializeOneContentForRequest(req *UnifiedRequest, msg OagMessage, toolNames map[string]string) map[string]any {
	role := oagRoleToGemini(msg.Role)
	var parts []any

	for _, b := range msg.Content {
		switch block := b.(type) {
		case TextBlock:
			parts = append(parts, map[string]any{"text": block.Text})
		case ImageBlock:
			if block.Data != "" {
				parts = append(parts, map[string]any{
					"inlineData": map[string]any{
						"mimeType": block.MediaType,
						"data":     block.Data,
					},
				})
			}
		case FileBlock:
			if block.Data != "" {
				parts = append(parts, map[string]any{
					"inlineData": map[string]any{
						"mimeType": block.MediaType,
						"data":     block.Data,
					},
				})
			}
		case ToolUseBlock:
			fc := map[string]any{
				"name": block.Name,
				"args": block.Input,
			}
			if block.ID != "" {
				fc["id"] = block.ID
			}
			part := map[string]any{"functionCall": fc}
			if block.Signature != "" {
				part["thoughtSignature"] = block.Signature
			}
			parts = append(parts, part)
		case ToolResultBlock:
			result, mediaParts := geminiFunctionResponsePayload(block.Content)
			resp := map[string]any{"result": result}
			name := toolNames[block.ToolUseID]
			if name == "" {
				name = block.ToolUseID
			}
			fr := map[string]any{
				"id":       block.ToolUseID,
				"name":     name,
				"response": resp,
			}
			if len(mediaParts) > 0 {
				fr["parts"] = mediaParts
			}
			parts = append(parts, map[string]any{"functionResponse": fr})
		case ThinkingBlock:
			block, keep := requestThinkingForTarget(req, FormatGemini, msg.Role, block, signature.SignatureBlockKindGeminiModelPart)
			if !keep {
				continue
			}
			if !block.Redacted && (block.Thinking != "" || block.hasSignatureField()) {
				part := map[string]any{"text": block.Thinking, "thought": true}
				if block.hasSignatureField() {
					part["thoughtSignature"] = block.Signature
				}
				parts = append(parts, part)
			}
		case AudioBlock:
			mimeType := "audio/" + block.Format
			if block.Format == "" {
				mimeType = "audio/wav"
			}
			parts = append(parts, map[string]any{
				"inlineData": map[string]any{
					"mimeType": mimeType,
					"data":     block.Data,
				},
			})
		case RawBlock:
			parts = append(parts, block.RawData)
		}
	}

	return map[string]any{"role": role, "parts": parts}
}

func geminiFunctionResponseResult(content any) any {
	items, ok := content.([]any)
	if !ok || len(items) != 1 {
		return content
	}
	return items[0]
}

func geminiFunctionResponsePayload(content any) (any, []any) {
	items, ok := content.([]any)
	if !ok {
		return geminiFunctionResponseResult(content), nil
	}
	resultItems := make([]any, 0, len(items))
	mediaParts := make([]any, 0)
	for _, item := range items {
		mediaPart, mediaOK := geminiFunctionResponseMediaPart(item)
		if mediaOK {
			mediaParts = append(mediaParts, mediaPart)
			continue
		}
		resultItems = append(resultItems, item)
	}
	if len(mediaParts) == 0 {
		return geminiFunctionResponseResult(content), nil
	}
	if len(resultItems) == 0 {
		return "", mediaParts
	}
	if text, okText := geminiFunctionResponseTextResult(resultItems); okText {
		return text, mediaParts
	}
	return geminiFunctionResponseResult(resultItems), mediaParts
}

func geminiFunctionResponseTextResult(items []any) (string, bool) {
	if len(items) != 1 {
		return "", false
	}
	item, ok := items[0].(map[string]any)
	if !ok {
		return "", false
	}
	switch strings.TrimSpace(stringValue(item["type"])) {
	case "input_text", "output_text", "text":
		text := stringValue(item["text"])
		return text, text != ""
	default:
		return "", false
	}
}

func geminiFunctionResponseMediaPart(item any) (map[string]any, bool) {
	itemMap, ok := item.(map[string]any)
	if !ok {
		return nil, false
	}
	if inlineData, okInline := geminiInlineDataFromValue(itemMap["inlineData"]); okInline {
		return inlineData, true
	}
	if inlineData, okInline := geminiInlineDataFromValue(itemMap["inline_data"]); okInline {
		return inlineData, true
	}
	switch strings.TrimSpace(stringValue(itemMap["type"])) {
	case "input_image", "output_image", "image":
	default:
		return nil, false
	}
	imageURL := stringValue(itemMap["image_url"])
	if imageURL == "" {
		if nested, okNested := itemMap["image_url"].(map[string]any); okNested {
			imageURL = stringValue(nested["url"])
		}
	}
	if imageURL == "" {
		imageURL = stringValue(itemMap["url"])
	}
	if strings.HasPrefix(imageURL, "data:") {
		mediaType, data, okData := splitBase64DataURL(imageURL)
		if !okData {
			return nil, false
		}
		return geminiInlineDataPart(mediaType, data), true
	}
	if data := stringValue(itemMap["image_data"]); data != "" {
		return geminiInlineDataPart(geminiImageMediaType(itemMap), data), true
	}
	return nil, false
}

func geminiInlineDataFromValue(value any) (map[string]any, bool) {
	inlineMap, ok := value.(map[string]any)
	if !ok {
		return nil, false
	}
	data := stringValue(inlineMap["data"])
	if data == "" {
		return nil, false
	}
	mediaType := firstNonEmptyString(
		stringValue(inlineMap["mimeType"]),
		stringValue(inlineMap["mime_type"]),
		stringValue(inlineMap["media_type"]),
	)
	return geminiInlineDataPart(mediaType, data), true
}

func geminiImageMediaType(item map[string]any) string {
	return firstNonEmptyString(
		stringValue(item["mimeType"]),
		stringValue(item["mime_type"]),
		stringValue(item["media_type"]),
		"image/png",
	)
}

func geminiInlineDataPart(mediaType, data string) map[string]any {
	if mediaType == "" {
		mediaType = "image/png"
	}
	return map[string]any{
		"inlineData": map[string]any{
			"mimeType": mediaType,
			"data":     data,
		},
	}
}

func ensureGeminiLeadingUserContentItems(contents []any) []any {
	if len(contents) == 0 {
		return contents
	}
	first, ok := contents[0].(map[string]any)
	if !ok || stringValue(first["role"]) != "model" {
		return contents
	}
	normalized := make([]any, 0, len(contents)+1)
	normalized = append(normalized, map[string]any{
		"role":  "user",
		"parts": []any{map[string]any{"text": ""}},
	})
	normalized = append(normalized, contents...)
	return normalized
}

func normalizeGeminiRequestContents(contents []any) []any {
	if len(contents) <= 1 {
		return contents
	}
	normalized := make([]any, 0, len(contents))
	for _, content := range contents {
		if isEmptyGeminiSerializedContent(content) {
			continue
		}
		if len(normalized) == 0 {
			normalized = append(normalized, content)
			continue
		}
		lastIndex := len(normalized) - 1
		if shouldMergeGeminiRequestContentTurns(normalized[lastIndex], content) {
			normalized[lastIndex] = appendGeminiContentParts(normalized[lastIndex], content)
			continue
		}
		normalized = append(normalized, content)
	}
	return normalized
}

func isEmptyGeminiSerializedContent(content any) bool {
	contentMap, ok := content.(map[string]any)
	if !ok {
		return false
	}
	parts, ok := contentMap["parts"].([]any)
	return !ok || len(parts) == 0
}

func shouldMergeGeminiRequestContentTurns(previous, current any) bool {
	previousContent, previousParts, ok := geminiSerializedContentParts(previous)
	if !ok {
		return false
	}
	currentContent, currentParts, ok := geminiSerializedContentParts(current)
	if !ok {
		return false
	}
	previousRole := strings.TrimSpace(strings.ToLower(stringValue(previousContent["role"])))
	currentRole := strings.TrimSpace(strings.ToLower(stringValue(currentContent["role"])))
	if previousRole == "user" && currentRole == "user" {
		previousHasFunctionResponse := geminiContentPartsContainFunctionResponse(previousParts)
		currentHasFunctionResponse := geminiContentPartsContainFunctionResponse(currentParts)
		if previousHasFunctionResponse || currentHasFunctionResponse {
			return geminiContentPartsAreOnlyFunctionResponses(previousParts) && geminiContentPartsAreOnlyFunctionResponses(currentParts)
		}
		return true
	}
	if previousRole == "model" && currentRole == "model" {
		return geminiContentPartsAreOnlyFunctionCalls(previousParts) && geminiContentPartsAreOnlyFunctionCalls(currentParts)
	}
	return false
}

func appendGeminiContentParts(previous, current any) any {
	previousContent, previousParts, previousOK := geminiSerializedContentParts(previous)
	_, currentParts, currentOK := geminiSerializedContentParts(current)
	if !previousOK || !currentOK {
		return previous
	}
	combined := make([]any, 0, len(previousParts)+len(currentParts))
	combined = append(combined, previousParts...)
	combined = append(combined, currentParts...)
	updated := make(map[string]any, len(previousContent))
	for key, value := range previousContent {
		updated[key] = value
	}
	updated["parts"] = combined
	return updated
}

func geminiSerializedContentParts(content any) (map[string]any, []any, bool) {
	contentMap, ok := content.(map[string]any)
	if !ok {
		return nil, nil, false
	}
	parts, ok := contentMap["parts"].([]any)
	if !ok || len(parts) == 0 {
		return nil, nil, false
	}
	return contentMap, parts, true
}

func geminiContentPartsContainFunctionResponse(parts []any) bool {
	for _, part := range parts {
		partMap, ok := part.(map[string]any)
		if !ok {
			continue
		}
		if _, ok := partMap["functionResponse"]; ok {
			return true
		}
		if _, ok := partMap["function_response"]; ok {
			return true
		}
	}
	return false
}

func geminiContentPartsAreOnlyFunctionResponses(parts []any) bool {
	if len(parts) == 0 {
		return false
	}
	for _, part := range parts {
		partMap, ok := part.(map[string]any)
		if !ok {
			return false
		}
		if _, ok := partMap["functionResponse"]; ok {
			continue
		}
		if _, ok := partMap["function_response"]; ok {
			continue
		}
		return false
	}
	return true
}

func geminiContentPartsAreOnlyFunctionCalls(parts []any) bool {
	if len(parts) == 0 {
		return false
	}
	for _, part := range parts {
		partMap, ok := part.(map[string]any)
		if !ok {
			return false
		}
		if _, ok := partMap["functionCall"]; !ok {
			return false
		}
		for key := range partMap {
			switch key {
			case "functionCall", "thoughtSignature":
			default:
				return false
			}
		}
	}
	return true
}

// SerializeRequest converts a UnifiedRequest to Gemini generateContent JSON.
func (h *GeminiHandler) SerializeRequest(req *UnifiedRequest) ([]byte, error) {
	out := map[string]any{}

	if req.Model != "" {
		out["model"] = req.Model
	}

	// SystemInstruction
	var systemParts []any
	var contents []any
	toolNames := make(map[string]string)
	for _, msg := range req.Messages {
		for _, use := range msg.GetToolUses() {
			if use.ID != "" && use.Name != "" {
				toolNames[use.ID] = use.Name
			}
		}
	}

	hasEncounteredConversation := false
	pendingGeminiToolCall := false
	var pendingSystemDemotions []OagMessage
	flushPendingSystemDemotions := func() {
		for _, pending := range pendingSystemDemotions {
			contents = append(contents, h.serializeOneContentForRequest(req, pending, toolNames))
		}
		pendingSystemDemotions = nil
	}

	for _, msg := range req.Messages {
		if isGeminiSystemRole(msg.Role) {
			if !hasEncounteredConversation {
				systemParts = appendGeminiSystemInstructionParts(systemParts, msg)
				continue
			}
			demoted := OagMessage{Role: "user", Content: msg.Content}
			if pendingGeminiToolCall {
				pendingSystemDemotions = append(pendingSystemDemotions, demoted)
			} else {
				contents = append(contents, h.serializeOneContentForRequest(req, demoted, toolNames))
			}
			continue
		}

		hasEncounteredConversation = true
		hasToolResult := messageHasGeminiToolResult(msg)
		if len(pendingSystemDemotions) > 0 && strings.EqualFold(msg.Role, "user") && !hasToolResult {
			flushPendingSystemDemotions()
			pendingGeminiToolCall = false
		}
		contents = append(contents, h.serializeOneContentForRequest(req, msg, toolNames))
		if messageHasGeminiToolUse(msg) {
			pendingGeminiToolCall = true
		}
		if hasToolResult {
			pendingGeminiToolCall = false
			flushPendingSystemDemotions()
		}
	}
	flushPendingSystemDemotions()
	contents = ensureGeminiLeadingUserContentItems(contents)
	contents = normalizeGeminiRequestContents(contents)

	if len(systemParts) > 0 {
		out["systemInstruction"] = map[string]any{
			"role":  "user",
			"parts": systemParts,
		}
	}

	out["contents"] = contents

	// GenerationConfig
	genConfig := map[string]any{}
	if req.Temperature != nil {
		genConfig["temperature"] = *req.Temperature
	}
	if req.TopP != nil {
		genConfig["topP"] = *req.TopP
	}
	if req.MaxTokens != nil {
		genConfig["maxOutputTokens"] = *req.MaxTokens
	}
	if len(req.Stop) > 0 {
		genConfig["stopSequences"] = req.Stop
	}
	if req.Thinking != nil {
		ApplyGeminiThinking(req.Thinking, genConfig)
	} else if req.ReasoningEffort != "" {
		if config := thinkingFromLevel(req.ReasoningEffort); config != nil {
			ApplyGeminiThinking(config, genConfig)
		}
	}
	if len(genConfig) > 0 {
		out["generationConfig"] = genConfig
	}

	if len(req.Tools) > 0 {
		normalized := make([]map[string]any, 0, len(req.Tools))
		for _, tool := range req.Tools {
			normalized = append(normalized, NormalizeToolToGemini(tool))
		}
		out["tools"] = normalized
	}
	if req.ToolChoice != nil {
		if toolConfig := NormalizeToolChoiceToGemini(req.ToolChoice); toolConfig != nil {
			out["toolConfig"] = toolConfig
		}
	}

	return json.Marshal(out)
}

func isGeminiSystemRole(role string) bool {
	return strings.EqualFold(role, "system") || strings.EqualFold(role, "developer")
}

func appendGeminiSystemInstructionParts(parts []any, msg OagMessage) []any {
	for _, b := range msg.Content {
		if tb, ok := b.(TextBlock); ok {
			parts = append(parts, map[string]any{"text": tb.Text})
		}
	}
	return parts
}

func messageHasGeminiToolUse(msg OagMessage) bool {
	for _, block := range msg.Content {
		if _, ok := block.(ToolUseBlock); ok {
			return true
		}
	}
	return false
}

func messageHasGeminiToolResult(msg OagMessage) bool {
	for _, block := range msg.Content {
		if _, ok := block.(ToolResultBlock); ok {
			return true
		}
	}
	return false
}

// ParseResponse parses a non-streaming Gemini generateContent response.
func (h *GeminiHandler) ParseResponse(rawJSON []byte) (*UnifiedResponse, error) {
	if err := validateJSONObject(rawJSON); err != nil {
		return nil, err
	}

	root := util.ParseGJSONBytesNoCopy(rawJSON)
	resp := &UnifiedResponse{
		ID:    root.Get("responseId").String(),
		Model: root.Get("modelVersion").String(),
	}
	if nested := root.Get("response"); nested.Exists() && nested.Get("candidates").Exists() {
		root = nested
		resp.ID = root.Get("responseId").String()
		resp.Model = root.Get("modelVersion").String()
	}

	candidate := root.Get("candidates.0")
	if candidate.Exists() {
		resp.FinishReason = geminiToStandardFinishReason(candidate.Get("finishReason").String())
		// Extract text from parts
		var textParts []string
		for _, part := range candidate.Get("content.parts").Array() {
			if part.Get("thought").Bool() {
				resp.ThinkingContent += part.Get("text").String()
				resp.ThinkingSignature = geminiPartSignature(part)
			} else if text := part.Get("text"); text.Exists() {
				textParts = append(textParts, text.String())
			}
			if fc := part.Get("functionCall"); fc.Exists() {
				var tcMap map[string]any
				if err := json.Unmarshal([]byte(part.Raw), &tcMap); err == nil {
					resp.ToolCalls = appendResponseToolCall(resp.ToolCalls, tcMap)
				}
			}
		}
		resp.Content = strings.Join(textParts, "")
	}
	if items := geminiResponseSignatureOutputItems(rawJSON, resp.ID); len(items) > 0 {
		resp.responsesOutput = items
	}

	// Usage
	if usage := root.Get("usageMetadata"); usage.Exists() {
		resp.Usage = geminiUsage(usage)
	}

	normalizeResponseToolCallFinish(resp)
	return resp, nil
}

// geminiToStandardFinishReason maps Gemini finish reasons to standard ones.
func geminiToStandardFinishReason(reason string) string {
	switch reason {
	case "STOP":
		return "stop"
	case "MAX_TOKENS":
		return "length"
	case "SAFETY":
		return "content_filter"
	default:
		if reason != "" {
			return strings.ToLower(reason)
		}
		return "stop"
	}
}

func (h *GeminiHandler) FormatResponse(resp *UnifiedResponse, model string) ([]byte, error) {
	var parts []any
	if resp.ThinkingContent != "" {
		part := map[string]any{"text": resp.ThinkingContent, "thought": true}
		if resp.ThinkingSignature != "" {
			part["thoughtSignature"] = resp.ThinkingSignature
		}
		parts = append(parts, part)
	}
	parts = append(parts, map[string]any{"text": resp.Content})
	for _, call := range resp.ToolCalls {
		parts = append(parts, NormalizeToolCallToGemini(call))
	}

	candidate := map[string]any{
		"content": map[string]any{
			"role":  "model",
			"parts": parts,
		},
		"finishReason": geminiFinishReason(resp.FinishReason),
	}

	out := map[string]any{
		"candidates": []any{candidate},
	}

	if resp.Usage != nil {
		usageMap := map[string]any{}
		if usageHasPrompt(resp.Usage) {
			usageMap["promptTokenCount"] = usagePromptForTarget(resp.Usage, FormatGemini)
		}
		if usageHasCompletion(resp.Usage) {
			usageMap["candidatesTokenCount"] = resp.Usage.CompletionTokens
		}
		if total, ok := usageTotalForTarget(resp.Usage, FormatGemini); ok {
			usageMap["totalTokenCount"] = total
		}
		if cached, ok := usageCachedForTarget(resp.Usage, FormatGemini); ok {
			usageMap["cachedContentTokenCount"] = cached
		}
		if usageHasReasoning(resp.Usage) {
			usageMap["thoughtsTokenCount"] = resp.Usage.ReasoningTokens
		}
		out["usageMetadata"] = usageMap
	}

	return json.Marshal(out)
}

// FormatError formats a UnifiedError into Gemini error JSON.
func (h *GeminiHandler) FormatError(err *UnifiedError) ([]byte, error) {
	out := map[string]any{
		"error": map[string]any{
			"code":    err.StatusCode,
			"message": err.Message,
			"status":  err.ErrorType,
		},
	}
	return json.Marshal(out)
}

// HasToolsDefined checks if tools are defined in the raw Gemini JSON.
func (h *GeminiHandler) HasToolsDefined(rawJSON []byte) bool {
	tools := util.GetGJSONBytesNoCopy(rawJSON, "tools")
	return tools.Exists() && tools.IsArray() && len(tools.Array()) > 0
}

// ----------------------------------------------------------------
// Gemini role mapping helpers
// ----------------------------------------------------------------

func geminiRoleToOag(role string) string {
	switch role {
	case "model":
		return "assistant"
	case "user":
		return "user"
	default:
		return role
	}
}

func normalizeGeminiRequestContentRole(content gjson.Result, previousRole string) string {
	role := content.Get("role").String()
	if role == "user" || role == "model" {
		return role
	}
	if geminiContentResultHasFunctionResponse(content) {
		return "user"
	}
	if previousRole == "" || previousRole == "model" {
		return "user"
	}
	return "model"
}

func geminiContentResultHasFunctionResponse(content gjson.Result) bool {
	for _, part := range content.Get("parts").Array() {
		if part.Get("functionResponse").Exists() || part.Get("function_response").Exists() {
			return true
		}
	}
	return false
}

func oagRoleToGemini(role string) string {
	switch role {
	case "assistant":
		return "model"
	default:
		return role
	}
}

func geminiFinishReason(reason string) string {
	switch reason {
	case "stop":
		return "STOP"
	case "length":
		return "MAX_TOKENS"
	case "tool_calls":
		return "FUNCTION_CALL"
	default:
		return fmt.Sprintf("OTHER_%s", strings.ToUpper(reason))
	}
}

var _ ProtocolHandler = (*GeminiHandler)(nil)
