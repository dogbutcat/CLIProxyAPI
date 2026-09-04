package oagmsg

import (
	"encoding/base64"
	"encoding/json"
	"strings"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/cache"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/signature"
	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

const (
	geminiClaudeCarrierPrefix     = "cpa-gemini-carrier-v1:"
	geminiClaudeCarrierNext       = "next"
	geminiClaudeCarrierPrevious   = "previous"
	geminiClaudeCarrierStandalone = "standalone"
	geminiClaudeCarrierText       = "text"
	geminiClaudeCarrierFunction   = "function"
	geminiClaudeCarrierAny        = "any"
)

// StripEmptySignatureThinkingBlocks removes thinking blocks that do not carry
// a valid Claude thinking signature.
func StripEmptySignatureThinkingBlocks(payload []byte) []byte {
	return signature.StripInvalidClaudeThinkingBlocks(payload, signature.ClaudeSignatureValidationOptions{PrefixOnly: true})
}

// StripInvalidGeminiSignatureThinkingBlocks preserves only replayable Gemini
// signatures carried through a Claude request.
func StripInvalidGeminiSignatureThinkingBlocks(payload []byte) []byte {
	messages := gjson.GetBytes(payload, "messages")
	if !messages.IsArray() {
		return payload
	}

	changed := false
	messageItems := make([]json.RawMessage, 0, len(messages.Array()))
	for _, message := range messages.Array() {
		messageJSON := []byte(message.Raw)
		content := message.Get("content")
		if !content.IsArray() {
			messageItems = append(messageItems, json.RawMessage(messageJSON))
			continue
		}

		contentChanged := false
		assistantMessage := strings.EqualFold(message.Get("role").String(), "assistant")
		contentBlocks := content.Array()
		contentItems := make([]json.RawMessage, 0, len(contentBlocks))
		pendingCarrierTargetKind := ""
		for blockIndex, block := range contentBlocks {
			if block.Get("type").String() != "thinking" {
				pendingCarrierTargetKind = ""
				contentItems = append(contentItems, json.RawMessage(block.Raw))
				continue
			}

			rawSignature := strings.TrimSpace(block.Get("signature").String())
			thinkingText := strings.TrimSpace(block.Get("thinking").String())
			if rawSignature == "" && thinkingText != "" && (pendingCarrierTargetKind == geminiClaudeCarrierAny || pendingCarrierTargetKind == geminiClaudeCarrierText) {
				pendingCarrierTargetKind = ""
				contentItems = append(contentItems, json.RawMessage(block.Raw))
				continue
			}

			innerSignature, direction, targetKind, marked, validCarrier := decodeGeminiClaudeCarrierSignature(rawSignature)
			blockKind := signature.SignatureBlockKindGeminiModelPart
			if marked && targetKind == geminiClaudeCarrierFunction {
				blockKind = signature.SignatureBlockKindGeminiFunctionCall
			}
			invalidPlacement := false
			if marked {
				switch direction {
				case geminiClaudeCarrierNext, geminiClaudeCarrierPrevious:
					invalidPlacement = !geminiClaudeCarrierMatchesAdjacent(contentBlocks, blockIndex, direction, targetKind)
				case geminiClaudeCarrierStandalone:
					invalidPlacement = thinkingText != "" && targetKind == geminiClaudeCarrierFunction
				}
				if thinkingText != "" && direction == geminiClaudeCarrierPrevious {
					invalidPlacement = true
				}
			}
			if !validCarrier || !assistantMessage || invalidPlacement {
				pendingCarrierTargetKind = ""
				contentChanged = true
				continue
			}
			if !marked {
				innerSignature = rawSignature
			}
			if _, ok := signature.CompatibleSignatureForProviderBlock(signature.SignatureProviderGemini, innerSignature, blockKind); !ok {
				pendingCarrierTargetKind = ""
				contentChanged = true
				continue
			}
			if marked && direction == geminiClaudeCarrierNext {
				pendingCarrierTargetKind = targetKind
			} else {
				pendingCarrierTargetKind = ""
			}
			contentItems = append(contentItems, json.RawMessage(block.Raw))
		}

		if contentChanged {
			encoded, errMarshal := json.Marshal(contentItems)
			if errMarshal == nil {
				messageJSON, _ = sjson.SetRawBytes(messageJSON, "content", encoded)
				changed = true
			}
		}
		messageItems = append(messageItems, json.RawMessage(messageJSON))
	}
	if !changed {
		return payload
	}
	encoded, errMarshal := json.Marshal(messageItems)
	if errMarshal != nil {
		return payload
	}
	updated, errSet := sjson.SetRawBytes(payload, "messages", encoded)
	if errSet != nil {
		return payload
	}
	return updated
}

// StripInvalidBypassSignatureThinkingBlocks removes invalid Claude signatures
// when strict signature bypass validation is enabled.
func StripInvalidBypassSignatureThinkingBlocks(payload []byte) []byte {
	return signature.StripInvalidClaudeThinkingBlocks(payload, signature.ClaudeSignatureValidationOptions{Strict: cache.SignatureBypassStrictMode()})
}

func decodeGeminiClaudeCarrierSignature(rawSignature string) (signatureValue, direction, targetKind string, marked, ok bool) {
	rawSignature = strings.TrimSpace(rawSignature)
	if !strings.HasPrefix(rawSignature, geminiClaudeCarrierPrefix) {
		return rawSignature, "", "", false, true
	}
	marked = true
	if len(rawSignature) > (signature.MaxGeminiThoughtSignatureLen*4/3)+1024 {
		return "", "", "", true, false
	}
	fields := strings.SplitN(strings.TrimPrefix(rawSignature, geminiClaudeCarrierPrefix), ":", 3)
	if len(fields) != 3 {
		return "", "", "", true, false
	}
	direction, targetKind = fields[0], fields[1]
	switch direction {
	case geminiClaudeCarrierNext, geminiClaudeCarrierPrevious, geminiClaudeCarrierStandalone:
	default:
		return "", "", "", true, false
	}
	switch targetKind {
	case geminiClaudeCarrierText, geminiClaudeCarrierFunction, geminiClaudeCarrierAny:
	default:
		return "", "", "", true, false
	}
	decoded, errDecode := base64.RawStdEncoding.DecodeString(fields[2])
	if errDecode != nil || len(decoded) == 0 || strings.HasPrefix(string(decoded), geminiClaudeCarrierPrefix) {
		return "", "", "", true, false
	}
	blockKind := signature.SignatureBlockKindGeminiModelPart
	if targetKind == geminiClaudeCarrierFunction {
		blockKind = signature.SignatureBlockKindGeminiFunctionCall
	}
	normalized, compatible := signature.CompatibleSignatureForProviderBlock(signature.SignatureProviderGemini, string(decoded), blockKind)
	if !compatible || signature.IsGeminiThoughtSignatureBypass(signature.SignaturePayloadWithoutProviderPrefix(normalized)) {
		return "", "", "", true, false
	}
	return normalized, direction, targetKind, true, true
}

func geminiClaudeCarrierMatchesAdjacent(blocks []gjson.Result, index int, direction, targetKind string) bool {
	step := 1
	if direction == geminiClaudeCarrierPrevious {
		step = -1
	}
	for adjacent := index + step; adjacent >= 0 && adjacent < len(blocks); adjacent += step {
		if kind := geminiClaudeSemanticTargetKind(blocks[adjacent]); kind != "" {
			return targetKind == geminiClaudeCarrierAny || targetKind == kind
		}
		if blocks[adjacent].Get("type").String() != "thinking" || strings.TrimSpace(blocks[adjacent].Get("thinking").String()) != "" {
			return false
		}
	}
	return false
}

func geminiClaudeSemanticTargetKind(block gjson.Result) string {
	switch block.Get("type").String() {
	case "text":
		return geminiClaudeCarrierText
	case "tool_use":
		return geminiClaudeCarrierFunction
	case "thinking":
		if strings.TrimSpace(block.Get("thinking").String()) != "" {
			return geminiClaudeCarrierText
		}
	}
	return ""
}
