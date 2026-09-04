package oagmsg

import (
	"reflect"
	"strings"
	"testing"

	responses_to_claude "github.com/router-for-me/CLIProxyAPI/v7/internal/translator/claude/openai/responses"
	"github.com/tidwall/gjson"
)

func TestResponsesToClaudeCacheControlContentUpstreamOracle(t *testing.T) {
	raw := []byte(`{
		"model":"gpt-test",
		"input":[
			{"type":"message","role":"system","cache_control":{"type":"ephemeral","ttl":"system-item"},"content":[
				{"type":"input_text","text":"S1"},
				{"type":"input_text","text":"S2"}
			]},
			{"type":"message","role":"developer","cache_control":{"type":"ephemeral","ttl":"developer-item"},"content":[
				{"type":"input_text","text":"D1"},
				{"type":"input_text","text":"D2","cache_control":{"type":"ephemeral","ttl":"developer-part"}}
			]},
			{"type":"message","role":"user","cache_control":{"type":"ephemeral","ttl":"user-item"},"content":[
				{"type":"input_text","text":"cached prefix","cache_control":{"type":"ephemeral","ttl":"text-part"}},
				{"type":"input_image","image_url":"https://example.com/cache.png","cache_control":{"type":"ephemeral","ttl":"image-part"}},
				{"type":"input_file","file_data":"data:application/pdf;base64,ZmlsZQ=="}
			]},
			{"type":"message","role":"assistant","cache_control":{"type":"ephemeral","ttl":"assistant-item"},"content":[
				{"type":"output_text","text":"A1"},
				{"type":"output_text","text":"A2","cache_control":{"type":"ephemeral","ttl":"assistant-part"}}
			]},
			{"type":"message","role":"user","cache_control":{"type":"ephemeral","ttl":"item-fallback"},"content":[
				{"type":"input_text","text":"malformed part fallback","cache_control":"bad"}
			]}
		]
	}`)

	assertResponsesToClaudeCacheControlOracle(t, raw)
}

func TestResponsesToClaudeCacheControlMediaPartsUpstreamOracle(t *testing.T) {
	raw := []byte(`{
		"model":"gpt-test",
		"input":[
			{"type":"message","role":"user","content":[
				{"type":"input_image","image_url":"https://example.com/cache.png","cache_control":{"type":"ephemeral","ttl":"http-image"}},
				{"type":"input_image","image_url":"data:image/png;base64,aW1hZ2U=","cache_control":{"type":"ephemeral","ttl":"data-image"}},
				{"type":"input_image","image_url":"","url":"https://example.com/fallback.png","cache_control":{"type":"ephemeral","ttl":"url-fallback"}},
				{"type":"input_image","image_url":"https://example.com/path;base64,not-an-image","cache_control":{"type":"ephemeral","ttl":"literal-url"}},
				{"type":"input_file","file_data":"data:application/pdf;base64,ZmlsZQ==","cache_control":{"type":"ephemeral","ttl":"data-file"}},
				{"type":"input_file","file_data":"cmF3LWZpbGU=","cache_control":{"type":"ephemeral","ttl":"raw-file"}}
			]},
			{"type":"message","role":"user","content":[
				{"type":"input_text","text":"valid text"},
				{"type":"input_image","image_url":"data:image/png;base64,","cache_control":{"type":"ephemeral","ttl":"malformed-data-image"}}
			]}
		]
	}`)

	assertResponsesToClaudeCacheControlOracle(t, raw)
}

func TestResponsesToClaudeCacheControlToolsUpstreamOracle(t *testing.T) {
	raw := []byte(`{
		"model":"gpt-test",
		"tools":[
			{"type":"function","name":"root_marker","description":"root wins","parameters":{"type":"object","properties":{}},"cache_control":{"type":"ephemeral","ttl":"root"},"function":{"cache_control":{"type":"ephemeral","ttl":"nested-loser"}}},
			{"type":"function","function":{"name":"nested_marker","description":"nested fallback","parameters":{"type":"object","properties":{}},"cache_control":{"type":"ephemeral","ttl":"nested"}}},
			{"type":"function","function":{"name":"root_string_fallback","description":"root string fallback","parameters":{"type":"object","properties":{}},"cache_control":{"type":"ephemeral","ttl":"nested-valid"}},"cache_control":"bad-root"},
			{"type":"function","function":{"name":"root_null_fallback","description":"root null fallback","parameters":{"type":"object","properties":{}},"cache_control":{"type":"ephemeral","ttl":"nested-valid-null-root"}},"cache_control":null},
			{"type":"function","function":{"name":"nested_malformed","description":"nested malformed","parameters":{"type":"object","properties":{}},"cache_control":"bad-nested"}},
			{"type":"custom","name":"custom_marker","description":"custom marker","cache_control":{"type":"ephemeral","ttl":"custom"}},
			{"type":"custom","name":"custom_malformed","description":"custom malformed","cache_control":"bad-custom"},
			{"type":"namespace","name":"terminal","cache_control":{"type":"ephemeral","ttl":"container"},"tools":[
				{"type":"function","name":"exec","description":"child marker","parameters":{"type":"object","properties":{}},"cache_control":{"type":"ephemeral","ttl":"child-function"}},
				{"type":"custom","name":"raw","description":"child custom marker","cache_control":{"type":"ephemeral","ttl":"child-custom"}},
				{"type":"function","name":"plain","description":"container must not leak","parameters":{"type":"object","properties":{}}}
			]},
			{"type":"function","name":"no_marker","description":"absence","parameters":{"type":"object","properties":{}}}
		],
		"input":[{"type":"message","role":"user","content":[{"type":"input_text","text":"hello"}]}]
	}`)

	assertResponsesToClaudeCacheControlOracle(t, raw)
}

func TestResponsesToClaudeCacheControlAbsenceUpstreamOracle(t *testing.T) {
	raw := []byte(`{
		"model":"gpt-test",
		"tools":[{"type":"function","name":"lookup","parameters":{"type":"object","properties":{}}}],
		"input":[{"type":"message","role":"user","content":[{"type":"input_text","text":"hello"}]}]
	}`)

	assertResponsesToClaudeCacheControlOracle(t, raw)
	oag := TranslateRequest(FormatOpenAIResponse, FormatAnthropic, "claude-test", raw, false)
	if strings.Contains(string(oag), "cache_control") {
		t.Fatalf("cache_control should stay absent; output=%s", oag)
	}
}

func assertResponsesToClaudeCacheControlOracle(t *testing.T, raw []byte) {
	t.Helper()
	upstream := responses_to_claude.ConvertOpenAIResponsesRequestToClaude("claude-test", raw, false)
	oag := TranslateRequest(FormatOpenAIResponse, FormatAnthropic, "claude-test", raw, false)

	if got, want := normalizedClaudeCacheControlSurface(t, oag), normalizedClaudeCacheControlSurface(t, upstream); !reflect.DeepEqual(got, want) {
		t.Fatalf("claude cache-control surface mismatch\noag:      %#v\nupstream: %#v\noagJSON: %s\nupJSON:  %s", got, want, oag, upstream)
	}
}

func normalizedClaudeCacheControlSurface(t *testing.T, raw []byte) map[string]any {
	t.Helper()
	root := gjson.ParseBytes(raw)
	return map[string]any{
		"system":   normalizeJSONResult(t, root.Get("system")),
		"messages": normalizeJSONResult(t, root.Get("messages")),
		"tools":    normalizeJSONResult(t, root.Get("tools")),
	}
}
