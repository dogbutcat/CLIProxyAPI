package helps

import (
	"encoding/json"
	"fmt"
	"strconv"
	"strings"

	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
)

// OpenCodeGoTruncationLimits contains static request-size limits configured on
// an OpenCode Go auth entry.
type OpenCodeGoTruncationLimits struct {
	MaxMessages int
	MaxBodySize int
}

// ReadOpenCodeGoTruncationLimits extracts static truncation limits from the
// selected auth attributes. Zero values disable the corresponding limit.
func ReadOpenCodeGoTruncationLimits(auth *cliproxyauth.Auth) OpenCodeGoTruncationLimits {
	if auth == nil || len(auth.Attributes) == 0 {
		return OpenCodeGoTruncationLimits{}
	}
	return OpenCodeGoTruncationLimits{
		MaxMessages: attrPositiveInt(auth.Attributes, "max_messages", "max-messages"),
		MaxBodySize: attrPositiveInt(auth.Attributes, "max_body_size", "max-body-size"),
	}
}

// TruncateOpenCodeGoPayload applies static message/body truncation while
// preserving valid JSON and a user-starting conversation after any system turns.
func TruncateOpenCodeGoPayload(payload []byte, limits OpenCodeGoTruncationLimits) ([]byte, bool, error) {
	if limits.MaxMessages <= 0 && limits.MaxBodySize <= 0 {
		return payload, false, nil
	}

	request, messages, err := parseOpenCodeGoMessages(payload)
	if err != nil {
		return nil, false, err
	}
	if len(messages) == 0 {
		return payload, false, nil
	}

	changed := false
	if limits.MaxMessages > 0 && len(messages) > limits.MaxMessages {
		messages = append([]json.RawMessage(nil), messages[len(messages)-limits.MaxMessages:]...)
		messages = normalizeOpenCodeGoLeadingRole(messages)
		changed = true
	}

	if changed {
		payload, err = marshalOpenCodeGoRequestWithMessages(request, messages)
		if err != nil {
			return nil, false, err
		}
	}

	if limits.MaxBodySize > 0 && len(payload) > limits.MaxBodySize {
		for len(payload) > limits.MaxBodySize {
			nextMessages, ok := removeOldestOpenCodeGoMessage(messages)
			if !ok {
				break
			}
			messages = normalizeOpenCodeGoLeadingRole(nextMessages)
			nextPayload, errMarshal := marshalOpenCodeGoRequestWithMessages(request, messages)
			if errMarshal != nil {
				return nil, false, errMarshal
			}
			payload = nextPayload
			changed = true
		}
	}

	return payload, changed, nil
}

func parseOpenCodeGoMessages(payload []byte) (map[string]json.RawMessage, []json.RawMessage, error) {
	var request map[string]json.RawMessage
	if err := json.Unmarshal(payload, &request); err != nil {
		return nil, nil, fmt.Errorf("opencode-go truncation: invalid JSON payload: %w", err)
	}
	rawMessages, ok := request["messages"]
	if !ok {
		return request, nil, nil
	}
	var messages []json.RawMessage
	if err := json.Unmarshal(rawMessages, &messages); err != nil {
		return nil, nil, fmt.Errorf("opencode-go truncation: messages must be an array: %w", err)
	}
	return request, messages, nil
}

func marshalOpenCodeGoRequestWithMessages(request map[string]json.RawMessage, messages []json.RawMessage) ([]byte, error) {
	rawMessages, err := json.Marshal(messages)
	if err != nil {
		return nil, fmt.Errorf("opencode-go truncation: marshal messages: %w", err)
	}
	request["messages"] = rawMessages
	payload, err := json.Marshal(request)
	if err != nil {
		return nil, fmt.Errorf("opencode-go truncation: marshal request: %w", err)
	}
	return payload, nil
}

func normalizeOpenCodeGoLeadingRole(messages []json.RawMessage) []json.RawMessage {
	if len(messages) == 0 {
		return messages
	}

	systemPrefix := 0
	for systemPrefix < len(messages) && openCodeGoMessageRole(messages[systemPrefix]) == "system" {
		systemPrefix++
	}
	if systemPrefix >= len(messages) {
		return messages
	}
	if openCodeGoMessageRole(messages[systemPrefix]) == "user" {
		return messages
	}

	nextUser := -1
	for i := systemPrefix + 1; i < len(messages); i++ {
		if openCodeGoMessageRole(messages[i]) == "user" {
			nextUser = i
			break
		}
	}
	if nextUser < 0 {
		return messages
	}

	normalized := make([]json.RawMessage, 0, systemPrefix+len(messages)-nextUser)
	normalized = append(normalized, messages[:systemPrefix]...)
	normalized = append(normalized, messages[nextUser:]...)
	return normalized
}

func removeOldestOpenCodeGoMessage(messages []json.RawMessage) ([]json.RawMessage, bool) {
	if len(messages) <= 1 {
		return messages, false
	}

	removeIndex := 0
	for removeIndex < len(messages) && openCodeGoMessageRole(messages[removeIndex]) == "system" {
		removeIndex++
	}
	if removeIndex >= len(messages)-1 {
		return messages, false
	}

	next := make([]json.RawMessage, 0, len(messages)-1)
	next = append(next, messages[:removeIndex]...)
	next = append(next, messages[removeIndex+1:]...)
	return next, true
}

func openCodeGoMessageRole(message json.RawMessage) string {
	var decoded struct {
		Role string `json:"role"`
	}
	if err := json.Unmarshal(message, &decoded); err != nil {
		return ""
	}
	return strings.ToLower(strings.TrimSpace(decoded.Role))
}

func attrPositiveInt(attrs map[string]string, keys ...string) int {
	for _, key := range keys {
		value := strings.TrimSpace(attrs[key])
		if value == "" {
			continue
		}
		parsed, err := strconv.Atoi(value)
		if err != nil || parsed <= 0 {
			return 0
		}
		return parsed
	}
	return 0
}
