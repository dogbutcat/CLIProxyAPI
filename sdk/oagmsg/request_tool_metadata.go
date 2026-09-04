package oagmsg

import (
	"encoding/json"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/util"
	"strconv"
	"strings"
)

const codexToolNameLimitBytes = 64

type requestToolMetadata struct {
	customToolNames   map[string]struct{}
	functionToolNames map[string]struct{}
	toolNameForward   map[string]string
	toolNameReverse   map[string]string
}

func buildRequestToolMetadataFromRequests(rawRequests ...[]byte) requestToolMetadata {
	for _, raw := range rawRequests {
		if err := validateJSONObject(raw); err != nil {
			continue
		}
		return buildRequestToolMetadataFromIndex(buildToolDescriptorIndex(raw))
	}
	return emptyRequestToolMetadata()
}

func buildRequestToolMetadataFromIndex(index toolDescriptorIndex) requestToolMetadata {
	metadata := emptyRequestToolMetadata()
	ordered := orderedWinningToolDescriptors(index)
	if len(ordered) == 0 {
		return metadata
	}

	names := make([]string, 0, len(ordered))
	seenNames := make(map[string]struct{}, len(ordered))
	for _, descriptor := range ordered {
		if descriptor.name == "" {
			continue
		}
		switch descriptor.toolType {
		case "function":
			metadata.functionToolNames[descriptor.name] = struct{}{}
			delete(metadata.customToolNames, descriptor.name)
		case "custom":
			if _, function := metadata.functionToolNames[descriptor.name]; !function {
				metadata.customToolNames[descriptor.name] = struct{}{}
			}
		default:
			continue
		}
		if _, seen := seenNames[descriptor.name]; !seen {
			names = append(names, descriptor.name)
			seenNames[descriptor.name] = struct{}{}
		}
	}
	metadata.toolNameForward = buildCodexShortNameMap(names)
	metadata.toolNameReverse = reverseStringMap(metadata.toolNameForward)
	return metadata
}

func emptyRequestToolMetadata() requestToolMetadata {
	return requestToolMetadata{
		customToolNames:   map[string]struct{}{},
		functionToolNames: map[string]struct{}{},
		toolNameForward:   map[string]string{},
		toolNameReverse:   map[string]string{},
	}
}

func orderedWinningToolDescriptors(index toolDescriptorIndex) []toolDescriptor {
	if len(index.descriptors) == 0 {
		return nil
	}
	ordered := make([]toolDescriptor, 0, len(index.winners))
	for _, descriptor := range index.descriptors {
		winner, ok := index.winners[descriptor.name]
		if !ok || winner.order != descriptor.order {
			continue
		}
		ordered = append(ordered, descriptor)
	}
	return ordered
}

func buildCodexShortNameMap(names []string) map[string]string {
	used := make(map[string]struct{}, len(names))
	forward := make(map[string]string, len(names))
	for _, name := range names {
		if name == "" {
			continue
		}
		if _, seen := forward[name]; seen {
			continue
		}
		short := makeUniqueCodexToolName(shortenCodexToolNameBase(name), used)
		used[short] = struct{}{}
		forward[name] = short
	}
	return forward
}

func shortenCodexToolNameBase(name string) string {
	if len(name) <= codexToolNameLimitBytes {
		return name
	}
	if strings.HasPrefix(name, "mcp__") {
		if idx := strings.LastIndex(name, "__"); idx > 0 {
			candidate := "mcp__" + name[idx+2:]
			if len(candidate) > codexToolNameLimitBytes {
				return candidate[:codexToolNameLimitBytes]
			}
			return candidate
		}
	}
	return name[:codexToolNameLimitBytes]
}

func makeUniqueCodexToolName(base string, used map[string]struct{}) string {
	if _, ok := used[base]; !ok {
		return base
	}
	for i := 1; ; i++ {
		suffix := "_" + strconv.Itoa(i)
		allowed := codexToolNameLimitBytes - len(suffix)
		if allowed < 0 {
			allowed = 0
		}
		candidateBase := base
		if len(candidateBase) > allowed {
			candidateBase = candidateBase[:allowed]
		}
		candidate := candidateBase + suffix
		if _, ok := used[candidate]; !ok {
			return candidate
		}
	}
}

func reverseStringMap(forward map[string]string) map[string]string {
	reverse := make(map[string]string, len(forward))
	for original, emitted := range forward {
		reverse[emitted] = original
	}
	return reverse
}

func applyCodexRequestToolMetadata(body []byte, metadata requestToolMetadata) ([]byte, error) {
	if len(metadata.toolNameForward) == 0 && len(metadata.customToolNames) == 0 {
		return body, nil
	}
	if !codexRequestNeedsToolMetadata(body) {
		return body, nil
	}
	var root map[string]any
	if err := json.Unmarshal(body, &root); err != nil {
		return nil, err
	}
	applyCodexMetadataToToolDeclarations(root, metadata)
	applyCodexMetadataToToolChoice(root, metadata)
	applyCodexMetadataToInputHistory(root, metadata)
	return json.Marshal(root)
}

func codexRequestNeedsToolMetadata(body []byte) bool {
	if len(body) == 0 {
		return false
	}
	root := util.ParseGJSONBytesNoCopy(body)
	tools := root.Get("tools")
	if tools.IsArray() && len(tools.Array()) > 0 {
		return true
	}
	if _, ok := root.Get("tool_choice").Value().(map[string]any); ok {
		return true
	}
	input := root.Get("input")
	if !input.IsArray() {
		return false
	}
	for _, item := range input.Array() {
		switch item.Get("type").String() {
		case "function_call", "custom_tool_call", "function_call_output", "custom_tool_call_output", "additional_tools":
			return true
		}
	}
	return false
}

func applyCodexMetadataToToolDeclarations(root map[string]any, metadata requestToolMetadata) {
	applyCodexMetadataToToolList(anySlice(root["tools"]), metadata)
}

func applyCodexMetadataToToolList(tools []any, metadata requestToolMetadata) {
	for _, rawTool := range tools {
		applyCodexMetadataToTool(rawTool, metadata)
	}
}

func applyCodexMetadataToTool(rawTool any, metadata requestToolMetadata) {
	tool, ok := rawTool.(map[string]any)
	if !ok {
		return
	}
	applyCodexMetadataToGeminiDeclarations(tool, metadata)
	switch descriptorToolType(tool) {
	case "function", "custom":
		applyCodexMetadataToNameField(tool, "name", metadata)
		if function, okFunction := tool["function"].(map[string]any); okFunction {
			applyCodexMetadataToNameField(function, "name", metadata)
		}
	case "namespace":
		applyCodexMetadataToToolList(anySlice(tool["tools"]), metadata)
	}
}

func applyCodexMetadataToGeminiDeclarations(tool map[string]any, metadata requestToolMetadata) {
	for _, key := range []string{"functionDeclarations", "function_declarations"} {
		for _, rawDeclaration := range anySlice(tool[key]) {
			declaration, ok := rawDeclaration.(map[string]any)
			if !ok {
				continue
			}
			applyCodexMetadataToNameField(declaration, "name", metadata)
		}
	}
}

func applyCodexMetadataToToolChoice(root map[string]any, metadata requestToolMetadata) {
	choice, ok := root["tool_choice"].(map[string]any)
	if !ok {
		return
	}
	if name, namespace, found := toolChoiceNameAndNamespace(choice); found {
		if namespace != "" {
			name = qualifyToolDescriptorName(namespace, name)
		}
		if short := codexEmittedToolName(name, metadata); short != name {
			choice["name"] = short
			if function, okFunction := choice["function"].(map[string]any); okFunction {
				function["name"] = short
			}
			if custom, okCustom := choice["custom"].(map[string]any); okCustom {
				custom["name"] = short
			}
		}
	}
}

func applyCodexMetadataToInputHistory(root map[string]any, metadata requestToolMetadata) {
	customCallIDs := make(map[string]struct{})
	for _, rawItem := range anySlice(root["input"]) {
		item, ok := rawItem.(map[string]any)
		if !ok {
			continue
		}
		switch strings.TrimSpace(stringValue(item["type"])) {
		case "function_call", "custom_tool_call":
			name := strings.TrimSpace(stringValue(item["name"]))
			originalName := codexOriginalToolName(name, metadata)
			if _, isCustom := metadata.customToolNames[originalName]; isCustom {
				item["type"] = "custom_tool_call"
				if arguments, hasArguments := item["arguments"]; hasArguments {
					item["input"] = stringValue(arguments)
					delete(item, "arguments")
				}
				if callID := strings.TrimSpace(stringValue(item["call_id"])); callID != "" {
					customCallIDs[callID] = struct{}{}
				}
			}
			applyCodexMetadataToNameField(item, "name", metadata)
		case "function_call_output", "custom_tool_call_output":
			if callID := strings.TrimSpace(stringValue(item["call_id"])); callID != "" {
				if _, isCustom := customCallIDs[callID]; isCustom {
					item["type"] = "custom_tool_call_output"
				}
			}
		case "additional_tools":
			applyCodexMetadataToToolList(anySlice(item["tools"]), metadata)
		}
	}
}

func applyCodexMetadataToNameField(item map[string]any, field string, metadata requestToolMetadata) {
	name := strings.TrimSpace(stringValue(item[field]))
	if name == "" {
		return
	}
	item[field] = codexEmittedToolName(name, metadata)
}

func codexEmittedToolName(name string, metadata requestToolMetadata) string {
	if short, ok := metadata.toolNameForward[name]; ok {
		return short
	}
	return shortenCodexToolNameBase(name)
}

func codexOriginalToolName(name string, metadata requestToolMetadata) string {
	if original, ok := metadata.toolNameReverse[name]; ok {
		return original
	}
	return name
}
