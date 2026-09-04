package helps

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

const openAICacheControlMaxBlocks = 4

var openAICacheControlEphemeral = map[string]string{"type": "ephemeral"}

// EnsureOpenCodeGoOpenAICacheControl prepares OpenAI-format payloads for
// OpenCode Go's OpenAI delegate. Anthropic-compatible model families receive
// cache hints; other providers have unsupported cache_control fields removed.
func EnsureOpenCodeGoOpenAICacheControl(payload []byte, model string) []byte {
	if len(payload) == 0 || !gjson.ValidBytes(payload) {
		return payload
	}
	if !SupportsOpenCodeGoOpenAICacheControlModel(model) {
		return StripOpenAICacheControl(payload)
	}

	payload = enforceOpenAICacheControlLimit(payload, openAICacheControlMaxBlocks)
	if countOpenAICacheControls(payload) < openAICacheControlMaxBlocks {
		payload = injectOpenAISystemCacheControl(payload)
	}
	if countOpenAICacheControls(payload) < openAICacheControlMaxBlocks {
		payload = injectOpenAIToolsCacheControl(payload)
	}
	if countOpenAICacheControls(payload) < openAICacheControlMaxBlocks {
		payload = injectOpenAILastUserCacheControl(payload)
	}
	return payload
}

func SupportsOpenCodeGoOpenAICacheControlModel(model string) bool {
	model = strings.ToLower(strings.TrimSpace(model))
	if model == "" {
		return false
	}
	return strings.Contains(model, "claude") ||
		strings.Contains(model, "anthropic") ||
		strings.Contains(model, "sonnet") ||
		strings.Contains(model, "opus") ||
		strings.Contains(model, "haiku")
}

func StripOpenAICacheControl(payload []byte) []byte {
	var decoded any
	decoder := json.NewDecoder(bytes.NewReader(payload))
	decoder.UseNumber()
	if err := decoder.Decode(&decoded); err != nil {
		return payload
	}
	if !stripCacheControlValue(decoded) {
		return payload
	}
	var buf bytes.Buffer
	encoder := json.NewEncoder(&buf)
	encoder.SetEscapeHTML(false)
	if err := encoder.Encode(decoded); err != nil {
		return payload
	}
	return bytes.TrimSuffix(buf.Bytes(), []byte("\n"))
}

func stripCacheControlValue(value any) bool {
	switch typed := value.(type) {
	case map[string]any:
		changed := false
		for key, child := range typed {
			if strings.EqualFold(key, "cache_control") {
				delete(typed, key)
				changed = true
				continue
			}
			if stripCacheControlValue(child) {
				changed = true
			}
		}
		return changed
	case []any:
		changed := false
		for _, child := range typed {
			if stripCacheControlValue(child) {
				changed = true
			}
		}
		return changed
	default:
		return false
	}
}

func injectOpenAISystemCacheControl(payload []byte) []byte {
	messages := gjson.GetBytes(payload, "messages")
	if !messages.IsArray() {
		return payload
	}
	lastSystemIndex := -1
	messages.ForEach(func(index, message gjson.Result) bool {
		if message.Get("role").String() == "system" {
			lastSystemIndex = int(index.Int())
		}
		return true
	})
	if lastSystemIndex < 0 {
		return payload
	}
	return injectOpenAIMessageCacheControl(payload, lastSystemIndex)
}

func injectOpenAIToolsCacheControl(payload []byte) []byte {
	tools := gjson.GetBytes(payload, "tools")
	if !tools.IsArray() {
		return payload
	}
	lastEligibleToolIndex := -1
	hasCacheControl := false
	tools.ForEach(func(index, tool gjson.Result) bool {
		if tool.Get("cache_control").Exists() {
			hasCacheControl = true
			return false
		}
		if !tool.Get("defer_loading").Bool() {
			lastEligibleToolIndex = int(index.Int())
		}
		return true
	})
	if hasCacheControl || lastEligibleToolIndex < 0 {
		return payload
	}
	updated, err := sjson.SetBytes(payload, fmt.Sprintf("tools.%d.cache_control", lastEligibleToolIndex), openAICacheControlEphemeral)
	if err != nil {
		return payload
	}
	return updated
}

func injectOpenAILastUserCacheControl(payload []byte) []byte {
	messages := gjson.GetBytes(payload, "messages")
	if !messages.IsArray() || hasOpenAIConversationCacheControl(messages) {
		return payload
	}
	lastUserIndex := -1
	messages.ForEach(func(index, message gjson.Result) bool {
		if message.Get("role").String() == "user" {
			lastUserIndex = int(index.Int())
		}
		return true
	})
	if lastUserIndex < 0 {
		return payload
	}
	return injectOpenAIMessageCacheControl(payload, lastUserIndex)
}

func hasOpenAIConversationCacheControl(messages gjson.Result) bool {
	hasCacheControl := false
	messages.ForEach(func(_, message gjson.Result) bool {
		if message.Get("role").String() == "system" {
			return true
		}
		if message.Get("cache_control").Exists() {
			hasCacheControl = true
			return false
		}
		content := message.Get("content")
		if content.IsArray() {
			content.ForEach(func(_, item gjson.Result) bool {
				if item.Get("cache_control").Exists() {
					hasCacheControl = true
					return false
				}
				return true
			})
		}
		return !hasCacheControl
	})
	return hasCacheControl
}

func injectOpenAIMessageCacheControl(payload []byte, messageIndex int) []byte {
	messagePath := fmt.Sprintf("messages.%d", messageIndex)
	if gjson.GetBytes(payload, messagePath+".cache_control").Exists() {
		return payload
	}
	contentPath := messagePath + ".content"
	content := gjson.GetBytes(payload, contentPath)
	if content.IsArray() {
		count := int(content.Get("#").Int())
		if count == 0 {
			return payload
		}
		blockPath := fmt.Sprintf("%s.%d.cache_control", contentPath, count-1)
		if gjson.GetBytes(payload, blockPath).Exists() {
			return payload
		}
		updated, err := sjson.SetBytes(payload, blockPath, openAICacheControlEphemeral)
		if err != nil {
			return payload
		}
		return updated
	}
	updated, err := sjson.SetBytes(payload, messagePath+".cache_control", openAICacheControlEphemeral)
	if err != nil {
		return payload
	}
	return updated
}

func countOpenAICacheControls(payload []byte) int {
	count := 0
	tools := gjson.GetBytes(payload, "tools")
	if tools.IsArray() {
		tools.ForEach(func(_, tool gjson.Result) bool {
			if tool.Get("cache_control").Exists() {
				count++
			}
			return true
		})
	}
	messages := gjson.GetBytes(payload, "messages")
	if messages.IsArray() {
		messages.ForEach(func(_, message gjson.Result) bool {
			if message.Get("cache_control").Exists() {
				count++
			}
			content := message.Get("content")
			if content.IsArray() {
				content.ForEach(func(_, item gjson.Result) bool {
					if item.Get("cache_control").Exists() {
						count++
					}
					return true
				})
			}
			return true
		})
	}
	return count
}

func enforceOpenAICacheControlLimit(payload []byte, maxBlocks int) []byte {
	if maxBlocks < 0 {
		maxBlocks = 0
	}
	for countOpenAICacheControls(payload) > maxBlocks {
		path := nextOpenAICacheControlRemovalPath(payload)
		if path == "" {
			return payload
		}
		updated, err := sjson.DeleteBytes(payload, path)
		if err != nil {
			return payload
		}
		payload = updated
	}
	return payload
}

func nextOpenAICacheControlRemovalPath(payload []byte) string {
	if path := firstNonLastOpenAIToolCacheControlPath(payload); path != "" {
		return path
	}
	if path := firstNonLastOpenAIMessageCacheControlPath(payload, "system"); path != "" {
		return path
	}
	if path := firstNonLastOpenAIMessageCacheControlPath(payload, "user"); path != "" {
		return path
	}
	if path := firstOpenAIMessageCacheControlPath(payload, "assistant"); path != "" {
		return path
	}
	if path := firstOpenAIMessageCacheControlPath(payload, "system"); path != "" {
		return path
	}
	if path := firstOpenAIToolCacheControlPath(payload); path != "" {
		return path
	}
	if path := firstOpenAIMessageCacheControlPath(payload, "user"); path != "" {
		return path
	}
	return ""
}

func firstNonLastOpenAIToolCacheControlPath(payload []byte) string {
	tools := gjson.GetBytes(payload, "tools")
	if !tools.IsArray() {
		return ""
	}
	last := -1
	tools.ForEach(func(index, tool gjson.Result) bool {
		if tool.Get("cache_control").Exists() {
			last = int(index.Int())
		}
		return true
	})
	if last < 0 {
		return ""
	}
	path := ""
	tools.ForEach(func(index, tool gjson.Result) bool {
		i := int(index.Int())
		if i != last && tool.Get("cache_control").Exists() {
			path = fmt.Sprintf("tools.%d.cache_control", i)
			return false
		}
		return true
	})
	return path
}

func firstOpenAIToolCacheControlPath(payload []byte) string {
	tools := gjson.GetBytes(payload, "tools")
	if !tools.IsArray() {
		return ""
	}
	path := ""
	tools.ForEach(func(index, tool gjson.Result) bool {
		if tool.Get("cache_control").Exists() {
			path = fmt.Sprintf("tools.%d.cache_control", int(index.Int()))
			return false
		}
		return true
	})
	return path
}

func firstNonLastOpenAIMessageCacheControlPath(payload []byte, role string) string {
	messages := gjson.GetBytes(payload, "messages")
	if !messages.IsArray() {
		return ""
	}
	last := -1
	messages.ForEach(func(index, message gjson.Result) bool {
		if message.Get("role").String() == role && openAIMessageHasCacheControl(message) {
			last = int(index.Int())
		}
		return true
	})
	if last < 0 {
		return ""
	}
	path := ""
	messages.ForEach(func(index, message gjson.Result) bool {
		i := int(index.Int())
		if i == last || message.Get("role").String() != role {
			return true
		}
		if next := firstOpenAIMessageResultCacheControlPath(message, i); next != "" {
			path = next
			return false
		}
		return true
	})
	return path
}

func firstOpenAIMessageCacheControlPath(payload []byte, role string) string {
	messages := gjson.GetBytes(payload, "messages")
	if !messages.IsArray() {
		return ""
	}
	path := ""
	messages.ForEach(func(index, message gjson.Result) bool {
		if message.Get("role").String() != role {
			return true
		}
		if next := firstOpenAIMessageResultCacheControlPath(message, int(index.Int())); next != "" {
			path = next
			return false
		}
		return true
	})
	return path
}

func firstOpenAIMessageResultCacheControlPath(message gjson.Result, index int) string {
	if message.Get("cache_control").Exists() {
		return fmt.Sprintf("messages.%d.cache_control", index)
	}
	content := message.Get("content")
	if !content.IsArray() {
		return ""
	}
	path := ""
	content.ForEach(func(contentIndex, item gjson.Result) bool {
		if item.Get("cache_control").Exists() {
			path = fmt.Sprintf("messages.%d.content.%d.cache_control", index, int(contentIndex.Int()))
			return false
		}
		return true
	})
	return path
}

func openAIMessageHasCacheControl(message gjson.Result) bool {
	if message.Get("cache_control").Exists() {
		return true
	}
	content := message.Get("content")
	if !content.IsArray() {
		return false
	}
	found := false
	content.ForEach(func(_, item gjson.Result) bool {
		if item.Get("cache_control").Exists() {
			found = true
			return false
		}
		return true
	})
	return found
}
