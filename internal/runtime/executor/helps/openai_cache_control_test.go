package helps

import (
	"strings"
	"testing"

	"github.com/tidwall/gjson"
)

func TestEnsureOpenCodeGoOpenAICacheControlInjectsSupportedBreakpoints(t *testing.T) {
	payload := []byte(`{
		"model":"claude-sonnet-4",
		"messages":[
			{"role":"system","content":[{"type":"text","text":"sys1"},{"type":"text","text":"sys2"}]},
			{"role":"user","content":"first"},
			{"role":"assistant","content":"reply"},
			{"role":"user","content":[{"type":"text","text":"last1"},{"type":"text","text":"last2"}]}
		],
		"tools":[
			{"type":"function","function":{"name":"a"}},
			{"type":"function","function":{"name":"b"}}
		]
	}`)

	out := EnsureOpenCodeGoOpenAICacheControl(payload, "claude-sonnet-4")

	if got := gjson.GetBytes(out, "messages.0.content.1.cache_control.type").String(); got != "ephemeral" {
		t.Fatalf("system cache_control.type = %q, want ephemeral; body=%s", got, out)
	}
	if gjson.GetBytes(out, "messages.0.content.0.cache_control").Exists() {
		t.Fatalf("first system block should not receive cache_control: %s", out)
	}
	if got := gjson.GetBytes(out, "tools.1.cache_control.type").String(); got != "ephemeral" {
		t.Fatalf("tool cache_control.type = %q, want ephemeral; body=%s", got, out)
	}
	if gjson.GetBytes(out, "tools.0.cache_control").Exists() {
		t.Fatalf("first tool should not receive cache_control: %s", out)
	}
	if got := gjson.GetBytes(out, "messages.3.content.1.cache_control.type").String(); got != "ephemeral" {
		t.Fatalf("last user cache_control.type = %q, want ephemeral; body=%s", got, out)
	}
	if gjson.GetBytes(out, "messages.1.cache_control").Exists() {
		t.Fatalf("first user should not receive cache_control: %s", out)
	}
	if got := countOpenAICacheControls(out); got != 3 {
		t.Fatalf("cache_control count = %d, want 3; body=%s", got, out)
	}
}

func TestEnsureOpenCodeGoOpenAICacheControlSanitizesUnsupportedFamilies(t *testing.T) {
	for _, model := range []string{"gpt-5", "deepseek-chat", "glm-4.5", "qwen3-coder"} {
		t.Run(model, func(t *testing.T) {
			payload := []byte(`{
				"model":"` + model + `",
				"messages":[{"role":"system","content":"sys","cache_control":{"type":"ephemeral"}},{"role":"user","content":[{"type":"text","text":"hi","cache_control":{"type":"ephemeral"}}]}],
				"tools":[{"type":"function","function":{"name":"a"},"cache_control":{"type":"ephemeral"}}]
			}`)

			out := EnsureOpenCodeGoOpenAICacheControl(payload, model)
			if strings.Contains(string(out), "cache_control") {
				t.Fatalf("unsupported model kept cache_control: %s", out)
			}
		})
	}
}

func TestEnsureOpenCodeGoOpenAICacheControlSkipsDeferredTools(t *testing.T) {
	payload := []byte(`{
		"model":"claude-sonnet-4",
		"messages":[{"role":"user","content":"hi"}],
		"tools":[
			{"type":"function","function":{"name":"resident"}},
			{"type":"function","function":{"name":"deferred"},"defer_loading":true}
		]
	}`)

	out := EnsureOpenCodeGoOpenAICacheControl(payload, "sonnet")

	if got := gjson.GetBytes(out, "tools.0.cache_control.type").String(); got != "ephemeral" {
		t.Fatalf("resident tool cache_control.type = %q, want ephemeral; body=%s", got, out)
	}
	if gjson.GetBytes(out, "tools.1.cache_control").Exists() {
		t.Fatalf("deferred tool received cache_control: %s", out)
	}
}

func TestEnsureOpenCodeGoOpenAICacheControlPreservesExistingAndLimit(t *testing.T) {
	payload := []byte(`{
		"model":"claude-sonnet-4",
		"messages":[
			{"role":"system","content":"old","cache_control":{"type":"ephemeral","ttl":"1h"}},
			{"role":"system","content":"new","cache_control":{"type":"ephemeral"}},
			{"role":"user","content":[{"type":"text","text":"u1","cache_control":{"type":"ephemeral"}}]},
			{"role":"assistant","content":"reply"},
			{"role":"user","content":"u2"}
		],
		"tools":[
			{"type":"function","function":{"name":"a"},"cache_control":{"type":"ephemeral"}},
			{"type":"function","function":{"name":"b"},"cache_control":{"type":"ephemeral","ttl":"1h"}}
		]
	}`)

	out := EnsureOpenCodeGoOpenAICacheControl(payload, "claude-sonnet-4")

	if got := countOpenAICacheControls(out); got != 4 {
		t.Fatalf("cache_control count = %d, want provider limit 4; body=%s", got, out)
	}
	if gjson.GetBytes(out, "tools.0.cache_control").Exists() {
		t.Fatalf("non-last tool cache_control should be removed before last tool: %s", out)
	}
	if got := gjson.GetBytes(out, "tools.1.cache_control.ttl").String(); got != "1h" {
		t.Fatalf("last tool existing ttl = %q, want 1h; body=%s", got, out)
	}
	if got := gjson.GetBytes(out, "messages.0.cache_control.ttl").String(); got != "1h" {
		t.Fatalf("existing system ttl = %q, want 1h; body=%s", got, out)
	}
	if gjson.GetBytes(out, "messages.4.cache_control").Exists() {
		t.Fatalf("last user should not be injected when conversation cache_control already exists: %s", out)
	}
}

func TestStripOpenAICacheControlInvalidJSONUnchanged(t *testing.T) {
	payload := []byte(`{"messages":`)
	out := StripOpenAICacheControl(payload)
	if string(out) != string(payload) {
		t.Fatalf("invalid JSON changed: %s", out)
	}
}
