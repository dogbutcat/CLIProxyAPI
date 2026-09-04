package auth

import "strings"

// IsConfigAPIKeyAuth reports whether the auth entry is synthesized from config
// API-key config, including runtime-only virtual entries backed by config.
func IsConfigAPIKeyAuth(auth *Auth) bool {
	if auth == nil {
		return false
	}
	if auth.AuthKind() != AuthKindAPIKey {
		return false
	}
	if strings.EqualFold(authAttribute(auth, AttributeSourceBackend), AuthSourceConfig) {
		return true
	}
	if strings.HasPrefix(strings.ToLower(authAttribute(auth, AttributeSource)), AuthSourceConfig+":") {
		return true
	}
	return auth.AuthSourceKind() == AuthSourceConfig
}
