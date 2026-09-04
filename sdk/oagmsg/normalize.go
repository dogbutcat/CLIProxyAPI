package oagmsg

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
)

const (
	cacheAssistantPlaceholder = "__ASSISTANT__"
	cacheFingerprintPrefix    = "fp:"
)

// PrefixFingerprint contains the cache-affinity keys derived from a request.
type PrefixFingerprint struct {
	FingerprintID       string
	CacheID             string
	TailID              string
	CacheIDAfterSuccess string
}

// ComputePrefixFingerprint derives all cache-affinity keys for a request.
func ComputePrefixFingerprint(format Format, payload []byte, tailK int) PrefixFingerprint {
	return ComputePrefixFingerprintFromMessages(parseMessagesForCache(format, payload), tailK)
}

// ComputePrefixFingerprintFromMessages derives all cache-affinity keys from unified messages.
func ComputePrefixFingerprintFromMessages(messages []OagMessage, tailK int) PrefixFingerprint {
	if tailK <= 0 {
		tailK = 8
	}
	fp := PrefixFingerprint{
		FingerprintID: ComputeConversationFingerprintFromMessages(messages),
	}
	if len(messages) > 1 {
		prefix := messages[:len(messages)-1]
		if normalized := NormalizeForCacheHash(prefix); normalized != "" {
			fp.CacheID = HashNormalized(normalized)
		}
		if len(prefix) >= tailK {
			tail := prefix[len(prefix)-tailK:]
			if normalized := NormalizeForCacheHash(tail); normalized != "" {
				fp.TailID = fmt.Sprintf("tail%d:%s", tailK, HashNormalized(normalized))
			}
		}
	}
	if normalized := NormalizeForCacheHash(appendAssistantPlaceholder(messages)); normalized != "" {
		fp.CacheIDAfterSuccess = HashNormalized(normalized)
	}
	return fp
}

// ComputeFingerprint extracts a stable conversation fingerprint from the first
// relevant user turn. System/developer/instruction content is intentionally
// excluded because clients inject provider-specific approved instructions.
func ComputeFingerprint(format Format, payload []byte) string {
	return ComputeConversationFingerprintFromMessages(parseMessagesForCache(format, payload))
}

// ComputeConversationFingerprintFromMessages hashes only the first user message.
func ComputeConversationFingerprintFromMessages(messages []OagMessage) string {
	for _, msg := range messages {
		if strings.EqualFold(strings.TrimSpace(msg.Role), "user") {
			if normalized := NormalizeForCacheHash([]OagMessage{msg}); normalized != "" {
				return cacheFingerprintPrefix + HashNormalized(normalized)
			}
			return ""
		}
	}
	return ""
}

// ComputeCacheID hashes the normalized request prefix, excluding the final message.
func ComputeCacheID(format Format, payload []byte) string {
	messages := parseMessagesForCache(format, payload)
	if len(messages) <= 1 {
		return ""
	}
	normalized := NormalizeForCacheHash(messages[:len(messages)-1])
	if normalized == "" {
		return ""
	}
	return HashNormalized(normalized)
}

// ComputeTailHash hashes the last tailK messages in the request prefix.
func ComputeTailHash(format Format, payload []byte, tailK int) string {
	messages := parseMessagesForCache(format, payload)
	if len(messages) <= 1 || tailK <= 0 {
		return ""
	}
	prefix := messages[:len(messages)-1]
	if len(prefix) < tailK {
		return ""
	}
	normalized := NormalizeForCacheHash(prefix[len(prefix)-tailK:])
	if normalized == "" {
		return ""
	}
	return fmt.Sprintf("tail%d:%s", tailK, HashNormalized(normalized))
}

func parseMessagesForCache(format Format, payload []byte) []OagMessage {
	if len(payload) == 0 {
		return nil
	}
	if format == "" {
		format = FormatOpenAI
	}
	handler, ok := DefaultRegistry().Get(resolveFormat(format))
	if !ok {
		return nil
	}
	messages, err := handler.ParseMessages(payload)
	if err != nil {
		return nil
	}
	return messages
}

// NormalizeForCacheHash produces a deterministic string for cache affinity.
func NormalizeForCacheHash(messages []OagMessage) string {
	if len(messages) == 0 {
		return ""
	}
	normalized := make([]cacheMessage, 0, len(messages))
	for _, msg := range messages {
		role := strings.ToLower(strings.TrimSpace(msg.Role))
		if role == "" {
			continue
		}
		normalized = append(normalized, cacheMessage{
			Role:    role,
			Content: normalizeCacheBlocks(role, msg.Content),
		})
	}
	if len(normalized) == 0 {
		return ""
	}
	data, err := json.Marshal(normalized)
	if err != nil {
		return ""
	}
	return string(data)
}

// NormalizeForHash produces a deterministic string from a full message list.
func NormalizeForHash(messages []OagMessage) string {
	return NormalizeForCacheHash(messages)
}

// NormalizeTail normalizes the last k messages for tail matching.
func NormalizeTail(messages []OagMessage, k int) string {
	if k <= 0 {
		return ""
	}
	if k >= len(messages) {
		return NormalizeForCacheHash(messages)
	}
	return NormalizeForCacheHash(messages[len(messages)-k:])
}

// HashNormalized returns the SHA-256 hex digest of a normalized string.
func HashNormalized(normalized string) string {
	sum := sha256.Sum256([]byte(normalized))
	return hex.EncodeToString(sum[:])
}

type cacheMessage struct {
	Role    string       `json:"role"`
	Content []cacheBlock `json:"content"`
}

type cacheBlock struct {
	Type      string `json:"type"`
	Text      string `json:"text,omitempty"`
	ID        string `json:"id,omitempty"`
	Name      string `json:"name,omitempty"`
	Input     string `json:"input,omitempty"`
	MediaType string `json:"media_type,omitempty"`
	Size      int    `json:"size,omitempty"`
	URL       string `json:"url,omitempty"`
	Filename  string `json:"filename,omitempty"`
	IsError   bool   `json:"is_error,omitempty"`
}

func normalizeCacheBlocks(role string, blocks []ContentBlock) []cacheBlock {
	if role == "assistant" {
		out := []cacheBlock{{Type: "assistant", Text: cacheAssistantPlaceholder}}
		for _, block := range blocks {
			if toolUse, ok := block.(ToolUseBlock); ok {
				out = append(out, cacheBlock{
					Type:  "tool_use",
					ID:    toolUse.ID,
					Name:  toolUse.Name,
					Input: stableMapString(toolUse.Input),
				})
			}
		}
		return out
	}

	out := make([]cacheBlock, 0, len(blocks))
	for _, block := range blocks {
		switch typed := block.(type) {
		case TextBlock:
			out = append(out, cacheBlock{Type: "text", Text: typed.Text})
		case ToolResultBlock:
			out = append(out, cacheBlock{
				Type:    "tool_result",
				ID:      typed.ToolUseID,
				Text:    toolResultContentString(typed.Content),
				IsError: typed.IsError,
			})
		case ToolUseBlock:
			out = append(out, cacheBlock{
				Type:  "tool_use",
				ID:    typed.ID,
				Name:  typed.Name,
				Input: stableMapString(typed.Input),
			})
		case ImageBlock:
			if typed.URL != "" {
				out = append(out, cacheBlock{Type: "image", URL: typed.URL})
			} else {
				out = append(out, cacheBlock{Type: "image", MediaType: typed.MediaType, Size: len(typed.Data)})
			}
		case FileBlock:
			if typed.URL != "" {
				out = append(out, cacheBlock{Type: "file", Filename: typed.Filename, URL: typed.URL})
			} else {
				out = append(out, cacheBlock{Type: "file", Filename: typed.Filename, MediaType: typed.MediaType, Size: len(typed.Data)})
			}
		}
	}
	return out
}

func appendAssistantPlaceholder(messages []OagMessage) []OagMessage {
	out := make([]OagMessage, 0, len(messages)+1)
	out = append(out, messages...)
	out = append(out, OagMessage{Role: "assistant", Content: []ContentBlock{TextBlock{Text: cacheAssistantPlaceholder}}})
	return out
}

func toolResultContentString(content any) string {
	switch typed := content.(type) {
	case string:
		return typed
	case nil:
		return ""
	default:
		data, err := json.Marshal(typed)
		if err != nil {
			return fmt.Sprint(typed)
		}
		return string(data)
	}
}

func stableMapString(values map[string]any) string {
	if len(values) == 0 {
		return "{}"
	}
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	parts := make([]string, 0, len(keys))
	for _, key := range keys {
		parts = append(parts, fmt.Sprintf("%s=%s", key, stableValueString(values[key])))
	}
	return "{" + strings.Join(parts, ",") + "}"
}

func stableValueString(value any) string {
	data, err := json.Marshal(value)
	if err != nil {
		return fmt.Sprint(value)
	}
	return string(data)
}
