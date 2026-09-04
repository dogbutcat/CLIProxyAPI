package oagmsg

import (
	"encoding/json"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/util"
	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

// codexRejectedFields lists all top-level request fields that the Codex
// upstream rejects with parameter-error responses. These must be stripped
// before forwarding, regardless of source protocol.
var codexRejectedFields = []string{
	"max_output_tokens",
	"max_completion_tokens",
	"temperature",
	"top_p",
	"truncation",
	"prompt_cache_options",
	"prompt_cache_retention",
	"user",
	"context_management",
}

// FinalizeCodexRequest enforces all Codex upstream constraints on a serialized
// request body. It must be called as the last transformation step, AFTER
// unknown-field preservation, so that rejected source fields cannot survive.
//
// Rules audited from deployed Codex request finalization behavior:
//  1. store = false
//  2. stream = true (executor always streams upstream)
//  3. parallel_tool_calls = true
//  4. include = ["reasoning.encrypted_content"]
//  5. Delete rejected fields (max_output_tokens, temperature, top_p, etc.)
//  6. service_tier: keep only "priority", delete others
//  7. Normalize builtin tools (web_search_preview* → web_search)
//  8. String input → single user input-message array
//  9. Strip nested prompt_cache_breakpoint from input content items
//  10. Downgrade strict JSON schema when optional properties exist
//
// 11. system→developer role conversion in input array
func FinalizeCodexRequest(body []byte) []byte {
	// Required boolean fields.
	body = setCodexBool(body, "store", false)
	body = setCodexBool(body, "stream", true)
	body = setCodexBool(body, "parallel_tool_calls", true)

	// Required include array.
	body = setCodexInclude(body)

	// Strip rejected fields.
	for _, field := range codexRejectedFields {
		if gjson.GetBytes(body, field).Exists() {
			if updated, err := sjson.DeleteBytes(body, field); err == nil {
				body = updated
			}
		}
	}

	// Conditional service_tier: keep only "priority".
	if st := gjson.GetBytes(body, "service_tier"); st.Exists() && st.String() != "priority" {
		if updated, err := sjson.DeleteBytes(body, "service_tier"); err == nil {
			body = updated
		}
	}

	// Normalize builtin tools.
	body = normalizeCodexBuiltinTools(body)

	// Normalize shorthand string input before role validation.
	body = normalizeCodexStringInput(body)

	// Strip nested cache breakpoints unsupported by Codex upstream.
	body = stripCodexNestedPromptCacheBreakpoints(body)

	// Downgrade unsatisfiable strict JSON schemas before upstream validation.
	body = downgradeCodexStrictJSONSchema(body)

	// Convert system role to developer in input array.
	body = convertCodexSystemToDeveloper(body)

	return body
}

func finalizeCodexRequestForTarget(target Format, body []byte) []byte {
	if resolveFormat(target) != FormatCodex {
		return body
	}
	return FinalizeCodexRequest(body)
}

func codexRequestAlreadyFinalized(body []byte) bool {
	root := util.ParseGJSONBytesNoCopy(body)
	if !root.IsObject() {
		return false
	}
	if root.Get("store").Type != gjson.False {
		return false
	}
	if root.Get("stream").Type != gjson.True {
		return false
	}
	if root.Get("parallel_tool_calls").Type != gjson.True {
		return false
	}
	if !codexIncludeAlreadyFinalized(root.Get("include")) {
		return false
	}
	for _, field := range codexRejectedFields {
		if root.Get(field).Exists() {
			return false
		}
	}
	if serviceTier := root.Get("service_tier"); serviceTier.Exists() && serviceTier.String() != "priority" {
		return false
	}
	if root.Get("input").Type == gjson.String {
		return false
	}
	if codexInputHasNestedPromptCacheBreakpoint(root.Get("input")) {
		return false
	}
	if codexInputHasSystemRole(root.Get("input")) {
		return false
	}
	if codexStrictJSONSchemaNeedsDowngrade(root) {
		return false
	}
	return !codexBuiltinToolsNeedNormalization(root)
}

func downgradeCodexStrictJSONSchema(body []byte) []byte {
	root := util.ParseGJSONBytesNoCopy(body)
	if !codexStrictJSONSchemaNeedsDowngrade(root) {
		return body
	}
	if updated, err := sjson.SetBytes(body, "text.format.strict", false); err == nil {
		return updated
	}
	return body
}

func codexStrictJSONSchemaNeedsDowngrade(root gjson.Result) bool {
	format := root.Get("text.format")
	if !format.IsObject() || format.Get("type").String() != "json_schema" || !format.Get("strict").Bool() {
		return false
	}
	schema := format.Get("schema")
	if !schema.Exists() {
		schema = format.Get("json_schema.schema")
	}
	return codexJSONSchemaMissesRequired(schema)
}

var codexJSONSchemaMapKeywords = [...]string{
	"properties",
	"$defs",
	"definitions",
	"patternProperties",
	"dependentSchemas",
	"dependencies",
}

var codexJSONSchemaValueKeywords = [...]string{
	"items",
	"prefixItems",
	"contains",
	"additionalProperties",
	"propertyNames",
	"unevaluatedProperties",
	"unevaluatedItems",
	"additionalItems",
	"contentSchema",
	"anyOf",
	"oneOf",
	"allOf",
	"not",
	"if",
	"then",
	"else",
}

func codexJSONSchemaMissesRequired(schema gjson.Result) bool {
	if schema.IsArray() {
		for _, child := range schema.Array() {
			if codexJSONSchemaMissesRequired(child) {
				return true
			}
		}
		return false
	}
	if !schema.IsObject() {
		return false
	}
	if properties := schema.Get("properties"); properties.IsObject() {
		required := schema.Get("required")
		if !required.IsArray() {
			return len(properties.Map()) > 0
		}
		requiredNames := make(map[string]struct{}, len(required.Array()))
		for _, item := range required.Array() {
			if item.Type == gjson.String {
				requiredNames[item.String()] = struct{}{}
			}
		}
		for name := range properties.Map() {
			if _, ok := requiredNames[name]; !ok {
				return true
			}
		}
	}
	for _, keyword := range codexJSONSchemaMapKeywords {
		children := schema.Get(keyword)
		if !children.IsObject() {
			continue
		}
		for _, child := range children.Map() {
			if codexJSONSchemaMissesRequired(child) {
				return true
			}
		}
	}
	for _, keyword := range codexJSONSchemaValueKeywords {
		if child := schema.Get(keyword); child.Exists() && codexJSONSchemaMissesRequired(child) {
			return true
		}
	}
	return false
}

func codexInputHasNestedPromptCacheBreakpoint(input gjson.Result) bool {
	if !input.IsArray() {
		return false
	}
	found := false
	input.ForEach(func(_, item gjson.Result) bool {
		content := item.Get("content")
		if !content.IsArray() {
			return true
		}
		content.ForEach(func(_, part gjson.Result) bool {
			if part.Get("prompt_cache_breakpoint").Exists() {
				found = true
				return false
			}
			return true
		})
		return !found
	})
	return found
}

func codexIncludeAlreadyFinalized(include gjson.Result) bool {
	values := include.Array()
	return include.IsArray() &&
		len(values) == 1 &&
		values[0].Type == gjson.String &&
		values[0].String() == "reasoning.encrypted_content"
}

func codexInputHasSystemRole(input gjson.Result) bool {
	if !input.IsArray() {
		return false
	}
	for _, item := range input.Array() {
		if item.IsObject() && item.Get("role").String() == "system" {
			return true
		}
	}
	return false
}

func codexBuiltinToolsNeedNormalization(root gjson.Result) bool {
	return codexToolArrayNeedsNormalization(root.Get("tools")) ||
		codexToolTypeNeedsNormalization(root.Get("tool_choice.type").String()) ||
		codexToolArrayNeedsNormalization(root.Get("tool_choice.tools"))
}

func codexToolArrayNeedsNormalization(tools gjson.Result) bool {
	if !tools.IsArray() {
		return false
	}
	needsNormalization := false
	tools.ForEach(func(_, tool gjson.Result) bool {
		if codexToolTypeNeedsNormalization(tool.Get("type").String()) {
			needsNormalization = true
			return false
		}
		return true
	})
	return needsNormalization
}

func codexToolTypeNeedsNormalization(toolType string) bool {
	return normalizeCodexBuiltinToolType(toolType) != ""
}

// setCodexBool sets a boolean field, skipping if already correct.
func setCodexBool(body []byte, path string, value bool) []byte {
	current := util.GetGJSONBytesNoCopy(body, path)
	if value && current.Type == gjson.True || !value && current.Type == gjson.False {
		return body
	}
	updated, err := sjson.SetBytes(body, path, value)
	if err != nil {
		return body
	}
	return updated
}

// setCodexInclude ensures include is exactly ["reasoning.encrypted_content"].
func setCodexInclude(body []byte) []byte {
	current := util.GetGJSONBytesNoCopy(body, "include")
	values := current.Array()
	if current.IsArray() && len(values) == 1 &&
		values[0].Type == gjson.String &&
		values[0].String() == "reasoning.encrypted_content" {
		return body
	}
	updated, err := sjson.SetRawBytes(body, "include", []byte(`["reasoning.encrypted_content"]`))
	if err != nil {
		return body
	}
	return updated
}

// normalizeCodexBuiltinTools rewrites legacy builtin tool type variants
// to the stable names expected by Codex upstream.
func normalizeCodexBuiltinTools(body []byte) []byte {
	body = normalizeCodexToolArray(body, "tools")
	body = normalizeCodexToolAtPath(body, "tool_choice.type")
	return normalizeCodexToolArray(body, "tool_choice.tools")
}

func normalizeCodexToolArray(body []byte, path string) []byte {
	tools := util.GetGJSONBytesNoCopy(body, path)
	if !tools.IsArray() {
		return body
	}
	changed := false
	var items [][]byte
	tools.ForEach(func(_, tool gjson.Result) bool {
		item := []byte(tool.Raw)
		currentType := tool.Get("type").String()
		if normalized := normalizeCodexBuiltinToolType(currentType); normalized != "" {
			if updated, err := sjson.SetBytes(item, "type", normalized); err == nil {
				item = updated
				changed = true
			}
		}
		items = append(items, item)
		return true
	})
	if !changed {
		return body
	}
	joined := joinRawArray(items)
	if updated, err := sjson.SetRawBytes(body, path, joined); err == nil {
		return updated
	}
	return body
}

func normalizeCodexToolAtPath(body []byte, path string) []byte {
	currentType := util.GetGJSONBytesNoCopy(body, path).String()
	if normalized := normalizeCodexBuiltinToolType(currentType); normalized != "" {
		if updated, err := sjson.SetBytes(body, path, normalized); err == nil {
			return updated
		}
	}
	return body
}

// normalizeCodexBuiltinToolType maps legacy/preview tool type names to their
// current stable equivalents. Returns empty string if no normalization needed.
func normalizeCodexBuiltinToolType(toolType string) string {
	switch toolType {
	case "web_search_preview", "web_search_preview_2025_03_11":
		return "web_search"
	default:
		return ""
	}
}

// normalizeCodexStringInput converts the public Codex shorthand
// {"input":"..."} into the input-message array accepted by Codex upstream.
func normalizeCodexStringInput(body []byte) []byte {
	input := util.GetGJSONBytesNoCopy(body, "input")
	if input.Type != gjson.String {
		return body
	}
	item := map[string]any{
		"type": "message",
		"role": "user",
		"content": []any{
			map[string]any{
				"type": "input_text",
				"text": input.String(),
			},
		},
	}
	inputRaw, err := json.Marshal([]any{item})
	if err != nil {
		return body
	}
	if updated, err := sjson.SetRawBytes(body, "input", inputRaw); err == nil {
		return updated
	}
	return body
}

func stripCodexNestedPromptCacheBreakpoints(body []byte) []byte {
	inputResult := util.GetGJSONBytesNoCopy(body, "input")
	if !inputResult.IsArray() {
		return body
	}
	items := inputResult.Array()
	if len(items) == 0 {
		return body
	}
	changed := false
	rebuilt := make([][]byte, 0, len(items))
	for _, item := range items {
		raw := []byte(item.Raw)
		content := item.Get("content")
		if item.IsObject() && content.IsArray() {
			parts := content.Array()
			partChanged := false
			rebuiltParts := make([][]byte, 0, len(parts))
			for _, part := range parts {
				partRaw := []byte(part.Raw)
				if part.IsObject() && part.Get("prompt_cache_breakpoint").Exists() {
					if updated, err := sjson.DeleteBytes(partRaw, "prompt_cache_breakpoint"); err == nil {
						partRaw = updated
						partChanged = true
					}
				}
				rebuiltParts = append(rebuiltParts, partRaw)
			}
			if partChanged {
				if updated, err := sjson.SetRawBytes(raw, "content", joinRawArray(rebuiltParts)); err == nil {
					raw = updated
					changed = true
				}
			}
		}
		rebuilt = append(rebuilt, raw)
	}
	if !changed {
		return body
	}
	if updated, err := sjson.SetRawBytes(body, "input", joinRawArray(rebuilt)); err == nil {
		return updated
	}
	return body
}

// convertCodexSystemToDeveloper converts any input items with role "system"
// to role "developer". Codex API does not accept "system" role in input.
func convertCodexSystemToDeveloper(body []byte) []byte {
	inputResult := util.GetGJSONBytesNoCopy(body, "input")
	if !inputResult.IsArray() {
		return body
	}
	items := inputResult.Array()
	if len(items) == 0 {
		return body
	}
	hasSystem := false
	for _, item := range items {
		if item.IsObject() && item.Get("role").String() == "system" {
			hasSystem = true
			break
		}
	}
	if !hasSystem {
		return body
	}
	changed := false
	rebuilt := make([]json.RawMessage, 0, len(items))
	for _, item := range items {
		raw := []byte(item.Raw)
		if item.IsObject() && item.Get("role").String() == "system" {
			if updated, err := sjson.SetRawBytes(raw, "role", []byte(`"developer"`)); err == nil {
				raw = updated
				changed = true
			}
		}
		rebuilt = append(rebuilt, json.RawMessage(raw))
	}
	if !changed {
		return body
	}
	inputRaw, err := json.Marshal(rebuilt)
	if err != nil {
		return body
	}
	if updated, err := sjson.SetRawBytes(body, "input", inputRaw); err == nil {
		return updated
	}
	return body
}

// joinRawArray joins pre-serialized JSON items into a JSON array.
func joinRawArray(items [][]byte) []byte {
	if len(items) == 0 {
		return []byte("[]")
	}
	size := 2 // [ ]
	for i, item := range items {
		size += len(item)
		if i > 0 {
			size++ // ,
		}
	}
	out := make([]byte, 0, size)
	out = append(out, '[')
	for i, item := range items {
		if i > 0 {
			out = append(out, ',')
		}
		out = append(out, item...)
	}
	out = append(out, ']')
	return out
}
