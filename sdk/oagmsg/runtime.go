package oagmsg

import (
	"context"
	"strings"
	"sync"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/thinking"
	log "github.com/sirupsen/logrus"
	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

// PluginHooks defines optional normalization and fallback translation hooks.
// Native protocol conversion remains owned by oagmsg.
type PluginHooks interface {
	NormalizeRequest(ctx context.Context, from, to Format, model string, body []byte, stream bool) []byte
	TranslateRequest(ctx context.Context, from, to Format, model string, body []byte, stream bool) ([]byte, bool)
	NormalizeResponseBefore(ctx context.Context, from, to Format, model string, originalRequestRawJSON, requestRawJSON, body []byte, stream bool) []byte
	TranslateResponse(ctx context.Context, from, to Format, model string, originalRequestRawJSON, requestRawJSON, body []byte, stream bool) ([]byte, bool)
	NormalizeResponseAfter(ctx context.Context, from, to Format, model string, originalRequestRawJSON, requestRawJSON, body []byte, stream bool) []byte
}

var runtimeHooks struct {
	sync.RWMutex
	value PluginHooks
}

// SetPluginHooks installs process-wide oagmsg plugin hooks.
func SetPluginHooks(hooks PluginHooks) {
	runtimeHooks.Lock()
	runtimeHooks.value = hooks
	runtimeHooks.Unlock()
}

// HasPluginHooks reports whether process-wide oagmsg plugin hooks are installed.
func HasPluginHooks() bool {
	return currentPluginHooks() != nil
}

func currentPluginHooks() PluginHooks {
	runtimeHooks.RLock()
	hooks := runtimeHooks.value
	runtimeHooks.RUnlock()
	return hooks
}

// HasRequestTransformer reports whether both native protocol handlers exist.
func HasRequestTransformer[From ~string, To ~string](from From, to To) bool {
	registry := DefaultRegistry()
	_, sourceOK := registry.Get(Format(from))
	_, targetOK := registry.Get(Format(to))
	return sourceOK && targetOK
}

// HasResponseTransformer reports whether oagmsg can convert a non-stream response.
func HasResponseTransformer[From ~string, To ~string](from From, to To) bool {
	registry := DefaultRegistry()
	_, sourceOK := registry.Get(Format(from))
	_, targetOK := registry.Get(Format(to))
	return sourceOK && targetOK
}

// HasStreamResponseTransformer reports whether oagmsg can parse and serialize
// streaming responses for both formats.
func HasStreamResponseTransformer[From ~string, To ~string](from From, to To) bool {
	registry := DefaultRegistry()
	source, sourceOK := registry.Get(Format(from))
	target, targetOK := registry.Get(Format(to))
	if !sourceOK || !targetOK {
		return false
	}
	_, sourceStreams := source.(StreamHandler)
	_, targetStreams := target.(StreamHandler)
	return sourceStreams && targetStreams
}

// RequestTranslationOptions controls narrowly scoped request compatibility
// behavior. The zero value preserves the default provider semantics.
type RequestTranslationOptions struct {
	// PreserveThinkingBlocks keeps unsigned and opaque thinking history for
	// explicitly configured third-party compatibility endpoints.
	PreserveThinkingBlocks bool
}

// TranslateRequest converts a request directly through oagmsg.
func TranslateRequest[From ~string, To ~string](fromValue From, toValue To, model string, rawJSON []byte, stream bool) []byte {
	return TranslateRequestWithOptions(fromValue, toValue, model, rawJSON, stream, RequestTranslationOptions{})
}

// TranslateRequestWithOptions converts a request directly through oagmsg with
// explicit per-call compatibility behavior.
func TranslateRequestWithOptions[From ~string, To ~string](fromValue From, toValue To, model string, rawJSON []byte, stream bool, options RequestTranslationOptions) []byte {
	from := Format(fromValue)
	to := Format(toValue)
	hooks := currentPluginHooks()

	switch translationPath := selectRequestTranslationPath(from, to, model, rawJSON, hooks); translationPath {
	case requestTranslationPathIdentity:
		return finalizeRequestForTarget(to, rawJSON, stream)
	case requestTranslationPathSameWire:
		fallthrough
	case requestTranslationPathCodexFinalize:
		body := setRuntimeModel(rawJSON, model)
		if hooks != nil {
			body = hooks.NormalizeRequest(context.Background(), from, to, model, body, stream)
		}
		body = applyCodexRequestMetadataForTarget(to, body, rawJSON)
		// Codex target constraints must be enforced after same-family hooks
		// (e.g., OpenAI Responses → Codex) so hooks cannot reintroduce rejects.
		return finalizeRequestForTarget(to, body, stream)
	default:
	}

	registry := DefaultRegistry()
	source, sourceOK := registry.Get(from)
	target, targetOK := registry.Get(to)
	body := rawJSON
	if sourceOK && targetOK {
		summaryConfig := thinking.ExtractSummaryConfig(rawJSON, from.String())
		req, err := source.ParseRequest(rawJSON)
		if err == nil {
			req.Model = modelOrExisting(model, req.Model)
			req.Stream = stream
			req.translationOptions = options
			body, err = target.SerializeRequest(req)
			if err == nil {
				body = preserveUnknownFieldsForSource(from, rawJSON, body)
				body = thinking.ApplySummaryConfigForModel(body, to.String(), req.Model, summaryConfig)
				if hooks != nil {
					body = hooks.NormalizeRequest(context.Background(), from, to, req.Model, body, stream)
				}
				body = applyCodexRequestMetadataForTarget(to, body, rawJSON)
				body = applyOpenAIChatCodexRequestDefaults(from, to, body)
				// Apply Codex target finalization after preservation, summary
				// mapping, and the last request hook mutation.
				return finalizeRequestForTarget(to, body, stream)
			}
		}
		log.WithError(err).Warnf("oagmsg: request translation %s to %s failed", from, to)
	}

	body = setRuntimeModel(body, model)
	if hooks == nil {
		body = applyCodexRequestMetadataForTarget(to, body, rawJSON)
		body = applyOpenAIChatCodexRequestDefaults(from, to, body)
		return finalizeRequestForTarget(to, body, stream)
	}
	body = hooks.NormalizeRequest(context.Background(), from, to, model, body, stream)
	summaryConfig := thinking.ExtractSummaryConfig(body, from.String())
	if translated, ok := hooks.TranslateRequest(context.Background(), from, to, model, body, stream); ok {
		translated = thinking.ApplySummaryConfigForModel(translated, to.String(), model, summaryConfig)
		translated = applyCodexRequestMetadataForTarget(to, translated, rawJSON)
		translated = applyOpenAIChatCodexRequestDefaults(from, to, translated)
		return finalizeRequestForTarget(to, translated, stream)
	}
	body = applyCodexRequestMetadataForTarget(to, body, rawJSON)
	body = applyOpenAIChatCodexRequestDefaults(from, to, body)
	return finalizeRequestForTarget(to, body, stream)
}

type requestPathKind int

const (
	requestTranslationPathCanonical requestPathKind = iota
	requestTranslationPathIdentity
	requestTranslationPathSameWire
	requestTranslationPathCodexFinalize
)

func selectRequestTranslationPath(from, to Format, model string, rawJSON []byte, hooks PluginHooks) requestPathKind {
	ctx := requestTranslationPathContext{
		from:    from,
		to:      to,
		model:   model,
		rawJSON: rawJSON,
		hooks:   hooks,
	}
	return defaultRequestTranslationPathPolicy().Select(ctx).kind
}

// TranslateNonStream converts a non-stream response directly through oagmsg.
func TranslateNonStream[From ~string, To ~string](ctx context.Context, fromValue From, toValue To, model string, originalRequestRawJSON, requestRawJSON, rawJSON []byte, _ *any) []byte {
	from := Format(fromValue)
	to := Format(toValue)
	hooks := currentPluginHooks()
	body := rawJSON
	if hooks != nil {
		body = hooks.NormalizeResponseBefore(ctx, from, to, model, originalRequestRawJSON, requestRawJSON, body, false)
	}
	translationContext := newRuntimeTranslationContext(from, to, model, originalRequestRawJSON, requestRawJSON, false)
	if sameResponseWireFormat(from, to) && !requiresRuntimeResponseToolMetadata(translationContext) {
		if from != to {
			body = unwrapResponsesEvent(body)
		}
		if to == FormatOpenAIResponse {
			selection := responsesModelSelection{model: translationContext.responsesModel, set: translationContext.responsesModelSet}
			if !selection.set {
				selection = providerModelSelection(body, from)
			}
			body = overrideResponsesModelSelection(body, selection, sameResponsesFamily(from, to))
		}
		if resolveFormat(to) == FormatOpenAI {
			body = SanitizeOpenAICompatibleResponse(body)
		}
		if hooks != nil {
			body = hooks.NormalizeResponseAfter(ctx, from, to, model, originalRequestRawJSON, requestRawJSON, body, false)
		}
		return body
	}
	if sameResponseWireFormat(from, to) {
		if completed, ok := translateRuntimeCompletedResponse(from, to, model, body, translationContext); ok {
			body = completed
			if hooks != nil {
				body = hooks.NormalizeResponseAfter(ctx, from, to, model, originalRequestRawJSON, requestRawJSON, body, false)
			}
			return body
		}
	}
	if response, isSSE, aggregateErr := aggregateSSEResponse(from, body, translationContext); isSSE {
		if aggregateErr == nil {
			restoreUnifiedResponseToolNames(response, translationContext)
			if target, ok := DefaultRegistry().Get(to); ok {
				formatModel := model
				omitEmptyModel := false
				if to == FormatOpenAIResponse && translationContext.responsesModelSet {
					formatModel = translationContext.responsesModel
				} else if to == FormatOpenAIResponse && translationContext.responsesModelNoRuntime {
					formatModel = ""
					omitEmptyModel = response.Model == ""
				}
				if formatted, formatErr := target.FormatResponse(response, formatModel); formatErr == nil {
					body = formatted
					if to == FormatOpenAIResponse && translationContext.responsesModelSet {
						selection := responsesModelSelection{model: translationContext.responsesModel, set: true}
						body = overrideResponsesModelSelection(body, selection, false)
					}
					if to == FormatOpenAIResponse && omitEmptyModel {
						body = omitEmptyResponsesModel(body)
					}
					if hooks != nil {
						body = hooks.NormalizeResponseAfter(ctx, from, to, model, originalRequestRawJSON, requestRawJSON, body, false)
					}
					return body
				}
			}
		}
		log.WithError(aggregateErr).Warnf("oagmsg: SSE aggregation %s to %s failed", from, to)
	}
	translated, err := DefaultRegistry().TranslateResponseWithContext(from, to, model, body, translationContext)
	if err == nil {
		body = translated
	} else if hooks != nil {
		if pluginBody, ok := hooks.TranslateResponse(ctx, from, to, model, originalRequestRawJSON, requestRawJSON, body, false); ok {
			body = pluginBody
		}
	} else {
		log.WithError(err).Warnf("oagmsg: non-stream translation %s to %s failed", from, to)
	}
	if hooks != nil {
		body = hooks.NormalizeResponseAfter(ctx, from, to, model, originalRequestRawJSON, requestRawJSON, body, false)
	}
	return body
}

// TranslateStream converts one upstream stream chunk directly through an oagmsg session.
func TranslateStream[From ~string, To ~string](ctx context.Context, fromValue From, toValue To, model string, originalRequestRawJSON, requestRawJSON, rawJSON []byte, param *any) [][]byte {
	from := Format(fromValue)
	to := Format(toValue)
	hooks := currentPluginHooks()
	body := rawJSON
	if hooks != nil {
		body = hooks.NormalizeResponseBefore(ctx, from, to, model, originalRequestRawJSON, requestRawJSON, body, true)
	}
	translationContext := newRuntimeTranslationContext(from, to, model, originalRequestRawJSON, requestRawJSON, true)
	if sameResponseWireFormat(from, to) && !requiresRuntimeResponseToolMetadata(translationContext) {
		if to == FormatOpenAIResponse {
			selection := responsesModelSelection{model: translationContext.responsesModel, set: translationContext.responsesModelSet}
			body = overrideResponsesModelSelection(body, selection, sameResponsesFamily(from, to))
		}
		return normalizeRuntimeStreamOutputs(hooks, ctx, from, to, model, originalRequestRawJSON, requestRawJSON, [][]byte{body})
	}
	if sameResponseWireFormat(from, to) && runtimeStreamSessionUninitialized(param) {
		if completed, ok := translateRuntimeCompletedResponseEvent(from, to, model, body, translationContext); ok {
			return normalizeRuntimeStreamOutputs(hooks, ctx, from, to, model, originalRequestRawJSON, requestRawJSON, [][]byte{completed})
		}
	}

	session, err := runtimeStreamSessionWithContext(from, to, model, translationContext, param)
	if err != nil {
		if hooks != nil {
			if pluginBody, ok := hooks.TranslateResponse(ctx, from, to, model, originalRequestRawJSON, requestRawJSON, body, true); ok {
				return normalizeRuntimeStreamOutputs(hooks, ctx, from, to, model, originalRequestRawJSON, requestRawJSON, [][]byte{pluginBody})
			}
		}
		log.WithError(err).Warnf("oagmsg: stream translation %s to %s failed", from, to)
		return [][]byte{body}
	}
	outputs, err := session.Translate(body)
	if err != nil {
		log.WithError(err).Warnf("oagmsg: stream chunk translation %s to %s failed", from, to)
		return [][]byte{body}
	}
	return normalizeRuntimeStreamOutputs(hooks, ctx, from, to, model, originalRequestRawJSON, requestRawJSON, outputs)
}

// TranslateTokenCount formats a token count for the downstream client format.
func TranslateTokenCount[From ~string, To ~string](_ context.Context, _ From, toValue To, count int64, rawJSON []byte) []byte {
	target, ok := DefaultRegistry().Get(Format(toValue))
	if !ok {
		return rawJSON
	}
	if output := FormatTokenCount(target, count); output != nil {
		return output
	}
	return rawJSON
}

func runtimeStreamSession(from, to Format, model string, originalRequestRawJSON, requestRawJSON []byte, param *any) (*StreamTranslateSession, error) {
	return runtimeStreamSessionWithContext(from, to, model, newRuntimeTranslationContext(from, to, model, originalRequestRawJSON, requestRawJSON, true), param)
}

func runtimeStreamSessionWithContext(from, to Format, model string, translationContext *TranslationContext, param *any) (*StreamTranslateSession, error) {
	if param != nil && *param != nil {
		if session, ok := (*param).(*StreamTranslateSession); ok {
			return session, nil
		}
	}
	session, err := NewStreamSession(from, to, model, WithContext(translationContext))
	if err != nil {
		return nil, err
	}
	if param != nil {
		*param = session
	}
	return session, nil
}

func newRuntimeTranslationContext(upstream, client Format, model string, originalRequestRawJSON, requestRawJSON []byte, stream bool) *TranslationContext {
	ctx := &TranslationContext{
		OriginalRequestJSON:   originalRequestRawJSON,
		translatedRequestJSON: requestRawJSON,
		IsStreaming:           stream,
		ModelName:             model,
		SourceFormat:          client,
		TargetFormat:          upstream,
	}
	ctx.responseTools = selectResponseToolDescriptorIndex(originalRequestRawJSON, requestRawJSON)
	if client == FormatOpenAIResponse {
		selection := resolveResponsesModelSelection(upstream, client, model, originalRequestRawJSON, requestRawJSON, stream)
		ctx.responsesModel = selection.model
		ctx.responsesModelSet = selection.set
		ctx.responsesModelNoRuntime = selection.noRuntime
	}
	if resolveFormat(upstream) == FormatCodex {
		metadata := buildRequestToolMetadataFromIndex(ctx.responseTools)
		ctx.ToolNameForward = metadata.toolNameForward
		ctx.ToolNameReverse = metadata.toolNameReverse
	}
	return ctx
}

func requiresRuntimeResponseToolMetadata(ctx *TranslationContext) bool {
	return ctx != nil && ctx.responseToolMetadataApplicable() && len(ctx.ToolNameForward) > 0 && len(ctx.ToolNameReverse) > 0
}

func runtimeStreamSessionUninitialized(param *any) bool {
	return param == nil || *param == nil
}

func translateRuntimeCompletedResponse(from, to Format, model string, body []byte, translationContext *TranslationContext) ([]byte, bool) {
	payload, isSSE := sseDataPayload(body)
	if !isSSE {
		payload = body
	}
	root := gjson.ParseBytes(payload)
	eventType := root.Get("type").String()
	if !strings.HasPrefix(eventType, "response.") {
		return nil, false
	}
	response := root.Get("response")
	if !response.Exists() || !response.IsObject() || len(response.Get("output").Array()) == 0 {
		return nil, false
	}
	translated, err := DefaultRegistry().TranslateResponseWithContext(from, to, model, []byte(response.Raw), translationContext)
	if err != nil {
		return nil, false
	}
	return translated, true
}

func translateRuntimeCompletedResponseEvent(from, to Format, model string, body []byte, translationContext *TranslationContext) ([]byte, bool) {
	payload, ok := sseDataPayload(body)
	if !ok {
		return nil, false
	}
	translated, ok := translateRuntimeCompletedResponse(from, to, model, payload, translationContext)
	if !ok {
		return nil, false
	}
	eventType := gjson.GetBytes(payload, "type").String()
	if eventType == "" {
		eventType = "response.completed"
	}
	updated, err := sjson.SetRawBytes(payload, "response", translated)
	if err != nil {
		return nil, false
	}
	return formatSSEEventData(eventType, updated), true
}

func normalizeRuntimeStreamOutputs(hooks PluginHooks, ctx context.Context, from, to Format, model string, originalRequestRawJSON, requestRawJSON []byte, outputs [][]byte) [][]byte {
	if hooks == nil {
		return outputs
	}
	for index := range outputs {
		outputs[index] = hooks.NormalizeResponseAfter(ctx, from, to, model, originalRequestRawJSON, requestRawJSON, outputs[index], true)
	}
	return outputs
}

func modelOrExisting(model, existing string) string {
	if model != "" {
		return model
	}
	return existing
}

func sameRequestWireFormat(from, to Format) bool {
	return from == to || sameResponsesFamily(from, to)
}

func sameResponsesFamily(from, to Format) bool {
	return isResponsesFamily(from) && isResponsesFamily(to)
}

func sameResponseWireFormat(from, to Format) bool {
	return from == to || sameResponsesFamily(from, to)
}

func isResponsesFamily(format Format) bool {
	return format == FormatOpenAIResponse || format == FormatCodex
}

func unwrapResponsesEvent(body []byte) []byte {
	response := gjson.GetBytes(body, "response")
	if response.Exists() && response.IsObject() && strings.HasPrefix(gjson.GetBytes(body, "type").String(), "response.") {
		return []byte(response.Raw)
	}
	return body
}

func setRuntimeModel(body []byte, model string) []byte {
	if model == "" || gjson.GetBytes(body, "model").String() == model {
		return body
	}
	updated, err := sjson.SetBytes(body, "model", model)
	if err != nil {
		return body
	}
	return updated
}

func applyCodexRequestMetadataForTarget(target Format, body []byte, rawRequests ...[]byte) []byte {
	if resolveFormat(target) != FormatCodex {
		return body
	}
	metadata := buildRequestToolMetadataFromRequests(rawRequests...)
	updated, err := applyCodexRequestToolMetadata(body, metadata)
	if err != nil {
		log.WithError(err).Warn("oagmsg: Codex request tool metadata application failed")
		return body
	}
	return updated
}

func applyOpenAIChatCodexRequestDefaults(source, target Format, body []byte) []byte {
	if source != FormatOpenAI || resolveFormat(target) != FormatCodex {
		return body
	}
	if gjson.GetBytes(body, "reasoning.effort").Exists() {
		return body
	}
	updated, err := sjson.SetBytes(body, "reasoning.effort", "medium")
	if err != nil {
		return body
	}
	return updated
}
