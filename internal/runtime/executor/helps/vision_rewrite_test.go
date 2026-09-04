package helps

import (
	"context"
	"encoding/base64"
	"net/http"
	"strings"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/registry"
	"github.com/router-for-me/CLIProxyAPI/v7/sdk/oagmsg"
	"github.com/tidwall/gjson"
)

func TestShouldApplyVisionRewriteRegistryImageBypassesByDefault(t *testing.T) {
	modelID := "vision-registry-image-test"
	clientID := "vision-registry-image-client"
	reg := registry.GetGlobalRegistry()
	reg.RegisterClient(clientID, "openai", []*registry.ModelInfo{{
		ID:                       modelID,
		SupportedInputModalities: []string{"TEXT", "IMAGE"},
	}})
	defer reg.UnregisterClient(clientID)

	cfg := &config.VisionConfig{Enabled: true}
	if ShouldApplyVisionRewrite(modelID, cfg, nil, nil) {
		t.Fatal("ShouldApplyVisionRewrite() = true, want false for IMAGE modality")
	}
}

func TestShouldApplyVisionRewriteRules(t *testing.T) {
	cfg := &config.VisionConfig{
		Enabled: true,
		Include: []string{"native-text-only"},
		Exclude: []string{"custom-vision"},
	}
	if !ShouldApplyVisionRewrite("native-text-only-variant", cfg, nil, nil) {
		t.Fatal("include rule should force vision rewrite")
	}
	if ShouldApplyVisionRewrite("custom-vision-v1", cfg, nil, nil) {
		t.Fatal("exclude rule should bypass vision rewrite")
	}
	if !ShouldApplyVisionRewrite("unknown-model-for-vision", &config.VisionConfig{Enabled: true}, nil, nil) {
		t.Fatal("unknown model should use configured vision rewrite fallback policy")
	}
	if ShouldApplyVisionRewrite("anything", &config.VisionConfig{Enabled: false}, nil, nil) {
		t.Fatal("disabled config should bypass vision rewrite")
	}
}

func TestVisionAntiLoopDetection(t *testing.T) {
	headers := http.Header{}
	headers.Set(VisionAntiLoopHeader, "0")
	if !HasVisionAntiLoopHeader(headers) {
		t.Fatal("expected anti-loop header with any non-empty value")
	}
	MarkVisionAntiLoopHeader(headers)
	if !HasVisionAntiLoopHeader(headers) {
		t.Fatal("expected anti-loop header")
	}
	cfg := &config.VisionConfig{Enabled: true}
	if ShouldApplyVisionRewrite("unknown", cfg, headers, nil) {
		t.Fatal("anti-loop header should bypass vision rewrite")
	}
	if !HasVisionAntiLoopMetadata(map[string]any{VisionAntiLoopMetadataKey: true}) {
		t.Fatal("expected anti-loop metadata")
	}
	if !PayloadHasVisionAntiLoopMetadata([]byte(`{"metadata":{"cpa":{"vision_proxy":true}}}`)) {
		t.Fatal("expected anti-loop payload metadata")
	}
	if ShouldApplyVisionRewrite("unknown", cfg, nil, map[string]any{"cpa": map[string]any{"vision_proxy": "1"}}) {
		t.Fatal("anti-loop metadata should bypass vision rewrite")
	}
}

func TestHasVisionImages(t *testing.T) {
	if HasVisionImages(nil) {
		t.Fatal("nil request should not contain images")
	}
	withoutImage := &oagmsg.UnifiedRequest{Messages: []oagmsg.OagMessage{{Role: "user", Content: []oagmsg.ContentBlock{oagmsg.TextBlock{Text: "hello"}}}}}
	if HasVisionImages(withoutImage) {
		t.Fatal("text-only request should not contain images")
	}
	withImage := &oagmsg.UnifiedRequest{Messages: []oagmsg.OagMessage{{Role: "user", Content: []oagmsg.ContentBlock{oagmsg.ImageBlock{Data: "abc"}}}}}
	if !HasVisionImages(withImage) {
		t.Fatal("request with image block should contain images")
	}
}

func TestApplyVisionRewriteLatestPreservesOpenAITopLevelFields(t *testing.T) {
	imageData := base64.StdEncoding.EncodeToString(tinyPNG)
	payload := []byte(`{
		"model":"text-only-model",
		"stream":true,
		"temperature":0.2,
		"reasoning_effort":"high",
		"parallel_tool_calls":false,
		"metadata":{"trace":"keep"},
		"tools":[{"type":"function","function":{"name":"lookup","parameters":{"type":"object"}}}],
		"messages":[
			{"role":"user","content":[{"type":"text","text":"old"},{"type":"image_url","image_url":{"url":"data:image/png;base64,` + imageData + `"}}]},
			{"role":"assistant","content":"ok"},
			{"role":"user","content":[{"type":"text","text":"new"},{"type":"image_url","image_url":{"url":"data:image/png;base64,` + imageData + `"}}]}
		]
	}`)
	cfg := visionProviderTestConfig("openai")
	cfg.Vision.Model = "vision-model"
	cfg.Vision.Scope = VisionScopeLatest
	providerCalls := 0
	ctx := context.WithValue(context.Background(), "cliproxy.roundtripper", visionProviderRoundTripFunc(func(req *http.Request) (*http.Response, error) {
		providerCalls++
		return visionProviderResponse(http.StatusOK, `{"choices":[{"message":{"content":"a chart with text"}}]}`), nil
	}))

	out, changed, err := ApplyVisionRewrite(ctx, cfg, "openai", "text-only-model", payload, nil, nil)
	if err != nil {
		t.Fatalf("ApplyVisionRewrite() error = %v", err)
	}
	if !changed {
		t.Fatal("ApplyVisionRewrite() changed = false")
	}
	if providerCalls != 1 {
		t.Fatalf("provider calls = %d, want 1", providerCalls)
	}
	if strings.Contains(string(out), "image_url") {
		t.Fatalf("rewritten payload still contains image_url: %s", out)
	}
	if got := gjson.GetBytes(out, "model").String(); got != "text-only-model" {
		t.Fatalf("model = %q", got)
	}
	if got := gjson.GetBytes(out, "tools.0.function.name").String(); got != "lookup" {
		t.Fatalf("tool name = %q; body=%s", got, out)
	}
	if got := gjson.GetBytes(out, "reasoning_effort").String(); got != "high" {
		t.Fatalf("reasoning_effort = %q; body=%s", got, out)
	}
	if got := gjson.GetBytes(out, "metadata.trace").String(); got != "keep" {
		t.Fatalf("metadata.trace = %q; body=%s", got, out)
	}
	if got := gjson.GetBytes(out, "parallel_tool_calls").Bool(); got {
		t.Fatalf("parallel_tool_calls should remain false; body=%s", out)
	}
	if got := gjson.GetBytes(out, "messages.0.content.1.text").String(); got != visionPlaceholderLatest {
		t.Fatalf("old image placeholder = %q", got)
	}
	if got := gjson.GetBytes(out, "messages.2.content.1.text").String(); !strings.Contains(got, "a chart with text") {
		t.Fatalf("latest image description = %q", got)
	}
}

func TestApplyVisionRewriteAllDescribesEveryImage(t *testing.T) {
	imageData := base64.StdEncoding.EncodeToString(tinyPNG)
	payload := []byte(`{"model":"text-only-model","messages":[{"role":"user","content":[{"type":"image_url","image_url":{"url":"data:image/png;base64,` + imageData + `"}}]},{"role":"user","content":[{"type":"image_url","image_url":{"url":"data:image/png;base64,` + imageData + `"}}]}]}`)
	cfg := visionProviderTestConfig("openai")
	cfg.Vision.Scope = VisionScopeAll
	providerCalls := 0
	ctx := context.WithValue(context.Background(), "cliproxy.roundtripper", visionProviderRoundTripFunc(func(req *http.Request) (*http.Response, error) {
		providerCalls++
		return visionProviderResponse(http.StatusOK, `{"choices":[{"message":{"content":"description"}}]}`), nil
	}))

	out, changed, err := ApplyVisionRewrite(ctx, cfg, "openai", "text-only-model", payload, nil, nil)
	if err != nil {
		t.Fatalf("ApplyVisionRewrite() error = %v", err)
	}
	if !changed || providerCalls != 2 {
		t.Fatalf("changed=%v providerCalls=%d, want true/2", changed, providerCalls)
	}
	if strings.Contains(string(out), visionPlaceholderLatest) || strings.Contains(string(out), "image_url") {
		t.Fatalf("all scope left placeholder or image: %s", out)
	}
}

func TestApplyVisionRewriteAntiLoopBypassesProvider(t *testing.T) {
	imageData := base64.StdEncoding.EncodeToString(tinyPNG)
	payload := []byte(`{"model":"text-only-model","messages":[{"role":"user","content":[{"type":"image_url","image_url":{"url":"data:image/png;base64,` + imageData + `"}}]}]}`)
	cfg := visionProviderTestConfig("openai")
	headers := http.Header{}
	MarkVisionAntiLoopHeader(headers)
	called := false
	ctx := context.WithValue(context.Background(), "cliproxy.roundtripper", visionProviderRoundTripFunc(func(req *http.Request) (*http.Response, error) {
		called = true
		return visionProviderResponse(http.StatusOK, `{}`), nil
	}))

	out, changed, err := ApplyVisionRewrite(ctx, cfg, "openai", "text-only-model", payload, headers, nil)
	if err != nil {
		t.Fatalf("ApplyVisionRewrite() error = %v", err)
	}
	if changed || string(out) != string(payload) || called {
		t.Fatalf("anti-loop did not bypass: changed=%v called=%v out=%s", changed, called, out)
	}
}
