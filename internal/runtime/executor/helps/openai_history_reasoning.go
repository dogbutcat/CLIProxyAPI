package helps

import (
	"fmt"
	"strings"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/registry"
	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

// SanitizeOpenAICompatHistoryReasoning removes historical assistant
// reasoning_content only for known OpenAI-compatible models that do not need
// that field replayed. Unknown and user-defined models are preserved by
// default because some upstreams, notably DeepSeek-style APIs, require it.
func SanitizeOpenAICompatHistoryReasoning(payload []byte, modelID, provider string) []byte {
	info := registry.LookupModelInfo(modelID, provider)
	if !shouldStripOpenAICompatHistoryReasoning(modelID, info) {
		return payload
	}
	return stripAssistantHistoryReasoningContent(payload)
}

func shouldStripOpenAICompatHistoryReasoning(modelID string, info *registry.ModelInfo) bool {
	if modelFactMatchesDeepSeek(modelID) || modelInfoMatchesDeepSeek(info) {
		return false
	}
	if info == nil || info.UserDefined || !modelInfoHasThinkingSupport(info) {
		return false
	}
	return true
}

func modelInfoHasThinkingSupport(info *registry.ModelInfo) bool {
	if info == nil || info.Thinking == nil {
		return false
	}
	thinking := info.Thinking
	return thinking.Min != 0 ||
		thinking.Max != 0 ||
		thinking.ZeroAllowed ||
		thinking.DynamicAllowed ||
		len(thinking.Levels) > 0
}

func modelInfoMatchesDeepSeek(info *registry.ModelInfo) bool {
	if info == nil {
		return false
	}
	return modelFactMatchesDeepSeek(info.ID) ||
		modelFactMatchesDeepSeek(info.Name) ||
		modelFactMatchesDeepSeek(info.DisplayName) ||
		modelFactMatchesDeepSeek(info.OwnedBy) ||
		modelFactMatchesDeepSeek(info.Type)
}

func modelFactMatchesDeepSeek(value string) bool {
	return strings.Contains(strings.ToLower(strings.TrimSpace(value)), "deepseek")
}

func stripAssistantHistoryReasoningContent(payload []byte) []byte {
	if len(payload) == 0 || !gjson.ValidBytes(payload) {
		return payload
	}
	messages := gjson.GetBytes(payload, "messages")
	if !messages.IsArray() {
		return payload
	}

	out := payload
	for index, message := range messages.Array() {
		if !strings.EqualFold(message.Get("role").String(), "assistant") {
			continue
		}
		if !message.Get("reasoning_content").Exists() {
			continue
		}
		updated, errDelete := sjson.DeleteBytes(out, fmt.Sprintf("messages.%d.reasoning_content", index))
		if errDelete != nil {
			continue
		}
		out = updated
	}
	return out
}
