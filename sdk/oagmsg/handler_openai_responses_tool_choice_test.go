package oagmsg

import (
	"strconv"
	"testing"

	"github.com/tidwall/gjson"
)

func TestResponsesToOpenAIChatPreservesResponsesToolChoiceObject(t *testing.T) {
	tests := []struct {
		name          string
		raw           string
		wantType      string
		wantName      string
		wantNamespace string
		wantTools     []string
	}{
		{
			name: "namespaced function choice",
			raw: `{
				"model":"gpt-test",
				"parallel_tool_calls":false,
				"tools":[
					{"type":"function","name":"exec","description":"top-level exec","parameters":{"type":"object","properties":{"command":{"type":"string"}}}},
					{"type":"custom","name":"freeform","description":"freeform input"},
					{"type":"namespace","name":"collaboration","tools":[
						{"type":"function","name":"spawn","description":"spawn worker","parameters":{"type":"object","properties":{}}},
						{"type":"custom","name":"send","description":"send message"}
					]}
				],
				"input":[
					{"type":"additional_tools","tools":[{"type":"function","name":"wait","parameters":{"type":"object","properties":{}}}]},
					{"type":"message","role":"user","content":[{"type":"input_text","text":"hello"}]}
				],
				"tool_choice":{"type":"function","name":"spawn","namespace":"collaboration"}
			}`,
			wantType:      "function",
			wantName:      "spawn",
			wantNamespace: "collaboration",
			wantTools:     []string{"exec", "freeform", "collaboration__spawn", "collaboration__send", "wait"},
		},
		{
			name: "first expanded declaration collision winner",
			raw: `{
				"model":"gpt-test",
				"tools":[
					{"type":"namespace","name":"n","tools":[{"type":"function","name":"x","description":"namespace x","parameters":{"type":"object","properties":{}}}]},
					{"type":"custom","name":"n__x","description":"direct custom"}
				],
				"input":[
					{"type":"additional_tools","tools":[
						{"type":"function","name":"n__x","description":"additional duplicate","parameters":{"type":"object","properties":{}}},
						{"type":"namespace","name":"n","tools":[{"type":"custom","name":"y","description":"namespace custom"}]}
					]}
				],
				"tool_choice":{"type":"custom","name":"n__x"}
			}`,
			wantType:  "custom",
			wantName:  "n__x",
			wantTools: []string{"n__x", "n__y"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			out := TranslateRequest(FormatOpenAIResponse, FormatOpenAI, "gpt-5.4", []byte(tt.raw), false)
			root := gjson.ParseBytes(out)

			if root.Get("tool_choice.function").Exists() {
				t.Fatalf("tool_choice was rewritten to Chat function form: %s", out)
			}
			if got := root.Get("tool_choice.type").String(); got != tt.wantType {
				t.Fatalf("tool_choice.type = %q, want %q; output=%s", got, tt.wantType, out)
			}
			if got := root.Get("tool_choice.name").String(); got != tt.wantName {
				t.Fatalf("tool_choice.name = %q, want %q; output=%s", got, tt.wantName, out)
			}
			if got := root.Get("tool_choice.namespace").String(); got != tt.wantNamespace {
				t.Fatalf("tool_choice.namespace = %q, want %q; output=%s", got, tt.wantNamespace, out)
			}
			for i, wantName := range tt.wantTools {
				if got := root.Get("tools." + strconv.Itoa(i) + ".function.name").String(); got != wantName {
					t.Fatalf("tools.%d.function.name = %q, want %q; output=%s", i, got, wantName, out)
				}
			}
		})
	}
}
