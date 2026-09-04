package api

import (
	"context"
	"strings"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/usage"
	coreauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
)

func (s *Server) monitoringAuthMetadataByAuthIndex(_ context.Context) map[string]usage.MonitoringAuthMetadata {
	if s == nil || s.handlers == nil || s.handlers.AuthManager == nil {
		return nil
	}
	return monitoringAuthMetadataByAuths(s.handlers.AuthManager.List())
}

func monitoringAuthMetadataByAuths(auths []*coreauth.Auth) map[string]usage.MonitoringAuthMetadata {
	if len(auths) == 0 {
		return nil
	}
	out := make(map[string]usage.MonitoringAuthMetadata, len(auths))
	for _, auth := range auths {
		authIndex, metadata, ok := monitoringAuthMetadataFromAuth(auth)
		if !ok {
			continue
		}
		out[authIndex] = metadata
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

func monitoringAuthMetadataFromAuth(auth *coreauth.Auth) (string, usage.MonitoringAuthMetadata, bool) {
	if auth == nil {
		return "", usage.MonitoringAuthMetadata{}, false
	}
	authIndex := strings.TrimSpace(auth.EnsureIndex())
	if authIndex == "" {
		return "", usage.MonitoringAuthMetadata{}, false
	}

	label := strings.TrimSpace(auth.Label)
	fileName := strings.TrimSpace(auth.FileName)
	id := strings.TrimSpace(auth.ID)
	keyName := strings.TrimSpace(monitoringAuthAttribute(auth, "key_name"))
	generatedName := strings.TrimSpace(monitoringAuthAttribute(auth, "generated_name"))
	protocol := strings.TrimSpace(monitoringAuthAttribute(auth, "protocol"))
	provider := strings.TrimSpace(auth.Provider)
	if provider == "" {
		provider = strings.TrimSpace(monitoringAuthAttribute(auth, "provider_key"))
	}
	isOpenCodeGo := strings.EqualFold(provider, "opencode-go") ||
		strings.EqualFold(strings.TrimSpace(monitoringAuthAttribute(auth, "provider_key")), "opencode-go")

	email := monitoringAuthEmail(auth)
	accountName := firstNonEmptyServerUsageMetadataString(keyName, email, label, generatedName, fileName, id)
	authLabel := firstNonEmptyServerUsageMetadataString(label, generatedName, fileName, id)
	authFile := firstNonEmptyServerUsageMetadataString(generatedName, fileName, id)
	if isOpenCodeGo {
		accountName = firstNonEmptyServerUsageMetadataString(keyName, accountName)
		authLabel = firstNonEmptyServerUsageMetadataString(keyName, label, generatedName, fileName, id)
	}

	metadata := usage.MonitoringAuthMetadata{
		AccountName:   accountName,
		AuthLabel:     authLabel,
		AuthFile:      authFile,
		Protocol:      protocol,
		GeneratedName: generatedName,
		ProjectID:     monitoringAuthProjectID(auth, isOpenCodeGo),
		AuthProvider:  provider,
	}
	if isOpenCodeGo {
		metadata.Provider = normalizeOpenCodeGoMonitoringProvider(protocol)
		if metadata.Provider == "" {
			metadata.Provider = provider
		}
	} else {
		metadata.Provider = provider
	}
	return authIndex, metadata, true
}

func monitoringAuthEmail(auth *coreauth.Auth) string {
	if auth == nil {
		return ""
	}
	if auth.Metadata != nil {
		if raw, ok := auth.Metadata["email"].(string); ok {
			if email := strings.TrimSpace(raw); email != "" {
				return email
			}
		}
	}
	if email := strings.TrimSpace(monitoringAuthAttribute(auth, "email")); email != "" {
		return email
	}
	return strings.TrimSpace(monitoringAuthAttribute(auth, "account_email"))
}

func monitoringAuthProjectID(auth *coreauth.Auth, preferWorkspaceID bool) string {
	if auth == nil {
		return ""
	}
	if preferWorkspaceID {
		if workspaceID := strings.TrimSpace(monitoringAuthAttribute(auth, "workspace_id")); workspaceID != "" {
			return workspaceID
		}
	}
	if auth.Metadata != nil {
		if raw, ok := auth.Metadata["project_id"].(string); ok {
			if projectID := strings.TrimSpace(raw); projectID != "" {
				return projectID
			}
		}
	}
	return strings.TrimSpace(monitoringAuthAttribute(auth, "project_id"))
}

func monitoringAuthAttribute(auth *coreauth.Auth, key string) string {
	if auth == nil || len(auth.Attributes) == 0 {
		return ""
	}
	return strings.TrimSpace(auth.Attributes[key])
}

func normalizeOpenCodeGoMonitoringProvider(protocol string) string {
	switch strings.ToLower(strings.TrimSpace(protocol)) {
	case "anthropic", "claude":
		return "claude"
	case "openai", "openai-compatible":
		return "openai"
	default:
		return strings.TrimSpace(protocol)
	}
}

func firstNonEmptyServerUsageMetadataString(values ...string) string {
	for _, value := range values {
		if trimmed := strings.TrimSpace(value); trimmed != "" {
			return trimmed
		}
	}
	return ""
}
