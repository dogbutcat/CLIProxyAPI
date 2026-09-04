package oagmsg

import "fmt"

// MessageBuilder provides a fluent API for constructing OagMessage instances.
// Use Validate() before Build() to enforce role-block semantic constraints.
type MessageBuilder struct {
	role    string
	content []ContentBlock
	name    string
}

// NewMessageBuilder creates a new builder for the given role.
func NewMessageBuilder(role string) *MessageBuilder {
	return &MessageBuilder{role: role}
}

// SetName sets the optional participant name (OpenAI name field).
func (b *MessageBuilder) SetName(name string) *MessageBuilder {
	b.name = name
	return b
}

// AddText appends a TextBlock.
func (b *MessageBuilder) AddText(text string) *MessageBuilder {
	b.content = append(b.content, TextBlock{Text: text})
	return b
}

// AddTextWithCache appends a TextBlock with cache control hints.
func (b *MessageBuilder) AddTextWithCache(text string, cc map[string]any) *MessageBuilder {
	b.content = append(b.content, TextBlock{Text: text, CacheControl: cc})
	return b
}

// AddImage appends an ImageBlock with base64 data.
func (b *MessageBuilder) AddImage(mediaType, data string) *MessageBuilder {
	b.content = append(b.content, ImageBlock{MediaType: mediaType, Data: data})
	return b
}

// AddImageURL appends an ImageBlock with a URL reference.
func (b *MessageBuilder) AddImageURL(url string) *MessageBuilder {
	b.content = append(b.content, ImageBlock{URL: url})
	return b
}

// AddFile appends a FileBlock.
func (b *MessageBuilder) AddFile(filename, mediaType, data string) *MessageBuilder {
	b.content = append(b.content, FileBlock{Filename: filename, MediaType: mediaType, Data: data})
	return b
}

// AddFileURL appends a FileBlock with a URL reference.
func (b *MessageBuilder) AddFileURL(filename, url string) *MessageBuilder {
	b.content = append(b.content, FileBlock{Filename: filename, URL: url})
	return b
}

// AddToolUse appends a ToolUseBlock.
func (b *MessageBuilder) AddToolUse(id, name string, input map[string]any) *MessageBuilder {
	b.content = append(b.content, ToolUseBlock{ID: id, Name: name, Input: input})
	return b
}

// AddToolResult appends a ToolResultBlock.
func (b *MessageBuilder) AddToolResult(toolUseID, content string, isError bool) *MessageBuilder {
	b.content = append(b.content, ToolResultBlock{ToolUseID: toolUseID, Content: content, IsError: isError})
	return b
}

// AddRaw appends a RawBlock for unrecognized content passthrough.
func (b *MessageBuilder) AddRaw(data map[string]any) *MessageBuilder {
	b.content = append(b.content, RawBlock{RawData: data})
	return b
}

// AddBlock appends an arbitrary ContentBlock.
func (b *MessageBuilder) AddBlock(block ContentBlock) *MessageBuilder {
	b.content = append(b.content, block)
	return b
}

// Build returns the constructed OagMessage without validation.
func (b *MessageBuilder) Build() OagMessage {
	return OagMessage{
		Role:    b.role,
		Content: b.content,
		Name:    b.name,
	}
}

// Validate checks role-block semantic constraints:
//   - system messages can only contain TextBlock
//   - assistant messages cannot contain ToolResultBlock
//   - user messages cannot contain ToolUseBlock
func (b *MessageBuilder) Validate() error {
	for _, block := range b.content {
		switch b.role {
		case "system":
			if _, ok := block.(TextBlock); !ok {
				return fmt.Errorf("oagmsg: system message cannot contain %T, only TextBlock allowed", block)
			}
		case "assistant":
			if _, ok := block.(ToolResultBlock); ok {
				return fmt.Errorf("oagmsg: assistant message cannot contain ToolResultBlock")
			}
		case "user":
			if _, ok := block.(ToolUseBlock); ok {
				return fmt.Errorf("oagmsg: user message cannot contain ToolUseBlock")
			}
		}
	}
	return nil
}
