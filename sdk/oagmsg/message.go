package oagmsg

import "strings"

// OagMessage is the protocol-agnostic representation of a single message.
//
// Core invariants:
//   - Role is one of: "system", "user", "assistant"
//   - Tool semantics are expressed through content block types:
//     assistant message with ToolUseBlock -> "model wants to call a tool"
//     user message with ToolResultBlock -> "here is the tool execution result"
//   - No role="tool", no top-level tool_call_id field
type OagMessage struct {
	Role    string         `json:"role"`           // "system" | "user" | "assistant"
	Content []ContentBlock `json:"content"`        // ordered content blocks
	Name    string         `json:"name,omitempty"` // OpenAI participant identifier, passthrough
}

// ----------------------------------------------------------------
// Convenience constructors
// ----------------------------------------------------------------

// SystemMsg creates a system message with a single TextBlock.
func SystemMsg(text string) OagMessage {
	return OagMessage{Role: "system", Content: []ContentBlock{TextBlock{Text: text}}}
}

// UserTextMsg creates a user message with a single TextBlock.
func UserTextMsg(text string) OagMessage {
	return OagMessage{Role: "user", Content: []ContentBlock{TextBlock{Text: text}}}
}

// AssistantTextMsg creates an assistant message with a single TextBlock.
func AssistantTextMsg(text string) OagMessage {
	return OagMessage{Role: "assistant", Content: []ContentBlock{TextBlock{Text: text}}}
}

// ToolResultMsg creates a user message containing a ToolResultBlock.
func ToolResultMsg(toolUseID, content string, isError bool) OagMessage {
	return OagMessage{
		Role: "user",
		Content: []ContentBlock{
			ToolResultBlock{
				ToolUseID: toolUseID,
				Content:   content,
				IsError:   isError,
			},
		},
	}
}

// ----------------------------------------------------------------
// Introspection methods
// ----------------------------------------------------------------

// GetText extracts and joins all TextBlock content.
func (m OagMessage) GetText() string {
	var parts []string
	for _, b := range m.Content {
		if tb, ok := b.(TextBlock); ok {
			parts = append(parts, tb.Text)
		}
	}
	return strings.Join(parts, "\n")
}

// GetToolUses extracts all ToolUseBlocks from the message.
func (m OagMessage) GetToolUses() []ToolUseBlock {
	var result []ToolUseBlock
	for _, b := range m.Content {
		if tu, ok := b.(ToolUseBlock); ok {
			result = append(result, tu)
		}
	}
	return result
}

// GetToolResults extracts all ToolResultBlocks from the message.
func (m OagMessage) GetToolResults() []ToolResultBlock {
	var result []ToolResultBlock
	for _, b := range m.Content {
		if tr, ok := b.(ToolResultBlock); ok {
			result = append(result, tr)
		}
	}
	return result
}

// HasToolUse returns true if the message contains any ToolUseBlock.
func (m OagMessage) HasToolUse() bool {
	for _, b := range m.Content {
		if _, ok := b.(ToolUseBlock); ok {
			return true
		}
	}
	return false
}

// HasToolResult returns true if the message contains any ToolResultBlock.
func (m OagMessage) HasToolResult() bool {
	for _, b := range m.Content {
		if _, ok := b.(ToolResultBlock); ok {
			return true
		}
	}
	return false
}
