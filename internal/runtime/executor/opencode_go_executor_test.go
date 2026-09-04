package executor

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/registry"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/executor"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v7/sdk/translator"
	"github.com/tidwall/gjson"
)

const openCodeGoVisionTinyPNGDataURL = "data:image/png;base64,AA=="

func TestOpenCodeGoExecutorRequestToFormatUsesModelIndex(t *testing.T) {
	exec, _, _ := newOpenCodeGoExecutorForTest()

	if got := exec.RequestToFormat(cliproxyexecutor.Request{Model: "sonnet"}, cliproxyexecutor.Options{}); got != sdktranslator.FormatClaude {
		t.Fatalf("RequestToFormat(sonnet) = %q, want claude", got)
	}
	if got := exec.RequestToFormat(cliproxyexecutor.Request{Model: "latest"}, cliproxyexecutor.Options{}); got != sdktranslator.FormatOpenAI {
		t.Fatalf("RequestToFormat(latest) = %q, want openai", got)
	}
}

func TestOpenCodeGoExecutorDelegatesByResolvedProtocol(t *testing.T) {
	exec, claude, compat := newOpenCodeGoExecutorForTest()
	ctx := context.Background()
	payload := []byte(`{"messages":[{"role":"user","content":"hello"}]}`)

	resp, err := exec.Execute(ctx, openCodeGoAuthForTest("anthropic"), cliproxyexecutor.Request{Model: "sonnet", Payload: payload}, cliproxyexecutor.Options{})
	if err != nil {
		t.Fatalf("Execute() error = %v", err)
	}
	if string(resp.Payload) != "claude:execute" || claude.executeCalls != 1 || compat.executeCalls != 0 {
		t.Fatalf("Execute delegated incorrectly: resp=%q claude=%d compat=%d", resp.Payload, claude.executeCalls, compat.executeCalls)
	}

	stream, err := exec.ExecuteStream(ctx, openCodeGoAuthForTest("openai"), cliproxyexecutor.Request{Model: "latest", Payload: payload}, cliproxyexecutor.Options{Stream: true})
	if err != nil {
		t.Fatalf("ExecuteStream() error = %v", err)
	}
	chunk := <-stream.Chunks
	if string(chunk.Payload) != "openai:stream" || compat.streamCalls != 1 {
		t.Fatalf("ExecuteStream delegated incorrectly: chunk=%q compat=%d", chunk.Payload, compat.streamCalls)
	}

	_, err = exec.CountTokens(ctx, openCodeGoAuthForTest("anthropic"), cliproxyexecutor.Request{Model: "claude-sonnet", Payload: payload}, cliproxyexecutor.Options{})
	if err != nil {
		t.Fatalf("CountTokens() error = %v", err)
	}
	if claude.countCalls != 1 {
		t.Fatalf("CountTokens claude calls = %d, want 1", claude.countCalls)
	}

	_, err = exec.Refresh(ctx, openCodeGoAuthForTest("openai"))
	if err != nil {
		t.Fatalf("Refresh() error = %v", err)
	}
	if compat.refreshCalls != 1 {
		t.Fatalf("Refresh compat calls = %d, want 1", compat.refreshCalls)
	}

	httpReq, err := http.NewRequest(http.MethodGet, "https://example.test", nil)
	if err != nil {
		t.Fatalf("NewRequest() error = %v", err)
	}
	if errPrepare := exec.PrepareRequest(httpReq, openCodeGoAuthForTest("anthropic")); errPrepare != nil {
		t.Fatalf("PrepareRequest() error = %v", errPrepare)
	}
	if got := httpReq.Header.Get("X-Delegate"); got != "claude" {
		t.Fatalf("PrepareRequest delegate = %q, want claude", got)
	}

	_, err = exec.HttpRequest(ctx, openCodeGoAuthForTest("anthropic"), httpReq)
	if err != nil {
		t.Fatalf("HttpRequest() error = %v", err)
	}
	if claude.httpCalls != 1 {
		t.Fatalf("HttpRequest claude calls = %d, want 1", claude.httpCalls)
	}
}

func TestOpenCodeGoExecutorAnthropicStreamPreservesStreamFlag(t *testing.T) {
	bodyCh := make(chan []byte, 1)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, errRead := io.ReadAll(r.Body)
		if errRead != nil {
			t.Errorf("read request body: %v", errRead)
			http.Error(w, errRead.Error(), http.StatusBadRequest)
			return
		}
		bodyCh <- body
		if !gjson.GetBytes(body, "stream").Bool() {
			w.Header().Set("Content-Type", "application/json")
			_, _ = io.WriteString(w, `{"id":"msg_json","type":"message","role":"assistant","model":"qwen3.7-plus","content":[{"type":"text","text":"json-fallback"}],"usage":{"input_tokens":1,"output_tokens":1}}`)
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w,
			"event: message_start\n"+
				`data: {"type":"message_start","message":{"id":"msg_stream","type":"message","role":"assistant","model":"qwen3.7-plus","content":[],"usage":{"input_tokens":1,"output_tokens":0}}}`+"\n\n"+
				"event: content_block_start\n"+
				`data: {"type":"content_block_start","index":0,"content_block":{"type":"text","text":""}}`+"\n\n"+
				"event: content_block_delta\n"+
				`data: {"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"qwen-stream-ok"}}`+"\n\n"+
				"event: content_block_stop\n"+
				`data: {"type":"content_block_stop","index":0}`+"\n\n"+
				"event: message_delta\n"+
				`data: {"type":"message_delta","delta":{"stop_reason":"end_turn","stop_sequence":null},"usage":{"output_tokens":1}}`+"\n\n"+
				"event: message_stop\n"+
				`data: {"type":"message_stop"}`+"\n\n")
	}))
	defer server.Close()

	cfg := &config.Config{OpenCodeGo: config.OpenCodeGoConfig{KeyGroups: []config.OpenCodeGoKeyGroup{{
		NamePrefix: "opencode-go",
		Anthropic: &config.OpenCodeGoProtocolConfig{
			BaseURL: server.URL,
			Models:  []config.OpenCodeGoModelEntry{{Name: "qwen3.7-plus"}},
		},
		Keys: []config.OpenCodeGoKeyEntry{{KeyName: "test", APIKey: "secret"}},
	}}}}
	exec := NewOpenCodeGoExecutor("opencode-go", cfg)
	payload := []byte(`{"model":"qwen3.7-plus","messages":[{"role":"user","content":"hello"}],"stream":true,"max_tokens":16}`)

	stream, err := exec.ExecuteStream(context.Background(), canonicalOpenCodeGoAuthForTest(), cliproxyexecutor.Request{
		Model:   "qwen3.7-plus",
		Payload: payload,
	}, cliproxyexecutor.Options{
		Stream:          true,
		SourceFormat:    sdktranslator.FormatOpenAI,
		OriginalRequest: payload,
	})
	if err != nil {
		t.Fatalf("ExecuteStream() error = %v", err)
	}

	var combined strings.Builder
	for chunk := range stream.Chunks {
		if chunk.Err != nil {
			t.Fatalf("stream chunk error = %v", chunk.Err)
		}
		combined.Write(chunk.Payload)
	}
	if got := combined.String(); !strings.Contains(got, "qwen-stream-ok") {
		t.Fatalf("stream payload = %q, want translated qwen-stream-ok chunk", got)
	}
	select {
	case body := <-bodyCh:
		if !gjson.GetBytes(body, "stream").Bool() {
			t.Fatalf("upstream body stream = false/missing: %s", body)
		}
	default:
		t.Fatal("upstream did not receive request")
	}
}

func TestOpenCodeGoExecutorProjectsBothRoutesFromOneCanonicalKey(t *testing.T) {
	exec, claude, compat := newOpenCodeGoExecutorForTest()
	auth := canonicalOpenCodeGoAuthForTest()
	payload := []byte(`{"messages":[{"role":"user","content":"hello"}]}`)

	if _, err := exec.Execute(context.Background(), auth, cliproxyexecutor.Request{Model: "oc/sonnet", Payload: payload}, cliproxyexecutor.Options{}); err != nil {
		t.Fatalf("Execute(oc/sonnet) error = %v", err)
	}
	if claude.executeCalls != 1 || claude.lastAuth == nil || claude.lastAuth.ID != auth.ID || claude.lastAuth.Attributes["protocol"] != "claude" || claude.lastAuth.Attributes["base_url"] != "https://claude.example" {
		t.Fatalf("Claude route auth = %+v calls=%d", claude.lastAuth, claude.executeCalls)
	}
	if claude.lastReq.Model != "claude-sonnet" {
		t.Fatalf("Claude delegated model = %q, want claude-sonnet", claude.lastReq.Model)
	}

	if _, err := exec.Execute(context.Background(), auth, cliproxyexecutor.Request{Model: "og/latest", Payload: payload}, cliproxyexecutor.Options{}); err != nil {
		t.Fatalf("Execute(og/latest) error = %v", err)
	}
	if compat.executeCalls != 1 || compat.lastAuth == nil || compat.lastAuth.ID != auth.ID || compat.lastAuth.Attributes["protocol"] != "openai" || compat.lastAuth.Attributes["base_url"] != "https://openai.example" {
		t.Fatalf("OpenAI route auth = %+v calls=%d", compat.lastAuth, compat.executeCalls)
	}
	if auth.Attributes["protocol"] != "" || auth.Attributes["base_url"] != "" {
		t.Fatalf("canonical auth was mutated by route projection: %+v", auth.Attributes)
	}
	if compat.lastReq.Model != "gpt-5" {
		t.Fatalf("OpenAI delegated model = %q, want gpt-5", compat.lastReq.Model)
	}

	refreshed, errRefresh := exec.Refresh(context.Background(), auth)
	if errRefresh != nil || refreshed == nil || refreshed == auth || refreshed.ID != auth.ID {
		t.Fatalf("Refresh(canonical) = %+v, %v", refreshed, errRefresh)
	}
	if claude.refreshCalls != 0 || compat.refreshCalls != 0 {
		t.Fatalf("canonical API key refresh delegated: claude=%d compat=%d", claude.refreshCalls, compat.refreshCalls)
	}

	httpReq, errRequest := http.NewRequest(http.MethodPost, "https://claude.example/v1/messages", nil)
	if errRequest != nil {
		t.Fatalf("NewRequest() error = %v", errRequest)
	}
	if errPrepare := exec.PrepareRequest(httpReq, auth); errPrepare != nil {
		t.Fatalf("PrepareRequest(canonical) error = %v", errPrepare)
	}
	if got := httpReq.Header.Get("X-Delegate"); got != "claude" {
		t.Fatalf("PrepareRequest delegate = %q, want claude", got)
	}
	foreignReq, errForeignRequest := http.NewRequest(http.MethodPost, "https://claude.example.evil/v1/messages", nil)
	if errForeignRequest != nil {
		t.Fatalf("NewRequest(foreign) error = %v", errForeignRequest)
	}
	assertOpenCodeGoStatus(t, exec.PrepareRequest(foreignReq, auth), http.StatusBadRequest, "does not match")
}

func TestOpenCodeGoExecutorRewritesAliasModelPreservesThinkingSuffixAndResponseAlias(t *testing.T) {
	exec, _, compat := newOpenCodeGoExecutorForTest()
	compat.responsePayload = []byte(`{"model":"gpt-5(high)","choices":[]}`)
	auth := openCodeGoAuthForTest("openai")
	cliproxyauth.SetOAuthModelAliasesAttribute(auth, []config.OAuthModelAlias{{Name: "gpt-5", Alias: "latest"}})
	opts := cliproxyexecutor.Options{Metadata: map[string]any{cliproxyexecutor.RequestedModelMetadataKey: "latest(high)"}}

	resp, err := exec.Execute(context.Background(), auth, cliproxyexecutor.Request{Model: "latest(high)", Payload: []byte(`{"messages":[]}`)}, opts)
	if err != nil {
		t.Fatalf("Execute() error = %v", err)
	}
	if compat.lastReq.Model != "gpt-5(high)" {
		t.Fatalf("delegated model = %q, want gpt-5(high)", compat.lastReq.Model)
	}
	if got := gjson.GetBytes(resp.Payload, "model").String(); got != "latest(high)" {
		t.Fatalf("response model = %q, want latest(high); body=%s", got, resp.Payload)
	}
}

func TestOpenCodeGoExecutorDropsBlankOpenAICompatibleToolCalls(t *testing.T) {
	exec, _, compat := newOpenCodeGoExecutorForTest()
	compat.responsePayload = []byte(`{
		"id":"msg_blank_tool",
		"model":"gpt-5",
		"choices":[{
			"index":0,
			"message":{
				"role":"assistant",
				"content":"qwen-ok",
				"tool_calls":[{"id":"","type":"function","function":{"name":"","arguments":""}}]
			},
			"finish_reason":"tool_calls"
		}]
	}`)

	resp, err := exec.Execute(context.Background(), openCodeGoAuthForTest("openai"), cliproxyexecutor.Request{
		Model:   "gpt-5",
		Payload: []byte(`{"messages":[]}`),
	}, cliproxyexecutor.Options{SourceFormat: sdktranslator.FormatOpenAI})
	if err != nil {
		t.Fatalf("Execute() error = %v", err)
	}
	if gjson.GetBytes(resp.Payload, "choices.0.message.tool_calls").Exists() {
		t.Fatalf("blank tool_calls survived OpenCode Go facade: %s", resp.Payload)
	}
	if got := gjson.GetBytes(resp.Payload, "choices.0.finish_reason").String(); got != "stop" {
		t.Fatalf("finish_reason = %q, want stop; body=%s", got, resp.Payload)
	}
	if got := gjson.GetBytes(resp.Payload, "choices.0.message.content").String(); got != "qwen-ok" {
		t.Fatalf("content = %q, want qwen-ok; body=%s", got, resp.Payload)
	}
}

func TestOpenCodeGoExecutorStreamRewritesAliasResponseModel(t *testing.T) {
	exec, _, compat := newOpenCodeGoExecutorForTest()
	compat.streamPayload = []byte("data: {\"model\":\"gpt-5\",\"choices\":[]}\n\n")
	auth := openCodeGoAuthForTest("openai")
	cliproxyauth.SetOAuthModelAliasesAttribute(auth, []config.OAuthModelAlias{{Name: "gpt-5", Alias: "latest"}})

	stream, err := exec.ExecuteStream(context.Background(), auth, cliproxyexecutor.Request{Model: "latest", Payload: []byte(`{"messages":[]}`)}, cliproxyexecutor.Options{})
	if err != nil {
		t.Fatalf("ExecuteStream() error = %v", err)
	}
	if got := stream.Headers.Get("X-Delegate"); got != "openai" {
		t.Fatalf("stream header = %q, want openai", got)
	}
	chunk := <-stream.Chunks
	if !strings.Contains(string(chunk.Payload), `"model":"latest"`) {
		t.Fatalf("stream chunk = %q, want alias model", chunk.Payload)
	}
	if compat.lastReq.Model != "gpt-5" {
		t.Fatalf("delegated stream model = %q, want gpt-5", compat.lastReq.Model)
	}
}

func TestOpenCodeGoExecutorCountTokensDoesNotRewriteAliasResponseModel(t *testing.T) {
	exec, _, compat := newOpenCodeGoExecutorForTest()
	compat.countPayload = []byte(`{"model":"gpt-5","usage":{"total_tokens":1}}`)
	auth := openCodeGoAuthForTest("openai")
	cliproxyauth.SetOAuthModelAliasesAttribute(auth, []config.OAuthModelAlias{{Name: "gpt-5", Alias: "latest"}})

	resp, err := exec.CountTokens(context.Background(), auth, cliproxyexecutor.Request{Model: "latest", Payload: []byte(`{"messages":[]}`)}, cliproxyexecutor.Options{})
	if err != nil {
		t.Fatalf("CountTokens() error = %v", err)
	}
	if got := gjson.GetBytes(resp.Payload, "model").String(); got != "gpt-5" {
		t.Fatalf("count response model = %q, want upstream model gpt-5", got)
	}
	if compat.lastReq.Model != "gpt-5" {
		t.Fatalf("delegated count model = %q, want gpt-5", compat.lastReq.Model)
	}
}

func TestOpenCodeGoExecutorAppliesStaticTruncationBeforeDelegation(t *testing.T) {
	exec, _, compat := newOpenCodeGoExecutorForTest()
	auth := openCodeGoAuthForTest("openai")
	auth.Attributes["max_messages"] = "2"
	payload := []byte(`{"messages":[{"role":"assistant","content":"old"},{"role":"user","content":"new"},{"role":"assistant","content":"reply"}]}`)
	opts := cliproxyexecutor.Options{OriginalRequest: append([]byte(nil), payload...)}

	_, err := exec.Execute(context.Background(), auth, cliproxyexecutor.Request{Model: "gpt-5", Payload: payload}, opts)
	if err != nil {
		t.Fatalf("Execute() error = %v", err)
	}
	roles := opencodeGoExecutorPayloadRoles(t, compat.lastReq.Payload)
	if strings.Join(roles, ",") != "user,assistant" {
		t.Fatalf("delegated roles = %v, want user,assistant", roles)
	}
	if string(compat.lastOpts.OriginalRequest) != string(compat.lastReq.Payload) {
		t.Fatalf("OriginalRequest was not updated with truncated payload")
	}
}

func TestOpenCodeGoExecutorPreservesResolvedModelInfoForDelegateThinking(t *testing.T) {
	exec, _, compat := newOpenCodeGoExecutorForTest()
	info := &registry.ModelInfo{
		ID: "gpt-5",
		Thinking: &registry.ThinkingSupport{
			Levels: []string{"high"},
		},
	}
	req := cliproxyexecutor.Request{
		Model:   "gpt-5",
		Payload: []byte(`{"messages":[]}`),
		Metadata: map[string]any{
			"cliproxy.resolved_api_key_model_info": info,
		},
	}

	if _, err := exec.Execute(context.Background(), openCodeGoAuthForTest("openai"), req, cliproxyexecutor.Options{}); err != nil {
		t.Fatalf("Execute() error = %v", err)
	}
	got, ok := cliproxyauth.ResolvedAPIKeyModelInfo(compat.lastReq)
	if !ok || got != info {
		t.Fatalf("resolved model info = (%+v, %t), want original model info", got, ok)
	}
}

func TestOpenCodeGoExecutorPreprocessesOpenAICacheControlBeforeDelegation(t *testing.T) {
	claude := &openCodeGoCaptureDelegate{name: "claude"}
	compat := &openCodeGoCaptureDelegate{name: "openai"}
	cfg := &config.Config{OpenCodeGo: config.OpenCodeGoConfig{KeyGroups: []config.OpenCodeGoKeyGroup{{
		NamePrefix: "opencode-go",
		OpenAI: &config.OpenCodeGoProtocolConfig{BaseURL: "https://openai.example", Prefix: "og", Models: []config.OpenCodeGoModelEntry{
			{Name: "claude-openai-sonnet"},
			{Name: "gpt-5"},
		}},
		Anthropic: &config.OpenCodeGoProtocolConfig{BaseURL: "https://claude.example", Prefix: "oc", Models: []config.OpenCodeGoModelEntry{
			{Name: "claude-native-sonnet"},
		}},
		Keys: []config.OpenCodeGoKeyEntry{{KeyName: "test", APIKey: "secret"}},
	}}}}
	exec := &OpenCodeGoExecutor{providerKey: "opencode-go", models: buildOpenCodeGoModelIndex(cfg), claude: claude, compat: compat}
	payload := []byte(`{
		"messages":[
			{"role":"system","content":"sys"},
			{"role":"user","content":"hi"}
		],
		"tools":[{"type":"function","function":{"name":"lookup"}}]
	}`)

	_, err := exec.Execute(context.Background(), openCodeGoAuthForTest("openai"), cliproxyexecutor.Request{Model: "claude-openai-sonnet", Payload: payload}, cliproxyexecutor.Options{SourceFormat: sdktranslator.FormatOpenAI})
	if err != nil {
		t.Fatalf("Execute() error = %v", err)
	}
	if compat.executeCalls != 1 || claude.executeCalls != 0 {
		t.Fatalf("delegates called incorrectly: compat=%d claude=%d", compat.executeCalls, claude.executeCalls)
	}
	if got := gjson.GetBytes(compat.lastReq.Payload, "messages.0.cache_control.type").String(); got != "ephemeral" {
		t.Fatalf("system cache_control.type = %q, want ephemeral; body=%s", got, compat.lastReq.Payload)
	}
	if got := gjson.GetBytes(compat.lastReq.Payload, "tools.0.cache_control.type").String(); got != "ephemeral" {
		t.Fatalf("tool cache_control.type = %q, want ephemeral; body=%s", got, compat.lastReq.Payload)
	}
	if got := gjson.GetBytes(compat.lastReq.Payload, "messages.1.cache_control.type").String(); got != "ephemeral" {
		t.Fatalf("last user cache_control.type = %q, want ephemeral; body=%s", got, compat.lastReq.Payload)
	}
}

func TestOpenCodeGoExecutorVisionRewriteBeforeOpenAICacheControl(t *testing.T) {
	claude := &openCodeGoCaptureDelegate{name: "claude"}
	compat := &openCodeGoCaptureDelegate{name: "openai"}
	cfg := &config.Config{
		Vision: config.VisionConfig{
			Enabled: true,
			Model:   "vision-model",
			Scope:   "latest",
			Include: []string{"claude-openai-sonnet"},
			Provider: config.VisionProviderConfig{
				BaseURL:  "https://vision.example/v1",
				APIKey:   "vision-key",
				Protocol: "openai",
			},
		},
		OpenCodeGo: config.OpenCodeGoConfig{KeyGroups: []config.OpenCodeGoKeyGroup{{
			NamePrefix: "opencode-go",
			OpenAI: &config.OpenCodeGoProtocolConfig{BaseURL: "https://openai.example", Models: []config.OpenCodeGoModelEntry{
				{Name: "claude-openai-sonnet"},
			}},
			Keys: []config.OpenCodeGoKeyEntry{{KeyName: "test", APIKey: "secret"}},
		}}},
	}
	exec := &OpenCodeGoExecutor{providerKey: "opencode-go", cfg: cfg, models: buildOpenCodeGoModelIndex(cfg), claude: claude, compat: compat}
	payload := []byte(`{"messages":[{"role":"system","content":"sys"},{"role":"user","content":[{"type":"text","text":"look"},{"type":"image_url","image_url":{"url":"` + openCodeGoVisionTinyPNGDataURL + `"}}]}],"tools":[{"type":"function","function":{"name":"lookup"}}]}`)
	callCount := 0
	ctx := context.WithValue(context.Background(), "cliproxy.roundtripper", roundTripperFunc(func(req *http.Request) (*http.Response, error) {
		if req.URL.Host != "vision.example" {
			t.Fatalf("unexpected host %q", req.URL.Host)
		}
		callCount++
		return openCodeGoHTTPResponse(http.StatusOK, `{"choices":[{"message":{"content":"opencode image description"}}]}`), nil
	}))

	_, err := exec.Execute(ctx, openCodeGoAuthForTest("openai"), cliproxyexecutor.Request{Model: "claude-openai-sonnet", Payload: payload}, cliproxyexecutor.Options{SourceFormat: sdktranslator.FormatOpenAI})
	if err != nil {
		t.Fatalf("Execute() error = %v", err)
	}
	if callCount != 1 {
		t.Fatalf("vision provider calls = %d, want 1", callCount)
	}
	if strings.Contains(string(compat.lastReq.Payload), "image_url") {
		t.Fatalf("delegated payload still contains image: %s", compat.lastReq.Payload)
	}
	if got := gjson.GetBytes(compat.lastReq.Payload, "messages.1.content.1.text").String(); !strings.Contains(got, "opencode image description") {
		t.Fatalf("rewritten image text = %q; body=%s", got, compat.lastReq.Payload)
	}
	if got := gjson.GetBytes(compat.lastReq.Payload, "messages.1.content.1.cache_control.type").String(); got != "ephemeral" {
		t.Fatalf("last user cache_control after rewrite = %q; body=%s", got, compat.lastReq.Payload)
	}
	if got := gjson.GetBytes(compat.lastReq.Payload, "tools.0.cache_control.type").String(); got != "ephemeral" {
		t.Fatalf("tool cache_control = %q; body=%s", got, compat.lastReq.Payload)
	}
}

func TestOpenCodeGoExecutorSanitizesUnsupportedOpenAICacheControl(t *testing.T) {
	exec, _, compat := newOpenCodeGoExecutorForTest()
	payload := []byte(`{"messages":[{"role":"system","content":"sys","cache_control":{"type":"ephemeral"}},{"role":"user","content":[{"type":"text","text":"hi","cache_control":{"type":"ephemeral"}}]}],"tools":[{"type":"function","function":{"name":"lookup"},"cache_control":{"type":"ephemeral"}}]}`)

	_, err := exec.Execute(context.Background(), openCodeGoAuthForTest("openai"), cliproxyexecutor.Request{Model: "gpt-5", Payload: payload}, cliproxyexecutor.Options{SourceFormat: sdktranslator.FormatOpenAI})
	if err != nil {
		t.Fatalf("Execute() error = %v", err)
	}
	if strings.Contains(string(compat.lastReq.Payload), "cache_control") {
		t.Fatalf("unsupported OpenAI model kept cache_control: %s", compat.lastReq.Payload)
	}
}

func TestOpenCodeGoExecutorDoesNotPreprocessClaudeDelegateCacheControl(t *testing.T) {
	exec, claude, _ := newOpenCodeGoExecutorForTest()
	payload := []byte(`{"messages":[{"role":"system","content":"sys"},{"role":"user","content":"hi"}],"tools":[{"type":"function","function":{"name":"lookup"}}]}`)

	_, err := exec.Execute(context.Background(), openCodeGoAuthForTest("anthropic"), cliproxyexecutor.Request{Model: "sonnet", Payload: payload}, cliproxyexecutor.Options{SourceFormat: sdktranslator.FormatOpenAI})
	if err != nil {
		t.Fatalf("Execute() error = %v", err)
	}
	if strings.Contains(string(claude.lastReq.Payload), "cache_control") {
		t.Fatalf("Claude delegate payload was preprocessed by OpenAI cache helper: %s", claude.lastReq.Payload)
	}
}

func TestOpenCodeGoExecutorRejectsInconsistentSelectedProtocol(t *testing.T) {
	exec, claude, compat := newOpenCodeGoExecutorForTest()

	_, err := exec.Execute(context.Background(), openCodeGoAuthForTest("openai"), cliproxyexecutor.Request{Model: "sonnet", Payload: []byte(`{"messages":[]}`)}, cliproxyexecutor.Options{})
	assertOpenCodeGoStatus(t, err, http.StatusBadRequest, "inconsistent")
	if claude.executeCalls != 0 || compat.executeCalls != 0 {
		t.Fatalf("delegates were called on inconsistent protocol: claude=%d compat=%d", claude.executeCalls, compat.executeCalls)
	}
}

func TestOpenCodeGoExecutorRejectsAmbiguousOrMissingMetadata(t *testing.T) {
	cfg := &config.Config{OpenCodeGo: config.OpenCodeGoConfig{KeyGroups: []config.OpenCodeGoKeyGroup{{
		NamePrefix: "opencode-go",
		OpenAI:     &config.OpenCodeGoProtocolConfig{BaseURL: "https://openai.example", Models: []config.OpenCodeGoModelEntry{{Name: "shared"}}},
		Anthropic:  &config.OpenCodeGoProtocolConfig{BaseURL: "https://claude.example", Models: []config.OpenCodeGoModelEntry{{Name: "shared"}}},
		Keys:       []config.OpenCodeGoKeyEntry{{KeyName: "test", APIKey: "secret"}},
	}}}}
	exec := &OpenCodeGoExecutor{providerKey: "opencode-go", models: buildOpenCodeGoModelIndex(cfg), claude: &openCodeGoCaptureDelegate{name: "claude"}, compat: &openCodeGoCaptureDelegate{name: "openai"}}
	if got := exec.RequestToFormat(cliproxyexecutor.Request{Model: "shared"}, cliproxyexecutor.Options{}); got != "" {
		t.Fatalf("RequestToFormat(shared) = %q, want empty for ambiguous model", got)
	}
	_, err := exec.Execute(context.Background(), openCodeGoAuthForTest("openai"), cliproxyexecutor.Request{Model: "shared", Payload: []byte(`{"messages":[]}`)}, cliproxyexecutor.Options{})
	assertOpenCodeGoStatus(t, err, http.StatusBadRequest, "multiple protocols")

	missingProtocol := openCodeGoAuthForTest("openai")
	delete(missingProtocol.Attributes, "protocol")
	routeExec, _, _ := newOpenCodeGoExecutorForTest()
	_, err = routeExec.Execute(context.Background(), missingProtocol, cliproxyexecutor.Request{Model: "latest", Payload: []byte(`{"messages":[]}`)}, cliproxyexecutor.Options{})
	assertOpenCodeGoStatus(t, err, http.StatusBadRequest, "missing protocol metadata")
	_, err = exec.Refresh(context.Background(), missingProtocol)
	assertOpenCodeGoStatus(t, err, http.StatusBadRequest, "missing protocol metadata")
}

type openCodeGoCaptureDelegate struct {
	name            string
	executeCalls    int
	streamCalls     int
	refreshCalls    int
	countCalls      int
	httpCalls       int
	lastAuth        *cliproxyauth.Auth
	lastReq         cliproxyexecutor.Request
	lastOpts        cliproxyexecutor.Options
	responsePayload []byte
	streamPayload   []byte
	countPayload    []byte
}

func (d *openCodeGoCaptureDelegate) PrepareRequest(req *http.Request, auth *cliproxyauth.Auth) error {
	d.lastAuth = auth
	req.Header.Set("X-Delegate", d.name)
	return nil
}

func (d *openCodeGoCaptureDelegate) Execute(_ context.Context, auth *cliproxyauth.Auth, req cliproxyexecutor.Request, opts cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
	d.executeCalls++
	d.lastAuth = auth
	d.lastReq = req
	d.lastOpts = opts
	if len(d.responsePayload) > 0 {
		return cliproxyexecutor.Response{Payload: d.responsePayload}, nil
	}
	return cliproxyexecutor.Response{Payload: []byte(d.name + ":execute")}, nil
}

func (d *openCodeGoCaptureDelegate) ExecuteStream(_ context.Context, auth *cliproxyauth.Auth, req cliproxyexecutor.Request, opts cliproxyexecutor.Options) (*cliproxyexecutor.StreamResult, error) {
	d.streamCalls++
	d.lastAuth = auth
	d.lastReq = req
	d.lastOpts = opts
	chunks := make(chan cliproxyexecutor.StreamChunk, 1)
	payload := []byte(d.name + ":stream")
	if len(d.streamPayload) > 0 {
		payload = d.streamPayload
	}
	chunks <- cliproxyexecutor.StreamChunk{Payload: payload}
	close(chunks)
	return &cliproxyexecutor.StreamResult{Headers: http.Header{"X-Delegate": []string{d.name}}, Chunks: chunks}, nil
}

func (d *openCodeGoCaptureDelegate) Refresh(_ context.Context, auth *cliproxyauth.Auth) (*cliproxyauth.Auth, error) {
	d.refreshCalls++
	d.lastAuth = auth
	return auth, nil
}

func (d *openCodeGoCaptureDelegate) CountTokens(_ context.Context, auth *cliproxyauth.Auth, req cliproxyexecutor.Request, opts cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
	d.countCalls++
	d.lastAuth = auth
	d.lastReq = req
	d.lastOpts = opts
	if len(d.countPayload) > 0 {
		return cliproxyexecutor.Response{Payload: d.countPayload}, nil
	}
	return cliproxyexecutor.Response{Payload: []byte(d.name + ":count")}, nil
}

func (d *openCodeGoCaptureDelegate) HttpRequest(_ context.Context, auth *cliproxyauth.Auth, _ *http.Request) (*http.Response, error) {
	d.httpCalls++
	d.lastAuth = auth
	return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader("ok"))}, nil
}

func newOpenCodeGoExecutorForTest() (*OpenCodeGoExecutor, *openCodeGoCaptureDelegate, *openCodeGoCaptureDelegate) {
	claude := &openCodeGoCaptureDelegate{name: "claude"}
	compat := &openCodeGoCaptureDelegate{name: "openai"}
	cfg := &config.Config{OpenCodeGo: config.OpenCodeGoConfig{KeyGroups: []config.OpenCodeGoKeyGroup{{
		NamePrefix: "opencode-go",
		OpenAI: &config.OpenCodeGoProtocolConfig{BaseURL: "https://openai.example", Prefix: "og", Models: []config.OpenCodeGoModelEntry{
			{Name: "gpt-5", Alias: "latest"},
		}},
		Anthropic: &config.OpenCodeGoProtocolConfig{BaseURL: "https://claude.example", Prefix: "oc", Models: []config.OpenCodeGoModelEntry{
			{Name: "claude-sonnet", Alias: "sonnet"},
		}},
		Keys: []config.OpenCodeGoKeyEntry{{KeyName: "test", APIKey: "secret"}},
	}}}}
	return &OpenCodeGoExecutor{providerKey: "opencode-go", cfg: cfg, models: buildOpenCodeGoModelIndex(cfg), claude: claude, compat: compat}, claude, compat
}

func canonicalOpenCodeGoAuthForTest() *cliproxyauth.Auth {
	return &cliproxyauth.Auth{
		ID:       "auth-key",
		Provider: "opencode-go",
		Attributes: map[string]string{
			"api_key":     "secret",
			"name_prefix": "opencode-go",
			"key_name":    "test",
			"protocols":   "openai,anthropic",
		},
	}
}

func openCodeGoAuthForTest(protocol string) *cliproxyauth.Auth {
	attrsProtocol := protocol
	if protocol == "anthropic" {
		attrsProtocol = "anthropic"
	}
	return &cliproxyauth.Auth{
		ID:       "auth-" + protocol,
		Provider: "opencode-go",
		Attributes: map[string]string{
			"protocol": attrsProtocol,
			"base_url": "https://" + protocol + ".example",
			"api_key":  "secret",
		},
	}
}

func opencodeGoExecutorPayloadRoles(t *testing.T, payload []byte) []string {
	t.Helper()
	var decoded struct {
		Messages []struct {
			Role string `json:"role"`
		} `json:"messages"`
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

func assertOpenCodeGoStatus(t *testing.T, err error, status int, contains string) {
	t.Helper()
	if err == nil {
		t.Fatalf("error = nil, want status %d containing %q", status, contains)
	}
	if !strings.Contains(err.Error(), contains) {
		t.Fatalf("error = %v, want contains %q", err, contains)
	}
	statusErr, ok := err.(interface{ StatusCode() int })
	if !ok || statusErr.StatusCode() != status {
		t.Fatalf("status = (%d, %t), want %d", func() int {
			if !ok {
				return 0
			}
			return statusErr.StatusCode()
		}(), ok, status)
	}
	requestErr, ok := err.(interface{ IsRequestScoped() bool })
	if !ok || !requestErr.IsRequestScoped() {
		t.Fatalf("request scoped = (%t, %t), want true", ok, ok && requestErr.IsRequestScoped())
	}
}

func openCodeGoHTTPResponse(status int, body string) *http.Response {
	return &http.Response{
		StatusCode: status,
		Header:     http.Header{"Content-Type": []string{"application/json"}},
		Body:       io.NopCloser(strings.NewReader(body)),
	}
}
