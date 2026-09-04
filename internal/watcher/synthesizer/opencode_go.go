package synthesizer

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"sort"
	"strings"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	coreauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
)

type openCodeGoProtocolSpec struct {
	Name string
	Cfg  *config.OpenCodeGoProtocolConfig
}

// synthesizeOpenCodeGo creates one runtime-only Auth per OpenCode Go key.
// Protocol-specific routes remain children of that credential and are selected
// by the OpenCode Go executor from the requested model.
func (s *ConfigSynthesizer) synthesizeOpenCodeGo(ctx *SynthesisContext) []*coreauth.Auth {
	cfg := ctx.Config
	out := make([]*coreauth.Auth, 0)
	for groupIndex := range cfg.OpenCodeGo.KeyGroups {
		group := &cfg.OpenCodeGo.KeyGroups[groupIndex]
		if group.Disabled {
			continue
		}
		namePrefix := strings.TrimSpace(group.NamePrefix)
		if namePrefix == "" {
			continue
		}
		for keyIndex := range group.Keys {
			key := &group.Keys[keyIndex]
			apiKey := strings.TrimSpace(key.APIKey)
			keyName := strings.TrimSpace(key.KeyName)
			if apiKey == "" || keyName == "" {
				continue
			}
			if auth := s.synthesizeOpenCodeGoAuth(ctx, group, key, namePrefix, keyName, apiKey); auth != nil {
				out = append(out, auth)
			}
		}
	}
	return out
}

func (s *ConfigSynthesizer) synthesizeOpenCodeGoAuth(ctx *SynthesisContext, group *config.OpenCodeGoKeyGroup, key *config.OpenCodeGoKeyEntry, namePrefix, keyName, apiKey string) *coreauth.Auth {
	protocols := []openCodeGoProtocolSpec{
		{Name: "openai", Cfg: group.OpenAI},
		{Name: "anthropic", Cfg: group.Anthropic},
	}
	activeProtocols := make([]string, 0, len(protocols))
	modelHashes := make([]string, 0, len(protocols))
	modelPriorities := make(map[string]int)
	aliases := make([]config.OAuthModelAlias, 0)
	for _, protocol := range protocols {
		if protocol.Cfg == nil || strings.TrimSpace(protocol.Cfg.BaseURL) == "" {
			continue
		}
		activeProtocols = append(activeProtocols, protocol.Name)
		if hash := hashOpenCodeGoModelEntries(protocol.Cfg.Models); hash != "" {
			modelHashes = append(modelHashes, protocol.Name+":"+hash)
		}
		aliases = append(aliases, openCodeGoModelAliases(protocol.Cfg.Models)...)
		addOpenCodeGoModelPriorities(modelPriorities, protocol.Cfg)
	}
	if len(activeProtocols) == 0 {
		return nil
	}

	generatedName := strings.Join([]string{namePrefix, keyName}, "-")
	id, token := ctx.IDGenerator.Next("opencode-go", namePrefix, keyName)
	attrs := map[string]string{
		coreauth.AttributeSource:        fmt.Sprintf("config:opencode-go[%s]", token),
		coreauth.AttributeSourceBackend: coreauth.AuthSourceConfig,
		coreauth.AttributeAuthKind:      coreauth.AuthKindAPIKey,
		coreauth.AttributeRuntimeOnly:   "true",
		coreauth.AttributeAPIKey:        apiKey,
		"provider_key":                  "opencode-go",
		"name_prefix":                   namePrefix,
		"key_name":                      keyName,
		"generated_name":                generatedName,
		"protocols":                     strings.Join(activeProtocols, ","),
	}
	if workspaceID := strings.TrimSpace(key.WorkspaceID); workspaceID != "" {
		attrs["workspace_id"] = workspaceID
	}
	if authCookie := strings.TrimSpace(key.AuthCookie); authCookie != "" {
		attrs["auth_cookie"] = authCookie
	}
	if len(modelHashes) > 0 {
		attrs["models_hash"] = hashOpenCodeGoStrings(modelHashes)
	}
	if encodedPriorities, errMarshal := json.Marshal(modelPriorities); errMarshal == nil && len(modelPriorities) > 0 {
		attrs[coreauth.AttributeModelPriorities] = string(encodedPriorities)
	}
	addConfigHeadersToAttrs(group.Headers, attrs)

	metadata := map[string]any{}
	if group.DisableCooling {
		metadata["disable_cooling"] = true
	}
	auth := &coreauth.Auth{
		ID:         id,
		Provider:   "opencode-go",
		Label:      keyName,
		Status:     coreauth.StatusActive,
		ProxyURL:   strings.TrimSpace(key.ProxyURL),
		Attributes: attrs,
		Metadata:   metadata,
		CreatedAt:  ctx.Now,
		UpdatedAt:  ctx.Now,
	}
	coreauth.SetOAuthModelAliasesAttribute(auth, aliases)
	if len(auth.Metadata) == 0 {
		auth.Metadata = nil
	}
	return auth
}

func addOpenCodeGoModelPriorities(out map[string]int, protocol *config.OpenCodeGoProtocolConfig) {
	if out == nil || protocol == nil {
		return
	}
	prefix := strings.Trim(strings.TrimSpace(protocol.Prefix), "/")
	for _, model := range protocol.Models {
		name := strings.TrimSpace(model.Name)
		alias := strings.TrimSpace(model.Alias)
		if alias == "" {
			alias = name
		}
		for _, candidate := range []string{name, alias} {
			candidate = strings.ToLower(strings.TrimSpace(candidate))
			if candidate == "" {
				continue
			}
			out[candidate] = protocol.Priority
			if prefix != "" {
				out[strings.ToLower(prefix+"/"+candidate)] = protocol.Priority
			}
		}
	}
}

func hashOpenCodeGoStrings(values []string) string {
	if len(values) == 0 {
		return ""
	}
	sum := sha256.Sum256([]byte(strings.Join(values, "\x00")))
	return hex.EncodeToString(sum[:])
}

func openCodeGoModelAliases(models []config.OpenCodeGoModelEntry) []config.OAuthModelAlias {
	out := make([]config.OAuthModelAlias, 0, len(models))
	for _, model := range models {
		name := strings.TrimSpace(model.Name)
		alias := strings.TrimSpace(model.Alias)
		if name == "" || alias == "" {
			continue
		}
		out = append(out, config.OAuthModelAlias{Name: name, Alias: alias})
	}
	return out
}

func hashOpenCodeGoModelEntries(models []config.OpenCodeGoModelEntry) string {
	keys := make([]string, 0, len(models))
	for _, model := range models {
		name := strings.ToLower(strings.TrimSpace(model.Name))
		alias := strings.ToLower(strings.TrimSpace(model.Alias))
		if name == "" && alias == "" {
			continue
		}
		keys = append(keys, name+"|"+alias)
	}
	if len(keys) == 0 {
		return ""
	}
	sort.Strings(keys)
	sum := sha256.Sum256([]byte(strings.Join(keys, "\n")))
	return hex.EncodeToString(sum[:])
}
