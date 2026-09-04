package oagmsg

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/util"
	log "github.com/sirupsen/logrus"
	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

const (
	xaiCustomToolType          = "custom"
	xaiFunctionToolType        = "function"
	xaiImageGenerationToolType = "image_generation"
	xaiNamespaceToolType       = "namespace"
	xaiToolSearchType          = "tool_search"
	xaiWebSearchToolType       = "web_search"
	xaiXSearchToolType         = "x_search"
	xaiCodexAppNamespaceName   = "codex_app"
	xaiAutomationUpdateTool    = "automation_update"
	xaiDefaultMaxTools         = 200
	xaiSafeFunctionParameters  = `{"type":"object","properties":{},"additionalProperties":true}`
)

var xaiXSearchToolJSON = []byte(`{"type":"x_search"}`)

var xaiGrokImageGenerationMinVersion = xaiGrokVersion{major: 4, minor: 6}

type xaiGrokVersion struct {
	major int
	minor int
}

func xaiSupportsNativeImageGeneration(model string) bool {
	name := strings.ToLower(strings.TrimSpace(model))
	if idx := strings.LastIndex(name, "/"); idx >= 0 {
		name = name[idx+1:]
	}
	if name == "" || !strings.HasPrefix(name, "grok-") {
		return false
	}
	rest := strings.TrimPrefix(name, "grok-")
	if rest == "4.20" || strings.HasPrefix(rest, "4.20-") {
		return false
	}
	ver, ok := xaiParseGrokVersionPrefix(rest)
	if !ok {
		return false
	}
	return xaiCompareGrokVersion(ver, xaiGrokImageGenerationMinVersion) >= 0
}

func xaiParseGrokVersionPrefix(rest string) (xaiGrokVersion, bool) {
	i := 0
	for i < len(rest) && rest[i] >= '0' && rest[i] <= '9' {
		i++
	}
	if i == 0 {
		return xaiGrokVersion{}, false
	}
	major, err := strconv.Atoi(rest[:i])
	if err != nil {
		return xaiGrokVersion{}, false
	}
	if i == len(rest) || rest[i] != '.' {
		return xaiGrokVersion{major: major, minor: -1}, true
	}
	j := i + 1
	for j < len(rest) && rest[j] >= '0' && rest[j] <= '9' {
		j++
	}
	if j == i+1 {
		return xaiGrokVersion{major: major, minor: -1}, true
	}
	minor, err := strconv.Atoi(rest[i+1 : j])
	if err != nil {
		return xaiGrokVersion{}, false
	}
	return xaiGrokVersion{major: major, minor: minor}, true
}

func xaiCompareGrokVersion(a, b xaiGrokVersion) int {
	if a.major != b.major {
		if a.major < b.major {
			return -1
		}
		return 1
	}
	aMinor := a.minor
	if aMinor < 0 {
		aMinor = 0
	}
	bMinor := b.minor
	if bMinor < 0 {
		bMinor = 0
	}
	if aMinor < bMinor {
		return -1
	}
	if aMinor > bMinor {
		return 1
	}
	return 0
}

// XAINamespaceToolRef records the public Responses identity of a tool whose
// name must be flattened for the xAI upstream endpoint.
type XAINamespaceToolRef struct {
	Namespace    string
	Name         string
	IsDispatcher bool
}

// XAIClientToolKey identifies a client-declared callable tool after the xAI
// request adapter has normalized custom declarations to functions.
type XAIClientToolKey struct {
	Namespace string
	Name      string
	ToolType  string
}

// XAIResponsesToolState carries request-derived identity needed to restore and
// filter xAI Responses events. Its fields stay private so callers cannot
// accidentally couple executor state to the adapter representation.
type XAIResponsesToolState struct {
	namespaceTools      map[string]XAINamespaceToolRef
	clientDeclaredTools map[XAIClientToolKey]struct{}
	shouldFold          bool
	restorer            *XAIResponsesNamespaceRestorer
}

// XAIResponsesToolOptions controls provider-specific request preparation while
// keeping the wire-shape adaptation inside OAGMsg.
type XAIResponsesToolOptions struct {
	WillInjectXSearch bool
	MaxTools          int
}

func xaiResponsesToolOptions(options []XAIResponsesToolOptions) XAIResponsesToolOptions {
	if len(options) == 0 {
		return XAIResponsesToolOptions{MaxTools: xaiDefaultMaxTools}
	}
	opts := options[0]
	if opts.MaxTools <= 0 {
		opts.MaxTools = xaiDefaultMaxTools
	}
	return opts
}

// PreserveXAIResponsesOutputControls restores controls accepted by xAI
// Responses after generic Codex target finalization has removed them.
func PreserveXAIResponsesOutputControls(body, source []byte, from Format) []byte {
	var maxOutputTokens gjson.Result
	switch from {
	case FormatOpenAI:
		maxOutputTokens = gjson.GetBytes(source, "max_completion_tokens")
		if !maxOutputTokens.Exists() || maxOutputTokens.Type == gjson.Null {
			maxOutputTokens = gjson.GetBytes(source, "max_tokens")
		}
	case FormatOpenAIResponse:
		maxOutputTokens = gjson.GetBytes(source, "max_output_tokens")
	default:
		return body
	}
	if maxOutputTokens.Exists() && maxOutputTokens.Type != gjson.Null {
		body, _ = sjson.SetRawBytes(body, "max_output_tokens", []byte(maxOutputTokens.Raw))
	}
	for _, field := range []string{"temperature", "top_p", "top_k"} {
		value := gjson.GetBytes(source, field)
		if value.Exists() && value.Type != gjson.Null {
			body, _ = sjson.SetRawBytes(body, field, []byte(value.Raw))
		}
	}
	return body
}

// PrepareXAIResponsesTools adapts Responses/Codex tool declarations to the xAI
// Responses subset and captures the metadata needed for downstream restoration.
func PrepareXAIResponsesTools(body []byte, options ...XAIResponsesToolOptions) ([]byte, *XAIResponsesToolState) {
	opts := xaiResponsesToolOptions(options)
	shouldFold := xaiShouldFoldNamespaceTools(body, opts.WillInjectXSearch, opts.MaxTools)
	state := &XAIResponsesToolState{
		namespaceTools:      collectXAINamespaceToolRefsWithFold(body, shouldFold),
		clientDeclaredTools: CollectXAIClientDeclaredToolKeys(body),
		shouldFold:          shouldFold,
	}
	state.restorer = NewXAIResponsesNamespaceRestorer(state.namespaceTools)
	body = normalizeXAIToolsWithFold(body, shouldFold)
	body = PromoteXAIAdditionalTools(body)
	body = normalizeXAINamespaceToolChoiceWithFold(body, shouldFold)
	body = NormalizeXAIForcedWebSearchToolChoice(body)
	body = PruneXAIOrphanedToolChoice(body)
	body = NormalizeXAIForcedImageGenerationToolChoice(body)
	body = NormalizeXAIToolChoiceForTools(body)
	body = state.ClampToolsLimit(body, opts.MaxTools)
	return body, state
}

// FinalizeXAIResponsesHistory adapts source Responses history items after any
// runtime replay/cache augmentation has completed.
func FinalizeXAIResponsesHistory(body []byte) []byte {
	return (&XAIResponsesToolState{
		shouldFold: xaiShouldFoldNamespaceTools(body, false, xaiDefaultMaxTools),
	}).FinalizeHistory(body)
}

// FinalizeXAIResponsesHistoryWithState adapts source Responses history using
// request-time tool metadata captured by PrepareXAIResponsesTools.
func FinalizeXAIResponsesHistoryWithState(body []byte, state *XAIResponsesToolState) []byte {
	if state == nil {
		return FinalizeXAIResponsesHistory(body)
	}
	return state.FinalizeHistory(body)
}

// FinalizeHistory adapts source Responses history using the same fold decision
// captured during request preparation.
func (s *XAIResponsesToolState) FinalizeHistory(body []byte) []byte {
	body = NormalizeXAIInputCustomToolCalls(body)
	if s == nil {
		return NormalizeXAIInputNamespaceToolCalls(body)
	}
	return normalizeXAIInputNamespaceToolCallsWithFold(body, s.shouldFold)
}

// RestoreResponse restores namespace-qualified tool call names in an xAI event.
func (s *XAIResponsesToolState) RestoreResponse(data []byte) []byte {
	if s == nil {
		return data
	}
	if s.restorer == nil {
		s.restorer = NewXAIResponsesNamespaceRestorer(s.namespaceTools)
	}
	return s.restorer.Restore(data)
}

// ClampToolsLimit preserves namespace dispatchers first when trimming a request
// to xAI's maximum tools payload.
func (s *XAIResponsesToolState) ClampToolsLimit(body []byte, maxTools int) []byte {
	var refs map[string]XAINamespaceToolRef
	if s != nil {
		refs = s.namespaceTools
	}
	return ClampXAIResponsesToolsLimit(body, maxTools, refs)
}

// NewResponseFilter creates the stateful xAI internal-search event filter.
func (s *XAIResponsesToolState) NewResponseFilter(enabled bool) *XAIResponsesResponseFilter {
	var declared map[XAIClientToolKey]struct{}
	if s != nil {
		declared = s.clientDeclaredTools
	}
	return NewXAIResponsesResponseFilter(enabled, declared)
}

// XAIResponsesRequestHasNativeXSearch reports whether a prepared request uses
// xAI's server-side search tool.
func XAIResponsesRequestHasNativeXSearch(body []byte) bool {
	if gjson.GetBytes(body, `tools.#(type=="x_search")`).Exists() {
		return true
	}
	return len(gjson.GetBytes(body, `input.#(type=="additional_tools")#.tools.#(type=="x_search")`).Array()) > 0
}

// EnsureXAIResponsesNativeXSearchTool appends xAI's native X Search tool and
// keeps allowed_tools choices synchronized with the injected tool.
func EnsureXAIResponsesNativeXSearchTool(body []byte) []byte {
	if !gjson.ValidBytes(body) {
		return body
	}
	if !XAIResponsesRequestHasNativeXSearch(body) {
		tools := gjson.GetBytes(body, "tools")
		if !tools.Exists() || !tools.IsArray() {
			body, _ = sjson.SetRawBytes(body, "tools", []byte(`[{"type":"x_search"}]`))
		} else {
			body, _ = sjson.SetRawBytes(body, "tools.-1", xaiXSearchToolJSON)
		}
	}
	return ensureXAIResponsesNativeXSearchAllowedTools(body)
}

func ensureXAIResponsesNativeXSearchAllowedTools(body []byte) []byte {
	choice := gjson.GetBytes(body, "tool_choice")
	if !choice.IsObject() || choice.Get("type").String() != "allowed_tools" {
		return body
	}
	allowed := choice.Get("tools")
	if !allowed.Exists() || !allowed.IsArray() {
		body, _ = sjson.SetRawBytes(body, "tool_choice.tools", []byte(`[{"type":"x_search"}]`))
		return body
	}
	for _, tool := range allowed.Array() {
		if strings.TrimSpace(tool.Get("type").String()) == xaiXSearchToolType {
			return body
		}
	}
	body, _ = sjson.SetRawBytes(body, "tool_choice.tools.-1", xaiXSearchToolJSON)
	return body
}

// PruneXAIOrphanedToolChoice removes choices that no longer resolve after xAI
// request tool normalization.
func PruneXAIOrphanedToolChoice(body []byte) []byte {
	if !gjson.ValidBytes(body) {
		return body
	}
	choice := gjson.GetBytes(body, "tool_choice")
	if !choice.Exists() || choice.Type == gjson.String {
		return body
	}
	if !choice.IsObject() {
		return body
	}
	available := collectXAIAvailableToolChoiceKeys(body)
	choiceType := strings.TrimSpace(choice.Get("type").String())
	if choiceType == "allowed_tools" {
		return pruneXAIAllowedToolsChoice(body, available)
	}
	if choiceType != "" && !xaiToolChoiceMatchesAvailable(choice, available) {
		body, _ = sjson.DeleteBytes(body, "tool_choice")
	}
	return body
}

type xaiToolChoiceKey struct {
	toolType string
	name     string
}

func pruneXAIAllowedToolsChoice(body []byte, available map[xaiToolChoiceKey]struct{}) []byte {
	allowed := gjson.GetBytes(body, "tool_choice.tools")
	if !allowed.Exists() || !allowed.IsArray() {
		body, _ = sjson.DeleteBytes(body, "tool_choice")
		return body
	}
	items := allowed.Array()
	filtered := make([][]byte, 0, len(items))
	changed := false
	for _, tool := range items {
		if !xaiToolChoiceMatchesAvailable(tool, available) {
			changed = true
			continue
		}
		filtered = append(filtered, []byte(tool.Raw))
	}
	if !changed {
		return body
	}
	if len(filtered) == 0 {
		body, _ = sjson.DeleteBytes(body, "tool_choice")
		return body
	}
	body, _ = sjson.SetRawBytes(body, "tool_choice.tools", joinRawJSONArray(filtered))
	return body
}

func collectXAIAvailableToolChoiceKeys(body []byte) map[xaiToolChoiceKey]struct{} {
	keys := make(map[xaiToolChoiceKey]struct{})
	collect := func(tools gjson.Result) {
		if !tools.IsArray() {
			return
		}
		for _, tool := range tools.Array() {
			toolType := strings.TrimSpace(tool.Get("type").String())
			if toolType == "" {
				continue
			}
			key := xaiToolChoiceKey{toolType: toolType}
			if toolType == xaiFunctionToolType || toolType == xaiCustomToolType {
				key.name = strings.TrimSpace(tool.Get("name").String())
				if key.name == "" {
					continue
				}
			}
			keys[key] = struct{}{}
		}
	}
	collect(gjson.GetBytes(body, "tools"))
	for _, item := range gjson.GetBytes(body, "input").Array() {
		if item.Get("type").String() == "additional_tools" {
			collect(item.Get("tools"))
		}
	}
	return keys
}

func xaiToolChoiceMatchesAvailable(choice gjson.Result, available map[xaiToolChoiceKey]struct{}) bool {
	toolType := strings.TrimSpace(choice.Get("type").String())
	if toolType == "" {
		return false
	}
	key := xaiToolChoiceKey{toolType: toolType}
	if toolType == xaiFunctionToolType || toolType == xaiCustomToolType {
		key.name = strings.TrimSpace(choice.Get("name").String())
		if key.name == "" {
			return false
		}
	}
	_, ok := available[key]
	return ok
}

// NormalizeXAIForcedWebSearchToolChoice rewrites Codex's hosted-tool choice
// into the allowed_tools form accepted by xAI's ModelToolChoice schema.
func NormalizeXAIForcedWebSearchToolChoice(body []byte) []byte {
	return normalizeXAIForcedHostedToolChoice(body, normalizeXAIForcedWebSearchChoiceTool)
}

// NormalizeXAIForcedImageGenerationToolChoice rewrites image_generation choices
// into the ModelToolChoice variants accepted by xAI chat-proxy.
func NormalizeXAIForcedImageGenerationToolChoice(body []byte) []byte {
	choice := gjson.GetBytes(body, "tool_choice")
	if !choice.IsObject() {
		return body
	}
	choiceType := strings.TrimSpace(choice.Get("type").String())
	if choiceType == xaiImageGenerationToolType {
		body = xaiKeepOnlyImageGenerationTools(body)
		return xaiSetToolChoiceString(body, "required")
	}
	if choiceType != "allowed_tools" {
		return body
	}
	allowed := choice.Get("tools")
	if !allowed.IsArray() {
		return body
	}
	filtered := make([][]byte, 0, len(allowed.Array()))
	stripped := false
	for _, tool := range allowed.Array() {
		if strings.TrimSpace(tool.Get("type").String()) == xaiImageGenerationToolType {
			stripped = true
			continue
		}
		filtered = append(filtered, []byte(tool.Raw))
	}
	if !stripped {
		return body
	}
	if len(filtered) == 0 {
		mode := strings.TrimSpace(choice.Get("mode").String())
		if mode != "auto" {
			mode = "required"
		}
		body = xaiKeepOnlyImageGenerationTools(body)
		return xaiSetToolChoiceString(body, mode)
	}
	updated, err := sjson.SetRawBytes(body, "tool_choice.tools", joinRawJSONArray(filtered))
	if err != nil {
		return body
	}
	return updated
}

func normalizeXAIForcedHostedToolChoice(body []byte, normalize func(gjson.Result) ([]byte, bool)) []byte {
	choice := gjson.GetBytes(body, "tool_choice")
	normalizedTool, ok := normalize(choice)
	if !ok {
		return body
	}
	allowedChoice := []byte(`{"type":"allowed_tools","mode":"required","tools":[]}`)
	allowedChoice, err := sjson.SetRawBytes(allowedChoice, "tools.-1", normalizedTool)
	if err != nil {
		return body
	}
	updated, err := sjson.SetRawBytes(body, "tool_choice", allowedChoice)
	if err != nil {
		return body
	}
	return updated
}

func normalizeXAIForcedWebSearchChoiceTool(choice gjson.Result) ([]byte, bool) {
	if !choice.IsObject() {
		return nil, false
	}
	choiceType := strings.TrimSpace(choice.Get("type").String())
	switch choiceType {
	case xaiWebSearchToolType, "web_search_20250305", "web_search_20260209":
		return []byte(`{"type":"web_search"}`), true
	case "tool":
		if strings.TrimSpace(choice.Get("name").String()) == xaiWebSearchToolType {
			return []byte(`{"type":"web_search"}`), true
		}
	}
	return nil, false
}

func normalizeXAIForcedImageGenerationChoiceTool(choice gjson.Result) ([]byte, bool) {
	if !choice.IsObject() {
		return nil, false
	}
	choiceType := strings.TrimSpace(choice.Get("type").String())
	switch choiceType {
	case xaiImageGenerationToolType:
		return []byte(`{"type":"image_generation"}`), true
	case "tool":
		if strings.TrimSpace(choice.Get("name").String()) == xaiImageGenerationToolType {
			return []byte(`{"type":"image_generation"}`), true
		}
	}
	return nil, false
}

func xaiKeepOnlyImageGenerationTools(body []byte) []byte {
	tools := gjson.GetBytes(body, "tools")
	if !tools.IsArray() {
		return body
	}
	kept := make([][]byte, 0, 1)
	for _, tool := range tools.Array() {
		if strings.TrimSpace(tool.Get("type").String()) == xaiImageGenerationToolType {
			kept = append(kept, []byte(tool.Raw))
		}
	}
	if len(kept) == 0 || len(kept) == len(tools.Array()) {
		return body
	}
	updated, err := sjson.SetRawBytes(body, "tools", joinRawJSONArray(kept))
	if err != nil {
		return body
	}
	return updated
}

// XAIResponsesToolChoiceRequiresImageGenerationOnly reports whether a prepared
// payload restricts generation to the native image tool.
func XAIResponsesToolChoiceRequiresImageGenerationOnly(body []byte) bool {
	choice := gjson.GetBytes(body, "tool_choice")
	if choice.Type != gjson.String {
		return false
	}
	switch choice.String() {
	case "required", "auto":
	default:
		return false
	}
	tools := gjson.GetBytes(body, "tools")
	if !tools.IsArray() || len(tools.Array()) == 0 {
		return false
	}
	for _, tool := range tools.Array() {
		if strings.TrimSpace(tool.Get("type").String()) != xaiImageGenerationToolType {
			return false
		}
	}
	return true
}

func xaiSetToolChoiceString(body []byte, value string) []byte {
	updated, err := sjson.SetBytes(body, "tool_choice", value)
	if err != nil {
		return body
	}
	return updated
}

// NormalizeXAITools flattens namespace declarations and removes request tool
// kinds unsupported by xAI Responses.
func NormalizeXAITools(body []byte) []byte {
	return normalizeXAIToolsWithFold(body, xaiShouldFoldNamespaceTools(body, false, xaiDefaultMaxTools))
}

func xaiCountFlattenedTools(tools gjson.Result) int {
	if !tools.Exists() || !tools.IsArray() {
		return 0
	}
	count := 0
	for _, tool := range tools.Array() {
		switch tool.Get("type").String() {
		case xaiNamespaceToolType:
			if nestedTools := tool.Get("tools"); nestedTools.IsArray() {
				count += len(nestedTools.Array())
			} else {
				count++
			}
		case xaiToolSearchType:
		default:
			count++
		}
	}
	return count
}

func xaiTotalFlattenedToolsCount(body []byte, willInjectXSearch bool) int {
	count := xaiCountFlattenedTools(gjson.GetBytes(body, "tools"))
	for _, item := range gjson.GetBytes(body, "input").Array() {
		if item.Get("type").String() == "additional_tools" {
			count += xaiCountFlattenedTools(item.Get("tools"))
		}
	}
	if willInjectXSearch && !XAIResponsesRequestHasNativeXSearch(body) && !XAIResponsesToolChoiceRequiresImageGenerationOnly(body) {
		count++
	}
	return count
}

func xaiShouldFoldNamespaceTools(body []byte, willInjectXSearch bool, maxTools int) bool {
	if maxTools <= 0 {
		maxTools = xaiDefaultMaxTools
	}
	return xaiTotalFlattenedToolsCount(body, willInjectXSearch) > maxTools
}

func buildXAINamespaceDispatcherTool(tool gjson.Result) []byte {
	namespaceName := strings.TrimSpace(tool.Get("name").String())
	if namespaceName == "" {
		return nil
	}
	description := strings.TrimSpace(tool.Get("description").String())

	var toolNames []string
	var toolDescriptions []string
	if nestedTools := tool.Get("tools"); nestedTools.IsArray() {
		for _, child := range nestedTools.Array() {
			childName := strings.TrimSpace(child.Get("name").String())
			if childName == "" {
				continue
			}
			toolNames = append(toolNames, childName)
			childDescription := strings.TrimSpace(child.Get("description").String())

			params := child.Get("parameters")
			if !params.Exists() {
				params = child.Get("input_schema")
			}

			var paramString string
			if params.Exists() && params.Raw != "" {
				rawParams := strings.TrimSpace(params.Raw)
				if rawParams != "" && rawParams != "{}" && rawParams != `{"type":"object","properties":{}}` {
					inlined := util.InlineLocalRefs(rawParams)
					if gjson.Valid(inlined) {
						cleaned := []byte(inlined)
						if gjson.GetBytes(cleaned, "$defs").Exists() {
							cleaned, _ = sjson.DeleteBytes(cleaned, "$defs")
						}
						if gjson.GetBytes(cleaned, "definitions").Exists() {
							cleaned, _ = sjson.DeleteBytes(cleaned, "definitions")
						}
						paramString = string(cleaned)
					} else {
						paramString = inlined
					}
				}
			}

			var entry string
			if childDescription != "" {
				if paramString != "" {
					entry = fmt.Sprintf("- %s: %s\n  Parameters: %s", childName, childDescription, paramString)
				} else {
					entry = fmt.Sprintf("- %s: %s", childName, childDescription)
				}
			} else if paramString != "" {
				entry = fmt.Sprintf("- %s\n  Parameters: %s", childName, paramString)
			} else {
				entry = fmt.Sprintf("- %s", childName)
			}
			toolDescriptions = append(toolDescriptions, entry)
		}
	}

	fullDescription := description
	if len(toolDescriptions) > 0 {
		catalog := "Available tools in this namespace:\n" + strings.Join(toolDescriptions, "\n")
		if fullDescription != "" {
			fullDescription += "\n\n" + catalog
		} else {
			fullDescription = fmt.Sprintf("Tools in namespace %s.\n\n%s", namespaceName, catalog)
		}
	} else if fullDescription == "" {
		fullDescription = fmt.Sprintf("Tools in namespace %s.", namespaceName)
	}

	nameProperty := map[string]any{
		"type":        "string",
		"description": fmt.Sprintf("Child tool name to execute in namespace %s", namespaceName),
	}
	if len(toolNames) > 0 {
		nameProperty["enum"] = toolNames
	}

	dispatcher := map[string]any{
		"type":        xaiFunctionToolType,
		"name":        namespaceName,
		"description": fullDescription,
		"parameters": map[string]any{
			"type": "object",
			"properties": map[string]any{
				"name": nameProperty,
				"arguments": map[string]any{
					"type":                 "object",
					"description":          "Arguments object matching the parameter schema of the selected child tool",
					"additionalProperties": true,
				},
			},
			"required": []string{"name"},
		},
	}

	raw, err := json.Marshal(dispatcher)
	if err != nil {
		return nil
	}
	return raw
}

func normalizeXAIToolsWithFold(body []byte, shouldFold bool) []byte {
	if !gjson.ValidBytes(body) {
		return body
	}
	original := body
	keepImageGeneration := xaiSupportsNativeImageGeneration(gjson.GetBytes(body, "model").String())
	normalizeAtPath := func(path string) bool {
		tools := gjson.GetBytes(body, path)
		if !tools.Exists() || !tools.IsArray() {
			return true
		}
		filtered, changed, ok := normalizeXAIToolArray(tools, keepImageGeneration, shouldFold)
		if !ok {
			return false
		}
		if !changed {
			return true
		}
		updated, err := sjson.SetRawBytes(body, path, filtered)
		if err != nil {
			return false
		}
		body = updated
		return true
	}
	if !normalizeAtPath("tools") {
		return original
	}
	for index, item := range gjson.GetBytes(body, "input").Array() {
		if item.Get("type").String() == "additional_tools" && !normalizeAtPath(fmt.Sprintf("input.%d.tools", index)) {
			return original
		}
	}
	return body
}

// PromoteXAIAdditionalTools moves Responses Lite declarations to top-level
// tools because xAI does not accept additional_tools input items.
func PromoteXAIAdditionalTools(body []byte) []byte {
	if !gjson.ValidBytes(body) {
		return body
	}
	input := gjson.GetBytes(body, "input")
	if !input.IsArray() {
		return body
	}
	inputItems := input.Array()
	remaining := make([]json.RawMessage, 0, len(inputItems))
	promoted := make([]json.RawMessage, 0)
	for _, item := range inputItems {
		if item.Get("type").String() != "additional_tools" {
			remaining = append(remaining, json.RawMessage(item.Raw))
			continue
		}
		for _, tool := range item.Get("tools").Array() {
			promoted = append(promoted, json.RawMessage(tool.Raw))
		}
	}
	if len(remaining) == len(inputItems) {
		return body
	}
	rawInput, err := json.Marshal(remaining)
	if err != nil {
		return body
	}
	updated, err := sjson.SetRawBytes(body, "input", rawInput)
	if err != nil {
		return body
	}
	if len(promoted) == 0 {
		return updated
	}
	tools := make([]json.RawMessage, 0, len(gjson.GetBytes(updated, "tools").Array())+len(promoted))
	for _, tool := range gjson.GetBytes(updated, "tools").Array() {
		tools = append(tools, json.RawMessage(tool.Raw))
	}
	tools = append(tools, promoted...)
	rawTools, err := json.Marshal(tools)
	if err != nil {
		return body
	}
	result, err := sjson.SetRawBytes(updated, "tools", rawTools)
	if err != nil {
		return body
	}
	return result
}

func normalizeXAIToolArray(tools gjson.Result, keepImageGeneration, shouldFold bool) ([]byte, bool, bool) {
	toolItems := tools.Array()
	filtered := make([][]byte, 0, len(toolItems))
	changed := false
	for _, tool := range toolItems {
		toolType := tool.Get("type").String()
		if toolType == xaiNamespaceToolType {
			changed = true
			if shouldFold {
				if dispatcher := buildXAINamespaceDispatcherTool(tool); len(dispatcher) > 0 {
					filtered = append(filtered, dispatcher)
				}
				continue
			}
			namespaceName := tool.Get("name").String()
			if namespaceTools := tool.Get("tools"); namespaceTools.IsArray() {
				for _, nested := range namespaceTools.Array() {
					raw, nestedChanged, ok := NormalizeXAITool(nested, namespaceName, keepImageGeneration)
					if !ok {
						return nil, false, false
					}
					changed = changed || nestedChanged
					if len(raw) > 0 {
						filtered = append(filtered, raw)
					}
				}
			}
			continue
		}
		raw, toolChanged, ok := NormalizeXAITool(tool, "", keepImageGeneration)
		if !ok {
			return nil, false, false
		}
		changed = changed || toolChanged
		if len(raw) > 0 {
			filtered = append(filtered, raw)
		}
	}
	if !changed {
		return nil, false, true
	}
	return joinRawJSONArray(filtered), true, true
}

func xaiHasFunctionToolNamed(body []byte, name string) bool {
	if name == "" {
		return false
	}
	for _, tool := range gjson.GetBytes(body, "tools").Array() {
		if tool.Get("type").String() == xaiFunctionToolType && tool.Get("name").String() == name {
			return true
		}
	}
	for _, item := range gjson.GetBytes(body, "input").Array() {
		if item.Get("type").String() != "additional_tools" {
			continue
		}
		for _, tool := range item.Get("tools").Array() {
			if tool.Get("type").String() == xaiFunctionToolType && tool.Get("name").String() == name {
				return true
			}
		}
	}
	return false
}

// ClampXAIResponsesToolsLimit trims tools to the xAI limit while preserving
// folded namespace dispatcher tools first.
func ClampXAIResponsesToolsLimit(body []byte, maxTools int, refs map[string]XAINamespaceToolRef) []byte {
	if maxTools <= 0 {
		maxTools = xaiDefaultMaxTools
	}
	tools := gjson.GetBytes(body, "tools")
	if !tools.IsArray() || len(tools.Array()) <= maxTools {
		return body
	}
	var dispatchers []json.RawMessage
	var regular []json.RawMessage
	for _, tool := range tools.Array() {
		name := strings.TrimSpace(tool.Get("name").String())
		if ref, ok := refs[name]; ok && ref.IsDispatcher {
			dispatchers = append(dispatchers, json.RawMessage(tool.Raw))
			continue
		}
		regular = append(regular, json.RawMessage(tool.Raw))
	}

	capped := make([]json.RawMessage, 0, maxTools)
	if len(dispatchers) >= maxTools {
		capped = append(capped, dispatchers[:maxTools]...)
	} else {
		capped = append(capped, dispatchers...)
		remaining := maxTools - len(dispatchers)
		if len(regular) > remaining {
			capped = append(capped, regular[:remaining]...)
		} else {
			capped = append(capped, regular...)
		}
	}

	raw, err := json.Marshal(capped)
	if err != nil {
		return body
	}
	updated, err := sjson.SetRawBytes(body, "tools", raw)
	if err != nil {
		return body
	}
	updated = PruneXAIOrphanedToolChoice(updated)
	return NormalizeXAIToolChoiceForTools(updated)
}

// NormalizeXAIToolChoiceForTools removes choices that xAI rejects when no
// callable tools remain.
func NormalizeXAIToolChoiceForTools(body []byte) []byte {
	tools := gjson.GetBytes(body, "tools")
	hasTools := tools.Exists() && tools.IsArray() && len(tools.Array()) > 0
	if !hasTools {
		for _, item := range gjson.GetBytes(body, "input").Array() {
			additional := item.Get("tools")
			if item.Get("type").String() == "additional_tools" && additional.IsArray() && len(additional.Array()) > 0 {
				hasTools = true
				break
			}
		}
	}
	if hasTools {
		return body
	}
	for _, path := range []string{"tools", "tool_choice", "parallel_tool_calls"} {
		if gjson.GetBytes(body, path).Exists() {
			body, _ = sjson.DeleteBytes(body, path)
		}
	}
	return body
}

// NormalizeXAINamespaceToolChoice qualifies namespaced choices to match the
// flattened declarations sent upstream.
func NormalizeXAINamespaceToolChoice(body []byte) []byte {
	return normalizeXAINamespaceToolChoiceWithFold(body, xaiShouldFoldNamespaceTools(body, false, xaiDefaultMaxTools))
}

func normalizeXAINamespaceToolChoiceWithFold(body []byte, shouldFold bool) []byte {
	if !gjson.ValidBytes(body) {
		return body
	}
	original := body
	normalizeAtPath := func(path string) bool {
		choice := gjson.GetBytes(body, path)
		if !choice.IsObject() || choice.Get("type").String() != xaiFunctionToolType {
			return true
		}
		namespaceName := strings.TrimSpace(choice.Get("namespace").String())
		toolName := strings.TrimSpace(choice.Get("name").String())
		if namespaceName == "" {
			return true
		}
		qualified := QualifyXAINamespaceToolName(namespaceName, toolName)
		targetName := ""
		switch {
		case xaiHasFunctionToolNamed(body, namespaceName):
			targetName = namespaceName
		case xaiHasFunctionToolNamed(body, qualified):
			targetName = qualified
		case shouldFold:
			targetName = namespaceName
		default:
			targetName = qualified
		}
		if targetName == "" {
			return true
		}
		updated, err := sjson.SetBytes(body, path+".name", targetName)
		if err != nil {
			return false
		}
		updated, err = sjson.DeleteBytes(updated, path+".namespace")
		if err != nil {
			return false
		}
		body = updated
		return true
	}
	if !normalizeAtPath("tool_choice") {
		return original
	}
	for index := range gjson.GetBytes(body, "tool_choice.tools").Array() {
		if !normalizeAtPath(fmt.Sprintf("tool_choice.tools.%d", index)) {
			return original
		}
	}
	return body
}

// NormalizeXAITool adapts one Responses tool declaration to xAI's accepted shape.
func NormalizeXAITool(tool gjson.Result, namespaceName string, keepImageGenerationOpt ...bool) ([]byte, bool, bool) {
	keepImageGeneration := len(keepImageGenerationOpt) > 0 && keepImageGenerationOpt[0]
	toolType := tool.Get("type").String()
	changed := false
	if toolType == xaiToolSearchType || (toolType == xaiImageGenerationToolType && !keepImageGeneration) ||
		(toolType == xaiCustomToolType && tool.Get("name").String() == "apply_patch") {
		return nil, true, true
	}
	raw := []byte(tool.Raw)
	schemaTool := tool
	if toolType == xaiFunctionToolType || toolType == xaiCustomToolType {
		if rawParams := schemaTool.Get("parameters"); rawParams.Exists() {
			inlinedParams := util.InlineLocalRefs(rawParams.Raw)
			if inlinedParams != rawParams.Raw {
				if updated, err := sjson.SetRawBytes(raw, "parameters", []byte(inlinedParams)); err == nil {
					if gjson.GetBytes(updated, "parameters.$defs").Exists() {
						updated, _ = sjson.DeleteBytes(updated, "parameters.$defs")
					}
					if gjson.GetBytes(updated, "parameters.definitions").Exists() {
						updated, _ = sjson.DeleteBytes(updated, "parameters.definitions")
					}
					raw = updated
					schemaTool = gjson.ParseBytes(raw)
					changed = true
				}
			}
		}
		updated, schemaChanged, ok := normalizeXAIObjectRootUnionBranchTypes(raw)
		if !ok {
			return nil, false, false
		}
		raw = updated
		if schemaChanged {
			schemaTool = gjson.ParseBytes(raw)
			changed = true
			log.Debugf("oagmsg: xai added object types to root union branches for tool %s.%s", namespaceName, tool.Get("name").String())
		}
	}
	if toolType == xaiCustomToolType {
		updated, err := sjson.SetBytes(raw, "type", xaiFunctionToolType)
		if err != nil {
			return nil, false, false
		}
		raw, toolType, changed = updated, xaiFunctionToolType, true
	}
	if toolType == "web_search_20250305" || toolType == "web_search_20260209" {
		updated, err := sjson.SetBytes(raw, "type", xaiWebSearchToolType)
		if err != nil {
			return nil, false, false
		}
		raw, toolType, changed = updated, xaiWebSearchToolType, true
	}
	if toolType == xaiWebSearchToolType && tool.Get("external_web_access").Exists() {
		updated, err := sjson.DeleteBytes(raw, "external_web_access")
		if err != nil {
			return nil, false, false
		}
		raw, changed = updated, true
	}
	if toolType == xaiFunctionToolType && !schemaTool.Get("parameters").Exists() {
		updated, err := sjson.SetRawBytes(raw, "parameters", []byte(`{"type":"object","properties":{}}`))
		if err != nil {
			return nil, false, false
		}
		raw, changed = updated, true
	}
	if toolType == xaiFunctionToolType && XAIFunctionParametersNeedSimplification(schemaTool, namespaceName) {
		updated, err := sjson.SetRawBytes(raw, "parameters", []byte(xaiSafeFunctionParameters))
		if err != nil {
			return nil, false, false
		}
		raw = updated
		if tool.Get("strict").Bool() {
			updated, err = sjson.SetBytes(raw, "strict", false)
			if err != nil {
				return nil, false, false
			}
			raw = updated
		}
		changed = true
		log.Debugf("oagmsg: xai simplified parameters for tool %s.%s", namespaceName, tool.Get("name").String())
	}
	if toolType == xaiFunctionToolType && strings.TrimSpace(namespaceName) != "" {
		qualified := QualifyXAINamespaceToolName(namespaceName, tool.Get("name").String())
		if qualified == "" {
			return nil, false, false
		}
		updated, err := sjson.SetBytes(raw, "name", qualified)
		if err != nil {
			return nil, false, false
		}
		raw, changed = updated, true
	}
	return raw, changed, true
}

// QualifyXAINamespaceToolName creates the flat upstream name for a Responses namespace child.
func QualifyXAINamespaceToolName(namespaceName, toolName string) string {
	namespaceName = strings.TrimSpace(namespaceName)
	toolName = strings.TrimSpace(toolName)
	if namespaceName == "" || toolName == "" || strings.HasPrefix(toolName, "mcp__") {
		return toolName
	}
	prefix := namespaceName
	if !strings.HasSuffix(prefix, "__") {
		prefix += "__"
	}
	if strings.HasPrefix(toolName, prefix) {
		return toolName
	}
	return prefix + toolName
}

// CollectXAINamespaceToolRefs captures namespace identities before flattening.
func CollectXAINamespaceToolRefs(body []byte) map[string]XAINamespaceToolRef {
	return collectXAINamespaceToolRefsWithFold(body, xaiShouldFoldNamespaceTools(body, false, xaiDefaultMaxTools))
}

func collectXAINamespaceToolRefsWithFold(body []byte, shouldFold bool) map[string]XAINamespaceToolRef {
	refs := make(map[string]XAINamespaceToolRef)
	collect := func(tools gjson.Result) {
		for _, tool := range tools.Array() {
			if tool.Get("type").String() != xaiNamespaceToolType {
				continue
			}
			namespaceName := strings.TrimSpace(tool.Get("name").String())
			if namespaceName == "" {
				continue
			}
			if shouldFold {
				refs[namespaceName] = XAINamespaceToolRef{Namespace: namespaceName, IsDispatcher: true}
			}
			for _, nested := range tool.Get("tools").Array() {
				toolName := strings.TrimSpace(nested.Get("name").String())
				qualified := QualifyXAINamespaceToolName(namespaceName, toolName)
				if qualified != "" {
					refs[qualified] = XAINamespaceToolRef{Namespace: namespaceName, Name: toolName}
				}
			}
		}
	}
	collect(gjson.GetBytes(body, "tools"))
	for _, item := range gjson.GetBytes(body, "input").Array() {
		if item.Get("type").String() == "additional_tools" {
			collect(item.Get("tools"))
		}
	}
	return refs
}

// NormalizeXAIInputCustomToolCalls converts custom history items into the
// function-call subset accepted by xAI Responses.
func NormalizeXAIInputCustomToolCalls(body []byte) []byte {
	input := gjson.GetBytes(body, "input")
	if !input.IsArray() {
		return body
	}
	changed := false
	items := make([]json.RawMessage, 0, len(input.Array()))
	for _, item := range input.Array() {
		var normalized []byte
		switch item.Get("type").String() {
		case "custom_tool_call":
			callID := strings.TrimSpace(item.Get("call_id").String())
			name := strings.TrimSpace(item.Get("name").String())
			if callID == "" || name == "" {
				changed = true
				continue
			}
			normalized = []byte(`{"type":"function_call"}`)
			normalized, _ = sjson.SetBytes(normalized, "call_id", callID)
			normalized, _ = sjson.SetBytes(normalized, "name", name)
			normalized, _ = sjson.SetBytes(normalized, "arguments", xaiCustomToolCallArguments(item.Get("input")))
		case "custom_tool_call_output":
			callID := strings.TrimSpace(item.Get("call_id").String())
			if callID == "" {
				changed = true
				continue
			}
			normalized = []byte(`{"type":"function_call_output"}`)
			normalized, _ = sjson.SetBytes(normalized, "call_id", callID)
			normalized, _ = sjson.SetBytes(normalized, "output", xaiCustomToolCallOutput(item.Get("output")))
		default:
			items = append(items, json.RawMessage(item.Raw))
			continue
		}
		items = append(items, json.RawMessage(normalized))
		changed = true
	}
	if !changed {
		return body
	}
	rawInput, err := json.Marshal(items)
	if err != nil {
		return body
	}
	updated, err := sjson.SetRawBytes(body, "input", rawInput)
	if err != nil {
		return body
	}
	return updated
}

func xaiCustomToolCallArguments(input gjson.Result) string {
	if !input.Exists() {
		return "{}"
	}
	if input.Type == gjson.String {
		text := input.String()
		trimmed := strings.TrimSpace(text)
		if gjson.Valid(trimmed) && gjson.Parse(trimmed).IsObject() {
			return gjson.Parse(trimmed).Raw
		}
		encoded, err := json.Marshal(text)
		if err != nil {
			return "{}"
		}
		return `{"input":` + string(encoded) + `}`
	}
	if input.IsObject() {
		return input.Raw
	}
	if input.Raw != "" {
		return `{"input":` + input.Raw + `}`
	}
	return "{}"
}

func xaiCustomToolCallOutput(output gjson.Result) string {
	if !output.Exists() {
		return ""
	}
	if output.Type == gjson.String {
		return output.String()
	}
	return output.Raw
}

// CollectXAIClientDeclaredToolKeys records the effective upstream identities of
// client function/custom declarations before namespace flattening.
func CollectXAIClientDeclaredToolKeys(body []byte) map[XAIClientToolKey]struct{} {
	keys := make(map[XAIClientToolKey]struct{})
	collect := func(tools gjson.Result) {
		for _, tool := range tools.Array() {
			switch toolType := strings.TrimSpace(tool.Get("type").String()); toolType {
			case xaiNamespaceToolType:
				namespaceName := strings.TrimSpace(tool.Get("name").String())
				if namespaceName == "" {
					continue
				}
				for _, nested := range tool.Get("tools").Array() {
					nestedType := strings.TrimSpace(nested.Get("type").String())
					if nestedType != xaiFunctionToolType && nestedType != xaiCustomToolType {
						continue
					}
					name := strings.TrimSpace(nested.Get("name").String())
					if name != "" {
						keys[XAIClientToolKey{Namespace: namespaceName, Name: name, ToolType: xaiEffectiveDeclaredToolType(nestedType)}] = struct{}{}
					}
				}
			case xaiFunctionToolType, xaiCustomToolType:
				name := strings.TrimSpace(tool.Get("name").String())
				if name != "" {
					keys[XAIClientToolKey{Name: name, ToolType: xaiEffectiveDeclaredToolType(toolType)}] = struct{}{}
				}
			}
		}
	}
	collect(gjson.GetBytes(body, "tools"))
	for _, item := range gjson.GetBytes(body, "input").Array() {
		if item.Get("type").String() == "additional_tools" {
			collect(item.Get("tools"))
		}
	}
	return keys
}

func xaiEffectiveDeclaredToolType(toolType string) string {
	if strings.TrimSpace(toolType) == xaiCustomToolType {
		return xaiFunctionToolType
	}
	return strings.TrimSpace(toolType)
}

// XAIIsInternalXSearchCall reports whether an output item is an xAI
// server-side search trace rather than a client-declared tool call.
func XAIIsInternalXSearchCall(item gjson.Result, clientDeclaredTools map[XAIClientToolKey]struct{}) bool {
	itemType := strings.TrimSpace(item.Get("type").String())
	declaredType := ""
	switch itemType {
	case "function_call":
		declaredType = xaiFunctionToolType
	case "custom_tool_call":
		declaredType = xaiCustomToolType
	default:
		return false
	}
	name := strings.TrimSpace(item.Get("name").String())
	switch name {
	case "x_user_search", "x_semantic_search", "x_keyword_search", "x_thread_fetch":
	default:
		return false
	}
	namespace := strings.TrimSpace(item.Get("namespace").String())
	if namespace != "" {
		return false
	}
	if strings.HasPrefix(strings.TrimSpace(item.Get("call_id").String()), "xs_call") {
		return true
	}
	_, declared := clientDeclaredTools[XAIClientToolKey{Namespace: namespace, Name: name, ToolType: declaredType}]
	return !declared
}

// XAIResponsesResponseFilter removes internal xAI search trace events while
// preserving a coherent public Responses output index sequence.
type XAIResponsesResponseFilter struct {
	enabled              bool
	clientDeclaredTools  map[XAIClientToolKey]struct{}
	droppedOutputIndexes map[int64]struct{}
	droppedItemIDs       map[string]struct{}
}

// NewXAIResponsesResponseFilter creates a stateful response event filter.
func NewXAIResponsesResponseFilter(enabled bool, clientDeclaredTools map[XAIClientToolKey]struct{}) *XAIResponsesResponseFilter {
	filter := &XAIResponsesResponseFilter{
		enabled:             enabled,
		clientDeclaredTools: clientDeclaredTools,
	}
	if enabled {
		filter.droppedOutputIndexes = make(map[int64]struct{})
		filter.droppedItemIDs = make(map[string]struct{})
	}
	return filter
}

// Apply filters one xAI Responses event. A nil result means the event belongs
// to an internal search trace and must not be emitted downstream.
func (f *XAIResponsesResponseFilter) Apply(eventData []byte) []byte {
	if f == nil || !f.enabled || len(eventData) == 0 || !gjson.ValidBytes(eventData) {
		return eventData
	}
	if item := gjson.GetBytes(eventData, "item"); XAIIsInternalXSearchCall(item, f.clientDeclaredTools) {
		f.recordDroppedItem(eventData, item)
		return nil
	}
	eventData = f.filterCompletedOutput(eventData)
	if f.referencesDroppedItem(eventData) {
		return nil
	}
	return f.compactOutputIndex(eventData)
}

func (f *XAIResponsesResponseFilter) recordDroppedItem(eventData []byte, item gjson.Result) {
	if outputIndex := gjson.GetBytes(eventData, "output_index"); outputIndex.Exists() {
		f.droppedOutputIndexes[outputIndex.Int()] = struct{}{}
	}
	for _, path := range []string{"id", "call_id"} {
		if id := strings.TrimSpace(item.Get(path).String()); id != "" {
			f.droppedItemIDs[id] = struct{}{}
		}
	}
}

func (f *XAIResponsesResponseFilter) referencesDroppedItem(eventData []byte) bool {
	if outputIndex := gjson.GetBytes(eventData, "output_index"); outputIndex.Exists() {
		if _, dropped := f.droppedOutputIndexes[outputIndex.Int()]; dropped {
			return true
		}
	}
	for _, path := range []string{"item_id", "call_id"} {
		id := strings.TrimSpace(gjson.GetBytes(eventData, path).String())
		if _, dropped := f.droppedItemIDs[id]; id != "" && dropped {
			return true
		}
	}
	return false
}

func (f *XAIResponsesResponseFilter) compactOutputIndex(eventData []byte) []byte {
	outputIndex := gjson.GetBytes(eventData, "output_index")
	if !outputIndex.Exists() {
		return eventData
	}
	original := outputIndex.Int()
	removedBefore := int64(0)
	for dropped := range f.droppedOutputIndexes {
		if dropped < original {
			removedBefore++
		}
	}
	if removedBefore == 0 {
		return eventData
	}
	updated, err := sjson.SetBytes(eventData, "output_index", original-removedBefore)
	if err != nil {
		return eventData
	}
	return updated
}

func (f *XAIResponsesResponseFilter) filterCompletedOutput(eventData []byte) []byte {
	output := gjson.GetBytes(eventData, "response.output")
	if !output.IsArray() {
		return eventData
	}
	items := make([]json.RawMessage, 0, len(output.Array()))
	changed := false
	for _, item := range output.Array() {
		if XAIIsInternalXSearchCall(item, f.clientDeclaredTools) {
			changed = true
			continue
		}
		items = append(items, json.RawMessage(item.Raw))
	}
	if !changed {
		return eventData
	}
	rawOutput, err := json.Marshal(items)
	if err != nil {
		return eventData
	}
	updated, err := sjson.SetRawBytes(eventData, "response.output", rawOutput)
	if err != nil {
		return eventData
	}
	return updated
}

// NormalizeXAIInputNamespaceToolCalls qualifies source history calls to match
// declarations flattened for xAI.
func NormalizeXAIInputNamespaceToolCalls(body []byte) []byte {
	return normalizeXAIInputNamespaceToolCallsWithFold(body, xaiShouldFoldNamespaceTools(body, false, xaiDefaultMaxTools))
}

func normalizeXAIInputNamespaceToolCallsWithFold(body []byte, shouldFold bool) []byte {
	if !gjson.ValidBytes(body) {
		return body
	}
	for index, item := range gjson.GetBytes(body, "input").Array() {
		if item.Get("type").String() != "function_call" {
			continue
		}
		namespaceName := strings.TrimSpace(item.Get("namespace").String())
		toolName := strings.TrimSpace(item.Get("name").String())
		if namespaceName == "" {
			continue
		}
		qualified := QualifyXAINamespaceToolName(namespaceName, toolName)
		isFolded := shouldFold
		if xaiHasFunctionToolNamed(body, namespaceName) {
			isFolded = true
		} else if xaiHasFunctionToolNamed(body, qualified) {
			isFolded = false
		}
		namePath := fmt.Sprintf("input.%d.name", index)
		namespacePath := fmt.Sprintf("input.%d.namespace", index)
		if isFolded {
			argsPath := fmt.Sprintf("input.%d.arguments", index)
			dispatcherArgs := map[string]any{"name": toolName}
			if rawArgs := item.Get("arguments").String(); rawArgs != "" {
				if gjson.Valid(rawArgs) {
					dispatcherArgs["arguments"] = json.RawMessage(rawArgs)
				} else {
					dispatcherArgs["arguments"] = rawArgs
				}
			}
			encodedArgs, err := json.Marshal(dispatcherArgs)
			if err != nil {
				continue
			}
			updated, err := sjson.SetBytes(body, namePath, namespaceName)
			if err != nil {
				continue
			}
			updated, err = sjson.SetBytes(updated, argsPath, string(encodedArgs))
			if err != nil {
				continue
			}
			updated, err = sjson.DeleteBytes(updated, namespacePath)
			if err == nil {
				body = updated
			}
			continue
		}
		if qualified == "" {
			continue
		}
		updated, err := sjson.SetBytes(body, namePath, qualified)
		if err != nil {
			continue
		}
		updated, err = sjson.DeleteBytes(updated, namespacePath)
		if err == nil {
			body = updated
		}
	}
	return body
}

// RestoreXAINamespaceToolCalls restores public namespace/name pairs in event
// items and completed response output.
func RestoreXAINamespaceToolCalls(data []byte, refs map[string]XAINamespaceToolRef) []byte {
	return NewXAIResponsesNamespaceRestorer(refs).Restore(data)
}

// XAIResponsesNamespaceRestorer restores namespace-qualified tool calls across
// multi-event Responses streams.
type XAIResponsesNamespaceRestorer struct {
	refs              map[string]XAINamespaceToolRef
	dispatcherItemIDs map[string]string
}

func NewXAIResponsesNamespaceRestorer(refs map[string]XAINamespaceToolRef) *XAIResponsesNamespaceRestorer {
	return &XAIResponsesNamespaceRestorer{
		refs:              refs,
		dispatcherItemIDs: make(map[string]string),
	}
}

func (r *XAIResponsesNamespaceRestorer) Restore(data []byte) []byte {
	if r == nil || len(r.refs) == 0 || len(data) == 0 || !gjson.ValidBytes(data) {
		return data
	}
	switch gjson.GetBytes(data, "type").String() {
	case "response.output_item.added":
		item := gjson.GetBytes(data, "item")
		if item.Get("type").String() != "function_call" {
			return data
		}
		name := strings.TrimSpace(item.Get("name").String())
		itemID := strings.TrimSpace(item.Get("id").String())
		ref, ok := r.refs[name]
		if !ok || !ref.IsDispatcher {
			return data
		}
		if itemID != "" {
			r.dispatcherItemIDs[itemID] = ref.Namespace
		}
		data, _ = sjson.SetBytes(data, "item.namespace", ref.Namespace)
		return data
	case "response.function_call_arguments.done":
		itemID := strings.TrimSpace(gjson.GetBytes(data, "item_id").String())
		namespaceName, isDispatcher := r.dispatcherItemIDs[itemID]
		if !isDispatcher {
			return data
		}
		rawArgs := gjson.GetBytes(data, "arguments").String()
		if _, childArgs, ok := unwrapXAIDispatcherArguments(rawArgs, namespaceName, r.refs); ok {
			if updated, err := sjson.SetBytes(data, "arguments", string(childArgs)); err == nil {
				data = updated
			}
		}
		return data
	default:
		data = r.restoreAtPath(data, "item")
		for index := range gjson.GetBytes(data, "response.output").Array() {
			data = r.restoreAtPath(data, fmt.Sprintf("response.output.%d", index))
		}
		return data
	}
}

func (r *XAIResponsesNamespaceRestorer) restoreAtPath(data []byte, path string) []byte {
	if gjson.GetBytes(data, path+".type").String() != "function_call" {
		return data
	}
	qualified := strings.TrimSpace(gjson.GetBytes(data, path+".name").String())
	ref, ok := r.refs[qualified]
	if !ok {
		return data
	}
	if ref.IsDispatcher {
		rawArgs := gjson.GetBytes(data, path+".arguments").String()
		childName, childArgs, unwrapped := unwrapXAIDispatcherArguments(rawArgs, ref.Namespace, r.refs)
		if !unwrapped && childName == "" {
			childName = ref.Name
		}
		updated, err := sjson.SetBytes(data, path+".namespace", ref.Namespace)
		if err != nil {
			return data
		}
		if childName != "" {
			if updatedName, errName := sjson.SetBytes(updated, path+".name", childName); errName == nil {
				updated = updatedName
			}
		}
		if len(childArgs) > 0 {
			if updatedArgs, errArgs := sjson.SetBytes(updated, path+".arguments", string(childArgs)); errArgs == nil {
				updated = updatedArgs
			}
		}
		return updated
	}

	updated, err := sjson.SetBytes(data, path+".name", ref.Name)
	if err != nil {
		return data
	}
	updated, err = sjson.SetBytes(updated, path+".namespace", ref.Namespace)
	if err != nil {
		return data
	}
	return updated
}

func unwrapXAIDispatcherArguments(rawArgs, namespaceName string, refs map[string]XAINamespaceToolRef) (string, []byte, bool) {
	if !gjson.Valid(rawArgs) {
		return "", nil, false
	}
	args := gjson.Parse(rawArgs)
	nameField := args.Get("name")
	if !nameField.Exists() || nameField.Type != gjson.String {
		return "", nil, false
	}
	childName := strings.TrimSpace(nameField.String())
	if childName == "" {
		return "", nil, false
	}

	if namespaceName != "" {
		qualified := QualifyXAINamespaceToolName(namespaceName, childName)
		if ref, ok := refs[qualified]; ok && ref.IsDispatcher {
			return "", nil, false
		}
	} else {
		isChildOfDispatcher := false
		for _, ref := range refs {
			if ref.IsDispatcher && (ref.Name == childName || ref.Namespace == childName) {
				isChildOfDispatcher = true
				break
			}
		}
		if !isChildOfDispatcher && !args.Get("arguments").Exists() {
			return "", nil, false
		}
	}

	var childArgs []byte
	if argsField := args.Get("arguments"); argsField.Exists() {
		if argsField.Type == gjson.String {
			childArgs = []byte(argsField.String())
		} else {
			childArgs = []byte(argsField.Raw)
		}
	} else {
		cleaned, err := sjson.DeleteBytes([]byte(rawArgs), "name")
		if err == nil && len(cleaned) > 0 && string(cleaned) != "{}" {
			childArgs = cleaned
		} else {
			childArgs = []byte("{}")
		}
	}
	if len(childArgs) == 0 {
		childArgs = []byte("{}")
	}
	return childName, childArgs, true
}

func normalizeXAIObjectRootUnionBranchTypes(tool []byte) ([]byte, bool, bool) {
	parameters := gjson.GetBytes(tool, "parameters")
	if rootType := parameters.Get("type"); rootType.Type != gjson.String || rootType.String() != "object" {
		return tool, false, true
	}
	original := tool
	changed := false
	for _, unionName := range []string{"anyOf", "oneOf"} {
		for index, branch := range parameters.Get(unionName).Array() {
			if !branch.IsObject() || branch.Get("type").Exists() || branch.Get("$ref").Exists() {
				continue
			}
			updated, err := sjson.SetBytes(tool, fmt.Sprintf("parameters.%s.%d.type", unionName, index), "object")
			if err != nil {
				return original, false, false
			}
			tool, changed = updated, true
		}
	}
	return tool, changed, true
}

func xaiSchemaTypeIsObjectOnly(schemaType gjson.Result) bool {
	if schemaType.Type == gjson.String {
		return strings.EqualFold(strings.TrimSpace(schemaType.String()), "object")
	}
	if !schemaType.IsArray() {
		return false
	}
	types := schemaType.Array()
	if len(types) == 0 {
		return false
	}
	for _, item := range types {
		if item.Type != gjson.String || !strings.EqualFold(strings.TrimSpace(item.String()), "object") {
			return false
		}
	}
	return true
}

func isXAICodexAppAutomationUpdate(toolName, namespaceName string) bool {
	cleanNamespace := strings.TrimPrefix(strings.TrimSpace(namespaceName), "mcp__")
	cleanTool := strings.TrimPrefix(strings.TrimSpace(toolName), "mcp__")
	if strings.EqualFold(cleanTool, xaiAutomationUpdateTool) &&
		(strings.EqualFold(cleanNamespace, xaiCodexAppNamespaceName) || strings.EqualFold(cleanNamespace, "codex_apps")) {
		return true
	}
	if strings.EqualFold(cleanTool, xaiCodexAppNamespaceName+"__"+xaiAutomationUpdateTool) ||
		strings.EqualFold(cleanTool, "codex_apps__"+xaiAutomationUpdateTool) {
		return true
	}
	return false
}

// XAIFunctionParametersNeedSimplification reports whether a function/custom
// schema must be widened to the safe xAI object form.
func XAIFunctionParametersNeedSimplification(tool gjson.Result, namespaceName string) bool {
	toolType := strings.TrimSpace(tool.Get("type").String())
	isFunction := strings.EqualFold(toolType, xaiFunctionToolType)
	isNormalizedCustom := strings.EqualFold(toolType, xaiCustomToolType)
	if !isFunction && !isNormalizedCustom {
		return false
	}
	toolName := strings.TrimSpace(tool.Get("name").String())
	if isFunction && isXAICodexAppAutomationUpdate(toolName, namespaceName) {
		return true
	}
	parameters := tool.Get("parameters")
	for _, unionName := range []string{"anyOf", "oneOf"} {
		for _, branch := range parameters.Get(unionName).Array() {
			if branch.Get("$ref").Exists() || !xaiSchemaTypeIsObjectOnly(branch.Get("type")) {
				return true
			}
		}
	}
	return false
}

func joinRawJSONArray(items [][]byte) []byte {
	joined := bytes.Join(items, []byte(","))
	result := make([]byte, 0, len(joined)+2)
	result = append(result, '[')
	result = append(result, joined...)
	return append(result, ']')
}
