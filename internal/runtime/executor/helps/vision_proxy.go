package helps

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v7/sdk/oagmsg"
	"github.com/tidwall/gjson"
)

const (
	maxVisionProviderResponseBytes = 4 << 20
	visionProviderMaxTokens        = 1024
	visionProviderPrompt           = "Describe the image for a downstream text-only model. Return a concise structured description covering visible objects, text, layout, and task-relevant details. Do not mention that you are an AI model."
)

// CallVisionProvider sends image to the configured vision provider and returns
// a plain text description suitable for replacing the original image block.
func CallVisionProvider(ctx context.Context, cfg *config.Config, image ResolvedVisionImage) (string, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if cfg == nil || !cfg.Vision.Enabled {
		return "", fmt.Errorf("vision provider: vision config is disabled")
	}
	models := visionProviderModels(cfg.Vision)
	if len(models) == 0 {
		return "", fmt.Errorf("vision provider: model is required")
	}
	baseURL := strings.TrimRight(strings.TrimSpace(cfg.Vision.Provider.BaseURL), "/")
	if baseURL == "" {
		return "", fmt.Errorf("vision provider: base URL is required")
	}
	protocol, errProtocol := normalizeVisionProviderProtocol(cfg.Vision.Provider)
	if errProtocol != nil {
		return "", errProtocol
	}

	var lastErr error
	for index, model := range models {
		description, errCall := callVisionProviderModel(ctx, cfg, protocol, model, image)
		if errCall == nil {
			return description, nil
		}
		lastErr = errCall
		if index == len(models)-1 {
			break
		}
		if ctx.Err() != nil || !retryableVisionProviderCallError(errCall) {
			break
		}
	}
	if lastErr == nil {
		lastErr = fmt.Errorf("no provider model attempted")
	}
	return "", fmt.Errorf("vision provider: %w", lastErr)
}

func callVisionProviderModel(ctx context.Context, cfg *config.Config, protocol string, model string, image ResolvedVisionImage) (string, error) {
	body, errBody := buildVisionProviderRequestBody(protocol, model, image)
	if errBody != nil {
		return "", newVisionProviderCallError(errBody, false)
	}
	url := visionProviderURL(strings.TrimRight(strings.TrimSpace(cfg.Vision.Provider.BaseURL), "/"), protocol)
	req, errReq := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if errReq != nil {
		return "", newVisionProviderCallError(fmt.Errorf("create request: %w", errReq), false)
	}
	applyVisionProviderHeaders(req.Header, cfg.Vision.Provider, protocol)

	client := NewProxyAwareHTTPClient(ctx, cfg, nil, 0)
	resp, errHTTP := client.Do(req)
	if errHTTP != nil {
		retryable := ctx.Err() == nil && hasVisionProviderTransientSignal([]byte(errHTTP.Error()))
		return "", newVisionProviderCallError(fmt.Errorf("HTTP request for model %q: %w", model, errHTTP), retryable)
	}
	defer func() {
		_ = resp.Body.Close()
	}()
	bodyResp, errRead := readVisionProviderBody(resp.Body)
	if errRead != nil {
		return "", newVisionProviderCallError(errRead, false)
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		retryable := ctx.Err() == nil && retryableVisionProviderHTTPStatus(resp.StatusCode, bodyResp)
		return "", newVisionProviderCallError(fmt.Errorf("HTTP status %d for model %q: %s", resp.StatusCode, model, SummarizeErrorBody(resp.Header.Get("Content-Type"), bodyResp)), retryable)
	}
	description, errParse := parseVisionProviderDescription(protocol, bodyResp)
	if errParse != nil {
		return "", newVisionProviderCallError(fmt.Errorf("parse response for model %q: %w", model, errParse), false)
	}
	return description, nil
}

func buildVisionProviderRequestBody(protocol string, model string, image ResolvedVisionImage) ([]byte, error) {
	model = strings.TrimSpace(model)
	if model == "" {
		return nil, fmt.Errorf("model is required")
	}
	switch protocol {
	case string(oagmsg.FormatAnthropic):
		return json.Marshal(map[string]any{
			"model":      model,
			"max_tokens": visionProviderMaxTokens,
			"system":     visionProviderPrompt,
			"messages": []any{map[string]any{
				"role": "user",
				"content": []any{
					map[string]any{"type": "text", "text": "Describe this image."},
					map[string]any{
						"type": "image",
						"source": map[string]any{
							"type":       "base64",
							"media_type": image.MediaType,
							"data":       image.Base64Data,
						},
					},
				},
			}},
		})
	case string(oagmsg.FormatOpenAI):
		return json.Marshal(map[string]any{
			"model":      model,
			"max_tokens": visionProviderMaxTokens,
			"stream":     false,
			"messages": []any{
				map[string]any{"role": "system", "content": visionProviderPrompt},
				map[string]any{
					"role": "user",
					"content": []any{
						map[string]any{"type": "text", "text": "Describe this image."},
						map[string]any{
							"type": "image_url",
							"image_url": map[string]any{
								"url": fmt.Sprintf("data:%s;base64,%s", image.MediaType, image.Base64Data),
							},
						},
					},
				},
			},
		})
	default:
		return nil, fmt.Errorf("unsupported protocol %q", protocol)
	}
}

func applyVisionProviderHeaders(headers http.Header, provider config.VisionProviderConfig, protocol string) {
	headers.Set("Content-Type", "application/json")
	headers.Set("User-Agent", "cli-proxy-vision")
	apiKey := strings.TrimSpace(provider.APIKey)
	switch protocol {
	case string(oagmsg.FormatAnthropic):
		if apiKey != "" {
			headers.Set("X-API-Key", apiKey)
		}
		if strings.TrimSpace(headers.Get("Anthropic-Version")) == "" {
			headers.Set("Anthropic-Version", "2023-06-01")
		}
	case string(oagmsg.FormatOpenAI):
		if apiKey != "" {
			headers.Set("Authorization", "Bearer "+apiKey)
		}
	}
	for key, value := range provider.Headers {
		key = strings.TrimSpace(key)
		if key == "" {
			continue
		}
		headers.Set(key, value)
	}
	MarkVisionAntiLoopHeader(headers)
}

func parseVisionProviderDescription(protocol string, body []byte) (string, error) {
	format := oagmsg.Format(protocol)
	handler, ok := oagmsg.DefaultRegistry().Get(format)
	if ok {
		resp, errParse := handler.ParseResponse(body)
		if errParse == nil && strings.TrimSpace(resp.Content) != "" {
			return strings.TrimSpace(resp.Content), nil
		}
	}

	root := gjson.ParseBytes(body)
	for _, path := range visionProviderDescriptionPaths(protocol) {
		if text := strings.TrimSpace(root.Get(path).String()); text != "" {
			return text, nil
		}
	}
	return "", fmt.Errorf("empty text description")
}

func visionProviderDescriptionPaths(protocol string) []string {
	switch protocol {
	case string(oagmsg.FormatAnthropic):
		return []string{"content.0.text", "completion"}
	case string(oagmsg.FormatOpenAI):
		return []string{"choices.0.message.content", "output_text", "choices.0.text"}
	default:
		return nil
	}
}

func readVisionProviderBody(r io.Reader) ([]byte, error) {
	data, errRead := io.ReadAll(io.LimitReader(r, maxVisionProviderResponseBytes+1))
	if errRead != nil {
		return nil, fmt.Errorf("read response: %w", errRead)
	}
	if len(data) > maxVisionProviderResponseBytes {
		return nil, fmt.Errorf("response exceeds %d bytes", maxVisionProviderResponseBytes)
	}
	return data, nil
}

func visionProviderModels(cfg config.VisionConfig) []string {
	seen := map[string]struct{}{}
	var models []string
	for _, model := range []string{cfg.Model, cfg.Fallback} {
		model = strings.TrimSpace(model)
		if model == "" {
			continue
		}
		key := strings.ToLower(model)
		if _, ok := seen[key]; ok {
			continue
		}
		seen[key] = struct{}{}
		models = append(models, model)
	}
	return models
}

func normalizeVisionProviderProtocol(provider config.VisionProviderConfig) (string, error) {
	protocol := strings.ToLower(strings.TrimSpace(provider.Protocol))
	if protocol == "" {
		protocol = strings.ToLower(strings.TrimSpace(provider.Name))
	}
	switch protocol {
	case "", "openai", "openai-compatible", "openai_compat":
		return string(oagmsg.FormatOpenAI), nil
	case "claude", "anthropic":
		return string(oagmsg.FormatAnthropic), nil
	default:
		return "", fmt.Errorf("vision provider: unsupported protocol %q", protocol)
	}
}

func visionProviderURL(baseURL string, protocol string) string {
	baseURL = strings.TrimRight(strings.TrimSpace(baseURL), "/")
	switch protocol {
	case string(oagmsg.FormatAnthropic):
		if strings.HasSuffix(baseURL, "/messages") {
			return baseURL
		}
		return baseURL + "/messages"
	case string(oagmsg.FormatOpenAI):
		if strings.HasSuffix(baseURL, "/chat/completions") {
			return baseURL
		}
		return baseURL + "/chat/completions"
	default:
		return baseURL
	}
}

type visionProviderCallError struct {
	err       error
	retryable bool
}

func (e visionProviderCallError) Error() string {
	if e.err == nil {
		return ""
	}
	return e.err.Error()
}

func (e visionProviderCallError) Unwrap() error {
	return e.err
}

func newVisionProviderCallError(err error, retryable bool) error {
	if err == nil {
		return nil
	}
	return visionProviderCallError{err: err, retryable: retryable}
}

func retryableVisionProviderCallError(err error) bool {
	var callErr visionProviderCallError
	return errors.As(err, &callErr) && callErr.retryable
}

func retryableVisionProviderHTTPStatus(status int, body []byte) bool {
	if status == http.StatusBadRequest || status == http.StatusUnauthorized || status == http.StatusForbidden {
		return false
	}
	if status == http.StatusTooManyRequests || status >= 500 {
		return true
	}
	return hasVisionProviderTransientSignal(body)
}

func hasVisionProviderTransientSignal(body []byte) bool {
	text := strings.ToLower(string(body))
	for _, signal := range []string{"rate limit", "rate_limit", "quota", "temporar", "transient", "overloaded", "try again"} {
		if strings.Contains(text, signal) {
			return true
		}
	}
	return false
}
