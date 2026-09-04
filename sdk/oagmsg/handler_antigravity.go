// Package oagmsg - AntigravityHandler implements ProtocolHandler for the Antigravity provider format.
//
// Antigravity uses Gemini-compatible wire format with an envelope wrapper:
//   - Request:  {"project":"", "request":{GEMINI_BODY}, "model":"MODEL"}
//   - Response: {"response":{GEMINI_BODY}} or raw Gemini body
//
// This handler embeds GeminiHandler and overrides only the envelope-specific methods.
// Signature validation and tool name sanitization are executor-layer concerns
// and are NOT handled here — they remain in the executor/shim layer.
package oagmsg

import (
	"fmt"
	"strconv"
	"strings"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/util"
	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

// AntigravityHandler implements ProtocolHandler for the Antigravity provider format.
// It embeds GeminiHandler and overrides envelope wrapping/unwrapping:
//   - ParseRequest:     unwraps {"project":"","request":{GEMINI},"model":""} → Gemini body
//   - SerializeRequest: wraps Gemini body → {"project":"","request":{GEMINI},"model":"MODEL"}
//   - ParseResponse:    unwraps {"response":{GEMINI}} → Gemini body, restores cpaUsageMetadata
//
// All content-level parsing (messages, parts, tools, thinking) is delegated to GeminiHandler.
type AntigravityHandler struct {
	GeminiHandler
}

// Format returns FormatAntigravity, distinguishing this handler from GeminiHandler in the registry.
func (h *AntigravityHandler) Format() Format { return FormatAntigravity }

// ParseRequest unwraps the Antigravity request envelope and delegates to GeminiHandler.
// Input format:  {"project":"", "request":{GEMINI_BODY}, "model":"MODEL"}
// The "request" field contains a standard Gemini generateContent body.
func (h *AntigravityHandler) ParseRequest(rawJSON []byte) (*UnifiedRequest, error) {
	inner := extractAntigravityRequestBody(rawJSON)
	if err := validateJSONObject(inner); err != nil {
		return nil, err
	}
	inner = rewriteAntigravityRequestToolNamesToClient(inner)
	req := h.GeminiHandler.parseRequestFromRootWithOptions(util.ParseGJSONBytesNoCopy(inner), geminiRequestParseOptions{})
	req.SourceFormat = FormatAntigravity

	// Use the envelope-level model if present (takes priority over request-level model).
	root := util.ParseGJSONBytesNoCopy(rawJSON)
	if envelopeModel := root.Get("model").String(); envelopeModel != "" {
		req.Model = envelopeModel
	}
	return req, nil
}

// SerializeRequest produces the Antigravity request envelope from a UnifiedRequest.
// Output format: {"project":"", "request":{GEMINI_BODY}, "model":"MODEL"}
//
// Tool parameter schemas are renamed from "parameters" to "parametersJsonSchema"
// to match the Antigravity API expectation.
func (h *AntigravityHandler) SerializeRequest(req *UnifiedRequest) ([]byte, error) {
	if webSearchBody, ok := buildAntigravityWebSearchRequest(req); ok {
		return webSearchBody, nil
	}

	geminiBody, err := h.GeminiHandler.SerializeRequest(req)
	if err != nil {
		return nil, err
	}
	if antigravityClaudeTarget(req.Model) {
		geminiBody = removeAntigravityClaudeSyntheticLeadingUser(geminiBody)
	}

	// Antigravity uses "parametersJsonSchema" instead of "parameters" for tool schemas.
	geminiBody = renameToolParameters(geminiBody)
	geminiBody = normalizeAntigravityToolRequestBody(geminiBody)
	if !antigravityClaudeTarget(req.Model) {
		geminiBody = rewriteAntigravityRequestToolNamesToUpstream(geminiBody)
	}

	return wrapAntigravityRequestEnvelope(geminiBody, req.Model)
}

func antigravityClaudeTarget(model string) bool {
	return strings.Contains(strings.ToLower(model), "claude")
}

const antigravityExternalToolPrefix = "external_"

var antigravityIntrinsicToolNameCollisions = map[string]struct{}{
	"read_file":    {},
	"write_file":   {},
	"execute_code": {},
}

func antigravityToolNameToUpstream(name string) string {
	if _, collides := antigravityIntrinsicToolNameCollisions[name]; collides {
		return antigravityExternalToolPrefix + name
	}
	return name
}

func antigravityToolNameToClient(name string) string {
	if !strings.HasPrefix(name, antigravityExternalToolPrefix) {
		return name
	}
	base := strings.TrimPrefix(name, antigravityExternalToolPrefix)
	if _, collides := antigravityIntrinsicToolNameCollisions[base]; collides {
		return base
	}
	return name
}

type antigravityToolNameMapper func(string) string

func rewriteAntigravityRequestToolNamesToUpstream(geminiBody []byte) []byte {
	geminiBody = rewriteAntigravityToolDeclarationNames(geminiBody, antigravityToolNameToUpstream)
	geminiBody = rewriteAntigravityToolConfigNames(geminiBody, antigravityToolNameToUpstream)
	return rewriteAntigravityContentsToolNames(geminiBody, "contents", antigravityToolNameToUpstream)
}

func rewriteAntigravityRequestToolNamesToClient(geminiBody []byte) []byte {
	geminiBody = rewriteAntigravityToolDeclarationNames(geminiBody, antigravityToolNameToClient)
	geminiBody = rewriteAntigravityToolConfigNames(geminiBody, antigravityToolNameToClient)
	return rewriteAntigravityContentsToolNames(geminiBody, "contents", antigravityToolNameToClient)
}

func rewriteAntigravityResponseToolNamesToClient(geminiBody []byte) []byte {
	root := util.ParseGJSONBytesNoCopy(geminiBody)
	if candidates := root.Get("candidates"); candidates.IsArray() {
		for candidateIdx, candidate := range candidates.Array() {
			geminiBody = rewriteAntigravityContentToolNames(
				geminiBody,
				candidate.Get("content"),
				fmt.Sprintf("candidates.%d.content", candidateIdx),
				antigravityToolNameToClient,
			)
		}
	}
	if content := root.Get("content"); content.Exists() {
		geminiBody = rewriteAntigravityContentToolNames(geminiBody, content, "content", antigravityToolNameToClient)
	}
	return geminiBody
}

func rewriteAntigravityToolDeclarationNames(geminiBody []byte, mapper antigravityToolNameMapper) []byte {
	tools := util.GetGJSONBytesNoCopy(geminiBody, "tools")
	if !tools.IsArray() {
		return geminiBody
	}
	for toolIdx, tool := range tools.Array() {
		geminiBody = rewriteAntigravityJSONString(
			geminiBody,
			tool.Get("name"),
			fmt.Sprintf("tools.%d.name", toolIdx),
			mapper,
		)
		geminiBody = rewriteAntigravityJSONString(
			geminiBody,
			tool.Get("function.name"),
			fmt.Sprintf("tools.%d.function.name", toolIdx),
			mapper,
		)
		for _, key := range []string{"functionDeclarations", "function_declarations"} {
			decls := tool.Get(key)
			if !decls.IsArray() {
				continue
			}
			for declIdx, decl := range decls.Array() {
				geminiBody = rewriteAntigravityJSONString(
					geminiBody,
					decl.Get("name"),
					fmt.Sprintf("tools.%d.%s.%d.name", toolIdx, key, declIdx),
					mapper,
				)
			}
		}
	}
	return geminiBody
}

func rewriteAntigravityToolConfigNames(geminiBody []byte, mapper antigravityToolNameMapper) []byte {
	root := util.ParseGJSONBytesNoCopy(geminiBody)
	for _, basePath := range []string{
		"toolConfig.functionCallingConfig",
		"tool_config.function_calling_config",
		"generationConfig.toolConfig.functionCallingConfig",
	} {
		for _, key := range []string{"allowedFunctionNames", "allowed_function_names"} {
			values := root.Get(basePath + "." + key)
			if !values.IsArray() {
				continue
			}
			for valueIdx, value := range values.Array() {
				geminiBody = rewriteAntigravityJSONString(
					geminiBody,
					value,
					fmt.Sprintf("%s.%s.%d", basePath, key, valueIdx),
					mapper,
				)
			}
		}
	}
	return geminiBody
}

func rewriteAntigravityContentsToolNames(geminiBody []byte, contentsPath string, mapper antigravityToolNameMapper) []byte {
	contents := util.GetGJSONBytesNoCopy(geminiBody, contentsPath)
	if !contents.IsArray() {
		return geminiBody
	}
	for contentIdx, content := range contents.Array() {
		geminiBody = rewriteAntigravityContentToolNames(
			geminiBody,
			content,
			fmt.Sprintf("%s.%d", contentsPath, contentIdx),
			mapper,
		)
	}
	return geminiBody
}

func rewriteAntigravityContentToolNames(geminiBody []byte, content gjson.Result, contentPath string, mapper antigravityToolNameMapper) []byte {
	parts := content.Get("parts")
	if !parts.IsArray() {
		return geminiBody
	}
	for partIdx, part := range parts.Array() {
		for _, key := range []string{"functionCall", "function_call"} {
			geminiBody = rewriteAntigravityJSONString(
				geminiBody,
				part.Get(key+".name"),
				fmt.Sprintf("%s.parts.%d.%s.name", contentPath, partIdx, key),
				mapper,
			)
		}
		for _, key := range []string{"functionResponse", "function_response"} {
			geminiBody = rewriteAntigravityJSONString(
				geminiBody,
				part.Get(key+".name"),
				fmt.Sprintf("%s.parts.%d.%s.name", contentPath, partIdx, key),
				mapper,
			)
		}
	}
	return geminiBody
}

func rewriteAntigravityJSONString(geminiBody []byte, value gjson.Result, path string, mapper antigravityToolNameMapper) []byte {
	if mapper == nil || !value.Exists() || value.Type != gjson.String {
		return geminiBody
	}
	name := value.String()
	mapped := mapper(name)
	if mapped == name {
		return geminiBody
	}
	updated, err := sjson.SetBytes(geminiBody, path, mapped)
	if err != nil {
		return geminiBody
	}
	return updated
}

func removeAntigravityClaudeSyntheticLeadingUser(geminiBody []byte) []byte {
	contents := util.GetGJSONBytesNoCopy(geminiBody, "contents")
	if !contents.IsArray() {
		return geminiBody
	}
	items := contents.Array()
	if len(items) < 2 || !isSyntheticGeminiLeadingUser(items[0]) || items[1].Get("role").String() != "model" {
		return geminiBody
	}
	rebuilt := make([][]byte, 0, len(items)-1)
	for _, item := range items[1:] {
		rebuilt = append(rebuilt, []byte(item.Raw))
	}
	out, errSet := sjson.SetRawBytes(geminiBody, "contents", joinRawArray(rebuilt))
	if errSet != nil {
		return geminiBody
	}
	return out
}

func isSyntheticGeminiLeadingUser(content gjson.Result) bool {
	if content.Get("role").String() != "user" {
		return false
	}
	parts := content.Get("parts")
	if !parts.IsArray() || len(parts.Array()) != 1 {
		return false
	}
	part := parts.Array()[0]
	return part.Get("text").Exists() && part.Get("text").String() == "" && len(part.Map()) == 1
}

// ParseResponse unwraps the Antigravity response envelope and delegates to GeminiHandler.
// The response may be wrapped in {"response":{GEMINI_BODY}} or be a raw Gemini body.
// Also restores "cpaUsageMetadata" → "usageMetadata" (executor renames this in non-terminal chunks).
func (h *AntigravityHandler) ParseResponse(rawJSON []byte) (*UnifiedResponse, error) {
	inner := extractAntigravityResponseBody(rawJSON)
	inner = restoreCpaUsageMetadata(inner)
	inner = rewriteAntigravityResponseToolNamesToClient(inner)
	return h.GeminiHandler.ParseResponse(inner)
}

func (h *AntigravityHandler) parseResponseWithContext(rawJSON []byte, ctx *TranslationContext) (*UnifiedResponse, error) {
	if shouldTranslateAntigravityWebSearchGrounding(ctx) {
		root := util.ParseGJSONBytesNoCopy(rawJSON)
		if groundingMetadata := antigravityGroundingMetadata(root); groundingMetadata.Exists() {
			usage := antigravityWebSearchUsage(root, false)
			if usage == nil {
				usage = newUsage(FormatAnthropic)
				usage.usagePresence.Prompt = true
				usage.usagePresence.Completion = true
			}
			if usage != nil {
				usage.serverToolUseWebSearchRequests = 1
			}
			return &UnifiedResponse{
				ID:                        root.Get("response.responseId").String(),
				Model:                     root.Get("response.modelVersion").String(),
				FinishReason:              "stop",
				Usage:                     usage,
				responseContent:           buildAntigravityWebSearchContent(newClaudeWebSearchToolUseID(), antigravityTextContent(root), groundingMetadata),
				preferResponseModel:       true,
				skipUnknownResponseFields: true,
			}, nil
		}
	}
	resp, err := h.ParseResponse(rawJSON)
	if err != nil {
		return nil, err
	}
	applyAntigravityClaudeResponseSemantics(resp, rawJSON, ctx)
	return resp, nil
}

// FormatResponse delegates to GeminiHandler since Antigravity response output
// format is identical to Gemini (Antigravity is used as UPSTREAM, not CLIENT).
func (h *AntigravityHandler) FormatResponse(resp *UnifiedResponse, model string) ([]byte, error) {
	return h.GeminiHandler.FormatResponse(resp, model)
}

// ----------------------------------------------------------------
// Envelope helpers
// ----------------------------------------------------------------

// extractAntigravityRequestBody unwraps the "request" field from the Antigravity envelope.
// Falls back to rawJSON if no "request" field exists (bare Gemini body).
func extractAntigravityRequestBody(rawJSON []byte) []byte {
	if inner := util.GetGJSONBytesNoCopy(rawJSON, "request"); inner.Exists() && inner.IsObject() {
		return []byte(inner.Raw)
	}
	return rawJSON
}

// wrapAntigravityRequestEnvelope wraps a Gemini body in the Antigravity envelope.
func wrapAntigravityRequestEnvelope(geminiBody []byte, model string) ([]byte, error) {
	envelope := []byte(`{"project":"","model":""}`)
	var err error
	if model != "" {
		envelope, err = sjson.SetBytes(envelope, "model", model)
		if err != nil {
			return nil, err
		}
	}
	envelope, err = sjson.SetRawBytes(envelope, "request", geminiBody)
	if err != nil {
		return nil, err
	}
	return envelope, nil
}

// extractAntigravityResponseBody unwraps the "response" field from Antigravity response.
// Falls back to rawJSON if no "response" field exists.
func extractAntigravityResponseBody(rawJSON []byte) []byte {
	if inner := util.GetGJSONBytesNoCopy(rawJSON, "response"); inner.Exists() {
		return []byte(inner.Raw)
	}
	return rawJSON
}

// restoreCpaUsageMetadata renames "cpaUsageMetadata" → "usageMetadata".
// The executor renames usageMetadata to cpaUsageMetadata in non-terminal stream chunks
// to preserve usage data while hiding it from clients that don't expect mid-stream usage.
// When translating back, we restore the standard field name.
func restoreCpaUsageMetadata(data []byte) []byte {
	if cpa := util.GetGJSONBytesNoCopy(data, "cpaUsageMetadata"); cpa.Exists() {
		data, _ = sjson.SetRawBytes(data, "usageMetadata", []byte(cpa.Raw))
		data, _ = sjson.DeleteBytes(data, "cpaUsageMetadata")
	}
	return data
}

// renameToolParameters renames "parameters" → "parametersJsonSchema" in tool declarations.
// The Antigravity API uses "parametersJsonSchema" for tool parameter schemas
// while standard Gemini uses "parameters".
func renameToolParameters(geminiBody []byte) []byte {
	tools := util.GetGJSONBytesNoCopy(geminiBody, "tools")
	if !tools.IsArray() || !antigravityHasToolParameters(tools) {
		return geminiBody
	}

	changed := false
	for toolIdx, tool := range tools.Array() {
		for _, key := range []string{"functionDeclarations", "function_declarations"} {
			decls := tool.Get(key)
			if !decls.IsArray() {
				continue
			}
			for declIdx, decl := range decls.Array() {
				params := decl.Get("parameters")
				if !params.Exists() {
					continue
				}
				path := "tools." + strconv.Itoa(toolIdx) + "." + key + "." + strconv.Itoa(declIdx)
				geminiBody, _ = sjson.SetRawBytes(geminiBody, path+".parametersJsonSchema", []byte(params.Raw))
				geminiBody, _ = sjson.DeleteBytes(geminiBody, path+".parameters")
				changed = true
			}
		}
	}
	_ = changed // suppress unused lint; side effects applied via sjson
	return geminiBody
}

func normalizeAntigravityToolRequestBody(geminiBody []byte) []byte {
	geminiBody = normalizeAntigravityFunctionResponseResults(geminiBody)
	return collapseAntigravityFunctionDeclarations(geminiBody)
}

func normalizeAntigravityFunctionResponseResults(geminiBody []byte) []byte {
	contents := util.GetGJSONBytesNoCopy(geminiBody, "contents")
	if !contents.IsArray() || !antigravityHasFunctionResponseNeedsNormalization(contents) {
		return geminiBody
	}

	changed := false
	for contentIdx, content := range contents.Array() {
		parts := content.Get("parts")
		if !parts.IsArray() {
			continue
		}
		normalizedParts, partsChanged := normalizeAntigravityFunctionResponseParts(parts)
		if !partsChanged {
			continue
		}
		path := fmt.Sprintf("contents.%d.parts", contentIdx)
		geminiBody, _ = sjson.SetRawBytes(geminiBody, path, []byte(`[`+strings.Join(normalizedParts, ",")+`]`))
		changed = true
	}
	if !changed {
		return geminiBody
	}
	return geminiBody
}

func normalizeAntigravityFunctionResponseParts(parts gjson.Result) ([]string, bool) {
	hasFunctionResponse := false
	for _, part := range parts.Array() {
		if part.Get("functionResponse").Exists() {
			hasFunctionResponse = true
			break
		}
	}
	if !hasFunctionResponse {
		return nil, false
	}

	changed := false
	normalized := make([]string, 0, len(parts.Array()))
	leadingImages := make([]string, 0)
	currentResponse := -1
	for _, part := range parts.Array() {
		if part.Get("functionResponse").Exists() {
			partRaw, resultChanged := normalizeAntigravityFunctionResponseResult(part)
			normalized = append(normalized, partRaw)
			currentResponse = len(normalized) - 1
			changed = changed || resultChanged
			if len(leadingImages) > 0 {
				normalized[currentResponse] = attachAntigravityInlineDataToFunctionResponse(normalized[currentResponse], leadingImages)
				leadingImages = nil
				changed = true
			}
			continue
		}
		imagePart, imageOK := normalizeAntigravityInlineDataPart(part)
		if imageOK {
			if currentResponse >= 0 {
				normalized[currentResponse] = attachAntigravityInlineDataToFunctionResponse(normalized[currentResponse], []string{imagePart})
			} else {
				leadingImages = append(leadingImages, imagePart)
			}
			changed = true
			continue
		}
		normalized = append(normalized, part.Raw)
	}
	return normalized, changed
}

func normalizeAntigravityFunctionResponseResult(part gjson.Result) (string, bool) {
	response := part.Get("functionResponse.response")
	if !response.Exists() || response.Get("result").Exists() {
		return part.Raw, false
	}
	resultRaw := response.Raw
	if response.IsArray() {
		items := response.Array()
		switch len(items) {
		case 0:
			resultRaw = `""`
		case 1:
			resultRaw = items[0].Raw
		default:
			resultRaw = response.Raw
		}
	}
	normalized := []byte(part.Raw)
	normalized, _ = sjson.SetRawBytes(normalized, "functionResponse.response", []byte(`{"result":`+resultRaw+`}`))
	return string(normalized), true
}

func normalizeAntigravityInlineDataPart(part gjson.Result) (string, bool) {
	inline := part.Get("inlineData")
	if !inline.Exists() {
		inline = part.Get("inline_data")
	}
	if !inline.Exists() {
		return "", false
	}
	data := inline.Get("data").String()
	if data == "" {
		return "", false
	}
	mimeType := inline.Get("mimeType").String()
	if mimeType == "" {
		mimeType = inline.Get("mime_type").String()
	}
	if mimeType == "" {
		mimeType = "image/png"
	}
	out := []byte(`{"inlineData":{"mimeType":"","data":""}}`)
	out, _ = sjson.SetBytes(out, "inlineData.mimeType", mimeType)
	out, _ = sjson.SetBytes(out, "inlineData.data", data)
	return string(out), true
}

func attachAntigravityInlineDataToFunctionResponse(response string, images []string) string {
	if len(images) == 0 {
		return response
	}
	target := []byte(response)
	for _, image := range images {
		target, _ = sjson.SetRawBytes(target, "functionResponse.parts.-1", []byte(image))
	}
	return string(target)
}

func collapseAntigravityFunctionDeclarations(geminiBody []byte) []byte {
	tools := util.GetGJSONBytesNoCopy(geminiBody, "tools")
	if !tools.IsArray() || !antigravityHasFunctionDeclarations(tools) {
		return geminiBody
	}
	var declarations []string
	var otherTools []string
	for _, tool := range tools.Array() {
		consumed := false
		for _, key := range []string{"functionDeclarations", "function_declarations"} {
			decls := tool.Get(key)
			if !decls.IsArray() {
				continue
			}
			for _, decl := range decls.Array() {
				declarations = append(declarations, decl.Raw)
			}
			consumed = true
		}
		if !consumed {
			otherTools = append(otherTools, tool.Raw)
		}
	}
	if len(declarations) == 0 {
		return geminiBody
	}
	grouped := `{"functionDeclarations":[` + strings.Join(declarations, ",") + `]}`
	toolItems := append([]string{grouped}, otherTools...)
	geminiBody, _ = sjson.SetRawBytes(geminiBody, "tools", []byte(`[`+strings.Join(toolItems, ",")+`]`))
	return geminiBody
}

func antigravityHasToolParameters(tools gjson.Result) bool {
	for _, tool := range tools.Array() {
		for _, key := range []string{"functionDeclarations", "function_declarations"} {
			decls := tool.Get(key)
			if !decls.IsArray() {
				continue
			}
			for _, decl := range decls.Array() {
				if decl.Get("parameters").Exists() {
					return true
				}
			}
		}
	}
	return false
}

func antigravityHasFunctionResponseNeedsNormalization(contents gjson.Result) bool {
	for _, content := range contents.Array() {
		parts := content.Get("parts")
		if !parts.IsArray() {
			continue
		}
		hasFunctionResponse := false
		hasInlineData := false
		for _, part := range parts.Array() {
			if part.Get("functionResponse").Exists() {
				hasFunctionResponse = true
			}
			if part.Get("inlineData.data").String() != "" || part.Get("inline_data.data").String() != "" {
				hasInlineData = true
			}
			response := part.Get("functionResponse.response")
			if response.Exists() && !response.Get("result").Exists() {
				return true
			}
		}
		if hasFunctionResponse && hasInlineData {
			return true
		}
	}
	return false
}

func antigravityHasFunctionDeclarations(tools gjson.Result) bool {
	for _, tool := range tools.Array() {
		for _, key := range []string{"functionDeclarations", "function_declarations"} {
			if tool.Get(key).IsArray() {
				return true
			}
		}
	}
	return false
}

func applyAntigravityClaudeResponseSemantics(resp *UnifiedResponse, rawJSON []byte, ctx *TranslationContext) {
	if resp == nil || ctx == nil || resolveFormat(ctx.SourceFormat) != FormatAnthropic {
		return
	}
	resp.preferResponseModel = true
	if len(resp.ToolCalls) > 0 {
		resp.FinishReason = "tool_calls"
		for idx, call := range resp.ToolCalls {
			setAntigravityClaudeFallbackToolID(call, fmt.Sprintf("tool_%d", idx+1))
		}
	}
	if resp.Usage == nil {
		return
	}
	usage := antigravityResponseRoot(rawJSON).Get("usageMetadata")
	if !usage.Exists() {
		return
	}
	candidateTokens := usage.Get("candidatesTokenCount").Int()
	thoughtTokens := usage.Get("thoughtsTokenCount").Int()
	resp.Usage.CompletionTokens = int(candidateTokens + thoughtTokens)
	resp.Usage.usagePresence.Completion = usage.Get("candidatesTokenCount").Exists() || usage.Get("thoughtsTokenCount").Exists()
	resp.Usage.ReasoningTokens = 0
	resp.Usage.usagePresence.Reasoning = false
}

func setAntigravityClaudeFallbackToolID(call map[string]any, fallback string) {
	if call == nil || fallback == "" {
		return
	}
	if functionCall, ok := call["functionCall"].(map[string]any); ok {
		functionCall["id"] = fallback
		return
	}
	call["id"] = fallback
}

// ----------------------------------------------------------------
// StreamHandler interface implementation
// ----------------------------------------------------------------

// ParseStreamChunk unwraps the Antigravity response envelope from a stream line,
// restores cpaUsageMetadata, and delegates to GeminiHandler's stream parser.
//
// The raw SSE line has already had the "data:" prefix stripped by StreamTranslateSession.
// Antigravity stream data may contain {"response":{GEMINI_CHUNK}} wrapping.
func (h *AntigravityHandler) ParseStreamChunk(rawLine []byte) ([]StreamDelta, error) {
	inner := extractAntigravityResponseBody(rawLine)
	inner = restoreCpaUsageMetadata(inner)
	inner = rewriteAntigravityResponseToolNamesToClient(inner)
	return h.GeminiHandler.ParseStreamChunk(inner)
}

func (h *AntigravityHandler) parseStreamChunkWithContext(rawLine []byte, state *streamParseState, ctx *TranslationContext) ([]StreamDelta, error) {
	if !shouldTranslateAntigravityWebSearchGrounding(ctx) || state == nil {
		return h.ParseStreamChunk(rawLine)
	}

	root := gjson.ParseBytes(rawLine)
	body := antigravityResponseRoot(rawLine)
	var deltas []StreamDelta
	parsedHasDone := false
	parsedHasUsage := false
	if start := antigravityWebSearchStartDelta(rawLine); start != nil {
		deltas = append(deltas, *start)
	}

	handledGrounding := false
	webSearchState := &state.antigravityWebSearch
	if !webSearchState.hasSearch {
		if groundingMetadata := antigravityGroundingMetadata(root); groundingMetadata.Exists() {
			toolUseID := newClaudeWebSearchToolUseID()
			textContent := webSearchState.textBuffer.String() + antigravityTextContent(root)
			webSearchState.textBuffer.Reset()
			deltas = append(deltas, antigravityWebSearchStreamDeltas(buildAntigravityWebSearchContent(toolUseID, textContent, groundingMetadata))...)
			deltas = append(deltas, StreamDelta{
				Type: EventUsage,
				Usage: &UnifiedUsage{
					serverToolUseWebSearchRequests: 1,
					usageOrigin:                    FormatAnthropic,
				},
			})
			webSearchState.hasSearch = true
			handledGrounding = true
		}
	}

	parts := body.Get("candidates.0.content.parts")
	if parts.IsArray() && !webSearchState.hasSearch && !handledGrounding {
		appendAntigravityWebSearchBufferedText(parts, &webSearchState.textBuffer)
	} else if parts.IsArray() && webSearchState.hasSearch && !handledGrounding {
		parsed, err := h.ParseStreamChunk(rawLine)
		if err != nil {
			return nil, err
		}
		parsed, parsedHasDone, parsedHasUsage = stripDuplicateStartDeltas(parsed, webSearchState.hasSearch)
		deltas = append(deltas, parsed...)
	}

	if finish := body.Get("candidates.0.finishReason"); finish.Exists() {
		if !webSearchState.hasSearch && webSearchState.textBuffer.Len() > 0 {
			deltas = append(deltas, StreamDelta{
				Type:    EventTextDelta,
				Content: webSearchState.textBuffer.String(),
			})
			webSearchState.textBuffer.Reset()
		}
		if !parsedHasDone {
			nativeReason := strings.ToUpper(finish.String())
			deltas = append(deltas, StreamDelta{
				Type:               EventDone,
				FinishReason:       mapGeminiFinishReason(nativeReason),
				NativeFinishReason: strings.ToLower(nativeReason),
			})
		}
	}

	if usage := antigravityWebSearchUsage(root, true); usage != nil && !parsedHasUsage {
		if webSearchState.hasSearch {
			usage.serverToolUseWebSearchRequests = 1
		}
		deltas = append(deltas, StreamDelta{
			Type:  EventUsage,
			Usage: usage,
		})
	}

	return deltas, nil
}

func antigravityWebSearchStartDelta(rawLine []byte) *StreamDelta {
	if strings.TrimSpace(string(rawLine)) == "[DONE]" || strings.TrimSpace(string(rawLine)) == "data: [DONE]" {
		return nil
	}
	usage := antigravityWebSearchMessageStartUsage(rawLine)
	root := gjson.ParseBytes(rawLine)
	responseID := root.Get("response.responseId")
	modelVersion := root.Get("response.modelVersion")
	id := "msg_1nZdL29xx5MUA1yADyHTEsnR8uuvGzszyY"
	if responseID.Exists() {
		id = responseID.String()
	}
	model := "claude-3-5-sonnet-20241022"
	if modelVersion.Exists() {
		model = modelVersion.String()
	}
	return &StreamDelta{
		Type:  EventStart,
		ID:    id,
		Model: model,
		Usage: usage,
		Extra: map[string]any{
			"anthropic_message_start_usage": usage != nil,
		},
	}
}

func appendAntigravityWebSearchBufferedText(parts gjson.Result, buffer *strings.Builder) {
	for _, partResult := range parts.Array() {
		if partResult.Get("thought").Bool() || partResult.Get("functionCall").Exists() {
			continue
		}
		if partTextResult := partResult.Get("text"); partTextResult.Exists() {
			buffer.WriteString(partTextResult.String())
		}
	}
}

func stripDuplicateStartDeltas(deltas []StreamDelta, markWebSearchUsage bool) ([]StreamDelta, bool, bool) {
	out := deltas[:0]
	hasDone := false
	hasUsage := false
	for _, delta := range deltas {
		if delta.Type == EventStart {
			continue
		}
		if delta.Type == EventDone {
			hasDone = true
		}
		if delta.Type == EventUsage {
			hasUsage = true
			if markWebSearchUsage {
				hasUsage = false
				continue
			}
		}
		out = append(out, delta)
	}
	return out, hasDone, hasUsage
}

// NewStreamSerializer delegates to GeminiHandler since Antigravity stream output
// format is identical to Gemini. Antigravity is only used as UPSTREAM format.
func (h *AntigravityHandler) NewStreamSerializer(model string) StreamSerializer {
	return h.GeminiHandler.NewStreamSerializer(model)
}

// Compile-time interface checks.
var (
	_ ProtocolHandler = (*AntigravityHandler)(nil)
	_ StreamHandler   = (*AntigravityHandler)(nil)
)
