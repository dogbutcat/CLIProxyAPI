package oagmsg

import (
	"encoding/json"
	"testing"
)

func TestToolDescriptorBuildsFromTopLevelAndAdditionalTools(t *testing.T) {
	raw := []byte(`{
		"tools":[
			{"type":"function","name":"top"},
			{"type":"namespace","name":"n","tools":[{"type":"function","name":"child"}]}
		],
		"input":[
			{"type":"additional_tools","tools":[
				{"type":"custom","name":"extra"},
				{"type":"namespace","name":"m","tools":[{"type":"custom","name":"child"}]}
			]},
			{"type":"message","role":"user","content":[{"type":"input_text","text":"hello"}]}
		]
	}`)

	index := buildToolDescriptorIndex(raw)
	want := []string{"top", "n__child", "extra", "m__child"}
	if len(index.descriptors) != len(want) {
		t.Fatalf("descriptor count = %d, want %d", len(index.descriptors), len(want))
	}
	for i, wantName := range want {
		if got := index.descriptors[i].name; got != wantName {
			t.Fatalf("descriptors[%d].name = %q, want %q", i, got, wantName)
		}
	}
	if got := len(index.winners); got != len(want) {
		t.Fatalf("winner count = %d, want %d", got, len(want))
	}
}

func TestToolDescriptorFeedsUnifiedRequestToolsThroughWinners(t *testing.T) {
	raw := []byte(`{
		"model":"test",
		"tools":[{"type":"function","name":"top"}],
		"input":[{"type":"additional_tools","tools":[{"type":"custom","name":"extra"}]}]
	}`)

	req, err := (&InteractionsHandler{}).ParseRequest(raw)
	if err != nil {
		t.Fatal(err)
	}
	if len(req.Tools) != 2 {
		t.Fatalf("UnifiedRequest.Tools count = %d, want 2", len(req.Tools))
	}
	if got := req.Tools[0]["name"]; got != "top" {
		t.Fatalf("UnifiedRequest.Tools[0].name = %v, want top", got)
	}
	if got := req.Tools[1]["name"]; got != "extra" {
		t.Fatalf("UnifiedRequest.Tools[1].name = %v, want extra", got)
	}
	if got := req.Tools[1]["type"]; got != "custom" {
		t.Fatalf("UnifiedRequest.Tools[1].type = %v, want custom", got)
	}
	if _, ok := buildToolDescriptorIndex(raw).winner("extra"); !ok {
		t.Fatal("descriptor index did not include input[].additional_tools")
	}
}

func TestToolDescriptorNamespaceQualificationRules(t *testing.T) {
	tests := []struct {
		name      string
		namespace string
		child     string
		want      string
	}{
		{name: "plain child", namespace: "shell", child: "exec", want: "shell__exec"},
		{name: "namespace has separator", namespace: "shell__", child: "exec", want: "shell__exec"},
		{name: "already qualified", namespace: "shell", child: "shell__exec", want: "shell__exec"},
		{name: "same as namespace", namespace: "shell", child: "shell", want: "shell"},
		{name: "mcp child", namespace: "shell", child: "mcp__server__tool", want: "mcp__server__tool"},
		{name: "mcp namespace qualifies plain child", namespace: "mcp__node_repl", child: "js", want: "mcp__node_repl__js"},
		{name: "prefix is not qualified", namespace: "collab", child: "collaboration", want: "collab__collaboration"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := qualifyToolDescriptorName(tt.namespace, tt.child); got != tt.want {
				t.Fatalf("qualified name = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestNamespaceWinnerCollisionPrecedencePermutations(t *testing.T) {
	tests := []struct {
		name       string
		raw        string
		wantType   string
		wantDirect bool
		wantDesc   string
	}{
		{
			name: "top direct beats earlier top namespace",
			raw: `{"tools":[
				{"type":"namespace","name":"n","tools":[{"type":"function","name":"x","description":"top namespace"}]},
				{"type":"custom","name":"n__x","description":"top direct"}
			]}`,
			wantType:   "custom",
			wantDirect: true,
			wantDesc:   "top direct",
		},
		{
			name: "top direct beats later top namespace",
			raw: `{"tools":[
				{"type":"function","name":"n__x","description":"top direct"},
				{"type":"namespace","name":"n","tools":[{"type":"custom","name":"x","description":"top namespace"}]}
			]}`,
			wantType:   "function",
			wantDirect: true,
			wantDesc:   "top direct",
		},
		{
			name: "additional direct beats earlier additional namespace",
			raw: `{"input":[
				{"type":"additional_tools","tools":[{"type":"namespace","name":"n","tools":[{"type":"function","name":"x","description":"additional namespace"}]}]},
				{"type":"additional_tools","tools":[{"type":"custom","name":"n__x","description":"additional direct"}]}
			]}`,
			wantType:   "custom",
			wantDirect: true,
			wantDesc:   "additional direct",
		},
		{
			name: "additional direct beats later additional namespace",
			raw: `{"input":[
				{"type":"additional_tools","tools":[{"type":"function","name":"n__x","description":"additional direct"}]},
				{"type":"additional_tools","tools":[{"type":"namespace","name":"n","tools":[{"type":"custom","name":"x","description":"additional namespace"}]}]}
			]}`,
			wantType:   "function",
			wantDirect: true,
			wantDesc:   "additional direct",
		},
		{
			name: "top namespace beats additional direct",
			raw: `{
				"tools":[{"type":"namespace","name":"n","tools":[{"type":"function","name":"x","description":"top namespace"}]}],
				"input":[{"type":"additional_tools","tools":[{"type":"custom","name":"n__x","description":"additional direct"}]}]
			}`,
			wantType:   "function",
			wantDirect: false,
			wantDesc:   "top namespace",
		},
		{
			name: "top direct beats additional namespace",
			raw: `{
				"tools":[{"type":"custom","name":"n__x","description":"top direct"}],
				"input":[{"type":"additional_tools","tools":[{"type":"namespace","name":"n","tools":[{"type":"function","name":"x","description":"additional namespace"}]}]}]
			}`,
			wantType:   "custom",
			wantDirect: true,
			wantDesc:   "top direct",
		},
		{
			name: "top direct beats additional direct",
			raw: `{
				"tools":[{"type":"function","name":"n__x","description":"top direct"}],
				"input":[{"type":"additional_tools","tools":[{"type":"custom","name":"n__x","description":"additional direct"}]}]
			}`,
			wantType:   "function",
			wantDirect: true,
			wantDesc:   "top direct",
		},
		{
			name: "top namespace beats additional namespace",
			raw: `{
				"tools":[{"type":"namespace","name":"n","tools":[{"type":"custom","name":"x","description":"top namespace"}]}],
				"input":[{"type":"additional_tools","tools":[{"type":"namespace","name":"n","tools":[{"type":"function","name":"x","description":"additional namespace"}]}]}]
			}`,
			wantType:   "custom",
			wantDirect: false,
			wantDesc:   "top namespace",
		},
		{
			name: "first additional direct wins same class",
			raw: `{"input":[
				{"type":"additional_tools","tools":[{"type":"custom","name":"n__x","description":"first additional direct"}]},
				{"type":"additional_tools","tools":[{"type":"function","name":"n__x","description":"second additional direct"}]}
			]}`,
			wantType:   "custom",
			wantDirect: true,
			wantDesc:   "first additional direct",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			index := buildToolDescriptorIndex([]byte(tt.raw))
			winner, ok := index.winner("n__x")
			if !ok {
				t.Fatal("missing n__x winner")
			}
			if winner.toolType != tt.wantType {
				t.Fatalf("winner type = %q, want %q", winner.toolType, tt.wantType)
			}
			if winner.direct != tt.wantDirect {
				t.Fatalf("winner direct = %v, want %v", winner.direct, tt.wantDirect)
			}
			if got := stringValue(winner.tool["description"]); got != tt.wantDesc {
				t.Fatalf("winner description = %q, want %q", got, tt.wantDesc)
			}
		})
	}
}

func TestFirstOrderToolDescriptorIndexUsesExpandedOrder(t *testing.T) {
	raw := []byte(`{
		"tools":[
			{"type":"namespace","name":"n","tools":[{"type":"function","name":"x","description":"namespace first"}]},
			{"type":"custom","name":"n__x","description":"direct second"}
		],
		"input":[{"type":"additional_tools","tools":[{"type":"function","name":"n__x","description":"additional duplicate"}]}]
	}`)

	index := firstOrderToolDescriptorIndex(buildToolDescriptorIndex(raw))
	winner, ok := index.winner("n__x")
	if !ok {
		t.Fatal("missing n__x winner")
	}
	if winner.toolType != "function" || winner.direct {
		t.Fatalf("winner = (type=%q, direct=%v), want namespace function", winner.toolType, winner.direct)
	}
	if got := stringValue(winner.tool["description"]); got != "namespace first" {
		t.Fatalf("winner description = %q, want namespace first", got)
	}
}

func TestNamespaceWinnerAliasesPreferDirectTools(t *testing.T) {
	raw := []byte(`{
		"tools":[
			{"type":"function","name":"send","description":"direct short"},
			{"type":"namespace","name":"n","tools":[
				{"type":"function","name":"send","description":"namespace send"},
				{"type":"custom","name":"exec","description":"namespace exec"}
			]},
			{"type":"custom","name":"n__lost","description":"direct qualified"},
			{"type":"namespace","name":"n","tools":[{"type":"function","name":"lost","description":"lost namespace"}]}
		]
	}`)

	index := buildToolDescriptorIndex(raw)
	if got, ok := index.lookup("send"); !ok || got.name != "send" {
		t.Fatalf("lookup(send) = (%q, %v), want direct send", got.name, ok)
	}
	if got, ok := index.lookup("exec"); !ok || got.name != "n__exec" || got.direct {
		t.Fatalf("lookup(exec) = (%q, direct=%v, ok=%v), want namespace n__exec", got.name, got.direct, ok)
	}
	if got, ok := index.lookup("lost"); ok {
		t.Fatalf("lookup(lost) = (%q, true), want no alias for losing namespace", got.name)
	}
	if got, ok := index.lookup("n__lost"); !ok || got.name != "n__lost" || !got.direct {
		t.Fatalf("lookup(n__lost) = (%q, direct=%v, ok=%v), want direct n__lost", got.name, got.direct, ok)
	}
}

func TestToolDescriptorWinnerMapIsDeterministic(t *testing.T) {
	raw := []byte(`{
		"tools":[
			{"type":"namespace","name":"n","tools":[{"type":"function","name":"x"}]},
			{"type":"custom","name":"n__x"}
		],
		"input":[{"type":"additional_tools","tools":[{"type":"function","name":"n__x"}]}]
	}`)

	first := buildToolDescriptorIndex(raw)
	second := buildToolDescriptorIndex(raw)
	if len(first.winners) != len(second.winners) {
		t.Fatalf("winner map size changed: %d != %d", len(first.winners), len(second.winners))
	}
	for name, firstWinner := range first.winners {
		secondWinner, ok := second.winners[name]
		if !ok {
			t.Fatalf("second build missing winner %q", name)
		}
		if firstWinner.order != secondWinner.order || firstWinner.name != secondWinner.name || firstWinner.toolType != secondWinner.toolType || firstWinner.direct != secondWinner.direct {
			t.Fatalf("winner %q changed: first=%+v second=%+v", name, firstWinner, secondWinner)
		}
	}
}

func TestToolDescriptorAcceptsOpenAINestedFunction(t *testing.T) {
	raw := []byte(`{"tools":[{"type":"function","function":{"name":"nested","description":"OpenAI shape"}}]}`)
	index := buildToolDescriptorIndex(raw)
	winner, ok := index.winner("nested")
	if !ok {
		t.Fatal("missing nested OpenAI function winner")
	}
	function, ok := winner.tool["function"].(map[string]any)
	if !ok || stringValue(function["description"]) != "OpenAI shape" {
		t.Fatalf("winner did not preserve nested tool: %#v", winner.tool)
	}
}

func TestToolDescriptorIgnoresMalformedJSON(t *testing.T) {
	index := buildToolDescriptorIndex([]byte(`{`))
	if len(index.descriptors) != 0 || len(index.winners) != 0 || len(index.aliases) != 0 {
		encoded, _ := json.Marshal(index)
		t.Fatalf("malformed JSON produced descriptors: %s", encoded)
	}
}
