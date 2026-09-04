package helps

import (
	"context"
	"fmt"
	"net/http"
	"strings"

	"github.com/tidwall/gjson"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/registry"
	"github.com/router-for-me/CLIProxyAPI/v7/sdk/oagmsg"
)

const (
	// VisionAntiLoopHeader marks internal vision requests so future executor hooks
	// can avoid recursively routing a vision-provider call through vision rewrite.
	VisionAntiLoopHeader = "X-CPA-Vision-Proxy"

	VisionAntiLoopMetadataKey = "cpa_vision_proxy"
	VisionScopeLatest         = "latest"
	VisionScopeAll            = "all"

	visionDescriptionPrefix = "[Image description]\n"
	visionPlaceholderLatest = "[Image omitted: vision preprocessing only describes images in the latest user message.]"
	visionPlaceholderAll    = "[Image omitted: vision preprocessing could not select this image for description.]"
)

// ShouldApplyVisionRewrite decides whether a request for model should use vision preprocessing.
// Unknown models are treated as not vision-capable so configured preprocessing can help them.
func ShouldApplyVisionRewrite(model string, cfg *config.VisionConfig, headers http.Header, metadata map[string]any, provider ...string) bool {
	if cfg == nil || !cfg.Enabled {
		return false
	}
	if HasVisionAntiLoopHeader(headers) || HasVisionAntiLoopMetadata(metadata) {
		return false
	}

	model = strings.TrimSpace(model)
	if matchVisionPattern(model, cfg.Include) {
		return true
	}
	if matchVisionPattern(model, cfg.Exclude) {
		return false
	}

	capability := registry.ModelVisionCapability(model, provider...)
	if capability.Known {
		return !capability.Supports
	}
	return true
}

// HasVisionImages reports whether an oagmsg request contains any image block.
func HasVisionImages(req *oagmsg.UnifiedRequest) bool {
	if req == nil {
		return false
	}
	for _, msg := range req.Messages {
		for _, block := range msg.Content {
			if _, ok := block.(oagmsg.ImageBlock); ok {
				return true
			}
		}
	}
	return false
}

// ApplyVisionRewrite replaces image blocks with text descriptions for requests
// whose target model needs configured vision preprocessing.
func ApplyVisionRewrite(ctx context.Context, cfg *config.Config, sourceFormat string, model string, payload []byte, headers http.Header, metadata map[string]any, provider ...string) ([]byte, bool, error) {
	if cfg == nil {
		return payload, false, nil
	}
	if !ShouldApplyVisionRewrite(model, &cfg.Vision, headers, metadata, provider...) || PayloadHasVisionAntiLoopMetadata(payload) {
		return payload, false, nil
	}
	format, errFormat := visionRewriteFormat(sourceFormat)
	if errFormat != nil {
		return payload, false, errFormat
	}
	handler, ok := oagmsg.DefaultRegistry().Get(format)
	if !ok {
		return payload, false, fmt.Errorf("vision rewrite: no handler for source format %q", format)
	}
	unified, errParse := handler.ParseRequest(payload)
	if errParse != nil {
		return payload, false, fmt.Errorf("vision rewrite: parse %s request: %w", format, errParse)
	}
	if !HasVisionImages(unified) {
		return payload, false, nil
	}

	latestUserIndex := latestUserMessageIndex(unified.Messages)
	scope := strings.ToLower(strings.TrimSpace(cfg.Vision.Scope))
	if scope == "" {
		scope = VisionScopeLatest
	}
	changed := false
	for msgIndex := range unified.Messages {
		message := &unified.Messages[msgIndex]
		blocks := make([]oagmsg.ContentBlock, 0, len(message.Content))
		for blockIndex, block := range message.Content {
			img, okImage := block.(oagmsg.ImageBlock)
			if !okImage {
				blocks = append(blocks, block)
				continue
			}

			changed = true
			if shouldDescribeVisionImage(scope, msgIndex, latestUserIndex) {
				resolved, errResolve := ResolveVisionImage(ctx, img)
				if errResolve != nil {
					return payload, false, fmt.Errorf("vision rewrite: resolve image in message %d block %d: %w", msgIndex, blockIndex, errResolve)
				}
				description, errDescribe := CallVisionProvider(ctx, cfg, resolved)
				if errDescribe != nil {
					return payload, false, fmt.Errorf("vision rewrite: describe image in message %d block %d: %w", msgIndex, blockIndex, errDescribe)
				}
				blocks = append(blocks, visionTextBlock(visionDescriptionPrefix+strings.TrimSpace(description), img.CacheControl))
				continue
			}

			blocks = append(blocks, visionTextBlock(visionPlaceholderText(scope), img.CacheControl))
		}
		message.Content = blocks
	}
	if !changed {
		return payload, false, nil
	}
	out, errSerialize := oagmsg.DefaultRegistry().SerializeRequestPreserving(format, unified, payload)
	if errSerialize != nil {
		return payload, false, fmt.Errorf("vision rewrite: serialize %s request: %w", format, errSerialize)
	}
	return out, true, nil
}

func visionRewriteFormat(format string) (oagmsg.Format, error) {
	switch strings.ToLower(strings.TrimSpace(format)) {
	case "", "openai":
		return oagmsg.FormatOpenAI, nil
	case "openai-response", "responses", "interactions":
		return oagmsg.FormatOpenAIResponse, nil
	case "claude", "anthropic":
		return oagmsg.FormatAnthropic, nil
	case "codex":
		return oagmsg.FormatCodex, nil
	case "gemini":
		return oagmsg.FormatGemini, nil
	default:
		return "", fmt.Errorf("vision rewrite: unsupported source format %q", format)
	}
}

func latestUserMessageIndex(messages []oagmsg.OagMessage) int {
	latest := -1
	for i, message := range messages {
		if message.Role == "user" {
			latest = i
		}
	}
	return latest
}

func shouldDescribeVisionImage(scope string, msgIndex int, latestUserIndex int) bool {
	return scope == VisionScopeAll || msgIndex == latestUserIndex
}

func visionTextBlock(text string, cacheControl map[string]any) oagmsg.TextBlock {
	return oagmsg.TextBlock{Text: text, CacheControl: cacheControl}
}

func visionPlaceholderText(scope string) string {
	if scope == VisionScopeLatest {
		return visionPlaceholderLatest
	}
	return visionPlaceholderAll
}

func HasVisionAntiLoopHeader(headers http.Header) bool {
	if headers == nil {
		return false
	}
	return strings.TrimSpace(headers.Get(VisionAntiLoopHeader)) != ""
}

func MarkVisionAntiLoopHeader(headers http.Header) {
	if headers == nil {
		return
	}
	headers.Set(VisionAntiLoopHeader, "1")
}

func HasVisionAntiLoopMetadata(metadata map[string]any) bool {
	if len(metadata) == 0 {
		return false
	}
	if truthyVisionMarker(metadata[VisionAntiLoopMetadataKey]) || truthyVisionMarker(metadata["vision_proxy"]) {
		return true
	}
	if nested, ok := metadata["cpa"].(map[string]any); ok {
		return truthyVisionMarker(nested["vision_proxy"])
	}
	return false
}

func PayloadHasVisionAntiLoopMetadata(payload []byte) bool {
	if len(payload) == 0 {
		return false
	}
	parsed := gjson.ParseBytes(payload)
	for _, path := range []string{"metadata.cpa_vision_proxy", "metadata.vision_proxy", "metadata.cpa.vision_proxy"} {
		if truthyVisionMarkerResult(parsed.Get(path)) {
			return true
		}
	}
	return false
}

func matchVisionPattern(model string, patterns []string) bool {
	model = strings.ToLower(strings.TrimSpace(model))
	if model == "" || len(patterns) == 0 {
		return false
	}
	for _, pattern := range patterns {
		pattern = strings.ToLower(strings.TrimSpace(pattern))
		if pattern == "" {
			continue
		}
		if model == pattern || strings.HasPrefix(model, pattern) {
			return true
		}
	}
	return false
}

func truthyVisionMarker(value any) bool {
	switch v := value.(type) {
	case bool:
		return v
	case string:
		v = strings.ToLower(strings.TrimSpace(v))
		return v == "1" || v == "true" || v == "yes" || v == "vision" || v == "rewrite"
	default:
		return false
	}
}

func truthyVisionMarkerResult(value gjson.Result) bool {
	if !value.Exists() {
		return false
	}
	if value.IsBool() {
		return value.Bool()
	}
	return truthyVisionMarker(value.String())
}
