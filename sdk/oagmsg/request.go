package oagmsg

import (
	"strings"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/signature"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/thinking"
)

// UnifiedRequest is the protocol-agnostic representation of a complete API request.
// Produced by ProtocolHandler.ParseRequest(), consumed by ProtocolHandler.SerializeRequest().
//
// Aligned with oag_server models.py UnifiedRequest (L244-270).
type UnifiedRequest struct {
	// Model is the user-specified model name (e.g. "gemini-2.5-pro", "claude-sonnet-4-20250514").
	Model string

	// Messages is the complete conversation history in protocol-agnostic form.
	Messages []OagMessage

	// Stream indicates whether to use streaming response.
	Stream bool

	// Temperature is the sampling temperature. Nil means use provider default.
	Temperature *float64

	// TopP is the nucleus sampling parameter. Nil means use provider default.
	TopP *float64

	// MaxTokens is the maximum number of output tokens. Nil means use provider default.
	MaxTokens *int

	// Stop is the list of stop sequences.
	Stop []string

	// Tools is the list of tool definitions.
	Tools []map[string]any

	// ToolChoice controls how the model selects tools. Can be string or map.
	ToolChoice any

	// Thinking is the canonical thinking/reasoning configuration.
	Thinking *thinking.ThinkingConfig

	// ReasoningEffort controls thinking intensity.
	// Deprecated: use Thinking. It is retained for compatibility with callers
	// that populated this field before oagmsg carried the canonical config.
	ReasoningEffort string

	// ResponseFormat controls structured output: {"type": "json_object"} or {"type": "json_schema", ...}.
	ResponseFormat map[string]any

	// SourceFormat records which protocol the request was parsed from.
	SourceFormat Format

	// anthropicWebSearch preserves request-scoped Claude web-search eligibility
	// facts that would otherwise be lost during generic request parsing.
	anthropicWebSearch *anthropicWebSearchRequestMetadata

	// responsesTools preserves request-scoped Responses tool declarations for
	// target-specific filtering. It is intentionally private to avoid expanding
	// the public UnifiedRequest API.
	responsesTools toolDescriptorIndex

	// responsesParallelToolCalls preserves the Responses request setting so Chat
	// serialization can emit it only when the target has surviving tools.
	responsesParallelToolCalls *bool

	// responsesServiceTier preserves OpenAI Responses service_tier for targets
	// with an equivalent request control, without making it canonical API.
	responsesServiceTier string

	// codexSourceInstructions preserves top-level Codex instructions presence so
	// parse/serialize can distinguish an explicit field from serializer output.
	codexSourceInstructions codexSourceInstructionsMetadata

	// translationOptions carries per-call behavior that must not become part of
	// the protocol-neutral public request model.
	translationOptions RequestTranslationOptions
}

type anthropicWebSearchRequestMetadata struct {
	onlyTypedSearchTools bool
	allowsToolChoice     bool
	query                string
	maxUses              int64
	includedDomains      []string
}

type codexSourceInstructionsMetadata struct {
	present bool
	raw     []byte
}

// SetThinking stores the canonical thinking config and syncs the legacy effort field.
func (r *UnifiedRequest) SetThinking(config *thinking.ThinkingConfig) {
	r.Thinking = config
	if effort := ThinkingEffort(config); effort != "" {
		r.ReasoningEffort = effort
	}
}

func requestThinkingForTarget(req *UnifiedRequest, target Format, role string, block ThinkingBlock, blockKind signature.SignatureBlockKind) (ThinkingBlock, bool) {
	if block.Redacted {
		return block, true
	}
	if req == nil || req.SourceFormat == "" || resolveFormat(req.SourceFormat) == resolveFormat(target) {
		return block, true
	}
	if role != "assistant" {
		return block, false
	}
	if req.translationOptions.PreserveThinkingBlocks {
		block.signaturePresent = true
		return block, true
	}

	rawSignature := block.Signature
	if strings.TrimSpace(rawSignature) == "" {
		return block, false
	}

	provider := requestThinkingTargetProvider(req, target)
	if provider == signature.SignatureProviderUnknown {
		return block, false
	}
	normalized, ok := signature.CompatibleSignatureForProviderBlock(provider, rawSignature, blockKind)
	if !ok {
		if target == FormatCodex && codexTargetAcceptsGrokSignature(req.Model) && signature.IsValidGrokEncryptedContent(rawSignature) {
			block.signaturePresent = true
			return block, true
		}
		return block, false
	}
	block.Signature = normalized
	block.signaturePresent = true
	return block, true
}

func requestThinkingTargetProvider(req *UnifiedRequest, target Format) signature.SignatureProvider {
	switch resolveFormat(target) {
	case FormatAnthropic:
		return signature.SignatureProviderClaude
	case FormatGemini, FormatAntigravity:
		return signature.SignatureProviderGemini
	case FormatOpenAI, FormatOpenAIResponse, FormatCodex:
		return signature.SignatureProviderGPT
	case FormatInteractions, FormatInteractionsSteps:
		provider := signature.SignatureProviderFromModelName(req.Model)
		if provider != signature.SignatureProviderUnknown {
			return provider
		}
		return signature.SignatureProviderGemini
	default:
		return signature.SignatureProviderUnknown
	}
}

func codexTargetAcceptsGrokSignature(modelName string) bool {
	baseModel := strings.ToLower(strings.TrimSpace(thinking.ParseSuffix(modelName).ModelName))
	return strings.Contains(baseModel, "grok")
}
