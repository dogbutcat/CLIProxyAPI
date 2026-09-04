package helps

import (
	"context"
	"encoding/base64"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	"github.com/tidwall/gjson"
)

func TestVisionProviderOpenAIPrimaryFallbackRequestConstruction(t *testing.T) {
	image := ResolvedVisionImage{Base64Data: base64.StdEncoding.EncodeToString(tinyPNG), MediaType: "image/png", SizeBytes: len(tinyPNG)}
	cfg := visionProviderTestConfig("openai")
	cfg.Vision.Model = "primary-vision"
	cfg.Vision.Fallback = "fallback-vision"

	var seenModels []string
	ctx := context.WithValue(context.Background(), "cliproxy.roundtripper", visionProviderRoundTripFunc(func(req *http.Request) (*http.Response, error) {
		body, errRead := io.ReadAll(req.Body)
		if errRead != nil {
			t.Fatalf("read body: %v", errRead)
		}
		if got := req.URL.String(); got != "https://vision.example/v1/chat/completions" {
			t.Fatalf("URL = %q, want chat completions", got)
		}
		if got := req.Header.Get("Authorization"); got != "Bearer vision-key" {
			t.Fatalf("Authorization = %q", got)
		}
		if got := req.Header.Get("X-Test"); got != "provider-header" {
			t.Fatalf("X-Test = %q", got)
		}
		if !HasVisionAntiLoopHeader(req.Header) {
			t.Fatalf("missing anti-loop header: %#v", req.Header)
		}
		model := gjson.GetBytes(body, "model").String()
		seenModels = append(seenModels, model)
		if got := gjson.GetBytes(body, "messages.1.content.1.image_url.url").String(); !strings.HasPrefix(got, "data:image/png;base64,") {
			t.Fatalf("image URL = %q", got)
		}
		if model == "primary-vision" {
			return visionProviderResponse(http.StatusBadGateway, `{"error":{"message":"try fallback"}}`), nil
		}
		return visionProviderResponse(http.StatusOK, `{"choices":[{"message":{"content":"fallback description"}}]}`), nil
	}))

	description, err := CallVisionProvider(ctx, cfg, image)
	if err != nil {
		t.Fatalf("CallVisionProvider() error = %v", err)
	}
	if description != "fallback description" {
		t.Fatalf("description = %q", description)
	}
	if strings.Join(seenModels, ",") != "primary-vision,fallback-vision" {
		t.Fatalf("seen models = %v", seenModels)
	}
}

func TestVisionProviderAnthropicRequestConstruction(t *testing.T) {
	image := ResolvedVisionImage{Base64Data: base64.StdEncoding.EncodeToString(tinyPNG), MediaType: "image/png", SizeBytes: len(tinyPNG)}
	cfg := visionProviderTestConfig("claude")
	cfg.Vision.Model = "claude-vision"
	cfg.Vision.Provider.BaseURL = "https://vision.example/v1/messages"

	ctx := context.WithValue(context.Background(), "cliproxy.roundtripper", visionProviderRoundTripFunc(func(req *http.Request) (*http.Response, error) {
		body, errRead := io.ReadAll(req.Body)
		if errRead != nil {
			t.Fatalf("read body: %v", errRead)
		}
		if got := req.URL.String(); got != "https://vision.example/v1/messages" {
			t.Fatalf("URL = %q, want messages", got)
		}
		if got := req.Header.Get("X-API-Key"); got != "vision-key" {
			t.Fatalf("X-API-Key = %q", got)
		}
		if got := req.Header.Get("Anthropic-Version"); got == "" {
			t.Fatalf("missing Anthropic-Version")
		}
		if !HasVisionAntiLoopHeader(req.Header) {
			t.Fatalf("missing anti-loop header: %#v", req.Header)
		}
		if got := gjson.GetBytes(body, "messages.0.content.1.source.media_type").String(); got != "image/png" {
			t.Fatalf("media type = %q", got)
		}
		return visionProviderResponse(http.StatusOK, `{"content":[{"type":"text","text":"anthropic description"}]}`), nil
	}))

	description, err := CallVisionProvider(ctx, cfg, image)
	if err != nil {
		t.Fatalf("CallVisionProvider() error = %v", err)
	}
	if description != "anthropic description" {
		t.Fatalf("description = %q", description)
	}
}

func TestVisionProviderCancellationPropagates(t *testing.T) {
	image := ResolvedVisionImage{Base64Data: base64.StdEncoding.EncodeToString(tinyPNG), MediaType: "image/png", SizeBytes: len(tinyPNG)}
	cfg := visionProviderTestConfig("openai")
	called := false
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	ctx = context.WithValue(ctx, "cliproxy.roundtripper", visionProviderRoundTripFunc(func(req *http.Request) (*http.Response, error) {
		called = true
		return nil, req.Context().Err()
	}))

	_, err := CallVisionProvider(ctx, cfg, image)
	if err == nil || !strings.Contains(err.Error(), "context canceled") {
		t.Fatalf("CallVisionProvider() error = %v, want context canceled", err)
	}
	if !called {
		t.Fatal("round tripper was not called")
	}
}

func TestVisionProviderPrimaryBadRequestDoesNotFallback(t *testing.T) {
	image := ResolvedVisionImage{Base64Data: base64.StdEncoding.EncodeToString(tinyPNG), MediaType: "image/png", SizeBytes: len(tinyPNG)}
	cfg := visionProviderTestConfig("openai")
	cfg.Vision.Model = "primary-vision"
	cfg.Vision.Fallback = "fallback-vision"
	var seenModels []string
	ctx := context.WithValue(context.Background(), "cliproxy.roundtripper", visionProviderRoundTripFunc(func(req *http.Request) (*http.Response, error) {
		body, errRead := io.ReadAll(req.Body)
		if errRead != nil {
			t.Fatalf("read body: %v", errRead)
		}
		model := gjson.GetBytes(body, "model").String()
		seenModels = append(seenModels, model)
		return visionProviderResponse(http.StatusBadRequest, `{"error":{"message":"bad image"}}`), nil
	}))

	_, err := CallVisionProvider(ctx, cfg, image)
	if err == nil || !strings.Contains(err.Error(), "HTTP status 400") {
		t.Fatalf("CallVisionProvider() error = %v, want 400", err)
	}
	if strings.Join(seenModels, ",") != "primary-vision" {
		t.Fatalf("seen models = %v, want primary only", seenModels)
	}
}

func TestVisionProviderCanceledPrimaryDoesNotFallback(t *testing.T) {
	image := ResolvedVisionImage{Base64Data: base64.StdEncoding.EncodeToString(tinyPNG), MediaType: "image/png", SizeBytes: len(tinyPNG)}
	cfg := visionProviderTestConfig("openai")
	cfg.Vision.Model = "primary-vision"
	cfg.Vision.Fallback = "fallback-vision"
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	var seenModels []string
	ctx = context.WithValue(ctx, "cliproxy.roundtripper", visionProviderRoundTripFunc(func(req *http.Request) (*http.Response, error) {
		body, errRead := io.ReadAll(req.Body)
		if errRead != nil {
			t.Fatalf("read body: %v", errRead)
		}
		seenModels = append(seenModels, gjson.GetBytes(body, "model").String())
		return nil, req.Context().Err()
	}))

	_, err := CallVisionProvider(ctx, cfg, image)
	if err == nil || !strings.Contains(err.Error(), "context canceled") {
		t.Fatalf("CallVisionProvider() error = %v, want context canceled", err)
	}
	if strings.Join(seenModels, ",") != "primary-vision" {
		t.Fatalf("seen models = %v, want primary only", seenModels)
	}
}

func TestVisionProviderRetryableStatusFallsBack(t *testing.T) {
	tests := []struct {
		name   string
		status int
		body   string
	}{
		{name: "rate limit", status: http.StatusTooManyRequests, body: `{"error":{"message":"retry later"}}`},
		{name: "server error", status: http.StatusInternalServerError, body: `{"error":{"message":"retry later"}}`},
		{name: "explicit transient signal", status: http.StatusConflict, body: `{"error":{"message":"temporary quota exhausted, try again"}}`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			image := ResolvedVisionImage{Base64Data: base64.StdEncoding.EncodeToString(tinyPNG), MediaType: "image/png", SizeBytes: len(tinyPNG)}
			cfg := visionProviderTestConfig("openai")
			cfg.Vision.Model = "primary-vision"
			cfg.Vision.Fallback = "fallback-vision"
			var seenModels []string
			ctx := context.WithValue(context.Background(), "cliproxy.roundtripper", visionProviderRoundTripFunc(func(req *http.Request) (*http.Response, error) {
				body, errRead := io.ReadAll(req.Body)
				if errRead != nil {
					t.Fatalf("read body: %v", errRead)
				}
				model := gjson.GetBytes(body, "model").String()
				seenModels = append(seenModels, model)
				if model == "primary-vision" {
					return visionProviderResponse(tt.status, tt.body), nil
				}
				return visionProviderResponse(http.StatusOK, `{"choices":[{"message":{"content":"fallback description"}}]}`), nil
			}))

			description, err := CallVisionProvider(ctx, cfg, image)
			if err != nil {
				t.Fatalf("CallVisionProvider() error = %v", err)
			}
			if description != "fallback description" || strings.Join(seenModels, ",") != "primary-vision,fallback-vision" {
				t.Fatalf("description=%q seen=%v", description, seenModels)
			}
		})
	}
}

func visionProviderTestConfig(protocol string) *config.Config {
	return &config.Config{Vision: config.VisionConfig{
		Enabled: true,
		Model:   "vision-model",
		Scope:   VisionScopeLatest,
		Provider: config.VisionProviderConfig{
			BaseURL:  "https://vision.example/v1",
			APIKey:   "vision-key",
			Protocol: protocol,
			Headers:  map[string]string{"X-Test": "provider-header"},
		},
	}}
}

type visionProviderRoundTripFunc func(*http.Request) (*http.Response, error)

func (f visionProviderRoundTripFunc) RoundTrip(req *http.Request) (*http.Response, error) {
	return f(req)
}

func visionProviderResponse(status int, body string) *http.Response {
	return &http.Response{
		StatusCode: status,
		Header:     http.Header{"Content-Type": []string{"application/json"}},
		Body:       io.NopCloser(strings.NewReader(body)),
	}
}
