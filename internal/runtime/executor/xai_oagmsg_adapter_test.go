package executor

import (
	"github.com/router-for-me/CLIProxyAPI/v7/sdk/oagmsg"
	"github.com/tidwall/gjson"
)

type xaiNamespaceToolRef struct {
	namespace    string
	name         string
	isDispatcher bool
}

type xaiClientToolKey struct {
	namespace string
	name      string
	toolType  string
}

type xaiInternalXSearchResponseFilter struct {
	inner *oagmsg.XAIResponsesResponseFilter
}

type xaiNamespaceRestorer struct {
	inner *oagmsg.XAIResponsesNamespaceRestorer
}

func normalizeXAITools(body []byte) []byte {
	return oagmsg.NormalizeXAITools(body)
}

func promoteXAIAdditionalTools(body []byte) []byte {
	return oagmsg.PromoteXAIAdditionalTools(body)
}

func pruneXAIOrphanedToolChoice(body []byte) []byte {
	return oagmsg.PruneXAIOrphanedToolChoice(body)
}

func normalizeXAIToolChoiceForTools(body []byte) []byte {
	return oagmsg.NormalizeXAIToolChoiceForTools(body)
}

func normalizeXAINamespaceToolChoice(body []byte) []byte {
	return oagmsg.NormalizeXAINamespaceToolChoice(body)
}

func normalizeXAITool(tool gjson.Result, namespaceName string) ([]byte, bool, bool) {
	return oagmsg.NormalizeXAITool(tool, namespaceName)
}

func qualifyXAINamespaceToolName(namespaceName, toolName string) string {
	return oagmsg.QualifyXAINamespaceToolName(namespaceName, toolName)
}

func collectXAINamespaceToolRefs(body []byte) map[string]xaiNamespaceToolRef {
	refs := oagmsg.CollectXAINamespaceToolRefs(body)
	converted := make(map[string]xaiNamespaceToolRef, len(refs))
	for key, ref := range refs {
		converted[key] = xaiNamespaceToolRef{namespace: ref.Namespace, name: ref.Name, isDispatcher: ref.IsDispatcher}
	}
	return converted
}

func normalizeXAIInputNamespaceToolCalls(body []byte) []byte {
	return oagmsg.NormalizeXAIInputNamespaceToolCalls(body)
}

func restoreXAINamespaceToolCalls(data []byte, refs map[string]xaiNamespaceToolRef) []byte {
	converted := make(map[string]oagmsg.XAINamespaceToolRef, len(refs))
	for key, ref := range refs {
		converted[key] = oagmsg.XAINamespaceToolRef{Namespace: ref.namespace, Name: ref.name, IsDispatcher: ref.isDispatcher}
	}
	return oagmsg.RestoreXAINamespaceToolCalls(data, converted)
}

func newXAINamespaceRestorer(refs map[string]xaiNamespaceToolRef) *xaiNamespaceRestorer {
	converted := make(map[string]oagmsg.XAINamespaceToolRef, len(refs))
	for key, ref := range refs {
		converted[key] = oagmsg.XAINamespaceToolRef{Namespace: ref.namespace, Name: ref.name, IsDispatcher: ref.isDispatcher}
	}
	return &xaiNamespaceRestorer{inner: oagmsg.NewXAIResponsesNamespaceRestorer(converted)}
}

func (r *xaiNamespaceRestorer) restore(data []byte) []byte {
	return r.inner.Restore(data)
}

func clampXAIToolsLimit(body []byte, maxTools int, refs map[string]xaiNamespaceToolRef) []byte {
	converted := make(map[string]oagmsg.XAINamespaceToolRef, len(refs))
	for key, ref := range refs {
		converted[key] = oagmsg.XAINamespaceToolRef{Namespace: ref.namespace, Name: ref.name, IsDispatcher: ref.isDispatcher}
	}
	return oagmsg.ClampXAIResponsesToolsLimit(body, maxTools, converted)
}

func collectXAIClientDeclaredToolKeys(body []byte) map[xaiClientToolKey]struct{} {
	keys := oagmsg.CollectXAIClientDeclaredToolKeys(body)
	converted := make(map[xaiClientToolKey]struct{}, len(keys))
	for key := range keys {
		converted[xaiClientToolKey{namespace: key.Namespace, name: key.Name, toolType: key.ToolType}] = struct{}{}
	}
	return converted
}

func xaiIsInternalXSearchCall(item gjson.Result, keys map[xaiClientToolKey]struct{}) bool {
	converted := make(map[oagmsg.XAIClientToolKey]struct{}, len(keys))
	for key := range keys {
		converted[oagmsg.XAIClientToolKey{Namespace: key.namespace, Name: key.name, ToolType: key.toolType}] = struct{}{}
	}
	return oagmsg.XAIIsInternalXSearchCall(item, converted)
}

func newXAIInternalXSearchResponseFilter(enabled bool, keys map[xaiClientToolKey]struct{}) *xaiInternalXSearchResponseFilter {
	converted := make(map[oagmsg.XAIClientToolKey]struct{}, len(keys))
	for key := range keys {
		converted[oagmsg.XAIClientToolKey{Namespace: key.namespace, Name: key.name, ToolType: key.toolType}] = struct{}{}
	}
	return &xaiInternalXSearchResponseFilter{inner: oagmsg.NewXAIResponsesResponseFilter(enabled, converted)}
}

func (f *xaiInternalXSearchResponseFilter) apply(eventData []byte) []byte {
	return f.inner.Apply(eventData)
}

func xaiRequestHasNativeXSearch(body []byte) bool {
	return oagmsg.XAIResponsesRequestHasNativeXSearch(body)
}

func xaiFunctionParametersNeedSimplification(tool gjson.Result, namespaceName string) bool {
	return oagmsg.XAIFunctionParametersNeedSimplification(tool, namespaceName)
}
