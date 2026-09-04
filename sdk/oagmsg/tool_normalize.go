package oagmsg

import (
	"encoding/json"
	"strings"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/util"
)

// NormalizeToolToOpenAI converts any known tool definition shape to OpenAI chat
// completions format: {"type":"function","function":{...}}.
func NormalizeToolToOpenAI(tool map[string]any) map[string]any {
	if descriptorToolType(tool) == "custom" {
		return normalizeCustomToolToOpenAI(tool)
	}
	name, desc, params := extractToolFields(tool)
	if name == "" {
		return tool
	}
	if isOpenAINestedFormat(tool) {
		return tool
	}

	fn := map[string]any{"name": name}
	if desc != "" {
		fn["description"] = desc
	}
	if params != nil {
		fn["parameters"] = params
	}
	result := map[string]any{"type": "function", "function": fn}
	preserveToolMetadata(tool, result)
	return result
}

// NormalizeToolToAnthropic converts any known tool definition shape to
// Anthropic messages format: {"name":"...","input_schema":{...}}.
func NormalizeToolToAnthropic(tool map[string]any) map[string]any {
	if descriptorToolType(tool) == "custom" {
		return normalizeCustomToolToAnthropic(tool)
	}
	name, desc, params := extractToolFields(tool)
	if name == "" {
		return tool
	}
	if _, hasInputSchema := tool["input_schema"]; hasInputSchema {
		if _, hasFunction := tool["function"]; !hasFunction {
			return tool
		}
	}

	result := map[string]any{"name": name}
	if desc != "" {
		result["description"] = desc
	}
	if params != nil {
		result["input_schema"] = params
	}
	preserveToolMetadata(tool, result)
	return result
}

// NormalizeToolToInteractions converts any known tool definition shape to
// Responses/Interactions format: {"type":"function","name":"..."}.
func NormalizeToolToInteractions(tool map[string]any) map[string]any {
	if descriptorToolType(tool) == "custom" {
		return normalizeCustomToolToInteractions(tool)
	}
	if isResponsesBuiltinOrPassthroughTool(tool) {
		return tool
	}
	name, desc, params := extractToolFields(tool)
	if name == "" {
		return tool
	}
	if isInteractionsFlatFormat(tool) {
		return tool
	}

	result := map[string]any{"type": "function", "name": name}
	if desc != "" {
		result["description"] = desc
	}
	if params != nil {
		result["parameters"] = params
	}
	preserveToolMetadata(tool, result)
	return result
}

// NormalizeToolToGemini converts any known tool definition shape to Gemini
// functionDeclarations format.
func NormalizeToolToGemini(tool map[string]any) map[string]any {
	if descriptorToolType(tool) == "custom" {
		tool = normalizeCustomToolToAnthropic(tool)
	}
	name, desc, params := extractToolFields(tool)
	if name == "" {
		return tool
	}
	if declarations, ok := tool["functionDeclarations"]; ok {
		if _, okSlice := declarations.([]any); okSlice {
			return tool
		}
	}
	declaration := map[string]any{"name": name}
	if desc != "" {
		declaration["description"] = desc
	}
	if params != nil {
		declaration["parameters"] = normalizeGeminiParametersValue(params)
	}
	return map[string]any{"functionDeclarations": []any{declaration}}
}

func normalizeGeminiParametersValue(schema any) any {
	raw, err := json.Marshal(schema)
	if err != nil || schema == nil {
		return schema
	}
	cleaned := util.CleanJSONSchemaForGemini(string(raw))
	var value any
	if err := json.Unmarshal([]byte(cleaned), &value); err != nil {
		return schema
	}
	return value
}

func isResponsesBuiltinOrPassthroughTool(tool map[string]any) bool {
	toolType := descriptorToolType(tool)
	return toolType != "" && toolType != "function" && toolType != "custom" && toolType != "namespace"
}

func normalizeCustomToolToOpenAI(tool map[string]any) map[string]any {
	name, desc := customToolNameAndDescription(tool)
	if name == "" {
		return tool
	}
	fn := map[string]any{
		"name":       name,
		"parameters": customToolInputSchema(),
	}
	if desc != "" {
		fn["description"] = desc
	}
	result := map[string]any{"type": "function", "function": fn}
	preserveToolMetadata(tool, result)
	return result
}

func normalizeCustomToolToAnthropic(tool map[string]any) map[string]any {
	name, desc := customToolNameAndDescription(tool)
	if name == "" {
		return tool
	}
	result := map[string]any{
		"name":         name,
		"input_schema": customToolInputSchema(),
	}
	if desc != "" {
		result["description"] = desc
	}
	preserveToolMetadata(tool, result)
	return result
}

func normalizeCustomToolToInteractions(tool map[string]any) map[string]any {
	name, desc := customToolNameAndDescription(tool)
	if name == "" {
		return tool
	}
	result := map[string]any{"type": "custom", "name": name}
	if desc != "" {
		result["description"] = desc
	}
	if format, ok := tool["format"]; ok {
		result["format"] = format
	}
	preserveToolMetadata(tool, result)
	return result
}

func customToolNameAndDescription(tool map[string]any) (string, string) {
	name := strings.TrimSpace(stringValue(tool["name"]))
	desc := stringValue(tool["description"])
	if custom, ok := tool["custom"].(map[string]any); ok {
		if customName := strings.TrimSpace(stringValue(custom["name"])); customName != "" {
			name = customName
		}
		if customDesc := stringValue(custom["description"]); customDesc != "" {
			desc = customDesc
		}
	}
	return name, desc
}

func customToolInputSchema() map[string]any {
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"input": map[string]any{"type": "string"},
		},
		"required": []any{"input"},
	}
}

func responsesRequestTools(index toolDescriptorIndex) []map[string]any {
	if len(index.descriptors) == 0 {
		return nil
	}
	tools := make([]map[string]any, 0, len(index.descriptors))
	for _, descriptor := range index.descriptors {
		tools = append(tools, descriptorToolForRequest(descriptor))
	}
	return tools
}

func descriptorToolForRequest(descriptor toolDescriptor) map[string]any {
	tool := cloneToolMap(descriptor.tool)
	if !descriptor.direct {
		tool["name"] = descriptor.name
		delete(tool, "function")
	}
	if descriptor.toolType != "" {
		tool["type"] = descriptor.toolType
	}
	if descriptor.toolType == "function" {
		return NormalizeToolToInteractions(tool)
	}
	if descriptor.toolType == "custom" {
		return normalizeCustomToolToInteractions(tool)
	}
	return tool
}

func cloneToolMap(tool map[string]any) map[string]any {
	out := make(map[string]any, len(tool))
	for key, value := range tool {
		out[key] = value
	}
	return out
}

func resolveResponsesToolChoice(choice any, index toolDescriptorIndex) any {
	name, namespace, ok := toolChoiceNameAndNamespace(choice)
	if !ok {
		return choice
	}
	if namespace != "" {
		name = qualifyToolDescriptorName(namespace, name)
	}
	if descriptor, found := index.lookup(name); found {
		choiceMap, _ := choice.(map[string]any)
		choiceType := strings.TrimSpace(stringValue(choiceMap["type"]))
		return map[string]any{"type": choiceType, "name": descriptor.name}
	}
	if namespace != "" {
		return map[string]any{"type": "function", "name": name}
	}
	return choice
}

func toolChoiceNameAndNamespace(choice any) (name string, namespace string, ok bool) {
	choiceMap, ok := choice.(map[string]any)
	if !ok {
		return "", "", false
	}
	choiceType := strings.TrimSpace(stringValue(choiceMap["type"]))
	switch choiceType {
	case "function", "custom", "tool":
	default:
		return "", "", false
	}
	name = strings.TrimSpace(stringValue(choiceMap["name"]))
	namespace = strings.TrimSpace(stringValue(choiceMap["namespace"]))
	for _, key := range []string{"function", "custom"} {
		child, okChild := choiceMap[key].(map[string]any)
		if !okChild {
			continue
		}
		if childName := strings.TrimSpace(stringValue(child["name"])); childName != "" {
			name = childName
		}
		if namespace == "" {
			namespace = strings.TrimSpace(stringValue(child["namespace"]))
		}
	}
	return name, namespace, name != ""
}

func normalizeResponsesToolToOpenAI(descriptor toolDescriptor) (map[string]any, bool) {
	if descriptor.name == "" {
		return nil, false
	}
	switch descriptor.toolType {
	case "function":
		result := map[string]any{
			"type": "function",
			"function": map[string]any{
				"name":        descriptor.name,
				"description": responsesToolDescriptionValue(descriptor.tool),
				"parameters":  responsesToolParametersValue(descriptor.tool),
			},
		}
		if result["function"].(map[string]any)["parameters"] == nil {
			result["function"].(map[string]any)["parameters"] = map[string]any{}
		}
		return result, true
	case "custom":
		result := map[string]any{
			"type": "function",
			"function": map[string]any{
				"name":        descriptor.name,
				"description": responsesToolDescriptionValue(descriptor.tool),
				"parameters":  customToolInputSchema(),
			},
		}
		return result, true
	default:
		return nil, false
	}
}

func openAIChatToolDescriptors(index toolDescriptorIndex) []toolDescriptor {
	if len(index.descriptors) == 0 {
		return nil
	}
	eligible := make([]toolDescriptor, 0, len(index.descriptors))
	for _, descriptor := range index.descriptors {
		if _, ok := normalizeResponsesToolToOpenAI(descriptor); ok {
			eligible = append(eligible, descriptor)
		}
	}
	return orderedWinningToolDescriptors(firstOrderToolDescriptorIndex(toolDescriptorIndex{descriptors: eligible}))
}

func eligibleResponsesAnthropicToolWinners(descriptors []toolDescriptor) map[string]toolDescriptor {
	eligible := make([]toolDescriptor, 0, len(descriptors))
	for _, descriptor := range descriptors {
		if _, ok := normalizeResponsesToolToAnthropic(descriptor); ok {
			eligible = append(eligible, descriptor)
		}
	}
	return buildToolDescriptorWinners(eligible)
}

func normalizeResponsesToolToAnthropic(descriptor toolDescriptor) (map[string]any, bool) {
	if descriptor.name == "" {
		return nil, false
	}
	switch descriptor.toolType {
	case "function":
		result := map[string]any{
			"name":         descriptor.name,
			"description":  responsesToolDescriptionValue(descriptor.tool),
			"input_schema": normalizeClaudeInputSchemaValue(responsesToolParametersValue(descriptor.tool)),
		}
		preserveResponsesFunctionToolCacheControl(descriptor.tool, result)
		return result, true
	case "custom":
		if isResponsesApplyPatchCustomTool(descriptor) {
			return nil, false
		}
		result := map[string]any{
			"name":         descriptor.name,
			"description":  responsesToolDescriptionValue(descriptor.tool),
			"input_schema": customToolInputSchema(),
		}
		preserveToolCacheControl(descriptor.tool, result)
		return result, true
	case "web_search":
		return normalizeResponsesWebSearchToAnthropic(descriptor.tool)
	default:
		if isUnsupportedResponsesClaudeToolType(descriptor.toolType) {
			return nil, false
		}
		return cloneToolMap(descriptor.tool), true
	}
}

func preserveResponsesFunctionToolCacheControl(source, target map[string]any) {
	if preserveToolCacheControl(source, target) {
		return
	}
	if fn, ok := source["function"].(map[string]any); ok {
		if cacheControl, ok := cacheControlMapValue(fn["cache_control"]); ok {
			target["cache_control"] = cacheControl
		}
	}
}

func preserveToolCacheControl(source, target map[string]any) bool {
	if value, ok := cacheControlMapValue(source["cache_control"]); ok {
		target["cache_control"] = value
		return true
	}
	return false
}

func cacheControlMapValue(value any) (map[string]any, bool) {
	cacheControl, ok := value.(map[string]any)
	return cacheControl, ok && cacheControl != nil
}

func normalizeResponsesWebSearchToAnthropic(tool map[string]any) (map[string]any, bool) {
	if externalWebAccess, ok := tool["external_web_access"].(bool); ok && !externalWebAccess {
		return nil, false
	}
	name := strings.TrimSpace(stringValue(tool["name"]))
	if name == "" {
		name = "web_search"
	}
	result := map[string]any{
		"type": "web_search_20250305",
		"name": name,
	}
	if maxUses, ok := integerNumberValue(tool["max_uses"]); ok {
		result["max_uses"] = maxUses
	}
	if filters, ok := tool["filters"].(map[string]any); ok {
		if allowedDomains, ok := filters["allowed_domains"].([]any); ok {
			result["allowed_domains"] = allowedDomains
		}
	}
	if userLocation, ok := tool["user_location"].(map[string]any); ok {
		result["user_location"] = userLocation
	}
	return result, true
}

func normalizeResponsesToolChoiceToOpenAI(choice any) any {
	_, _, ok := toolChoiceNameAndNamespace(choice)
	if !ok {
		return NormalizeToolChoiceToOpenAI(choice)
	}
	if choiceMap, ok := choice.(map[string]any); ok {
		return cloneToolMap(choiceMap)
	}
	return choice
}

func normalizeAnthropicToolChoiceForRequest(choice any, sourceFormat Format, includedToolNames map[string]struct{}) any {
	if sourceFormat != FormatOpenAIResponse && sourceFormat != FormatCodex {
		return NormalizeToolChoiceToAnthropic(choice)
	}
	switch v := choice.(type) {
	case string:
		switch v {
		case "auto":
			return map[string]any{"type": "auto"}
		case "required":
			if len(includedToolNames) > 0 {
				return map[string]any{"type": "any"}
			}
			return nil
		case "none":
			return nil
		default:
			return nil
		}
	case map[string]any:
		choiceType := strings.TrimSpace(stringValue(v["type"]))
		switch choiceType {
		case "auto":
			return map[string]any{"type": "auto"}
		case "required", "any":
			if len(includedToolNames) > 0 {
				return map[string]any{"type": "any"}
			}
			return nil
		case "none":
			return nil
		case "function", "custom", "tool":
			name, namespace, ok := toolChoiceNameAndNamespace(v)
			if !ok {
				return nil
			}
			if namespace != "" {
				name = qualifyToolDescriptorName(namespace, name)
			}
			if mappedName := responsesIncludedChoiceName(name, includedToolNames); mappedName != "" {
				return map[string]any{"type": "tool", "name": mappedName}
			}
		}
	}
	return nil
}

func responsesIncludedChoiceName(name string, includedToolNames map[string]struct{}) string {
	if _, ok := includedToolNames[name]; ok {
		return name
	}
	var match string
	suffix := "__" + name
	for includedName := range includedToolNames {
		if !strings.HasSuffix(includedName, suffix) {
			continue
		}
		if match != "" {
			return ""
		}
		match = includedName
	}
	return match
}

func responsesToolDescriptionValue(tool map[string]any) string {
	if desc := stringValue(tool["description"]); desc != "" {
		return desc
	}
	if fn, ok := tool["function"].(map[string]any); ok {
		return stringValue(fn["description"])
	}
	return ""
}

func responsesToolParametersValue(tool map[string]any) any {
	for _, key := range []string{"parameters", "parametersJsonSchema", "input_schema"} {
		if value, ok := tool[key]; ok {
			return value
		}
	}
	if fn, ok := tool["function"].(map[string]any); ok {
		for _, key := range []string{"parameters", "parametersJsonSchema"} {
			if value, ok := fn[key]; ok {
				return value
			}
		}
	}
	return nil
}

func normalizeClaudeInputSchemaValue(schema any) any {
	raw, err := json.Marshal(schema)
	if err != nil || schema == nil {
		raw = nil
	}
	normalized := util.NormalizeClaudeToolInputSchema(raw)
	var value any
	if err := json.Unmarshal(normalized, &value); err != nil {
		return map[string]any{"type": "object", "properties": map[string]any{}}
	}
	return value
}

func isResponsesApplyPatchCustomTool(descriptor toolDescriptor) bool {
	return descriptor.toolType == "custom" &&
		(strings.TrimSpace(stringValue(descriptor.tool["name"])) == "apply_patch" || descriptor.name == "apply_patch")
}

func isUnsupportedResponsesClaudeToolType(toolType string) bool {
	switch toolType {
	case "image_generation", "file_search", "code_interpreter", "computer_use_preview":
		return true
	default:
		return false
	}
}

func integerNumberValue(value any) (int64, bool) {
	switch v := value.(type) {
	case int:
		return int64(v), true
	case int64:
		return v, true
	case float64:
		return int64(v), true
	case json.Number:
		i, err := v.Int64()
		return i, err == nil
	default:
		return 0, false
	}
}

func extractToolFields(tool map[string]any) (name string, desc string, params any) {
	if fnRaw, ok := tool["function"]; ok {
		if fn, ok := fnRaw.(map[string]any); ok {
			name, _ = fn["name"].(string)
			desc, _ = fn["description"].(string)
			params = fn["parameters"]
			return
		}
	}

	name, _ = tool["name"].(string)
	desc, _ = tool["description"].(string)
	if inputSchema, ok := tool["input_schema"]; ok {
		params = inputSchema
	} else if parameters, ok := tool["parameters"]; ok {
		params = parameters
	}
	return
}

func isOpenAINestedFormat(tool map[string]any) bool {
	fnRaw, ok := tool["function"]
	if !ok {
		return false
	}
	_, isMap := fnRaw.(map[string]any)
	return isMap
}

func isInteractionsFlatFormat(tool map[string]any) bool {
	typ, _ := tool["type"].(string)
	_, hasName := tool["name"]
	_, hasFunction := tool["function"]
	return typ == "function" && hasName && !hasFunction
}

func preserveToolMetadata(source, target map[string]any) {
	for _, key := range []string{"cache_control", "strict"} {
		if value, ok := source[key]; ok {
			target[key] = value
		}
	}
}

// NormalizeToolChoiceToOpenAI converts tool_choice to OpenAI chat completions format.
func NormalizeToolChoiceToOpenAI(choice any) any {
	switch v := choice.(type) {
	case string:
		switch v {
		case "auto", "none", "required":
			return v
		default:
			return choice
		}
	case map[string]any:
		typ, _ := v["type"].(string)
		switch typ {
		case "auto":
			return "auto"
		case "none":
			return "none"
		case "any":
			return "required"
		case "tool":
			name, _ := v["name"].(string)
			if name == "" {
				return choice
			}
			return map[string]any{"type": "function", "function": map[string]any{"name": name}}
		case "custom":
			name, _ := v["name"].(string)
			if name == "" {
				if custom, ok := v["custom"].(map[string]any); ok {
					name, _ = custom["name"].(string)
				}
			}
			if name == "" {
				return choice
			}
			return map[string]any{"type": "function", "function": map[string]any{"name": name}}
		case "function":
			if fn, ok := v["function"].(map[string]any); ok {
				if _, okName := fn["name"].(string); okName {
					return choice
				}
			}
			if name, okName := v["name"].(string); okName && name != "" {
				return map[string]any{"type": "function", "function": map[string]any{"name": name}}
			}
		}
	}
	return choice
}

// NormalizeToolChoiceToAnthropic converts tool_choice to Anthropic messages format.
func NormalizeToolChoiceToAnthropic(choice any) any {
	switch v := choice.(type) {
	case string:
		switch v {
		case "auto":
			return map[string]any{"type": "auto"}
		case "required":
			return map[string]any{"type": "any"}
		case "none":
			return nil
		default:
			return choice
		}
	case map[string]any:
		typ, _ := v["type"].(string)
		switch typ {
		case "auto", "any", "tool":
			return choice
		case "function":
			name := ""
			if fn, ok := v["function"].(map[string]any); ok {
				name, _ = fn["name"].(string)
			}
			if name == "" {
				name, _ = v["name"].(string)
			}
			if name != "" {
				return map[string]any{"type": "tool", "name": name}
			}
		case "custom":
			name, _ := v["name"].(string)
			if name == "" {
				if custom, ok := v["custom"].(map[string]any); ok {
					name, _ = custom["name"].(string)
				}
			}
			if name != "" {
				return map[string]any{"type": "tool", "name": name}
			}
		case "none":
			return nil
		}
	}
	return choice
}

// NormalizeToolChoiceToInteractions converts tool_choice to Responses/Interactions format.
func NormalizeToolChoiceToInteractions(choice any) any {
	switch v := choice.(type) {
	case string:
		switch v {
		case "auto", "none", "required":
			return v
		default:
			return choice
		}
	case map[string]any:
		typ, _ := v["type"].(string)
		switch typ {
		case "auto":
			return "auto"
		case "none":
			return "none"
		case "any":
			return "required"
		case "tool":
			name, _ := v["name"].(string)
			if name == "" {
				return choice
			}
			if strings.TrimSpace(name) == "web_search" {
				return map[string]any{"type": "web_search"}
			}
			return map[string]any{"type": "function", "name": name}
		case "custom":
			name, _ := v["name"].(string)
			if name == "" {
				if custom, ok := v["custom"].(map[string]any); ok {
					name, _ = custom["name"].(string)
				}
			}
			if name == "" {
				return choice
			}
			return map[string]any{"type": "custom", "name": name}
		case "function":
			if fn, ok := v["function"].(map[string]any); ok {
				name, _ := fn["name"].(string)
				if name != "" {
					return map[string]any{"type": "function", "name": name}
				}
			}
			return choice
		}
	}
	return choice
}

// NormalizeToolChoiceToGemini converts tool choice to Gemini toolConfig.
func NormalizeToolChoiceToGemini(choice any) map[string]any {
	mode := ""
	var allowed []string
	switch value := choice.(type) {
	case string:
		switch value {
		case "auto":
			mode = "AUTO"
		case "none":
			mode = "NONE"
		case "required", "any":
			mode = "ANY"
		default:
			mode = "ANY"
			allowed = []string{value}
		}
	case map[string]any:
		typeName, _ := value["type"].(string)
		switch typeName {
		case "auto":
			mode = "AUTO"
		case "none":
			mode = "NONE"
		case "required", "any":
			mode = "ANY"
		case "tool", "function", "custom":
			mode = "ANY"
			name, _ := value["name"].(string)
			if function, ok := value["function"].(map[string]any); ok {
				if functionName, okName := function["name"].(string); okName {
					name = functionName
				}
			}
			if custom, ok := value["custom"].(map[string]any); ok {
				if customName, okName := custom["name"].(string); okName {
					name = customName
				}
			}
			if name != "" {
				allowed = []string{name}
			}
		}
	}
	if mode == "" {
		return nil
	}
	config := map[string]any{"mode": mode}
	if len(allowed) > 0 {
		config["allowedFunctionNames"] = allowed
	}
	return map[string]any{"functionCallingConfig": config}
}
