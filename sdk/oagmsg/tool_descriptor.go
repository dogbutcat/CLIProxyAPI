package oagmsg

import (
	"encoding/json"
	"strings"
)

type toolDescriptor struct {
	name           string
	childName      string
	namespace      string
	toolType       string
	tool           map[string]any
	sourcePriority int
	direct         bool
	order          int
}

type toolDescriptorIndex struct {
	descriptors []toolDescriptor
	winners     map[string]toolDescriptor
	aliases     map[string]toolDescriptor
}

func buildToolDescriptorIndex(rawJSON []byte) toolDescriptorIndex {
	var root map[string]any
	if err := json.Unmarshal(rawJSON, &root); err != nil {
		return newToolDescriptorIndex(nil)
	}
	return newToolDescriptorIndex(root)
}

func newToolDescriptorIndex(root map[string]any) toolDescriptorIndex {
	descriptors := collectToolDescriptors(root)
	winners := buildToolDescriptorWinners(descriptors)
	return toolDescriptorIndex{
		descriptors: descriptors,
		winners:     winners,
		aliases:     buildToolDescriptorAliases(descriptors, winners),
	}
}

func collectToolDescriptors(root map[string]any) []toolDescriptor {
	var descriptors []toolDescriptor
	appendSourceDescriptors := func(tools []any, sourcePriority int) {
		for _, rawTool := range tools {
			tool, ok := rawTool.(map[string]any)
			if !ok {
				continue
			}
			appendToolDescriptor(&descriptors, tool, sourcePriority)
		}
	}

	if root != nil {
		appendSourceDescriptors(anySlice(root["tools"]), 0)
		for _, rawItem := range anySlice(root["input"]) {
			item, ok := rawItem.(map[string]any)
			if !ok || strings.TrimSpace(stringValue(item["type"])) != "additional_tools" {
				continue
			}
			appendSourceDescriptors(anySlice(item["tools"]), 1)
		}
	}
	return descriptors
}

func appendToolDescriptor(descriptors *[]toolDescriptor, tool map[string]any, sourcePriority int) {
	if appendGeminiFunctionDeclarationDescriptors(descriptors, tool, sourcePriority) {
		return
	}
	toolType := descriptorToolType(tool)
	if toolType == "namespace" {
		appendNamespaceToolDescriptors(descriptors, tool, sourcePriority)
		return
	}

	name := descriptorToolName(tool)
	if name == "" && toolType == "web_search" {
		name = "web_search"
	}
	*descriptors = append(*descriptors, toolDescriptor{
		name:           name,
		toolType:       toolType,
		tool:           tool,
		sourcePriority: sourcePriority,
		direct:         true,
		order:          len(*descriptors),
	})
}

func appendGeminiFunctionDeclarationDescriptors(descriptors *[]toolDescriptor, tool map[string]any, sourcePriority int) bool {
	appended := false
	for _, key := range []string{"functionDeclarations", "function_declarations"} {
		for _, rawDeclaration := range anySlice(tool[key]) {
			declaration, ok := rawDeclaration.(map[string]any)
			if !ok {
				continue
			}
			name := descriptorToolName(declaration)
			if name == "" {
				continue
			}
			*descriptors = append(*descriptors, toolDescriptor{
				name:           name,
				toolType:       "function",
				tool:           declaration,
				sourcePriority: sourcePriority,
				direct:         true,
				order:          len(*descriptors),
			})
			appended = true
		}
	}
	return appended
}

func appendNamespaceToolDescriptors(descriptors *[]toolDescriptor, namespaceTool map[string]any, sourcePriority int) {
	namespaceName := strings.TrimSpace(stringValue(namespaceTool["name"]))
	if namespaceName == "" {
		return
	}
	for _, rawChild := range anySlice(namespaceTool["tools"]) {
		child, ok := rawChild.(map[string]any)
		if !ok {
			continue
		}
		childType := descriptorToolType(child)
		if childType != "function" && childType != "custom" {
			continue
		}
		childName := descriptorToolName(child)
		if childName == "" {
			continue
		}
		*descriptors = append(*descriptors, toolDescriptor{
			name:           qualifyToolDescriptorName(namespaceName, childName),
			childName:      childName,
			namespace:      namespaceName,
			toolType:       childType,
			tool:           child,
			sourcePriority: sourcePriority,
			direct:         false,
			order:          len(*descriptors),
		})
	}
}

func buildToolDescriptorWinners(descriptors []toolDescriptor) map[string]toolDescriptor {
	winners := make(map[string]toolDescriptor)
	for _, descriptor := range descriptors {
		if descriptor.name == "" {
			continue
		}
		current, exists := winners[descriptor.name]
		if !exists || toolDescriptorPrecedes(descriptor, current) {
			winners[descriptor.name] = descriptor
		}
	}
	return winners
}

func firstOrderToolDescriptorIndex(index toolDescriptorIndex) toolDescriptorIndex {
	if len(index.descriptors) == 0 {
		return index
	}
	winners := buildFirstOrderToolDescriptorWinners(index.descriptors)
	return toolDescriptorIndex{
		descriptors: index.descriptors,
		winners:     winners,
		aliases:     buildToolDescriptorAliases(index.descriptors, winners),
	}
}

func buildFirstOrderToolDescriptorWinners(descriptors []toolDescriptor) map[string]toolDescriptor {
	winners := make(map[string]toolDescriptor)
	for _, descriptor := range descriptors {
		if descriptor.name == "" {
			continue
		}
		if _, exists := winners[descriptor.name]; !exists {
			winners[descriptor.name] = descriptor
		}
	}
	return winners
}

func buildToolDescriptorAliases(descriptors []toolDescriptor, winners map[string]toolDescriptor) map[string]toolDescriptor {
	aliases := make(map[string]toolDescriptor)
	for _, descriptor := range descriptors {
		winner, ok := winners[descriptor.name]
		if !ok || winner.order != descriptor.order || !descriptorIsDirectTool(descriptor) {
			continue
		}
		aliases[descriptor.name] = descriptor
	}
	for _, descriptor := range descriptors {
		winner, ok := winners[descriptor.name]
		if !ok || winner.order != descriptor.order || descriptor.direct || descriptor.childName == "" {
			continue
		}
		if _, exists := aliases[descriptor.childName]; exists {
			continue
		}
		aliases[descriptor.childName] = descriptor
	}
	return aliases
}

func toolDescriptorPrecedes(left, right toolDescriptor) bool {
	leftPriority := toolDescriptorCollisionPriority(left)
	rightPriority := toolDescriptorCollisionPriority(right)
	if leftPriority != rightPriority {
		return leftPriority < rightPriority
	}
	return left.order < right.order
}

func toolDescriptorCollisionPriority(descriptor toolDescriptor) int {
	if descriptor.sourcePriority == 0 {
		if descriptor.direct {
			return 0
		}
		return 1
	}
	if descriptor.direct {
		return 2
	}
	return 3
}

func descriptorIsDirectTool(descriptor toolDescriptor) bool {
	return descriptor.direct && (descriptor.toolType == "function" || descriptor.toolType == "custom")
}

func (index toolDescriptorIndex) winner(name string) (toolDescriptor, bool) {
	descriptor, ok := index.winners[name]
	return descriptor, ok
}

func (index toolDescriptorIndex) lookup(name string) (toolDescriptor, bool) {
	if descriptor, ok := index.winner(name); ok {
		return descriptor, true
	}
	descriptor, ok := index.aliases[name]
	return descriptor, ok
}

func descriptorToolType(tool map[string]any) string {
	toolType := strings.TrimSpace(stringValue(tool["type"]))
	if toolType == "" {
		return "function"
	}
	return toolType
}

func descriptorToolName(tool map[string]any) string {
	if function, ok := tool["function"].(map[string]any); ok {
		if name := strings.TrimSpace(stringValue(function["name"])); name != "" {
			return name
		}
	}
	return strings.TrimSpace(stringValue(tool["name"]))
}

func qualifyToolDescriptorName(namespaceName, childName string) string {
	namespaceName = strings.TrimSpace(namespaceName)
	childName = strings.TrimSpace(childName)
	if namespaceName == "" || childName == "" || strings.HasPrefix(childName, "mcp__") {
		return childName
	}
	if childName == namespaceName || strings.HasPrefix(childName, namespaceName+"__") {
		return childName
	}
	if strings.HasSuffix(namespaceName, "__") {
		return namespaceName + childName
	}
	return namespaceName + "__" + childName
}

func anySlice(value any) []any {
	items, _ := value.([]any)
	return items
}

func stringValue(value any) string {
	text, _ := value.(string)
	return text
}
