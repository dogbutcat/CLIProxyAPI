package helps

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestTruncateOpenCodeGoPayload_MaxMessagesKeepsUserStart(t *testing.T) {
	payload := mustOpenCodeGoPayload(t, []string{"user", "assistant", "tool", "user", "assistant"})

	got, changed, err := TruncateOpenCodeGoPayload(payload, OpenCodeGoTruncationLimits{MaxMessages: 3})
	if err != nil {
		t.Fatalf("TruncateOpenCodeGoPayload() error = %v", err)
	}
	if !changed {
		t.Fatal("TruncateOpenCodeGoPayload() changed = false, want true")
	}
	if !json.Valid(got) {
		t.Fatalf("truncated payload is invalid JSON: %s", got)
	}
	if roles := openCodeGoPayloadRoles(t, got); strings.Join(roles, ",") != "user,assistant" {
		t.Fatalf("roles = %v, want user,assistant", roles)
	}
}

func TestTruncateOpenCodeGoPayload_MaxBodySizePreservesSystemThenUser(t *testing.T) {
	payload := mustOpenCodeGoPayloadWithText(t, []openCodeGoTestMessage{
		{Role: "system", Text: "policy"},
		{Role: "user", Text: strings.Repeat("old", 80)},
		{Role: "assistant", Text: strings.Repeat("assistant", 40)},
		{Role: "user", Text: "latest question"},
		{Role: "assistant", Text: "latest answer"},
	})
	expectedSmall := mustOpenCodeGoPayloadWithText(t, []openCodeGoTestMessage{
		{Role: "system", Text: "policy"},
		{Role: "user", Text: "latest question"},
		{Role: "assistant", Text: "latest answer"},
	})
	limit := len(expectedSmall) + 16

	got, changed, err := TruncateOpenCodeGoPayload(payload, OpenCodeGoTruncationLimits{MaxBodySize: limit})
	if err != nil {
		t.Fatalf("TruncateOpenCodeGoPayload() error = %v", err)
	}
	if !changed {
		t.Fatal("TruncateOpenCodeGoPayload() changed = false, want true")
	}
	if !json.Valid(got) {
		t.Fatalf("truncated payload is invalid JSON: %s", got)
	}
	if len(got) > limit {
		t.Fatalf("payload len = %d, want <= %d: %s", len(got), limit, got)
	}
	if roles := openCodeGoPayloadRoles(t, got); strings.Join(roles, ",") != "system,user,assistant" {
		t.Fatalf("roles = %v, want system,user,assistant", roles)
	}
}

func TestTruncateOpenCodeGoPayload_NoMessagesUnchanged(t *testing.T) {
	payload := []byte(`{"model":"gpt-5","input":"plain"}`)

	got, changed, err := TruncateOpenCodeGoPayload(payload, OpenCodeGoTruncationLimits{MaxMessages: 1, MaxBodySize: 10})
	if err != nil {
		t.Fatalf("TruncateOpenCodeGoPayload() error = %v", err)
	}
	if changed {
		t.Fatal("TruncateOpenCodeGoPayload() changed = true, want false")
	}
	if string(got) != string(payload) {
		t.Fatalf("payload changed: %s", got)
	}
}

func TestTruncateOpenCodeGoPayload_InvalidJSONFails(t *testing.T) {
	_, _, err := TruncateOpenCodeGoPayload([]byte(`{"messages":`), OpenCodeGoTruncationLimits{MaxMessages: 1})
	if err == nil || !strings.Contains(err.Error(), "invalid JSON payload") {
		t.Fatalf("error = %v, want invalid JSON payload", err)
	}
}

type openCodeGoTestMessage struct {
	Role string `json:"role"`
	Text string `json:"content"`
}

func mustOpenCodeGoPayload(t *testing.T, roles []string) []byte {
	t.Helper()
	messages := make([]openCodeGoTestMessage, 0, len(roles))
	for _, role := range roles {
		messages = append(messages, openCodeGoTestMessage{Role: role, Text: role + " content"})
	}
	return mustOpenCodeGoPayloadWithText(t, messages)
}

func mustOpenCodeGoPayloadWithText(t *testing.T, messages []openCodeGoTestMessage) []byte {
	t.Helper()
	payload, err := json.Marshal(map[string]any{
		"model":    "test-model",
		"messages": messages,
		"stream":   false,
	})
	if err != nil {
		t.Fatalf("marshal payload: %v", err)
	}
	return payload
}

func openCodeGoPayloadRoles(t *testing.T, payload []byte) []string {
	t.Helper()
	var decoded struct {
		Messages []openCodeGoTestMessage `json:"messages"`
	}
	if err := json.Unmarshal(payload, &decoded); err != nil {
		t.Fatalf("unmarshal payload: %v", err)
	}
	roles := make([]string, 0, len(decoded.Messages))
	for _, message := range decoded.Messages {
		roles = append(roles, message.Role)
	}
	return roles
}
