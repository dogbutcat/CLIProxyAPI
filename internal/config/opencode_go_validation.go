package config

import (
	"fmt"
	"strings"
	"time"
)

// ValidateOpenCodeGoConfig validates the canonical OpenCode Go provider config.
func (cfg *Config) ValidateOpenCodeGoConfig() error {
	if cfg == nil {
		return nil
	}
	if interval := strings.TrimSpace(cfg.OpenCodeGo.Quota.PollInterval); interval != "" {
		duration, errParse := time.ParseDuration(interval)
		if errParse != nil {
			return fmt.Errorf("opencode-go.quota.poll-interval: invalid duration: %w", errParse)
		}
		if duration <= 0 {
			return fmt.Errorf("opencode-go.quota.poll-interval: must be greater than zero")
		}
	}
	if cfg.OpenCodeGo.Quota.Threshold != nil {
		threshold := *cfg.OpenCodeGo.Quota.Threshold
		if threshold < 0 || threshold > 100 {
			return fmt.Errorf("opencode-go.quota.threshold: must be between 0 and 100")
		}
	}

	seenIdentities := map[string]string{}
	type openCodeGoClientModelDefinition struct {
		protocol string
		upstream string
		path     string
	}
	seenModels := map[string]openCodeGoClientModelDefinition{}
	for groupIndex, group := range cfg.OpenCodeGo.KeyGroups {
		groupPath := fmt.Sprintf("opencode-go.key-groups[%d]", groupIndex)
		if strings.TrimSpace(group.NamePrefix) == "" {
			return fmt.Errorf("%s.name-prefix: required for generated key identities", groupPath)
		}
		if len(group.Keys) == 0 {
			return fmt.Errorf("%s.keys: at least one key is required", groupPath)
		}
		if group.OpenAI == nil && group.Anthropic == nil {
			return fmt.Errorf("%s: at least one protocol config is required", groupPath)
		}
		protocols := []struct {
			name string
			cfg  *OpenCodeGoProtocolConfig
		}{
			{name: "openai", cfg: group.OpenAI},
			{name: "anthropic", cfg: group.Anthropic},
		}
		for _, protocol := range protocols {
			if protocol.cfg == nil {
				continue
			}
			protocolPath := groupPath + "." + protocol.name
			if strings.TrimSpace(protocol.cfg.NameSuffix) == "" {
				return fmt.Errorf("%s.name-suffix: required for generated route names", protocolPath)
			}
			if strings.TrimSpace(protocol.cfg.BaseURL) == "" {
				return fmt.Errorf("%s.base-url: required", protocolPath)
			}
			for modelIndex, model := range protocol.cfg.Models {
				modelPath := fmt.Sprintf("%s.models[%d]", protocolPath, modelIndex)
				clientName := strings.TrimSpace(model.Alias)
				if clientName == "" {
					clientName = strings.TrimSpace(model.Name)
				}
				upstreamName := strings.TrimSpace(model.Name)
				if clientName == "" {
					return fmt.Errorf("%s.name: required", modelPath)
				}
				key := strings.ToLower(clientName)
				if previous, exists := seenModels[key]; exists {
					if previous.protocol != protocol.name || !strings.EqualFold(previous.upstream, upstreamName) {
						return fmt.Errorf("%s: duplicate client-visible model name %q also defined at %s", modelPath, clientName, previous.path)
					}
					continue
				}
				seenModels[key] = openCodeGoClientModelDefinition{protocol: protocol.name, upstream: upstreamName, path: modelPath}
			}
		}
		for keyIndex, key := range group.Keys {
			keyPath := fmt.Sprintf("%s.keys[%d]", groupPath, keyIndex)
			if strings.TrimSpace(key.KeyName) == "" {
				return fmt.Errorf("%s.key-name: required for generated key identities", keyPath)
			}
			if strings.TrimSpace(key.APIKey) == "" {
				return fmt.Errorf("%s.api-key: required", keyPath)
			}
			identity := strings.ToLower(strings.Join([]string{group.NamePrefix, key.KeyName}, "/"))
			if previous, exists := seenIdentities[identity]; exists {
				return fmt.Errorf("%s: generated key identity duplicates %s", keyPath, previous)
			}
			seenIdentities[identity] = keyPath
		}
	}
	return nil
}
