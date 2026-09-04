package oagmsg

import (
	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

// Request and response structural keys represent protocol-specific payload
// shape and must not be merged across formats.
var requestStructuralKeys = map[string]bool{
	"messages":              true, // OpenAI
	"contents":              true, // Gemini
	"input":                 true, // Interactions/Responses API
	"system":                true, // Anthropic system prompt (handled by ParseRequest)
	"systemInstruction":     true, // Gemini system prompt (handled by ParseRequest)
	"instructions":          true, // Responses API system prompt (handled by ParseRequest)
	"generationConfig":      true, // Gemini generation config (handled by ParseRequest)
	"stream":                true, // Streaming is written according to the target protocol.
	"thinking":              true, // Anthropic thinking config (handled canonically).
	"reasoning":             true, // Responses/Codex reasoning config (handled canonically).
	"reasoning_effort":      true, // OpenAI reasoning config (handled canonically).
	"output_config":         true, // Anthropic adaptive thinking config.
	"max_tokens":            true,
	"max_completion_tokens": true,
	"max_output_tokens":     true,
	"temperature":           true,
	"top_p":                 true,
	"stop":                  true,
	"tools":                 true,
	"tool_choice":           true,
	"response_format":       true,
	"text":                  true,
}

var responseStructuralKeys = map[string]bool{
	"id":            true,
	"model":         true,
	"created":       true,
	"object":        true,
	"type":          true,
	"role":          true,
	"status":        true,
	"choices":       true, // OpenAI chat completions
	"content":       true, // Anthropic message content
	"output":        true, // Responses API output
	"response":      true, // Antigravity and Responses event envelope
	"candidates":    true, // Gemini candidates
	"usage":         true, // OpenAI/Anthropic/Responses usage
	"usageMetadata": true, // Gemini usage
	"stop_reason":   true,
	"stop_sequence": true,
	"finishReason":  true,
}

// preserveUnknownFields merges top-level fields from sourceJSON that are
// absent in targetJSON. This preserves provider-specific fields (e.g.
// presence_penalty, frequency_penalty, seed, logprobs, logit_bias, user,
// parallel_tool_calls) that UnifiedRequest doesn't explicitly model.
//
// Structural keys (messages, contents, input, system) are never merged
// because they are protocol-specific and already handled by the handler.
//
// This function is the SINGLE place where field preservation happens.
// Individual handlers do NOT need to handle RawExtra.
func preserveUnknownFields(sourceJSON, targetJSON []byte) []byte {
	return preserveUnknownFieldsForSource("", sourceJSON, targetJSON)
}

func preserveUnknownFieldsForSource(sourceFormat Format, sourceJSON, targetJSON []byte) []byte {
	structuralKeys := requestStructuralKeys
	if shouldSuppressParallelToolCallsPreserve(sourceFormat, sourceJSON) {
		structuralKeys = cloneStructuralKeys(requestStructuralKeys)
		structuralKeys["parallel_tool_calls"] = true
	}
	return preserveUnknownFieldsWithStructural(sourceJSON, targetJSON, structuralKeys)
}

func preserveUnknownResponseFields(sourceJSON, targetJSON []byte) []byte {
	return preserveUnknownFieldsWithStructural(sourceJSON, targetJSON, responseStructuralKeys)
}

func preserveUnknownFieldsWithStructural(sourceJSON, targetJSON []byte, structuralKeys map[string]bool) []byte {
	result := targetJSON

	// Collect keys already present in target.
	targetKeys := make(map[string]bool)
	gjson.ParseBytes(targetJSON).ForEach(func(key, _ gjson.Result) bool {
		targetKeys[key.String()] = true
		return true
	})

	// Merge source keys that are missing from target and not structural.
	gjson.ParseBytes(sourceJSON).ForEach(func(key, value gjson.Result) bool {
		k := key.String()
		if targetKeys[k] || structuralKeys[k] {
			return true
		}
		if updated, err := sjson.SetRawBytes(result, k, []byte(value.Raw)); err == nil {
			result = updated
		}
		return true
	})

	return result
}

func shouldSuppressParallelToolCallsPreserve(sourceFormat Format, sourceJSON []byte) bool {
	if sourceFormat != FormatOpenAIResponse && sourceFormat != FormatCodex {
		return false
	}
	return gjson.ParseBytes(sourceJSON).Get("parallel_tool_calls").Exists()
}

func cloneStructuralKeys(keys map[string]bool) map[string]bool {
	clone := make(map[string]bool, len(keys)+1)
	for key, value := range keys {
		clone[key] = value
	}
	return clone
}
