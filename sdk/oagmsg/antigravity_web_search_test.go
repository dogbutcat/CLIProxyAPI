package oagmsg

import (
	"encoding/json"
	"fmt"
	"reflect"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/registry"
	agclaude "github.com/router-for-me/CLIProxyAPI/v7/internal/translator/antigravity/claude"
	"github.com/tidwall/gjson"
)

func TestAntigravityNativeWebSearchRequest_DirectOraclePositiveToolChoices(t *testing.T) {
	model := "oagmsg-antigravity-web-search-positive"
	registerOagmsgAntigravityWebSearchModel(t, model, true)

	toolChoices := []struct {
		name string
		raw  string
	}{
		{name: "absent"},
		{name: "string_empty", raw: `"tool_choice":""`},
		{name: "string_auto", raw: `"tool_choice":"auto"`},
		{name: "string_any", raw: `"tool_choice":"any"`},
		{name: "object_type_empty", raw: `"tool_choice":{"type":""}`},
		{name: "object_auto", raw: `"tool_choice":{"type":"auto"}`},
		{name: "object_any", raw: `"tool_choice":{"type":"any"}`},
		{name: "object_tool_web_search", raw: `"tool_choice":{"type":"tool","name":"web_search"}`},
	}
	toolTypes := []string{"web_search_20250305", "web_search_20260209"}

	for _, toolType := range toolTypes {
		for _, toolChoice := range toolChoices {
			t.Run(toolType+"_"+toolChoice.name, func(t *testing.T) {
				raw := anthropicWebSearchRequestJSON(model, `[{"type":"`+toolType+`","name":"web_search"}]`, toolChoice.raw, `[{"role":"user","content":"search weather"}]`)
				got := TranslateRequest(FormatAnthropic, FormatAntigravity, model, []byte(raw), true)
				want := agclaude.ConvertClaudeRequestToAntigravity(model, []byte(raw), true)
				assertNormalizedJSONEqual(t, got, want)
				assertAntigravityWebSearchRequest(t, got)
			})
		}
	}
}

func TestAntigravityNativeWebSearchRequest_FullEnvelopeMaxUsesAndDomains(t *testing.T) {
	model := "oagmsg-antigravity-web-search-envelope"
	registerOagmsgAntigravityWebSearchModel(t, model, true)

	raw := anthropicWebSearchRequestJSON(
		model,
		`[
			{"type":"web_search_20250305","name":"web_search","max_uses":0,"allowed_domains":[" example.com ","",12,"docs.example"]},
			{"type":"web_search_20260209","name":"web_search","max_uses":7}
		]`,
		`"tool_choice":{"type":"tool","name":"web_search"}`,
		`[{"role":"user","content":"  search docs  "}]`,
	)

	got := TranslateRequest(FormatAnthropic, FormatAntigravity, model, []byte(raw), true)
	want := agclaude.ConvertClaudeRequestToAntigravity(model, []byte(raw), true)
	assertNormalizedJSONEqual(t, got, want)

	root := gjson.ParseBytes(got)
	if root.Get("model").String() != model {
		t.Fatalf("model = %q, want %q: %s", root.Get("model").String(), model, got)
	}
	if root.Get("requestType").String() != "web_search" {
		t.Fatalf("requestType = %q, want web_search: %s", root.Get("requestType").String(), got)
	}
	if root.Get("request.contents.#").Int() != 1 || root.Get("request.contents.0.parts.#").Int() != 1 {
		t.Fatalf("expected one user query text part: %s", got)
	}
	if root.Get("request.contents.0.role").String() != "user" {
		t.Fatalf("query role = %q, want user: %s", root.Get("request.contents.0.role").String(), got)
	}
	if root.Get("request.contents.0.parts.0.text").String() != "search docs" {
		t.Fatalf("query text = %q, want trimmed query: %s", root.Get("request.contents.0.parts.0.text").String(), got)
	}
	if root.Get("request.systemInstruction.parts.0.text").String() != antigravityWebSearchSystemInstruction {
		t.Fatalf("unexpected system instruction: %s", got)
	}
	if root.Get("request.tools.#").Int() != 1 || !root.Get("request.tools.0.googleSearch").Exists() {
		t.Fatalf("expected one googleSearch tool: %s", got)
	}
	if gotMax := root.Get("request.tools.0.googleSearch.enhancedContent.imageSearch.maxResultCount").Int(); gotMax != 7 {
		t.Fatalf("maxResultCount = %d, want 7: %s", gotMax, got)
	}
	if root.Get("request.tools.0.googleSearch.includedDomains.0").String() != "example.com" ||
		root.Get("request.tools.0.googleSearch.includedDomains.1").String() != "docs.example" {
		t.Fatalf("includedDomains not preserved in order: %s", got)
	}
	if gotCandidates := root.Get("request.generationConfig.candidateCount").Int(); gotCandidates != 1 {
		t.Fatalf("candidateCount = %d, want 1: %s", gotCandidates, got)
	}
}

func TestAntigravityNativeWebSearchRequest_DefaultMaxUses(t *testing.T) {
	model := "oagmsg-antigravity-web-search-default-max"
	registerOagmsgAntigravityWebSearchModel(t, model, true)

	raw := anthropicWebSearchRequestJSON(model, `[{"type":"web_search_20250305","name":"web_search"}]`, "", `[{"role":"user","content":"q"}]`)
	got := TranslateRequest(FormatAnthropic, FormatAntigravity, model, []byte(raw), false)
	want := agclaude.ConvertClaudeRequestToAntigravity(model, []byte(raw), false)
	assertNormalizedJSONEqual(t, got, want)
	if gotMax := gjson.GetBytes(got, "request.tools.0.googleSearch.enhancedContent.imageSearch.maxResultCount").Int(); gotMax != 5 {
		t.Fatalf("maxResultCount = %d, want default 5: %s", gotMax, got)
	}
}

func TestAntigravityNativeWebSearchRequest_RouteModelOverridesSourceModel(t *testing.T) {
	routeModel := "oagmsg-antigravity-web-search-route-model"
	sourceModel := "oagmsg-antigravity-web-search-source-model"
	registerOagmsgAntigravityWebSearchModel(t, routeModel, true)

	if got := registry.AntigravityWebSearchModelFor(sourceModel); got != "" {
		t.Fatalf("source model unexpectedly supports web search: %q", got)
	}
	if got := registry.AntigravityWebSearchModelFor(routeModel); got != routeModel {
		t.Fatalf("route model registry lookup = %q, want %q", got, routeModel)
	}

	raw := anthropicWebSearchRequestJSON(sourceModel, `[{"type":"web_search_20250305","name":"web_search"}]`, "", `[{"role":"user","content":"q"}]`)
	got := TranslateRequest(FormatAnthropic, FormatAntigravity, routeModel, []byte(raw), false)
	want := agclaude.ConvertClaudeRequestToAntigravity(routeModel, []byte(raw), false)
	assertNormalizedJSONEqual(t, got, want)
	assertAntigravityWebSearchRequest(t, got)
	if gotModel := gjson.GetBytes(got, "model").String(); gotModel != routeModel {
		t.Fatalf("envelope model = %q, want route model %q: %s", gotModel, routeModel, got)
	}
}

func TestAntigravityNativeWebSearchRequest_QueryExtractionDirectOracle(t *testing.T) {
	model := "oagmsg-antigravity-web-search-query"
	registerOagmsgAntigravityWebSearchModel(t, model, true)

	cases := []struct {
		name     string
		messages string
		want     string
	}{
		{
			name:     "string_trimmed",
			messages: `[{"role":"user","content":"  search weather  "}]`,
			want:     "search weather",
		},
		{
			name:     "array_text_blocks_joined_regardless_of_type",
			messages: `[{"role":"user","content":[{"type":"image","text":" first "},{"type":"text","text":"second"},{"type":"tool_result","content":"ignored"},{"text":" third "}]}]`,
			want:     "first\nsecond\nthird",
		},
		{
			name:     "newest_user_ignores_later_assistant",
			messages: `[{"role":"user","content":"old"},{"role":"user","content":"new"},{"role":"assistant","content":"tail"}]`,
			want:     "new",
		},
		{
			name:     "empty_role_candidate",
			messages: `[{"role":"user","content":"old"},{"role":"","content":"empty role query"},{"role":"assistant","content":"tail"}]`,
			want:     "empty role query",
		},
		{
			name:     "missing_role_candidate",
			messages: `[{"role":"user","content":"old"},{"content":"missing role query"},{"role":"assistant","content":"tail"}]`,
			want:     "missing role query",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			raw := anthropicWebSearchRequestJSON(model, `[{"type":"web_search_20250305","name":"web_search"}]`, "", tc.messages)
			got := TranslateRequest(FormatAnthropic, FormatAntigravity, model, []byte(raw), false)
			want := agclaude.ConvertClaudeRequestToAntigravity(model, []byte(raw), false)
			assertNormalizedJSONEqual(t, got, want)
			if query := gjson.GetBytes(got, "request.contents.0.parts.0.text").String(); query != tc.want {
				t.Fatalf("query = %q, want %q: %s", query, tc.want, got)
			}
		})
	}
}

func TestAntigravityNativeWebSearchRequest_NegativeGatesUseGenericPath(t *testing.T) {
	model := "oagmsg-antigravity-web-search-negative"
	unsupportedRegisteredModel := "oagmsg-antigravity-web-search-registered-no-support"
	registerOagmsgAntigravityWebSearchModel(t, model, true)
	registerOagmsgAntigravityWebSearchModel(t, unsupportedRegisteredModel, false)

	cases := []struct {
		name       string
		model      string
		tools      string
		toolChoice string
	}{
		{name: "no_tools", tools: `[]`},
		{name: "tools_null", tools: `null`},
		{name: "tools_object", tools: `{"type":"web_search_20250305","name":"web_search"}`},
		{name: "tools_string", tools: `"not an array"`},
		{name: "non_search_tool", tools: `[{"name":"lookup","input_schema":{"type":"object"}}]`},
		{name: "mixed_tools", tools: `[{"type":"web_search_20250305","name":"web_search"},{"name":"lookup","input_schema":{"type":"object"}}]`},
		{name: "typed_tool_plus_string_tool_entry", tools: `[{"type":"web_search_20250305","name":"web_search"},"not an object"]`},
		{name: "typed_tool_plus_numeric_tool_entry", tools: `[{"type":"web_search_20250305","name":"web_search"},42]`},
		{name: "tool_choice_string_none", tools: `[{"type":"web_search_20250305","name":"web_search"}]`, toolChoice: `"tool_choice":"none"`},
		{name: "tool_choice_object_none", tools: `[{"type":"web_search_20250305","name":"web_search"}]`, toolChoice: `"tool_choice":{"type":"none"}`},
		{name: "tool_choice_null", tools: `[{"type":"web_search_20250305","name":"web_search"}]`, toolChoice: `"tool_choice":null`},
		{name: "tool_choice_array", tools: `[{"type":"web_search_20250305","name":"web_search"}]`, toolChoice: `"tool_choice":["auto"]`},
		{name: "tool_choice_numeric", tools: `[{"type":"web_search_20250305","name":"web_search"}]`, toolChoice: `"tool_choice":1`},
		{name: "tool_choice_another_named_tool", tools: `[{"type":"web_search_20250305","name":"web_search"}]`, toolChoice: `"tool_choice":{"type":"tool","name":"lookup"}`},
		{name: "tool_choice_tool_missing_name", tools: `[{"type":"web_search_20250305","name":"web_search"}]`, toolChoice: `"tool_choice":{"type":"tool"}`},
		{name: "tool_choice_tool_non_string_name", tools: `[{"type":"web_search_20250305","name":"web_search"}]`, toolChoice: `"tool_choice":{"type":"tool","name":42}`},
		{name: "tool_choice_non_string_type", tools: `[{"type":"web_search_20250305","name":"web_search"}]`, toolChoice: `"tool_choice":{"type":42,"name":"web_search"}`},
		{name: "tool_choice_string_whitespace_auto", tools: `[{"type":"web_search_20250305","name":"web_search"}]`, toolChoice: `"tool_choice":" auto "`},
		{name: "tool_choice_object_whitespace_type", tools: `[{"type":"web_search_20250305","name":"web_search"}]`, toolChoice: `"tool_choice":{"type":" auto "}`},
		{name: "tool_choice_object_whitespace_name", tools: `[{"type":"web_search_20250305","name":"web_search"}]`, toolChoice: `"tool_choice":{"type":"tool","name":" web_search "}`},
		{name: "unsupported_model", model: "oagmsg-antigravity-web-search-unsupported", tools: `[{"type":"web_search_20250305","name":"web_search"}]`},
		{name: "registered_model_without_web_search_support", model: unsupportedRegisteredModel, tools: `[{"type":"web_search_20250305","name":"web_search"}]`},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			testModel := model
			if tc.model != "" {
				testModel = tc.model
			}
			raw := anthropicWebSearchRequestJSON(testModel, tc.tools, tc.toolChoice, `[{"role":"user","content":"q"}]`)
			assertAnthropicAntigravityGenericPath(t, []byte(raw))
			assertDirectOracleGenericWebSearchGate(t, []byte(raw), testModel)
		})
	}
}

func TestAntigravityNativeWebSearchRequest_AbsentVersusNullToolChoice(t *testing.T) {
	model := "oagmsg-antigravity-web-search-absent-null-choice"
	registerOagmsgAntigravityWebSearchModel(t, model, true)

	absent := anthropicWebSearchRequestJSON(model, `[{"type":"web_search_20250305","name":"web_search"}]`, "", `[{"role":"user","content":"q"}]`)
	absentOut := TranslateRequest(FormatAnthropic, FormatAntigravity, model, []byte(absent), false)
	absentOracle := agclaude.ConvertClaudeRequestToAntigravity(model, []byte(absent), false)
	assertNormalizedJSONEqual(t, absentOut, absentOracle)
	if got := gjson.GetBytes(absentOut, "requestType").String(); got != "web_search" {
		t.Fatalf("absent tool_choice requestType = %q, want web_search: %s", got, absentOut)
	}
	assertAntigravityWebSearchRequest(t, absentOracle)

	nullChoice := anthropicWebSearchRequestJSON(model, `[{"type":"web_search_20250305","name":"web_search"}]`, `"tool_choice":null`, `[{"role":"user","content":"q"}]`)
	assertAnthropicAntigravityGenericPath(t, []byte(nullChoice))
	assertDirectOracleGenericWebSearchGate(t, []byte(nullChoice), model)
}

func TestAntigravityNativeWebSearchRequest_NonClaudeSourceUsesGenericPath(t *testing.T) {
	model := "oagmsg-antigravity-web-search-non-claude"
	registerOagmsgAntigravityWebSearchModel(t, model, true)

	req := &UnifiedRequest{
		Model:        model,
		SourceFormat: FormatOpenAI,
		Messages:     []OagMessage{UserTextMsg("q")},
		Tools:        []map[string]any{{"type": "web_search_20250305", "name": "web_search"}},
	}
	out, err := (&AntigravityHandler{}).SerializeRequest(req)
	if err != nil {
		t.Fatalf("SerializeRequest error: %v", err)
	}
	if got := gjson.GetBytes(out, "requestType").String(); got == "web_search" {
		t.Fatalf("non-Claude source should use generic path: %s", out)
	}
	if !gjson.GetBytes(out, "request").IsObject() {
		t.Fatalf("generic Antigravity envelope missing request object: %s", out)
	}
}

func anthropicWebSearchRequestJSON(model, tools, toolChoice, messages string) string {
	extra := ""
	if toolChoice != "" {
		extra = "," + toolChoice
	}
	return fmt.Sprintf(`{"model":%q,"messages":%s,"tools":%s%s}`, model, messages, tools, extra)
}

func registerOagmsgAntigravityWebSearchModel(t *testing.T, model string, supportsWebSearch bool) {
	t.Helper()
	clientName := "test-" + model
	registry.GetGlobalRegistry().RegisterClient(clientName, "antigravity", []*registry.ModelInfo{
		{ID: model, SupportsWebSearch: supportsWebSearch},
	})
	t.Cleanup(func() { registry.GetGlobalRegistry().UnregisterClient(clientName) })
}

func assertAntigravityWebSearchRequest(t *testing.T, body []byte) {
	t.Helper()
	if got := gjson.GetBytes(body, "requestType").String(); got != "web_search" {
		t.Fatalf("requestType = %q, want web_search: %s", got, body)
	}
	if !gjson.GetBytes(body, "request.tools.0.googleSearch").Exists() {
		t.Fatalf("missing native googleSearch tool: %s", body)
	}
}

func assertAnthropicAntigravityGenericPath(t *testing.T, raw []byte) {
	t.Helper()
	req, err := (&AnthropicHandler{}).ParseRequest(raw)
	if err != nil {
		t.Fatalf("ParseRequest error: %v", err)
	}
	got, err := (&AntigravityHandler{}).SerializeRequest(req)
	if err != nil {
		t.Fatalf("SerializeRequest error: %v", err)
	}

	genericReq := *req
	genericReq.anthropicWebSearch = nil
	want, err := (&AntigravityHandler{}).SerializeRequest(&genericReq)
	if err != nil {
		t.Fatalf("generic SerializeRequest error: %v", err)
	}
	assertNormalizedJSONEqual(t, got, want)
	if requestType := gjson.GetBytes(got, "requestType").String(); requestType == "web_search" {
		t.Fatalf("ineligible request should use generic path: %s", got)
	}
	if gjson.GetBytes(got, "request.tools.#(googleSearch)").Exists() {
		t.Fatalf("generic path should not inject googleSearch: %s", got)
	}
}

func assertDirectOracleGenericWebSearchGate(t *testing.T, raw []byte, model string) {
	t.Helper()
	oracle := agclaude.ConvertClaudeRequestToAntigravity(model, raw, false)
	if requestType := gjson.GetBytes(oracle, "requestType").String(); requestType == "web_search" {
		t.Fatalf("direct oracle built web_search for ineligible request: %s", oracle)
	}
	if gjson.GetBytes(oracle, "request.tools.#(googleSearch)").Exists() {
		t.Fatalf("direct oracle injected googleSearch for ineligible request: %s", oracle)
	}
}

func assertNormalizedJSONEqual(t *testing.T, got, want []byte) {
	t.Helper()
	var gotJSON any
	if err := json.Unmarshal(got, &gotJSON); err != nil {
		t.Fatalf("got invalid JSON: %v\n%s", err, got)
	}
	var wantJSON any
	if err := json.Unmarshal(want, &wantJSON); err != nil {
		t.Fatalf("want invalid JSON: %v\n%s", err, want)
	}
	if !reflect.DeepEqual(gotJSON, wantJSON) {
		gotPretty, _ := json.MarshalIndent(gotJSON, "", "  ")
		wantPretty, _ := json.MarshalIndent(wantJSON, "", "  ")
		t.Fatalf("normalized JSON mismatch\ngot:\n%s\nwant:\n%s", gotPretty, wantPretty)
	}
}
