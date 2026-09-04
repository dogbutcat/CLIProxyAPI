package oagmsg

import (
	"bytes"
	"strings"

	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

type responsesModelSelection struct {
	model     string
	set       bool
	noRuntime bool
}

func resolvedResponsesModelName(runtimeModel string, originalRequestRawJSON, requestRawJSON []byte) string {
	if selected := resolveDefaultResponsesModel(runtimeModel, originalRequestRawJSON, requestRawJSON); selected.set {
		return selected.model
	}
	return ""
}

func resolveDefaultResponsesModel(runtimeModel string, originalRequestRawJSON, requestRawJSON []byte) responsesModelSelection {
	for _, rawJSON := range [][]byte{originalRequestRawJSON, requestRawJSON} {
		if model := requestModelName(rawJSON); model != "" {
			return responsesModelSelection{model: model, set: true}
		}
	}
	if runtimeModel != "" {
		return responsesModelSelection{model: runtimeModel, set: true}
	}
	return responsesModelSelection{}
}

func resolveResponsesModelSelection(upstream, client Format, runtimeModel string, originalRequestRawJSON, requestRawJSON []byte, stream bool) responsesModelSelection {
	if resolveFormat(client) != FormatOpenAIResponse {
		return responsesModelSelection{}
	}
	if resolveFormat(upstream) == FormatGemini && !stream {
		return resolveGeminiNonStreamResponsesModel(runtimeModel, originalRequestRawJSON, requestRawJSON)
	}
	return resolveDefaultResponsesModel(runtimeModel, originalRequestRawJSON, requestRawJSON)
}

func requestModelName(rawJSON []byte) string {
	if len(rawJSON) == 0 || !gjson.ValidBytes(rawJSON) {
		return ""
	}
	root := gjson.ParseBytes(rawJSON)
	for _, path := range []string{"model", "request.model"} {
		model := root.Get(path)
		if model.Type == gjson.String && strings.TrimSpace(model.String()) != "" {
			return model.String()
		}
	}
	return ""
}

func resolveGeminiNonStreamResponsesModel(_ string, originalRequestRawJSON, requestRawJSON []byte) responsesModelSelection {
	for _, rawJSON := range [][]byte{originalRequestRawJSON, requestRawJSON} {
		root, ok := validRequestRoot(rawJSON)
		if !ok {
			continue
		}
		if model := root.Get("model"); model.Exists() {
			return responsesModelSelection{model: model.String(), set: true}
		}
		return responsesModelSelection{noRuntime: true}
	}
	return responsesModelSelection{noRuntime: true}
}

func validRequestRoot(rawJSON []byte) (gjson.Result, bool) {
	if len(rawJSON) == 0 || !gjson.ValidBytes(rawJSON) {
		return gjson.Result{}, false
	}
	root := gjson.ParseBytes(rawJSON)
	request := root.Get("request")
	if request.Exists() && (request.Get("model").Exists() || request.Get("input").Exists() || request.Get("instructions").Exists()) {
		return request, true
	}
	return root, true
}

func providerModelSelection(body []byte, source Format) responsesModelSelection {
	if resolveFormat(source) != FormatGemini || len(body) == 0 || !gjson.ValidBytes(body) {
		return responsesModelSelection{}
	}
	root := gjson.ParseBytes(body)
	if nested := root.Get("response"); isGeminiProviderEnvelope(nested) {
		root = nested
	}
	model := root.Get("modelVersion")
	if !model.Exists() {
		return responsesModelSelection{}
	}
	return responsesModelSelection{model: model.String(), set: true}
}

func isGeminiProviderEnvelope(root gjson.Result) bool {
	if !root.Exists() || !root.IsObject() {
		return false
	}
	return root.Get("candidates").Exists() || root.Get("responseId").Exists() || root.Get("usageMetadata").Exists()
}

func overrideResponsesModel(body []byte, model string) []byte {
	return overrideResponsesModelSelection(body, responsesModelSelection{model: model, set: model != ""}, false)
}

func overrideResponsesModelSelection(body []byte, selection responsesModelSelection, preserveExistingEvents bool) []byte {
	if !selection.set || len(body) == 0 {
		return body
	}
	if payloadStart, payloadEnd, ok := sseJSONPayloadBounds(body); ok {
		payload := body[payloadStart:payloadEnd]
		updated := overrideResponsesModelSelection(payload, selection, preserveExistingEvents)
		if bytes.Equal(updated, payload) {
			return body
		}
		out := make([]byte, 0, len(body)-len(payload)+len(updated))
		out = append(out, body[:payloadStart]...)
		out = append(out, updated...)
		out = append(out, body[payloadEnd:]...)
		return out
	}
	if !gjson.ValidBytes(body) {
		return body
	}
	root := gjson.ParseBytes(body)
	if eventType := root.Get("type").String(); strings.HasPrefix(eventType, "response.") {
		switch eventType {
		case "response.created", "response.in_progress":
			if preserveExistingEvents && root.Get("response.model").Exists() {
				return body
			}
		case "response.completed":
			if preserveExistingEvents {
				return body
			}
		default:
			return body
		}
		updated, err := sjson.SetBytes(body, "response.model", selection.model)
		if err != nil {
			return body
		}
		return updated
	}
	if !isNormalResponsesObject(root) {
		return body
	}
	updated, err := sjson.SetBytes(body, "model", selection.model)
	if err != nil {
		return body
	}
	return updated
}

func omitEmptyResponsesModel(body []byte) []byte {
	if len(body) == 0 || !gjson.ValidBytes(body) {
		return body
	}
	if model := gjson.GetBytes(body, "model"); !model.Exists() || model.String() != "" {
		return body
	}
	updated, err := sjson.DeleteBytes(body, "model")
	if err != nil {
		return body
	}
	return updated
}

func sseJSONPayloadBounds(body []byte) (int, int, bool) {
	lineStart := 0
	for lineStart <= len(body) {
		lineEnd := len(body)
		if newline := bytes.IndexByte(body[lineStart:], '\n'); newline >= 0 {
			lineEnd = lineStart + newline
		}
		line := body[lineStart:lineEnd]
		if bytes.HasPrefix(line, dataPrefix2) {
			jsonStart := lineStart + len(dataPrefix2)
			for jsonStart < lineEnd && (body[jsonStart] == ' ' || body[jsonStart] == '\t') {
				jsonStart++
			}
			jsonEnd := lineEnd
			if jsonEnd > jsonStart && body[jsonEnd-1] == '\r' {
				jsonEnd--
			}
			for jsonEnd > jsonStart && (body[jsonEnd-1] == ' ' || body[jsonEnd-1] == '\t') {
				jsonEnd--
			}
			if jsonStart < jsonEnd && body[jsonStart] == '{' {
				return jsonStart, jsonEnd, true
			}
			return 0, 0, false
		}
		if lineEnd == len(body) {
			break
		}
		lineStart = lineEnd + 1
	}
	return 0, 0, false
}

func isNormalResponsesObject(root gjson.Result) bool {
	object := root.Get("object")
	if object.Exists() && object.Type == gjson.String && object.String() != "" {
		return object.String() == "response"
	}
	if object.Exists() && object.Type != gjson.Null && object.Type != gjson.String {
		return false
	}
	return root.Get("status").Exists() && root.Get("output").Exists()
}

func newUsage(origin Format) *UnifiedUsage {
	return &UnifiedUsage{usageOrigin: resolveFormat(origin)}
}

func openAIUsage(usage gjson.Result) *UnifiedUsage {
	u := newUsage(FormatOpenAI)
	setUsageInt(&u.PromptTokens, &u.usagePresence.Prompt, usage.Get("prompt_tokens"))
	setUsageInt(&u.CompletionTokens, &u.usagePresence.Completion, usage.Get("completion_tokens"))
	setUsageInt(&u.TotalTokens, &u.usagePresence.Total, usage.Get("total_tokens"))
	setUsageInt(&u.CachedTokens, &u.usagePresence.Cached, usage.Get("prompt_tokens_details.cached_tokens"))
	setUsageInt(&u.CacheCreationInputTokens, &u.usagePresence.CacheCreation, usage.Get("prompt_tokens_details.cached_creation_tokens"))
	setUsageInt(&u.ReasoningTokens, &u.usagePresence.Reasoning, usage.Get("completion_tokens_details.reasoning_tokens"))
	if !u.usagePresence.Reasoning {
		setUsageInt(&u.ReasoningTokens, &u.usagePresence.Reasoning, usage.Get("output_tokens_details.reasoning_tokens"))
	}
	return u
}

func responsesUsage(usage gjson.Result, origin Format) *UnifiedUsage {
	u := newUsage(origin)
	setUsageInt(&u.PromptTokens, &u.usagePresence.Prompt, usage.Get("input_tokens"))
	setUsageInt(&u.CompletionTokens, &u.usagePresence.Completion, usage.Get("output_tokens"))
	setUsageInt(&u.TotalTokens, &u.usagePresence.Total, usage.Get("total_tokens"))
	setUsageInt(&u.CachedTokens, &u.usagePresence.Cached, usage.Get("input_tokens_details.cached_tokens"))
	setUsageInt(&u.CacheCreationInputTokens, &u.usagePresence.CacheCreation, usage.Get("input_tokens_details.cache_write_tokens"))
	setUsageInt(&u.ReasoningTokens, &u.usagePresence.Reasoning, usage.Get("output_tokens_details.reasoning_tokens"))
	return u
}

func claudeUsage(usage gjson.Result) *UnifiedUsage {
	u := newUsage(FormatAnthropic)
	setUsageInt(&u.PromptTokens, &u.usagePresence.Prompt, usage.Get("input_tokens"))
	setUsageInt(&u.CompletionTokens, &u.usagePresence.Completion, usage.Get("output_tokens"))
	setUsageInt(&u.CacheCreationInputTokens, &u.usagePresence.CacheCreation, usage.Get("cache_creation_input_tokens"))
	setUsageInt(&u.CacheReadInputTokens, &u.usagePresence.CacheRead, usage.Get("cache_read_input_tokens"))
	if u.usagePresence.CacheRead {
		u.CachedTokens = u.CacheReadInputTokens
		u.usagePresence.Cached = true
	}
	setUsageInt(&u.ReasoningTokens, &u.usagePresence.Reasoning, usage.Get("thinking_tokens"))
	if u.usagePresence.Prompt || u.usagePresence.Completion {
		u.TotalTokens = u.PromptTokens + u.CompletionTokens
	}
	return u
}

func geminiUsage(usage gjson.Result) *UnifiedUsage {
	u := newUsage(FormatGemini)
	setUsageInt(&u.PromptTokens, &u.usagePresence.Prompt, usage.Get("promptTokenCount"))
	setUsageInt(&u.CompletionTokens, &u.usagePresence.Completion, usage.Get("candidatesTokenCount"))
	setUsageInt(&u.TotalTokens, &u.usagePresence.Total, usage.Get("totalTokenCount"))
	setUsageInt(&u.CachedTokens, &u.usagePresence.Cached, usage.Get("cachedContentTokenCount"))
	setUsageInt(&u.ReasoningTokens, &u.usagePresence.Reasoning, usage.Get("thoughtsTokenCount"))
	if !u.usagePresence.Total && (u.usagePresence.Prompt || u.usagePresence.Completion || u.usagePresence.Reasoning) {
		u.TotalTokens = u.PromptTokens + u.CompletionTokens + u.ReasoningTokens
		u.usagePresence.DerivedTotal = true
	}
	return u
}

func googleInteractionUsageFromResult(usage gjson.Result) *UnifiedUsage {
	u := newUsage(FormatInteractions)
	setFirstUsageInt(&u.PromptTokens, &u.usagePresence.Prompt, usage, "input_tokens", "total_input_tokens")
	setFirstUsageInt(&u.CompletionTokens, &u.usagePresence.Completion, usage, "output_tokens", "total_output_tokens")
	setUsageInt(&u.TotalTokens, &u.usagePresence.Total, usage.Get("total_tokens"))
	setFirstUsageInt(&u.CachedTokens, &u.usagePresence.Cached, usage, "cached_tokens", "total_cached_tokens")
	setFirstUsageInt(&u.ReasoningTokens, &u.usagePresence.Reasoning, usage, "reasoning_tokens", "total_thought_tokens")
	if !u.usagePresence.Total && (u.usagePresence.Prompt || u.usagePresence.Completion) {
		u.TotalTokens = u.PromptTokens + u.CompletionTokens
		u.usagePresence.DerivedTotal = true
	}
	return u
}

func setUsageInt(dst *int, present *bool, value gjson.Result) {
	if !value.Exists() {
		return
	}
	*dst = int(value.Int())
	*present = true
}

func setFirstUsageInt(dst *int, present *bool, root gjson.Result, paths ...string) {
	for _, path := range paths {
		if value := root.Get(path); value.Exists() {
			setUsageInt(dst, present, value)
			return
		}
	}
}

func usagePromptForTarget(u *UnifiedUsage, target Format) int {
	if u == nil {
		return 0
	}
	if u.usageOrigin == FormatAnthropic && (target == FormatOpenAI || target == FormatOpenAIResponse) {
		return u.PromptTokens + usageCacheReadValue(u) + u.CacheCreationInputTokens
	}
	return u.PromptTokens
}

func usageCachedForTarget(u *UnifiedUsage, target Format) (int, bool) {
	if u == nil {
		return 0, false
	}
	if u.usageOrigin == FormatAnthropic && (target == FormatGemini || target == FormatInteractions || target == FormatInteractionsSteps) {
		return usageCacheReadValue(u) + u.CacheCreationInputTokens, usageHasCacheRead(u) || usageHasCacheCreation(u)
	}
	if target == FormatGemini {
		return u.CachedTokens + u.CacheCreationInputTokens, usageHasCached(u) || usageHasCacheCreation(u)
	}
	return u.CachedTokens, usageHasCached(u)
}

func responsesCachedTokensForUsage(u *UnifiedUsage) (int, bool) {
	if cached, ok := usageCachedForTarget(u, FormatOpenAIResponse); ok {
		return cached, true
	}
	if u != nil && u.usageOrigin == FormatGemini {
		return 0, true
	}
	return 0, false
}

func usageCacheReadValue(u *UnifiedUsage) int {
	if u == nil {
		return 0
	}
	if usageHasCacheRead(u) {
		return u.CacheReadInputTokens
	}
	return u.CachedTokens
}

func usageTotalForTarget(u *UnifiedUsage, target Format) (int, bool) {
	if u == nil {
		return 0, false
	}
	if usageHasTotal(u) {
		return u.TotalTokens, true
	}
	if u.usagePresence.DerivedTotal {
		return u.TotalTokens, true
	}
	if usageHasPrompt(u) || usageHasCompletion(u) {
		return usagePromptForTarget(u, target) + u.CompletionTokens, true
	}
	return 0, false
}

func usageHasPrompt(u *UnifiedUsage) bool {
	return u != nil && (u.usagePresence.Prompt || !hasUsageMetadata(u) && u.PromptTokens != 0)
}

func usageHasCompletion(u *UnifiedUsage) bool {
	return u != nil && (u.usagePresence.Completion || !hasUsageMetadata(u) && u.CompletionTokens != 0)
}

func usageHasTotal(u *UnifiedUsage) bool {
	return u != nil && (u.usagePresence.Total || !hasUsageMetadata(u) && u.TotalTokens != 0)
}

func usageHasCached(u *UnifiedUsage) bool {
	return u != nil && (u.usagePresence.Cached || !hasUsageMetadata(u) && u.CachedTokens != 0)
}

func usageHasCacheCreation(u *UnifiedUsage) bool {
	return u != nil && (u.usagePresence.CacheCreation || !hasUsageMetadata(u) && u.CacheCreationInputTokens != 0)
}

func usageHasCacheRead(u *UnifiedUsage) bool {
	return u != nil && (u.usagePresence.CacheRead || !hasUsageMetadata(u) && u.CacheReadInputTokens != 0)
}

func usageHasReasoning(u *UnifiedUsage) bool {
	return u != nil && (u.usagePresence.Reasoning || !hasUsageMetadata(u) && u.ReasoningTokens != 0)
}

func hasUsageMetadata(u *UnifiedUsage) bool {
	return u != nil && (u.usageOrigin != "" ||
		u.usagePresence.Prompt ||
		u.usagePresence.Completion ||
		u.usagePresence.Total ||
		u.usagePresence.DerivedTotal ||
		u.usagePresence.Cached ||
		u.usagePresence.CacheCreation ||
		u.usagePresence.CacheRead ||
		u.usagePresence.Reasoning)
}

func mergeUsagePresence(dst *UnifiedUsage, incoming *UnifiedUsage) {
	if incoming == nil || dst == nil {
		return
	}
	if dst.usageOrigin == "" {
		dst.usageOrigin = incoming.usageOrigin
	}
	dst.usagePresence.Prompt = dst.usagePresence.Prompt || incoming.usagePresence.Prompt
	dst.usagePresence.Completion = dst.usagePresence.Completion || incoming.usagePresence.Completion
	dst.usagePresence.Total = dst.usagePresence.Total || incoming.usagePresence.Total
	dst.usagePresence.DerivedTotal = dst.usagePresence.DerivedTotal || incoming.usagePresence.DerivedTotal
	dst.usagePresence.Cached = dst.usagePresence.Cached || incoming.usagePresence.Cached
	dst.usagePresence.CacheCreation = dst.usagePresence.CacheCreation || incoming.usagePresence.CacheCreation
	dst.usagePresence.CacheRead = dst.usagePresence.CacheRead || incoming.usagePresence.CacheRead
	dst.usagePresence.Reasoning = dst.usagePresence.Reasoning || incoming.usagePresence.Reasoning
}
