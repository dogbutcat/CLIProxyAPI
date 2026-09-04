package oagmsg

import "github.com/tidwall/gjson"

type requestTranslationPathContext struct {
	from    Format
	to      Format
	model   string
	rawJSON []byte
	hooks   PluginHooks
}

type requestTranslationPathDecision struct {
	kind   requestPathKind
	reason string
}

type requestTranslationPathPolicy struct {
	identityGuards []requestIdentityGuard
}

type requestIdentityGuard interface {
	AllowIdentity(requestTranslationPathContext) requestIdentityGuardResult
}

type requestIdentityGuardResult struct {
	allowed bool
	reason  string
}

var defaultRequestTranslationPathPolicyValue = requestTranslationPathPolicy{
	identityGuards: []requestIdentityGuard{
		requestNoPluginHooksGuard{},
		requestExactFormatGuard{},
		requestResponsesFamilyGuard{},
		requestStableModelGuard{},
		requestCodexFinalWireGuard{},
		requestCodexToolMetadataGuard{},
	},
}

func defaultRequestTranslationPathPolicy() requestTranslationPathPolicy {
	return defaultRequestTranslationPathPolicyValue
}

func (p requestTranslationPathPolicy) Select(ctx requestTranslationPathContext) requestTranslationPathDecision {
	if !sameRequestWireFormat(ctx.from, ctx.to) {
		return requestTranslationPathDecision{kind: requestTranslationPathCanonical, reason: "different wire format"}
	}
	for _, guard := range p.identityGuards {
		if result := guard.AllowIdentity(ctx); !result.allowed {
			if resolveFormat(ctx.to) == FormatCodex {
				return requestTranslationPathDecision{kind: requestTranslationPathCodexFinalize, reason: result.reason}
			}
			return requestTranslationPathDecision{kind: requestTranslationPathSameWire, reason: result.reason}
		}
	}
	return requestTranslationPathDecision{kind: requestTranslationPathIdentity, reason: "identity passthrough"}
}

type requestNoPluginHooksGuard struct{}

func (requestNoPluginHooksGuard) AllowIdentity(ctx requestTranslationPathContext) requestIdentityGuardResult {
	if ctx.hooks != nil {
		return requestIdentityGuardResult{reason: "plugin hooks are installed"}
	}
	return requestIdentityGuardResult{allowed: true}
}

type requestExactFormatGuard struct{}

func (requestExactFormatGuard) AllowIdentity(ctx requestTranslationPathContext) requestIdentityGuardResult {
	if ctx.from != ctx.to {
		return requestIdentityGuardResult{reason: "source and target formats differ"}
	}
	return requestIdentityGuardResult{allowed: true}
}

type requestResponsesFamilyGuard struct{}

func (requestResponsesFamilyGuard) AllowIdentity(ctx requestTranslationPathContext) requestIdentityGuardResult {
	if sameResponsesFamily(ctx.from, ctx.to) && resolveFormat(ctx.to) != FormatCodex {
		return requestIdentityGuardResult{reason: "responses-family requests need same-wire normalization"}
	}
	return requestIdentityGuardResult{allowed: true}
}

type requestStableModelGuard struct{}

func (requestStableModelGuard) AllowIdentity(ctx requestTranslationPathContext) requestIdentityGuardResult {
	if ctx.model == "" || gjson.GetBytes(ctx.rawJSON, "model").String() == ctx.model {
		return requestIdentityGuardResult{allowed: true}
	}
	return requestIdentityGuardResult{reason: "runtime model override is required"}
}

type requestCodexFinalWireGuard struct{}

func (requestCodexFinalWireGuard) AllowIdentity(ctx requestTranslationPathContext) requestIdentityGuardResult {
	if resolveFormat(ctx.to) != FormatCodex {
		return requestIdentityGuardResult{allowed: true}
	}
	if ctx.from != FormatCodex {
		return requestIdentityGuardResult{reason: "codex target requires finalization"}
	}
	if !codexRequestAlreadyFinalized(ctx.rawJSON) {
		return requestIdentityGuardResult{reason: "codex request is not final-wire safe"}
	}
	return requestIdentityGuardResult{allowed: true}
}

type requestCodexToolMetadataGuard struct{}

func (requestCodexToolMetadataGuard) AllowIdentity(ctx requestTranslationPathContext) requestIdentityGuardResult {
	if resolveFormat(ctx.to) != FormatCodex {
		return requestIdentityGuardResult{allowed: true}
	}
	if codexRequestNeedsToolMetadata(ctx.rawJSON) {
		return requestIdentityGuardResult{reason: "codex tool metadata may need canonicalization"}
	}
	return requestIdentityGuardResult{allowed: true}
}
