package oagmsg

import (
	"strings"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/thinking"
	"github.com/tidwall/gjson"
)

// ThinkingEffort returns the canonical level label for a thinking config.
func ThinkingEffort(config *thinking.ThinkingConfig) string {
	if config == nil {
		return ""
	}
	switch config.Mode {
	case thinking.ModeNone:
		return string(thinking.LevelNone)
	case thinking.ModeAuto:
		return string(thinking.LevelAuto)
	case thinking.ModeLevel:
		return strings.ToLower(strings.TrimSpace(string(config.Level)))
	case thinking.ModeBudget:
		level, ok := thinking.ConvertBudgetToLevel(config.Budget)
		if !ok {
			return ""
		}
		return level
	default:
		return ""
	}
}

func thinkingBudget(config *thinking.ThinkingConfig) int {
	if config == nil {
		return 0
	}
	switch config.Mode {
	case thinking.ModeNone:
		return 0
	case thinking.ModeAuto:
		return -1
	case thinking.ModeBudget:
		return config.Budget
	case thinking.ModeLevel:
		budget, ok := thinking.ConvertLevelToBudget(string(config.Level))
		if !ok {
			return 0
		}
		return budget
	default:
		return 0
	}
}

func thinkingFromLevel(level string) *thinking.ThinkingConfig {
	level = strings.ToLower(strings.TrimSpace(level))
	if level == "" {
		return nil
	}
	switch level {
	case string(thinking.LevelNone):
		return &thinking.ThinkingConfig{Mode: thinking.ModeNone, Budget: 0}
	case string(thinking.LevelAuto):
		return &thinking.ThinkingConfig{Mode: thinking.ModeAuto, Budget: -1}
	default:
		if _, ok := thinking.ConvertLevelToBudget(level); !ok {
			return nil
		}
		return &thinking.ThinkingConfig{Mode: thinking.ModeLevel, Level: thinking.ThinkingLevel(level)}
	}
}

func thinkingFromBudget(budget int) *thinking.ThinkingConfig {
	switch {
	case budget == -1:
		return &thinking.ThinkingConfig{Mode: thinking.ModeAuto, Budget: -1}
	case budget == 0:
		return &thinking.ThinkingConfig{Mode: thinking.ModeNone, Budget: 0}
	case budget > 0:
		return &thinking.ThinkingConfig{Mode: thinking.ModeBudget, Budget: budget}
	default:
		return nil
	}
}

// ExtractAnthropicThinking extracts canonical thinking config from Anthropic request JSON.
func ExtractAnthropicThinking(root gjson.Result) *thinking.ThinkingConfig {
	value := root.Get("thinking")
	if !value.Exists() || !value.IsObject() {
		return nil
	}
	switch value.Get("type").String() {
	case "disabled":
		return &thinking.ThinkingConfig{Mode: thinking.ModeNone, Budget: 0}
	case "adaptive":
		if effort := value.Get("effort"); effort.Exists() {
			return thinkingFromLevel(effort.String())
		}
		if effort := root.Get("output_config.effort"); effort.Exists() {
			return thinkingFromLevel(effort.String())
		}
		return &thinking.ThinkingConfig{Mode: thinking.ModeAuto, Budget: -1}
	case "enabled":
		budget := int(value.Get("budget_tokens").Int())
		if budget == 0 {
			budget = int(value.Get("budgetTokens").Int())
		}
		if budget > 0 {
			return thinkingFromBudget(budget)
		}
		return &thinking.ThinkingConfig{Mode: thinking.ModeAuto, Budget: -1}
	default:
		return nil
	}
}

// ExtractOpenAIThinking extracts canonical thinking config from OpenAI chat request JSON.
func ExtractOpenAIThinking(root gjson.Result) *thinking.ThinkingConfig {
	value := root.Get("reasoning_effort")
	if !value.Exists() {
		return nil
	}
	return thinkingFromLevel(value.String())
}

// ExtractInteractionsThinking extracts canonical thinking config from Responses request JSON.
func ExtractInteractionsThinking(root gjson.Result) *thinking.ThinkingConfig {
	if value := root.Get("reasoning.effort"); value.Exists() {
		return thinkingFromLevel(value.String())
	}
	if value := root.Get("generation_config.thinking_level"); value.Exists() {
		return thinkingFromLevel(value.String())
	}
	if value := root.Get("generation_config.thinkingLevel"); value.Exists() {
		return thinkingFromLevel(value.String())
	}
	if value := root.Get("generation_config.thinking_budget"); value.Exists() {
		return thinkingFromBudget(int(value.Int()))
	}
	if value := root.Get("generation_config.thinkingBudget"); value.Exists() {
		return thinkingFromBudget(int(value.Int()))
	}
	return nil
}

// ExtractGeminiThinking extracts canonical thinking config from Gemini request JSON.
func ExtractGeminiThinking(root gjson.Result) *thinking.ThinkingConfig {
	for _, path := range []string{
		"generationConfig.thinkingConfig.thinkingLevel",
		"generationConfig.thinking_config.thinkingLevel",
		"generationConfig.thinking_config.thinking_level",
	} {
		if value := root.Get(path); value.Exists() {
			return thinkingFromLevel(value.String())
		}
	}
	for _, path := range []string{
		"generationConfig.thinkingConfig.thinkingBudget",
		"generationConfig.thinking_config.thinkingBudget",
		"generationConfig.thinking_config.thinking_budget",
	} {
		if value := root.Get(path); value.Exists() {
			return thinkingFromBudget(int(value.Int()))
		}
	}
	return nil
}

// ExtractCodexThinking extracts canonical thinking config from Codex request JSON.
func ExtractCodexThinking(root gjson.Result) *thinking.ThinkingConfig {
	if config := ExtractInteractionsThinking(root); config != nil {
		return config
	}
	return ExtractOpenAIThinking(root)
}

// ApplyAnthropicThinking writes canonical thinking config to Anthropic request JSON maps.
func ApplyAnthropicThinking(config *thinking.ThinkingConfig, out map[string]any) {
	if config == nil {
		return
	}
	switch config.Mode {
	case thinking.ModeNone:
		out["thinking"] = map[string]any{"type": "disabled"}
	case thinking.ModeAuto:
		out["thinking"] = map[string]any{"type": "adaptive"}
	case thinking.ModeLevel:
		out["thinking"] = map[string]any{"type": "adaptive"}
		out["output_config"] = map[string]any{"effort": string(config.Level)}
	case thinking.ModeBudget:
		if config.Budget > 0 {
			out["thinking"] = map[string]any{"type": "enabled", "budget_tokens": config.Budget}
		}
	}
}

// ApplyOpenAIThinking writes canonical thinking config to OpenAI chat request JSON maps.
func ApplyOpenAIThinking(config *thinking.ThinkingConfig, out map[string]any) {
	if effort := ThinkingEffort(config); effort != "" {
		out["reasoning_effort"] = effort
	}
}

// ApplyInteractionsThinking writes canonical thinking config to Responses request JSON maps.
func ApplyInteractionsThinking(config *thinking.ThinkingConfig, out map[string]any) {
	if effort := ThinkingEffort(config); effort != "" {
		out["reasoning"] = map[string]any{"effort": effort}
	}
}

// ApplyGeminiThinking writes canonical thinking config to a Gemini generationConfig map.
func ApplyGeminiThinking(config *thinking.ThinkingConfig, generationConfig map[string]any) {
	if config == nil {
		return
	}
	switch config.Mode {
	case thinking.ModeLevel:
		generationConfig["thinkingConfig"] = map[string]any{"thinkingLevel": string(config.Level)}
	case thinking.ModeAuto, thinking.ModeNone, thinking.ModeBudget:
		generationConfig["thinkingConfig"] = map[string]any{"thinkingBudget": thinkingBudget(config)}
	}
}

// ApplyCodexThinking writes canonical thinking config to Codex request JSON maps.
func ApplyCodexThinking(config *thinking.ThinkingConfig, out map[string]any) {
	ApplyInteractionsThinking(config, out)
}
