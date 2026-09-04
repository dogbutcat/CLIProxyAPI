package auth

import (
	"encoding/json"
	"strings"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/thinking"
)

// AttributeModelPriorities stores route-model priority overrides as a JSON object.
const AttributeModelPriorities = "model_priorities"

func authPriorityForModel(auth *Auth, model string) int {
	fallback := authPriority(auth)
	if auth == nil || len(auth.Attributes) == 0 {
		return fallback
	}
	raw := strings.TrimSpace(auth.Attributes[AttributeModelPriorities])
	if raw == "" {
		return fallback
	}
	model = strings.TrimSpace(thinking.ParseSuffix(model).ModelName)
	if model == "" {
		return fallback
	}
	priorities := make(map[string]int)
	if errUnmarshal := json.Unmarshal([]byte(raw), &priorities); errUnmarshal != nil {
		return fallback
	}
	if priority, ok := priorities[strings.ToLower(model)]; ok {
		return priority
	}
	return fallback
}
