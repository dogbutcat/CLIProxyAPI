package config

import (
	"sort"
	"strings"

	sdkpluginstore "github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginstore"
)

// NormalizePluginsConfig applies default plugin configuration values.
func (cfg *Config) NormalizePluginsConfig() {
	if cfg == nil {
		return
	}
	cfg.Plugins.Dir = strings.TrimSpace(cfg.Plugins.Dir)
	if cfg.Plugins.Dir == "" {
		cfg.Plugins.Dir = defaultPluginsDir
	}
	if len(cfg.Plugins.StoreSources) > 0 {
		sources := make([]string, 0, len(cfg.Plugins.StoreSources))
		for _, source := range cfg.Plugins.StoreSources {
			source = strings.TrimSpace(source)
			if source == "" {
				continue
			}
			sources = append(sources, source)
		}
		cfg.Plugins.StoreSources = sources
	}
	cfg.Plugins.StoreAuth = sdkpluginstore.NormalizeAuthConfigs(cfg.Plugins.StoreAuth)
	if cfg.Plugins.Configs == nil {
		cfg.Plugins.Configs = map[string]PluginInstanceConfig{}
	}
}

// NormalizeRoutingStrategy returns the canonical routing strategy for known aliases.
func NormalizeRoutingStrategy(strategy string) (string, bool) {
	normalized := strings.ToLower(strings.TrimSpace(strategy))
	switch normalized {
	case "", "round-robin", "roundrobin", "rr":
		return "round-robin", true
	case "weighted-round-robin", "weightedroundrobin", "wrr":
		return "weighted-round-robin", true
	case "fill-first", "fillfirst", "ff":
		return "fill-first", true
	case "seq-random", "sequential-random", "seqrandom", "sr":
		return "seq-random", true
	default:
		return "", false
	}
}

// NormalizeRoutingConfig canonicalizes known routing aliases and preserves
// unknown strategies for upstream validation/runtime behavior.
func (cfg *Config) NormalizeRoutingConfig() {
	if cfg == nil {
		return
	}
	if strategy, ok := NormalizeRoutingStrategy(cfg.Routing.Strategy); ok {
		cfg.Routing.Strategy = strategy
	}
}

// SanitizeCodexHeaderDefaults trims surrounding whitespace from the
// configured Codex header fallback values.
func (cfg *Config) SanitizeCodexHeaderDefaults() {
	if cfg == nil {
		return
	}
	cfg.CodexHeaderDefaults.UserAgent = strings.TrimSpace(cfg.CodexHeaderDefaults.UserAgent)
	cfg.CodexHeaderDefaults.BetaFeatures = strings.TrimSpace(cfg.CodexHeaderDefaults.BetaFeatures)
}

// SanitizeClaudeHeaderDefaults trims surrounding whitespace from the
// configured Claude fingerprint baseline values.
func (cfg *Config) SanitizeClaudeHeaderDefaults() {
	if cfg == nil {
		return
	}
	cfg.ClaudeHeaderDefaults.UserAgent = strings.TrimSpace(cfg.ClaudeHeaderDefaults.UserAgent)
	cfg.ClaudeHeaderDefaults.PackageVersion = strings.TrimSpace(cfg.ClaudeHeaderDefaults.PackageVersion)
	cfg.ClaudeHeaderDefaults.RuntimeVersion = strings.TrimSpace(cfg.ClaudeHeaderDefaults.RuntimeVersion)
	cfg.ClaudeHeaderDefaults.OS = strings.TrimSpace(cfg.ClaudeHeaderDefaults.OS)
	cfg.ClaudeHeaderDefaults.Arch = strings.TrimSpace(cfg.ClaudeHeaderDefaults.Arch)
	cfg.ClaudeHeaderDefaults.Timeout = strings.TrimSpace(cfg.ClaudeHeaderDefaults.Timeout)
	cfg.ClaudeHeaderDefaults.Timezone = strings.TrimSpace(cfg.ClaudeHeaderDefaults.Timezone)
}

// NormalizeVisionConfig migrates the original vision-proxy block into the
// canonical vision block when the canonical block is absent.
func (cfg *Config) NormalizeVisionConfig() {
	if cfg == nil {
		return
	}
	if !cfg.Vision.configured && cfg.LegacyVisionProxy.configured {
		cfg.Vision = cfg.LegacyVisionProxy
	}
}

// SanitizeVisionConfig trims and canonicalizes vision preprocessing settings.
func (cfg *Config) SanitizeVisionConfig() {
	if cfg == nil {
		return
	}
	cfg.NormalizeVisionConfig()
	cfg.Vision.Model = strings.TrimSpace(cfg.Vision.Model)
	cfg.Vision.Fallback = strings.TrimSpace(cfg.Vision.Fallback)
	cfg.Vision.Scope = strings.ToLower(strings.TrimSpace(cfg.Vision.Scope))
	switch cfg.Vision.Scope {
	case "", "latest":
		cfg.Vision.Scope = "latest"
	case "all":
		cfg.Vision.Scope = "all"
	default:
		cfg.Vision.Scope = "latest"
	}
	cfg.Vision.Include = normalizeVisionModelPatterns(cfg.Vision.Include)
	cfg.Vision.Exclude = normalizeVisionModelPatterns(cfg.Vision.Exclude)
	cfg.Vision.Provider.Name = strings.TrimSpace(cfg.Vision.Provider.Name)
	cfg.Vision.Provider.BaseURL = strings.TrimRight(strings.TrimSpace(cfg.Vision.Provider.BaseURL), "/")
	cfg.Vision.Provider.APIKey = strings.TrimSpace(cfg.Vision.Provider.APIKey)
	cfg.Vision.Provider.Protocol = strings.ToLower(strings.TrimSpace(cfg.Vision.Provider.Protocol))
	cfg.Vision.Provider.Headers = NormalizeHeaders(cfg.Vision.Provider.Headers)
}

func normalizeVisionModelPatterns(patterns []string) []string {
	if len(patterns) == 0 {
		return nil
	}
	out := make([]string, 0, len(patterns))
	seen := make(map[string]struct{}, len(patterns))
	for _, pattern := range patterns {
		pattern = strings.ToLower(strings.TrimSpace(pattern))
		if pattern == "" {
			continue
		}
		if _, ok := seen[pattern]; ok {
			continue
		}
		seen[pattern] = struct{}{}
		out = append(out, pattern)
	}
	return out
}

// SanitizeOAuthModelAlias normalizes and deduplicates global OAuth model name aliases.
// It trims whitespace, normalizes channel keys to lower-case, drops empty entries,
// allows multiple aliases per upstream name, and ensures aliases are unique within each channel.
func (cfg *Config) SanitizeOAuthModelAlias() {
	if cfg == nil || len(cfg.OAuthModelAlias) == 0 {
		return
	}
	out := make(map[string][]OAuthModelAlias, len(cfg.OAuthModelAlias))
	for rawChannel, aliases := range cfg.OAuthModelAlias {
		channel := strings.ToLower(strings.TrimSpace(rawChannel))
		if channel == "" || len(aliases) == 0 {
			continue
		}
		seenAlias := make(map[string]struct{}, len(aliases))
		clean := make([]OAuthModelAlias, 0, len(aliases))
		for _, entry := range aliases {
			name := strings.TrimSpace(entry.Name)
			alias := strings.TrimSpace(entry.Alias)
			if name == "" || alias == "" {
				continue
			}
			if strings.EqualFold(name, alias) {
				continue
			}
			aliasKey := strings.ToLower(alias)
			if _, ok := seenAlias[aliasKey]; ok {
				continue
			}
			seenAlias[aliasKey] = struct{}{}
			clean = append(clean, OAuthModelAlias{
				Name:         name,
				Alias:        alias,
				Fork:         entry.Fork,
				DisplayName:  strings.TrimSpace(entry.DisplayName),
				ForceMapping: entry.ForceMapping,
			})
		}
		if len(clean) > 0 {
			out[channel] = clean
		}
	}
	cfg.OAuthModelAlias = out
}

// SanitizeOAuthRequestScopedErrors normalizes and validates global OAuth request-scoped error rules.
// It trims whitespace, normalizes channel keys to lower-case, validates status/action, and drops invalid rules.
func (cfg *Config) SanitizeOAuthRequestScopedErrors() {
	if cfg == nil || len(cfg.OAuthRequestScopedErrors) == 0 {
		return
	}
	out := make(map[string][]RequestScopedErrorRule, len(cfg.OAuthRequestScopedErrors))
	for rawChannel, rules := range cfg.OAuthRequestScopedErrors {
		channel := strings.ToLower(strings.TrimSpace(rawChannel))
		if channel == "" || len(rules) == 0 {
			continue
		}
		clean := make([]RequestScopedErrorRule, 0, len(rules))
		for _, r := range rules {
			action := strings.ToLower(strings.TrimSpace(r.Action))
			match := make([]string, 0, len(r.Match))
			for _, m := range r.Match {
				if tm := strings.TrimSpace(m); tm != "" {
					match = append(match, tm)
				}
			}
			matchRegexr := make([]string, 0, len(r.MatchRegexr))
			for _, re := range r.MatchRegexr {
				if tre := strings.TrimSpace(re); tre != "" {
					matchRegexr = append(matchRegexr, tre)
				}
			}
			if r.Status <= 0 || (len(match) == 0 && len(matchRegexr) == 0) || action == "" {
				continue
			}
			clean = append(clean, RequestScopedErrorRule{
				Status:      r.Status,
				Match:       match,
				MatchRegexr: matchRegexr,
				Action:      action,
			})
		}
		if len(clean) > 0 {
			out[channel] = clean
		}
	}
	if len(out) == 0 {
		cfg.OAuthRequestScopedErrors = nil
		return
	}
	cfg.OAuthRequestScopedErrors = out
}

// SanitizeOpenAICompatibility removes OpenAI-compatibility provider entries that are
// not actionable, specifically those missing a BaseURL. It trims whitespace before
// evaluation and preserves the relative order of remaining entries.
func (cfg *Config) SanitizeOpenAICompatibility() {
	if cfg == nil || len(cfg.OpenAICompatibility) == 0 {
		return
	}
	out := make([]OpenAICompatibility, 0, len(cfg.OpenAICompatibility))
	for i := range cfg.OpenAICompatibility {
		e := cfg.OpenAICompatibility[i]
		e.Name = strings.TrimSpace(e.Name)
		e.Prefix = normalizeModelPrefix(e.Prefix)
		e.BaseURL = strings.TrimSpace(e.BaseURL)
		e.Headers = NormalizeHeaders(e.Headers)
		if e.BaseURL == "" {
			// Skip providers with no base-url; treated as removed
			continue
		}
		out = append(out, e)
	}
	cfg.OpenAICompatibility = out
}

// NormalizeOpenCodeGo moves legacy OpenCode Go settings into the canonical
// provider-scoped block and trims non-secret routing fields.
func (cfg *Config) NormalizeOpenCodeGo() {
	if cfg == nil {
		return
	}
	if len(cfg.LegacyOpenCodeGoKeyGroups) > 0 {
		cfg.OpenCodeGo.KeyGroups = append(cfg.OpenCodeGo.KeyGroups, cfg.LegacyOpenCodeGoKeyGroups...)
		cfg.LegacyOpenCodeGoKeyGroups = nil
	}
	if interval := strings.TrimSpace(cfg.Routing.OpenCodeGoPollInterval); interval != "" && cfg.OpenCodeGo.Quota.PollInterval == "" {
		cfg.OpenCodeGo.Quota.PollInterval = interval
	}
	if cfg.Routing.OpenCodeGoPollThreshold != nil && cfg.OpenCodeGo.Quota.Threshold == nil {
		threshold := *cfg.Routing.OpenCodeGoPollThreshold
		cfg.OpenCodeGo.Quota.Threshold = &threshold
	}
	cfg.Routing.OpenCodeGoPollInterval = ""
	cfg.Routing.OpenCodeGoPollThreshold = nil

	cfg.OpenCodeGo.Quota.PollInterval = strings.TrimSpace(cfg.OpenCodeGo.Quota.PollInterval)
	cfg.normalizeOpenCodeGoKeyGroups()
}

func (cfg *Config) normalizeOpenCodeGoKeyGroups() {
	if len(cfg.OpenCodeGo.KeyGroups) == 0 {
		return
	}
	for groupIndex := range cfg.OpenCodeGo.KeyGroups {
		group := &cfg.OpenCodeGo.KeyGroups[groupIndex]
		group.NamePrefix = strings.TrimSpace(group.NamePrefix)
		if group.NamePrefix == "" {
			group.NamePrefix = "opencode-go"
		}
		group.Headers = NormalizeHeaders(group.Headers)
		if group.OpenAI != nil {
			normalizeOpenCodeGoProtocolConfig(group.OpenAI, "openai")
		}
		if group.Anthropic != nil {
			normalizeOpenCodeGoProtocolConfig(group.Anthropic, "claude")
		}
		for keyIndex := range group.Keys {
			key := &group.Keys[keyIndex]
			key.KeyName = strings.TrimSpace(key.KeyName)
			key.APIKey = strings.TrimSpace(key.APIKey)
			key.ProxyURL = strings.TrimSpace(key.ProxyURL)
			key.WorkspaceID = strings.TrimSpace(key.WorkspaceID)
			key.AuthCookie = strings.TrimSpace(key.AuthCookie)
		}
	}
}

func normalizeOpenCodeGoProtocolConfig(protocol *OpenCodeGoProtocolConfig, defaultSuffix string) {
	if protocol == nil {
		return
	}
	protocol.NameSuffix = strings.TrimSpace(protocol.NameSuffix)
	if protocol.NameSuffix == "" {
		protocol.NameSuffix = defaultSuffix
	}
	protocol.BaseURL = strings.TrimSpace(protocol.BaseURL)
	protocol.Prefix = normalizeModelPrefix(protocol.Prefix)
	cleanModels := make([]OpenCodeGoModelEntry, 0, len(protocol.Models))
	for _, model := range protocol.Models {
		model.Name = strings.TrimSpace(model.Name)
		model.Alias = strings.TrimSpace(model.Alias)
		if model.Name == "" {
			continue
		}
		cleanModels = append(cleanModels, model)
	}
	protocol.Models = cleanModels
}

func legacyOpenCodeGoEntriesToKeyGroups(entries []OpenCodeGo) []OpenCodeGoKeyGroup {
	if len(entries) == 0 {
		return nil
	}
	groups := make([]OpenCodeGoKeyGroup, 0, len(entries))
	for _, entry := range entries {
		protocolName := normalizeOpenCodeGoProtocol(entry.Protocol, entry.Name)
		models := make([]OpenCodeGoModelEntry, 0, len(entry.Models))
		for _, model := range entry.Models {
			models = append(models, OpenCodeGoModelEntry{Name: model.Name, Alias: model.Alias})
		}
		protocol := &OpenCodeGoProtocolConfig{
			NameSuffix: protocolName,
			BaseURL:    entry.BaseURL,
			Prefix:     entry.Prefix,
			Priority:   entry.Priority,
			Models:     models,
		}
		group := OpenCodeGoKeyGroup{
			NamePrefix:     "opencode-go",
			Disabled:       entry.Disabled,
			DisableCooling: entry.DisableCooling,
			Headers:        entry.Headers,
			Keys: []OpenCodeGoKeyEntry{{
				KeyName:     entry.Name,
				APIKey:      entry.APIKey,
				ProxyURL:    entry.ProxyURL,
				WorkspaceID: entry.WorkspaceID,
				AuthCookie:  entry.AuthCookie,
			}},
		}
		if protocolName == "claude" {
			group.Anthropic = protocol
		} else {
			group.OpenAI = protocol
		}
		groups = append(groups, group)
	}
	return groups
}

func normalizeOpenCodeGoProtocol(protocol string, name string) string {
	trimmed := strings.ToLower(strings.TrimSpace(protocol))
	if trimmed == "claude" || trimmed == "anthropic" {
		return "claude"
	}
	if trimmed == "openai" {
		return "openai"
	}
	lowerName := strings.ToLower(strings.TrimSpace(name))
	if strings.HasSuffix(lowerName, "-claude") || strings.HasSuffix(lowerName, "-anthropic") {
		return "claude"
	}
	return "openai"
}

// SanitizeCodexKeys removes Codex API key entries missing a BaseURL.
// It trims whitespace and preserves order for remaining entries.
func (cfg *Config) SanitizeCodexKeys() {
	if cfg == nil {
		return
	}
	cfg.CodexKey = sanitizeCodexKeyEntries(cfg.CodexKey)
}

// SanitizeXAIKeys removes xAI API key entries missing a BaseURL.
// It applies the same normalization rules as codex-api-key.
func (cfg *Config) SanitizeXAIKeys() {
	if cfg == nil {
		return
	}
	cfg.XAIKey = sanitizeCodexKeyEntries(cfg.XAIKey)
	for i := range cfg.XAIKey {
		cfg.XAIKey[i].AlphaSearch = false
	}
}

func sanitizeCodexKeyEntries(entries []CodexKey) []CodexKey {
	if len(entries) == 0 {
		return entries
	}
	out := make([]CodexKey, 0, len(entries))
	for i := range entries {
		e := entries[i]
		e.Prefix = normalizeModelPrefix(e.Prefix)
		e.BaseURL = strings.TrimSpace(e.BaseURL)
		e.Headers = NormalizeHeaders(e.Headers)
		e.ExcludedModels = NormalizeExcludedModels(e.ExcludedModels)
		if e.BaseURL == "" {
			continue
		}
		out = append(out, e)
	}
	return out
}

// SanitizeClaudeKeys normalizes Claude credential groups and child entries.
func (cfg *Config) SanitizeClaudeKeys() {
	if cfg == nil || len(cfg.ClaudeKey) == 0 {
		return
	}
	out := make([]ClaudeKey, 0, len(cfg.ClaudeKey))
	for i := range cfg.ClaudeKey {
		entry := cfg.ClaudeKey[i]
		entry.Name = strings.TrimSpace(entry.Name)
		entry.APIKey = strings.TrimSpace(entry.APIKey)
		entry.Prefix = normalizeModelPrefix(entry.Prefix)
		entry.BaseURL = strings.TrimSpace(entry.BaseURL)
		entry.ProxyURL = strings.TrimSpace(entry.ProxyURL)
		entry.Headers = NormalizeHeaders(entry.Headers)
		entry.ExcludedModels = NormalizeExcludedModels(entry.ExcludedModels)
		// Only a recognized value is rewritten. An unrecognized one is preserved as
		// written so sanitizing a config file never destroys operator input; the
		// request path falls back to the default profile and reports it once.
		if normalized, ok := NormalizeClaudeFingerprintProfile(entry.FingerprintProfile); ok {
			entry.FingerprintProfile = normalized
		} else {
			entry.FingerprintProfile = strings.TrimSpace(entry.FingerprintProfile)
		}
		entry.APIKeyEntries = sanitizeClaudeAPIKeyEntries(entry.APIKeyEntries)
		if entry.Disabled {
			continue
		}
		if entry.APIKey == "" && entry.BaseURL == "" && len(entry.APIKeyEntries) == 0 {
			continue
		}
		out = append(out, entry)
	}
	cfg.ClaudeKey = out
}

func sanitizeClaudeAPIKeyEntries(entries []ClaudeAPIKeyEntry) []ClaudeAPIKeyEntry {
	if len(entries) == 0 {
		return entries
	}
	seen := make(map[string]struct{}, len(entries))
	out := make([]ClaudeAPIKeyEntry, 0, len(entries))
	for i := range entries {
		entry := entries[i]
		entry.Name = strings.TrimSpace(entry.Name)
		entry.APIKey = strings.TrimSpace(entry.APIKey)
		entry.BaseURL = strings.TrimSpace(entry.BaseURL)
		entry.ProxyURL = strings.TrimSpace(entry.ProxyURL)
		if entry.Disabled || (entry.APIKey == "" && entry.BaseURL == "") {
			continue
		}
		uniqueKey := entry.APIKey + "|" + entry.BaseURL + "|" + entry.ProxyURL + "|" + entry.Name
		if _, exists := seen[uniqueKey]; exists {
			continue
		}
		seen[uniqueKey] = struct{}{}
		out = append(out, entry)
	}
	return out
}

func sanitizeGeminiKeyEntries(entries []GeminiKey) []GeminiKey {
	seen := make(map[string]struct{}, len(entries))
	out := entries[:0]
	for i := range entries {
		entry := entries[i]
		entry.APIKey = strings.TrimSpace(entry.APIKey)
		entry.BaseURL = strings.TrimSpace(entry.BaseURL)
		if entry.APIKey == "" && entry.BaseURL == "" {
			continue
		}
		entry.Prefix = normalizeModelPrefix(entry.Prefix)
		entry.ProxyURL = strings.TrimSpace(entry.ProxyURL)
		entry.Headers = NormalizeHeaders(entry.Headers)
		entry.ExcludedModels = NormalizeExcludedModels(entry.ExcludedModels)
		uniqueKey := formatGeminiKeyDedupID(entry)
		if _, exists := seen[uniqueKey]; exists {
			continue
		}
		seen[uniqueKey] = struct{}{}
		out = append(out, entry)
	}
	return out
}

func formatGeminiKeyDedupID(entry GeminiKey) string {
	var b strings.Builder
	b.WriteString(entry.APIKey)
	b.WriteByte(0)
	b.WriteString(entry.BaseURL)
	b.WriteByte(0)
	b.WriteString(entry.ProxyURL)
	b.WriteByte(0)
	b.WriteString(entry.Prefix)
	b.WriteByte(0)
	b.WriteString(FormatSortedHeaders(entry.Headers))
	return b.String()
}

// FormatSortedHeaders serializes headers deterministically with null byte separators.
func FormatSortedHeaders(headers map[string]string) string {
	if len(headers) == 0 {
		return ""
	}
	keys := make([]string, 0, len(headers))
	for k := range headers {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	var b strings.Builder
	for _, k := range keys {
		b.WriteString(k)
		b.WriteByte(0)
		b.WriteString(headers[k])
		b.WriteByte(0)
	}
	return b.String()
}

// SanitizeGeminiKeys deduplicates and normalizes Gemini credentials.
// It uses API key, base URL, proxy URL, prefix, and custom headers as the uniqueness key.
func (cfg *Config) SanitizeGeminiKeys() {
	if cfg == nil {
		return
	}
	cfg.GeminiKey = sanitizeGeminiKeyEntries(cfg.GeminiKey)
}

// SanitizeInteractionsKeys deduplicates and normalizes native Interactions credentials.
// It uses API key, base URL, proxy URL, prefix, and custom headers as the uniqueness key.
func (cfg *Config) SanitizeInteractionsKeys() {
	if cfg == nil {
		return
	}
	cfg.InteractionsKey = sanitizeGeminiKeyEntries(cfg.InteractionsKey)
}

func normalizeModelPrefix(prefix string) string {
	trimmed := strings.TrimSpace(prefix)
	trimmed = strings.Trim(trimmed, "/")
	if trimmed == "" {
		return ""
	}
	if strings.Contains(trimmed, "/") {
		return ""
	}
	return trimmed
}

// NormalizeHeaders trims header keys and values and removes empty pairs.
func NormalizeHeaders(headers map[string]string) map[string]string {
	if len(headers) == 0 {
		return nil
	}
	clean := make(map[string]string, len(headers))
	for k, v := range headers {
		key := strings.TrimSpace(k)
		val := strings.TrimSpace(v)
		if key == "" || val == "" {
			continue
		}
		clean[key] = val
	}
	if len(clean) == 0 {
		return nil
	}
	return clean
}

// NormalizeExcludedModels trims, lowercases, and deduplicates model exclusion patterns.
// It preserves the order of first occurrences and drops empty entries.
func NormalizeExcludedModels(models []string) []string {
	if len(models) == 0 {
		return nil
	}
	seen := make(map[string]struct{}, len(models))
	out := make([]string, 0, len(models))
	for _, raw := range models {
		trimmed := strings.ToLower(strings.TrimSpace(raw))
		if trimmed == "" {
			continue
		}
		if _, exists := seen[trimmed]; exists {
			continue
		}
		seen[trimmed] = struct{}{}
		out = append(out, trimmed)
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

// NormalizeOAuthExcludedModels cleans provider -> excluded models mappings by normalizing provider keys
// and applying model exclusion normalization to each entry.
func NormalizeOAuthExcludedModels(entries map[string][]string) map[string][]string {
	if len(entries) == 0 {
		return nil
	}
	out := make(map[string][]string, len(entries))
	for provider, models := range entries {
		key := strings.ToLower(strings.TrimSpace(provider))
		if key == "" {
			continue
		}
		normalized := NormalizeExcludedModels(models)
		if len(normalized) == 0 {
			continue
		}
		out[key] = normalized
	}
	if len(out) == 0 {
		return nil
	}
	return out
}
