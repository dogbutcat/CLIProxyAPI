package conformance

import (
	"context"
	"encoding/json"
	"fmt"
	"reflect"
	"regexp"
	"sort"
	"strings"
	"testing"

	responses_to_claude "github.com/router-for-me/CLIProxyAPI/v7/internal/translator/claude/openai/responses"
	responses_to_chat "github.com/router-for-me/CLIProxyAPI/v7/internal/translator/openai/openai/responses"
	"github.com/router-for-me/CLIProxyAPI/v7/sdk/oagmsg"
)

type t81OracleDirection string
type t81CompareContract string

const (
	t81DirectionResponsesToClaudeRequest   t81OracleDirection = "responses_to_claude_request"
	t81DirectionResponsesToChatRequest     t81OracleDirection = "responses_to_chat_request"
	t81DirectionClaudeToResponsesStream    t81OracleDirection = "claude_to_responses_stream_response"
	t81DirectionClaudeToResponsesNonStream t81OracleDirection = "claude_to_responses_non_stream_response"

	t81ContractG2Request        t81CompareContract = "g2_custom_request"
	t81ContractG2Response       t81CompareContract = "g2_custom_response"
	t81ContractG3Request        t81CompareContract = "g3_namespace_request"
	t81ContractG3Response       t81CompareContract = "g3_namespace_response"
	t81ContractG5CachePlacement t81CompareContract = "g5_cache_control_placement"
	t81ContractG7ChatTools      t81CompareContract = "g7_chat_tools"
)

type t81OracleFixture struct {
	requestRaw []byte
	streamRaw  [][]byte
}

type t81OracleManifestRow struct {
	gap        string
	direction  t81OracleDirection
	stream     string
	fixture    string
	contract   t81CompareContract
	exclusions []string
}

var t81OracleManifest = []t81OracleManifestRow{
	{gap: "G2 custom/freeform declarations and history", direction: t81DirectionResponsesToClaudeRequest, stream: "non-stream", fixture: "g2_custom_history", contract: t81ContractG2Request, exclusions: []string{"metadata.user_id", "stream"}},
	{gap: "G2 custom/freeform empty input history", direction: t81DirectionResponsesToClaudeRequest, stream: "non-stream", fixture: "g2_custom_empty_input_history", contract: t81ContractG2Request, exclusions: []string{"metadata.user_id", "stream"}},
	{gap: "G2 custom/freeform non-JSON input history", direction: t81DirectionResponsesToClaudeRequest, stream: "non-stream", fixture: "g2_custom_non_json_input_history", contract: t81ContractG2Request, exclusions: []string{"metadata.user_id", "stream"}},
	{gap: "G2 custom/freeform stream response requires request context", direction: t81DirectionClaudeToResponsesStream, stream: "stream", fixture: "g2_custom_response_context", contract: t81ContractG2Response, exclusions: []string{"response.created.response.id", "response.created.response.created_at", "response.in_progress.response.id", "response.in_progress.response.created_at", "response.in_progress.response.output", "response.completed.response.id", "response.completed.response.created_at", "response.completed.response.usage"}},
	{gap: "G2 custom/freeform non-stream response requires request context", direction: t81DirectionClaudeToResponsesNonStream, stream: "non-stream", fixture: "g2_custom_response_context", contract: t81ContractG2Response, exclusions: []string{"id", "created_at", "background", "error", "incomplete_details", "usage"}},
	{gap: "G2 custom/freeform empty response input", direction: t81DirectionClaudeToResponsesStream, stream: "stream", fixture: "g2_custom_empty_response_context", contract: t81ContractG2Response, exclusions: []string{"response.created.response.id", "response.created.response.created_at", "response.in_progress.response.id", "response.in_progress.response.created_at", "response.in_progress.response.output", "response.completed.response.id", "response.completed.response.created_at", "response.completed.response.usage"}},
	{gap: "G3 namespace qualification and collision winners", direction: t81DirectionResponsesToClaudeRequest, stream: "non-stream", fixture: "g3_namespace_collisions", contract: t81ContractG3Request, exclusions: []string{"metadata.user_id", "stream"}},
	{gap: "G3 already-qualified mcp stability", direction: t81DirectionResponsesToClaudeRequest, stream: "non-stream", fixture: "g3_mcp_stability", contract: t81ContractG3Request, exclusions: []string{"metadata.user_id", "stream"}},
	{gap: "G3 top-level before additional ordering", direction: t81DirectionResponsesToClaudeRequest, stream: "non-stream", fixture: "g3_ordering", contract: t81ContractG3Request, exclusions: []string{"metadata.user_id", "stream"}},
	{gap: "G3 namespace response requires request context", direction: t81DirectionClaudeToResponsesStream, stream: "stream", fixture: "g3_namespace_response_context", contract: t81ContractG3Response, exclusions: []string{"response.created.response.id", "response.created.response.created_at", "response.in_progress.response.id", "response.in_progress.response.created_at", "response.in_progress.response.output", "response.completed.response.id", "response.completed.response.created_at", "response.completed.response.usage"}},
	{gap: "G3 namespace non-stream response requires request context", direction: t81DirectionClaudeToResponsesNonStream, stream: "non-stream", fixture: "g3_namespace_response_context", contract: t81ContractG3Response, exclusions: []string{"id", "created_at", "background", "error", "incomplete_details", "usage"}},
	{gap: "G5 cache_control content and system placement", direction: t81DirectionResponsesToClaudeRequest, stream: "non-stream", fixture: "g5_cache_content_system", contract: t81ContractG5CachePlacement, exclusions: []string{"metadata.user_id", "stream"}},
	{gap: "G5 cache_control tool declaration placement", direction: t81DirectionResponsesToClaudeRequest, stream: "non-stream", fixture: "g5_cache_tools", contract: t81ContractG5CachePlacement, exclusions: []string{"metadata.user_id", "stream"}},
	{gap: "G5 cache_control missing and empty cases", direction: t81DirectionResponsesToClaudeRequest, stream: "non-stream", fixture: "g5_cache_missing_empty", contract: t81ContractG5CachePlacement, exclusions: []string{"metadata.user_id", "stream"}},
	{gap: "G7 Responses tools to Chat function/custom/namespace/additional", direction: t81DirectionResponsesToChatRequest, stream: "non-stream", fixture: "g7_chat_tool_matrix", contract: t81ContractG7ChatTools, exclusions: []string{"stream", "messages"}},
	{gap: "G7 Responses tools to Chat ordering", direction: t81DirectionResponsesToChatRequest, stream: "non-stream", fixture: "g7_chat_ordering", contract: t81ContractG7ChatTools, exclusions: []string{"stream", "messages"}},
	{gap: "G7 Responses tools to Chat collision winners", direction: t81DirectionResponsesToChatRequest, stream: "non-stream", fixture: "g7_chat_collision_winners", contract: t81ContractG7ChatTools, exclusions: []string{"stream", "messages"}},
}

var t81OracleFixtures = map[string]t81OracleFixture{
	"g2_custom_history": {
		requestRaw: []byte(`{
			"model":"gpt-test",
			"tools":[{"type":"custom","name":"exec","description":"Run a command"}],
			"input":[
				{"type":"message","role":"user","content":[{"type":"input_text","text":"run tools"}]},
				{"type":"custom_tool_call","call_id":"call.custom:1","name":"exec","input":"pwd"},
				{"type":"custom_tool_call_output","call_id":"call.custom:1","output":"/workspace"}
			]
		}`),
	},
	"g2_custom_empty_input_history": {
		requestRaw: []byte(`{
			"model":"gpt-test",
			"tools":[{"type":"custom","name":"exec","description":"Run a command"}],
			"input":[
				{"type":"custom_tool_call","call_id":"call.custom:empty","name":"exec","input":""},
				{"type":"custom_tool_call_output","call_id":"call.custom:empty","output":""}
			]
		}`),
	},
	"g2_custom_non_json_input_history": {
		requestRaw: []byte(`{
			"model":"gpt-test",
			"tools":[{"type":"custom","name":"exec","description":"Run a command"}],
			"input":[
				{"type":"custom_tool_call","call_id":"call.custom:raw","name":"exec","input":"printf 'not json' && pwd"},
				{"type":"custom_tool_call_output","call_id":"call.custom:raw","output":"ok"}
			]
		}`),
	},
	"g2_custom_response_context": {
		requestRaw: []byte(`{
			"model":"gpt-test",
			"input":[{"type":"additional_tools","role":"developer","tools":[{"type":"custom","name":"exec"}]}]
		}`),
		streamRaw: [][]byte{
			[]byte(`data: {"type":"message_start","message":{"id":"msg_custom","usage":{"input_tokens":1,"output_tokens":0}}}`),
			[]byte(`data: {"type":"content_block_start","index":0,"content_block":{"type":"tool_use","id":"call_custom","name":"exec","input":{}}}`),
			[]byte(`data: {"type":"content_block_delta","index":0,"delta":{"type":"input_json_delta","partial_json":"{\"input\":\"pwd\"}"}}`),
			[]byte(`data: {"type":"content_block_stop","index":0}`),
			[]byte(`data: {"type":"message_stop"}`),
		},
	},
	"g2_custom_empty_response_context": {
		requestRaw: []byte(`{
			"model":"gpt-test",
			"tools":[{"type":"custom","name":"exec"}]
		}`),
		streamRaw: [][]byte{
			[]byte(`data: {"type":"message_start","message":{"id":"msg_custom_empty","usage":{"input_tokens":1,"output_tokens":0}}}`),
			[]byte(`data: {"type":"content_block_start","index":0,"content_block":{"type":"tool_use","id":"call_custom_empty","name":"exec","input":{}}}`),
			[]byte(`data: {"type":"content_block_stop","index":0}`),
			[]byte(`data: {"type":"message_stop"}`),
		},
	},
	"g3_namespace_collisions": {
		requestRaw: []byte(`{
			"model":"gpt-test",
			"tools":[
				{"type":"namespace","name":"n","tools":[{"type":"function","name":"x","description":"namespace x","parameters":{"type":"object","properties":{}}}]},
				{"type":"custom","name":"n__x","description":"direct custom wins"},
				{"type":"function","name":"direct","description":"direct function","parameters":{"type":"object","properties":{}}}
			],
			"input":[
				{"type":"additional_tools","tools":[
					{"type":"namespace","name":"n","tools":[{"type":"custom","name":"y","description":"namespace custom"}]},
					{"type":"function","name":"additional","description":"additional function","parameters":{"type":"object","properties":{}}}
				]},
				{"type":"message","role":"user","content":[{"type":"input_text","text":"hello"}]}
			],
			"tool_choice":{"type":"custom","name":"n__x"}
		}`),
	},
	"g3_mcp_stability": {
		requestRaw: []byte(`{
			"model":"gpt-test",
			"input":[
				{"type":"additional_tools","tools":[
					{"type":"namespace","name":"mcp__node_repl","tools":[
						{"type":"function","name":"js","description":"Run JS","parameters":{"type":"object","properties":{}}},
						{"type":"function","name":"mcp__already_qualified","description":"stable","parameters":{"type":"object","properties":{}}}
					]}
				]},
				{"type":"message","role":"user","content":[{"type":"input_text","text":"hello"}]}
			],
			"tool_choice":{"type":"function","name":"js","namespace":"mcp__node_repl"}
		}`),
	},
	"g3_ordering": {
		requestRaw: []byte(`{
			"model":"gpt-test",
			"tools":[
				{"type":"function","name":"first","parameters":{"type":"object","properties":{}}},
				{"type":"namespace","name":"n","tools":[{"type":"function","name":"middle","parameters":{"type":"object","properties":{}}}]},
				{"type":"function","name":"last","parameters":{"type":"object","properties":{}}}
			],
			"input":[
				{"type":"additional_tools","tools":[{"type":"function","name":"additional","parameters":{"type":"object","properties":{}}}]},
				{"type":"message","role":"user","content":[{"type":"input_text","text":"hello"}]}
			]
		}`),
	},
	"g3_namespace_response_context": {
		requestRaw: []byte(`{
			"model":"gpt-test",
			"input":[{"type":"additional_tools","tools":[{"type":"namespace","name":"mcp__node_repl","tools":[{"type":"function","name":"js","parameters":{"type":"object","properties":{}}}]}]}]
		}`),
		streamRaw: [][]byte{
			[]byte(`data: {"type":"message_start","message":{"id":"msg_namespace","usage":{"input_tokens":1,"output_tokens":0}}}`),
			[]byte(`data: {"type":"content_block_start","index":0,"content_block":{"type":"tool_use","id":"call_namespace","name":"mcp__node_repl__js","input":{}}}`),
			[]byte(`data: {"type":"content_block_delta","index":0,"delta":{"type":"input_json_delta","partial_json":"{\"code\":\"pwd\"}"}}`),
			[]byte(`data: {"type":"content_block_stop","index":0}`),
			[]byte(`data: {"type":"message_stop"}`),
		},
	},
	"g5_cache_content_system": {
		requestRaw: []byte(`{
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
				]}
			]
		}`),
	},
	"g5_cache_tools": {
		requestRaw: []byte(`{
			"model":"gpt-test",
			"tools":[
				{"type":"function","name":"root_marker","description":"root wins","parameters":{"type":"object","properties":{}},"cache_control":{"type":"ephemeral","ttl":"root"},"function":{"cache_control":{"type":"ephemeral","ttl":"nested-loser"}}},
				{"type":"function","function":{"name":"nested_marker","description":"nested fallback","parameters":{"type":"object","properties":{}},"cache_control":{"type":"ephemeral","ttl":"nested"}}},
				{"type":"custom","name":"custom_marker","description":"custom marker","cache_control":{"type":"ephemeral","ttl":"custom"}},
				{"type":"namespace","name":"terminal","cache_control":{"type":"ephemeral","ttl":"container"},"tools":[
					{"type":"function","name":"exec","description":"child marker","parameters":{"type":"object","properties":{}},"cache_control":{"type":"ephemeral","ttl":"child-function"}},
					{"type":"custom","name":"raw","description":"child custom marker","cache_control":{"type":"ephemeral","ttl":"child-custom"}},
					{"type":"function","name":"plain","description":"container must not leak","parameters":{"type":"object","properties":{}}}
				]}
			],
			"input":[{"type":"message","role":"user","content":[{"type":"input_text","text":"hello"}]}]
		}`),
	},
	"g5_cache_missing_empty": {
		requestRaw: []byte(`{
			"model":"gpt-test",
			"tools":[
				{"type":"function","name":"no_marker","description":"absence","parameters":{"type":"object","properties":{}}},
				{"type":"function","function":{"name":"nested_malformed","description":"nested malformed","parameters":{"type":"object","properties":{}},"cache_control":"bad-nested"}},
				{"type":"custom","name":"custom_malformed","description":"custom malformed","cache_control":"bad-custom"}
			],
			"input":[
				{"type":"message","role":"user","cache_control":{"type":"ephemeral","ttl":"item-fallback"},"content":[
					{"type":"input_text","text":"malformed part fallback","cache_control":"bad"}
				]},
				{"type":"message","role":"user","content":[{"type":"input_text","text":"plain"}]}
			]
		}`),
	},
	"g7_chat_tool_matrix": {
		requestRaw: []byte(`{
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
		}`),
	},
	"g7_chat_ordering": {
		requestRaw: []byte(`{
			"model":"gpt-test",
			"tools":[
				{"type":"function","name":"first","parameters":{"type":"object","properties":{}}},
				{"type":"namespace","name":"n","tools":[{"type":"function","name":"middle","parameters":{"type":"object","properties":{}}}]},
				{"type":"function","name":"last","parameters":{"type":"object","properties":{}}}
			],
			"input":[{"type":"additional_tools","tools":[{"type":"custom","name":"after","description":"after"}]}]
		}`),
	},
	"g7_chat_collision_winners": {
		requestRaw: []byte(`{
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
		}`),
	},
}

func TestOracleRequestToolsManifestCoverage(t *testing.T) {
	t81AssertManifestCoverage(t)
}

func TestOracleRequestToolsSemanticParity(t *testing.T) {
	t81AssertManifestCoverage(t)
	for _, row := range t81OracleManifest {
		row := row
		t.Run(t81ManifestName(row), func(t *testing.T) {
			fixture := t81OracleFixtures[row.fixture]
			upstream, oag := t81RunOracleRow(t, row, fixture)
			t81AssertProjectedSemanticJSONEqual(t, row, upstream, oag)
		})
	}
}

func t81RunOracleRow(t *testing.T, row t81OracleManifestRow, fixture t81OracleFixture) ([]byte, []byte) {
	t.Helper()
	switch row.direction {
	case t81DirectionResponsesToClaudeRequest:
		upstream := responses_to_claude.ConvertOpenAIResponsesRequestToClaude("claude-test", fixture.requestRaw, row.stream == "stream")
		oag := oagmsg.TranslateRequest(oagmsg.FormatOpenAIResponse, oagmsg.FormatAnthropic, "claude-test", fixture.requestRaw, row.stream == "stream")
		return upstream, oag
	case t81DirectionResponsesToChatRequest:
		upstream := responses_to_chat.ConvertOpenAIResponsesRequestToOpenAIChatCompletions("gpt-5.4", fixture.requestRaw, row.stream == "stream")
		oag := oagmsg.TranslateRequest(oagmsg.FormatOpenAIResponse, oagmsg.FormatOpenAI, "gpt-5.4", fixture.requestRaw, row.stream == "stream")
		return upstream, oag
	case t81DirectionClaudeToResponsesStream:
		var upstreamParam any
		var oagParam any
		var upstream [][]byte
		var oag [][]byte
		for _, chunk := range fixture.streamRaw {
			upstream = append(upstream, responses_to_claude.ConvertClaudeResponseToOpenAIResponses(context.Background(), "gpt-test", fixture.requestRaw, nil, chunk, &upstreamParam)...)
			oag = append(oag, oagmsg.TranslateStream(context.Background(), oagmsg.FormatAnthropic, oagmsg.FormatOpenAIResponse, "gpt-test", fixture.requestRaw, nil, chunk, &oagParam)...)
		}
		return t81NormalizeSSEEnvelope(t, upstream), t81NormalizeSSEEnvelope(t, oag)
	case t81DirectionClaudeToResponsesNonStream:
		raw := []byte(t81JoinSSELines(fixture.streamRaw))
		upstream := responses_to_claude.ConvertClaudeResponseToOpenAIResponsesNonStream(context.Background(), "gpt-test", fixture.requestRaw, nil, raw, nil)
		oag := oagmsg.TranslateNonStream(context.Background(), oagmsg.FormatAnthropic, oagmsg.FormatOpenAIResponse, "gpt-test", fixture.requestRaw, nil, raw, nil)
		return upstream, oag
	default:
		t.Fatalf("unsupported oracle direction %q", row.direction)
		return nil, nil
	}
}

func t81AssertManifestCoverage(t *testing.T) {
	t.Helper()
	seen := make(map[string]int)
	for _, row := range t81OracleManifest {
		if row.gap == "" {
			t.Fatalf("manifest row for fixture %q has empty gap", row.fixture)
		}
		if row.direction == "" {
			t.Fatalf("manifest row for fixture %q has empty direction", row.fixture)
		}
		if row.stream != "stream" && row.stream != "non-stream" {
			t.Fatalf("manifest row for fixture %q has stream=%q", row.fixture, row.stream)
		}
		if _, ok := t81OracleFixtures[row.fixture]; !ok {
			t.Fatalf("manifest references missing fixture %q", row.fixture)
		}
		if row.contract == "" {
			t.Fatalf("manifest row for fixture %q has empty compare contract", row.fixture)
		}
		if len(row.exclusions) == 0 {
			t.Fatalf("manifest row for fixture %q has no recorded scaffold exclusions", row.fixture)
		}
		for _, exclusion := range row.exclusions {
			if !t81AllowedExclusion(row.contract, exclusion) {
				t.Fatalf("manifest row for fixture %q has undeclared broad exclusion %q for contract %q", row.fixture, exclusion, row.contract)
			}
		}
		seen[row.fixture]++
	}
	var unused []string
	for name := range t81OracleFixtures {
		if seen[name] == 0 {
			unused = append(unused, name)
		}
	}
	sort.Strings(unused)
	if len(unused) > 0 {
		t.Fatalf("fixtures not referenced by manifest: %s", strings.Join(unused, ", "))
	}
}

func t81AllowedExclusion(contract t81CompareContract, exclusion string) bool {
	if exclusion == "" || strings.ContainsAny(exclusion, "*[]") {
		return false
	}
	allowed := map[t81CompareContract]map[string]struct{}{
		t81ContractG2Request: {
			"metadata.user_id": {},
			"stream":           {},
		},
		t81ContractG2Response: {
			"response.created.response.id":             {},
			"response.created.response.created_at":     {},
			"response.in_progress.response.id":         {},
			"response.in_progress.response.created_at": {},
			"response.in_progress.response.output":     {},
			"response.completed.response.id":           {},
			"response.completed.response.created_at":   {},
			"response.completed.response.usage":        {},
			"id":                                       {},
			"created_at":                               {},
			"background":                               {},
			"error":                                    {},
			"incomplete_details":                       {},
			"usage":                                    {},
		},
		t81ContractG3Request: {
			"metadata.user_id": {},
			"stream":           {},
		},
		t81ContractG3Response: {
			"response.created.response.id":             {},
			"response.created.response.created_at":     {},
			"response.in_progress.response.id":         {},
			"response.in_progress.response.created_at": {},
			"response.in_progress.response.output":     {},
			"response.completed.response.id":           {},
			"response.completed.response.created_at":   {},
			"response.completed.response.usage":        {},
			"id":                                       {},
			"created_at":                               {},
			"background":                               {},
			"error":                                    {},
			"incomplete_details":                       {},
			"usage":                                    {},
		},
		t81ContractG5CachePlacement: {
			"metadata.user_id": {},
			"stream":           {},
		},
		t81ContractG7ChatTools: {
			"stream":   {},
			"messages": {},
		},
	}
	_, ok := allowed[contract][exclusion]
	return ok
}

func t81AssertProjectedSemanticJSONEqual(t *testing.T, row t81OracleManifestRow, upstream, oag []byte) {
	t.Helper()
	upstreamSemantic := t81ProjectSemanticJSON(t, row, upstream)
	oagSemantic := t81ProjectSemanticJSON(t, row, oag)
	if !reflect.DeepEqual(oagSemantic, upstreamSemantic) {
		t.Fatalf("projected semantic JSON mismatch\ncontract: %s\nexcluded scaffold paths: %s\nupstream:\n%s\noagmsg:\n%s\nupstream raw:\n%s\noagmsg raw:\n%s",
			row.contract,
			strings.Join(row.exclusions, ", "),
			t81PrettyJSON(t, upstreamSemantic),
			t81PrettyJSON(t, oagSemantic),
			string(upstream),
			string(oag),
		)
	}
}

func t81ProjectSemanticJSON(t *testing.T, row t81OracleManifestRow, raw []byte) any {
	t.Helper()
	switch row.contract {
	case t81ContractG2Request:
		return t81ProjectG2Request(t, raw)
	case t81ContractG2Response:
		return t81ProjectG2Response(t, row.stream, raw)
	case t81ContractG3Request:
		return t81ProjectG3Request(t, raw)
	case t81ContractG3Response:
		return t81ProjectG3Response(t, row.stream, raw)
	case t81ContractG5CachePlacement:
		return t81ProjectG5CachePlacement(t, raw)
	case t81ContractG7ChatTools:
		return t81ProjectG7ChatTools(t, raw)
	default:
		t.Fatalf("unsupported compare contract %q", row.contract)
		return nil
	}
}

func t81ProjectG2Request(t *testing.T, raw []byte) any {
	t.Helper()
	root := t81JSONMap(t, raw)
	return map[string]any{
		"custom_tools": t81JSONField(root, "tools"),
		"history":      t81ClaudeToolHistory(root, map[string]struct{}{"tool_use": {}, "tool_result": {}}),
	}
}

func t81ProjectG2Response(t *testing.T, stream string, raw []byte) any {
	t.Helper()
	if stream == "stream" {
		return t81ProjectResponsesStreamOutput(raw, map[string]struct{}{"custom_tool_call": {}})
	}
	root := t81JSONMap(t, raw)
	return map[string]any{
		"output": t81ResponsesOutputItems(root, map[string]struct{}{"custom_tool_call": {}}, []string{"type", "name", "input", "call_id"}),
	}
}

func t81ProjectG3Request(t *testing.T, raw []byte) any {
	t.Helper()
	root := t81JSONMap(t, raw)
	return map[string]any{
		"tools":       t81JSONField(root, "tools"),
		"tool_choice": t81OptionalJSONField(root, "tool_choice"),
	}
}

func t81ProjectG3Response(t *testing.T, stream string, raw []byte) any {
	t.Helper()
	if stream == "stream" {
		return t81ProjectResponsesStreamOutput(raw, map[string]struct{}{"function_call": {}})
	}
	root := t81JSONMap(t, raw)
	return map[string]any{
		"output": t81ResponsesOutputItems(root, map[string]struct{}{"function_call": {}}, []string{"type", "name", "namespace", "arguments", "call_id"}),
	}
}

func t81ProjectG5CachePlacement(t *testing.T, raw []byte) any {
	t.Helper()
	root := t81JSONMap(t, raw)
	var placements []any
	t81CollectCacheControlPlacements(root, "", &placements)
	return placements
}

func t81ProjectG7ChatTools(t *testing.T, raw []byte) any {
	t.Helper()
	root := t81JSONMap(t, raw)
	return map[string]any{
		"tools":       t81JSONField(root, "tools"),
		"tool_choice": t81OptionalJSONField(root, "tool_choice"),
	}
}

func t81ClaudeToolHistory(root map[string]any, blockTypes map[string]struct{}) []any {
	var history []any
	for _, message := range t81AnySlice(root["messages"]) {
		messageMap, ok := message.(map[string]any)
		if !ok {
			continue
		}
		role, _ := messageMap["role"].(string)
		for _, block := range t81AnySlice(messageMap["content"]) {
			blockMap, ok := block.(map[string]any)
			if !ok {
				continue
			}
			blockType, _ := blockMap["type"].(string)
			if _, ok := blockTypes[blockType]; !ok {
				continue
			}
			item := map[string]any{
				"role": role,
				"type": blockType,
			}
			switch blockType {
			case "tool_use":
				t81CopyIfPresent(item, blockMap, "id")
				t81CopyIfPresent(item, blockMap, "name")
				t81CopyIfPresent(item, blockMap, "input")
			case "tool_result":
				t81CopyIfPresent(item, blockMap, "tool_use_id")
				t81CopyIfPresent(item, blockMap, "content")
			}
			history = append(history, item)
		}
	}
	return history
}

func t81ProjectResponsesStreamOutput(raw []byte, itemTypes map[string]struct{}) any {
	var events []any
	var streamEvents []any
	if err := json.Unmarshal(raw, &streamEvents); err != nil {
		return map[string]any{"invalid_stream_envelope": string(raw)}
	}
	for _, event := range streamEvents {
		eventMap, ok := event.(map[string]any)
		if !ok {
			continue
		}
		eventName, _ := eventMap["event"].(string)
		data := eventMap["data"]
		if data == "[DONE]" {
			continue
		}
		dataMap, ok := data.(map[string]any)
		if !ok {
			continue
		}
		switch eventName {
		case "response.output_item.added", "response.output_item.done":
			item, _ := dataMap["item"].(map[string]any)
			if t81ItemTypeAllowed(item, itemTypes) {
				projected := t81ProjectedResponseItem(item)
				if eventName == "response.output_item.added" {
					delete(projected, "arguments")
				}
				events = append(events, map[string]any{
					"event": eventName,
					"item":  projected,
				})
			}
		case "response.function_call_arguments.done":
			if _, ok := itemTypes["function_call"]; ok {
				events = append(events, map[string]any{
					"event":     eventName,
					"arguments": dataMap["arguments"],
				})
			}
		case "response.custom_tool_call_input.done":
			if _, ok := itemTypes["custom_tool_call"]; ok {
				events = append(events, map[string]any{
					"event": eventName,
					"input": dataMap["input"],
				})
			}
		case "response.completed":
			response, _ := dataMap["response"].(map[string]any)
			events = append(events, map[string]any{
				"event":  eventName,
				"output": t81ResponsesOutputItems(response, itemTypes, []string{"type", "name", "namespace", "arguments", "input", "call_id"}),
			})
		}
	}
	return events
}

func t81ResponsesOutputItems(root map[string]any, itemTypes map[string]struct{}, fields []string) []any {
	var out []any
	for _, item := range t81AnySlice(root["output"]) {
		itemMap, ok := item.(map[string]any)
		if !ok || !t81ItemTypeAllowed(itemMap, itemTypes) {
			continue
		}
		projected := make(map[string]any, len(fields))
		for _, field := range fields {
			t81CopyIfPresent(projected, itemMap, field)
		}
		out = append(out, projected)
	}
	return out
}

func t81ProjectedResponseItem(item map[string]any) map[string]any {
	projected := make(map[string]any)
	for _, field := range []string{"type", "name", "namespace", "arguments", "input", "call_id"} {
		t81CopyIfPresent(projected, item, field)
	}
	return projected
}

func t81ItemTypeAllowed(item map[string]any, itemTypes map[string]struct{}) bool {
	if item == nil {
		return false
	}
	itemType, _ := item["type"].(string)
	_, ok := itemTypes[itemType]
	return ok
}

func t81CollectCacheControlPlacements(value any, path string, placements *[]any) {
	switch typed := value.(type) {
	case map[string]any:
		if cacheControl, ok := typed["cache_control"]; ok {
			*placements = append(*placements, map[string]any{
				"path":  t81JoinJSONPath(path, "cache_control"),
				"value": cacheControl,
			})
		}
		keys := make([]string, 0, len(typed))
		for key := range typed {
			if key != "cache_control" {
				keys = append(keys, key)
			}
		}
		sort.Strings(keys)
		for _, key := range keys {
			t81CollectCacheControlPlacements(typed[key], t81JoinJSONPath(path, key), placements)
		}
	case []any:
		for i, child := range typed {
			t81CollectCacheControlPlacements(child, t81JoinJSONPath(path, fmt.Sprintf("%d", i)), placements)
		}
	}
}

func t81JSONMap(t *testing.T, raw []byte) map[string]any {
	t.Helper()
	var value map[string]any
	if err := json.Unmarshal(raw, &value); err != nil {
		t.Fatalf("invalid JSON object: %v\n%s", err, string(raw))
	}
	return value
}

func t81JSONField(root map[string]any, key string) any {
	if value, ok := root[key]; ok {
		return value
	}
	return nil
}

func t81OptionalJSONField(root map[string]any, key string) map[string]any {
	value, ok := root[key]
	if !ok {
		return map[string]any{"present": false}
	}
	return map[string]any{
		"present": true,
		"value":   value,
	}
}

func t81AnySlice(value any) []any {
	items, _ := value.([]any)
	return items
}

func t81CopyIfPresent(target, source map[string]any, key string) {
	if value, ok := source[key]; ok {
		target[key] = value
	}
}

func t81JoinJSONPath(prefix, key string) string {
	if prefix == "" {
		return key
	}
	return prefix + "." + key
}

func t81AssertNormalizedSemanticJSONEqual(t *testing.T, upstream, oag []byte) {
	t.Helper()
	upstreamSemantic := t81NormalizedSemanticJSON(t, upstream)
	oagSemantic := t81NormalizedSemanticJSON(t, oag)
	if !reflect.DeepEqual(oagSemantic, upstreamSemantic) {
		t.Fatalf("normalized semantic JSON mismatch\nupstream:\n%s\noagmsg:\n%s\nupstream raw:\n%s\noagmsg raw:\n%s",
			t81PrettyJSON(t, upstreamSemantic),
			t81PrettyJSON(t, oagSemantic),
			string(upstream),
			string(oag),
		)
	}
}

func t81NormalizedSemanticJSON(t *testing.T, raw []byte) any {
	t.Helper()
	var value any
	if err := json.Unmarshal(raw, &value); err != nil {
		t.Fatalf("invalid JSON: %v\n%s", err, string(raw))
	}
	return t81NormalizeGenerated(value, nil)
}

func t81NormalizeGenerated(value any, path []string) any {
	switch typed := value.(type) {
	case map[string]any:
		out := make(map[string]any, len(typed))
		for key, child := range typed {
			childPath := append(append([]string{}, path...), key)
			if t81IsGeneratedField(childPath) {
				out[key] = "<generated>"
				continue
			}
			out[key] = t81NormalizeGenerated(child, childPath)
		}
		return out
	case []any:
		out := make([]any, len(typed))
		for i, child := range typed {
			out[i] = t81NormalizeGenerated(child, append(append([]string{}, path...), fmt.Sprintf("%d", i)))
		}
		return out
	default:
		return value
	}
}

func t81IsGeneratedField(path []string) bool {
	joined := strings.Join(path, ".")
	if joined == "metadata.user_id" ||
		joined == "id" ||
		joined == "created_at" ||
		joined == "created" ||
		joined == "response.id" ||
		joined == "response.created_at" {
		return true
	}
	return strings.HasSuffix(joined, ".created_at") ||
		strings.HasSuffix(joined, ".created") ||
		strings.HasSuffix(joined, ".response.id") ||
		strings.HasSuffix(joined, ".response.created_at")
}

func t81NormalizeSSEEnvelope(t *testing.T, chunks [][]byte) []byte {
	t.Helper()
	events := make([]map[string]any, 0, len(chunks))
	for _, chunk := range chunks {
		eventName, dataRaw := t81ParseSSEChunk(t, chunk)
		event := map[string]any{"event": eventName}
		if string(dataRaw) == "[DONE]" {
			event["data"] = "[DONE]"
		} else {
			event["data"] = t81NormalizedSemanticJSON(t, dataRaw)
		}
		events = append(events, event)
	}
	normalized, err := json.Marshal(events)
	if err != nil {
		t.Fatal(err)
	}
	return normalized
}

func t81ParseSSEChunk(t *testing.T, chunk []byte) (string, []byte) {
	t.Helper()
	eventName := ""
	var dataLines []string
	for _, line := range strings.Split(strings.TrimSpace(string(chunk)), "\n") {
		line = strings.TrimRight(line, "\r")
		switch {
		case strings.HasPrefix(line, "event:"):
			eventName = strings.TrimSpace(strings.TrimPrefix(line, "event:"))
		case strings.HasPrefix(line, "data:"):
			dataLines = append(dataLines, strings.TrimSpace(strings.TrimPrefix(line, "data:")))
		}
	}
	if len(dataLines) == 0 {
		t.Fatalf("SSE chunk has no data line: %s", string(chunk))
	}
	data := []byte(strings.Join(dataLines, "\n"))
	if string(data) == "[DONE]" {
		if eventName == "" {
			eventName = "[DONE]"
		}
		return eventName, data
	}
	if eventName == "" {
		var payload map[string]any
		if err := json.Unmarshal(data, &payload); err == nil {
			if typ, ok := payload["type"].(string); ok {
				eventName = typ
			}
		}
	}
	return eventName, data
}

func t81JoinSSELines(chunks [][]byte) string {
	lines := make([]string, 0, len(chunks))
	for _, chunk := range chunks {
		lines = append(lines, strings.TrimSpace(string(chunk)))
	}
	return strings.Join(lines, "\n")
}

func t81ManifestName(row t81OracleManifestRow) string {
	clean := regexp.MustCompile(`[^A-Za-z0-9_]+`).ReplaceAllString(row.fixture+"_"+string(row.direction)+"_"+row.stream, "_")
	return strings.Trim(clean, "_")
}

func t81PrettyJSON(t *testing.T, value any) string {
	t.Helper()
	out, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	return string(out)
}
