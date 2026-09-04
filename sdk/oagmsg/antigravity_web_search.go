package oagmsg

import (
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/registry"
	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

const antigravityWebSearchSystemInstruction = "You are a search engine bot. You will be given a query from a user. Your task is to search the web for relevant information that will help the user. You MUST perform a web search. Do not respond or interact with the user, please respond as if they typed the query into a search bar."

type antigravityWebSearchGroundingSupport struct {
	StartIndex int64
	EndIndex   int64
	Text       string
	ChunkURLs  []string
	ChunkTitle string
}

type antigravityWebSearchStreamState struct {
	textBuffer strings.Builder
	hasSearch  bool
}

func newAnthropicWebSearchRequestMetadata(payload []byte) *anthropicWebSearchRequestMetadata {
	return &anthropicWebSearchRequestMetadata{
		onlyTypedSearchTools: hasOnlyClaudeTypedWebSearchTools(payload),
		allowsToolChoice:     allowsClaudeWebSearchToolChoice(payload),
		query:                extractClaudeWebSearchQuery(payload),
		maxUses:              extractClaudeWebSearchMaxUses(payload),
		includedDomains:      extractClaudeWebSearchAllowedDomains(payload),
	}
}

func shouldBuildAntigravityWebSearchRequest(req *UnifiedRequest) bool {
	if req == nil || req.SourceFormat != FormatAnthropic {
		return false
	}
	if registry.AntigravityWebSearchModelFor(req.Model) == "" {
		return false
	}
	meta := req.anthropicWebSearch
	return meta != nil && meta.onlyTypedSearchTools && meta.allowsToolChoice
}

func buildAntigravityWebSearchRequest(req *UnifiedRequest) ([]byte, bool) {
	if !shouldBuildAntigravityWebSearchRequest(req) {
		return nil, false
	}
	meta := req.anthropicWebSearch
	out := []byte(`{"model":"","requestType":"web_search","request":{"contents":[{"role":"user","parts":[{"text":""}]}],"systemInstruction":{"role":"user","parts":[{"text":""}]},"tools":[{"googleSearch":{"enhancedContent":{"imageSearch":{"maxResultCount":5}}}}],"generationConfig":{"candidateCount":1}}}`)
	out, _ = sjson.SetBytes(out, "model", req.Model)
	out, _ = sjson.SetBytes(out, "request.contents.0.parts.0.text", meta.query)
	out, _ = sjson.SetBytes(out, "request.systemInstruction.parts.0.text", antigravityWebSearchSystemInstruction)
	out, _ = sjson.SetBytes(out, "request.tools.0.googleSearch.enhancedContent.imageSearch.maxResultCount", meta.maxUses)
	if len(meta.includedDomains) > 0 {
		if domainsJSON, err := json.Marshal(meta.includedDomains); err == nil {
			out, _ = sjson.SetRawBytes(out, "request.tools.0.googleSearch.includedDomains", domainsJSON)
		}
	}
	return out, true
}

func isClaudeTypedWebSearchToolType(toolType string) bool {
	return toolType == "web_search_20250305" || toolType == "web_search_20260209"
}

func hasClaudeTypedWebSearchTool(payload []byte) bool {
	tools := gjson.GetBytes(payload, "tools")
	if !tools.IsArray() {
		return false
	}
	for _, tool := range tools.Array() {
		if isClaudeTypedWebSearchToolType(tool.Get("type").String()) {
			return true
		}
	}
	return false
}

func hasOnlyClaudeTypedWebSearchTools(payload []byte) bool {
	tools := gjson.GetBytes(payload, "tools")
	if !tools.IsArray() {
		return false
	}
	hasWebSearch := false
	for _, tool := range tools.Array() {
		if !tool.IsObject() {
			return false
		}
		if isClaudeTypedWebSearchToolType(tool.Get("type").String()) {
			hasWebSearch = true
			continue
		}
		return false
	}
	return hasWebSearch
}

func allowsClaudeWebSearchToolChoice(payload []byte) bool {
	toolChoice := gjson.GetBytes(payload, "tool_choice")
	if !toolChoice.Exists() {
		return true
	}
	if toolChoice.Type == gjson.String {
		switch toolChoice.String() {
		case "", "auto", "any":
			return true
		default:
			return false
		}
	}
	if !toolChoice.IsObject() {
		return false
	}
	switch toolChoice.Get("type").String() {
	case "", "auto", "any":
		return true
	case "tool":
		return toolChoice.Get("name").String() == "web_search"
	default:
		return false
	}
}

func extractClaudeWebSearchMaxUses(payload []byte) int64 {
	const defaultMaxResultCount int64 = 5

	tools := gjson.GetBytes(payload, "tools")
	if !tools.IsArray() {
		return defaultMaxResultCount
	}
	for _, tool := range tools.Array() {
		if !tool.IsObject() || !isClaudeTypedWebSearchToolType(tool.Get("type").String()) {
			continue
		}
		maxUses := tool.Get("max_uses").Int()
		if maxUses > 0 {
			return maxUses
		}
	}
	return defaultMaxResultCount
}

func extractClaudeWebSearchAllowedDomains(payload []byte) []string {
	tools := gjson.GetBytes(payload, "tools")
	if !tools.IsArray() {
		return nil
	}
	for _, tool := range tools.Array() {
		if !tool.IsObject() || !isClaudeTypedWebSearchToolType(tool.Get("type").String()) {
			continue
		}
		allowedDomains := tool.Get("allowed_domains")
		if !allowedDomains.IsArray() {
			return nil
		}
		domains := make([]string, 0, len(allowedDomains.Array()))
		for _, domain := range allowedDomains.Array() {
			if domain.Type != gjson.String {
				continue
			}
			if trimmed := strings.TrimSpace(domain.String()); trimmed != "" {
				domains = append(domains, trimmed)
			}
		}
		return domains
	}
	return nil
}

func extractClaudeWebSearchQuery(payload []byte) string {
	messages := gjson.GetBytes(payload, "messages")
	if !messages.IsArray() {
		return ""
	}
	messageResults := messages.Array()
	for i := len(messageResults) - 1; i >= 0; i-- {
		message := messageResults[i]
		if role := message.Get("role").String(); role != "" && role != "user" {
			continue
		}
		if query := extractClaudeTextContent(message.Get("content")); query != "" {
			return query
		}
	}
	return ""
}

func extractClaudeTextContent(content gjson.Result) string {
	if content.Type == gjson.String {
		return strings.TrimSpace(content.String())
	}
	if !content.IsArray() {
		return ""
	}
	var b strings.Builder
	for _, part := range content.Array() {
		if text := strings.TrimSpace(part.Get("text").String()); text != "" {
			if b.Len() > 0 {
				b.WriteByte('\n')
			}
			b.WriteString(text)
		}
	}
	return strings.TrimSpace(b.String())
}

func hasAntigravityGoogleSearchTool(payload []byte) bool {
	tools := gjson.GetBytes(payload, "request.tools")
	if !tools.IsArray() {
		return false
	}
	for _, tool := range tools.Array() {
		if tool.Get("googleSearch").Exists() {
			return true
		}
	}
	return false
}

func shouldTranslateAntigravityWebSearchGrounding(ctx *TranslationContext) bool {
	return ctx != nil &&
		hasClaudeTypedWebSearchTool(ctx.OriginalRequestJSON) &&
		hasAntigravityGoogleSearchTool(ctx.translatedRequestJSON)
}

func antigravityResponseRoot(rawJSON []byte) gjson.Result {
	root := gjson.ParseBytes(rawJSON)
	if nested := root.Get("response"); nested.Exists() && nested.Get("candidates").Exists() {
		return nested
	}
	return root
}

func antigravityGroundingMetadata(root gjson.Result) gjson.Result {
	groundingMetadata := root.Get("response.candidates.0.groundingMetadata")
	if groundingMetadata.Exists() {
		return groundingMetadata
	}
	if groundingMetadata = root.Get("candidates.0.groundingMetadata"); groundingMetadata.Exists() {
		return groundingMetadata
	}
	return gjson.Result{}
}

func antigravityTextContent(root gjson.Result) string {
	var textBuilder strings.Builder
	parts := root.Get("response.candidates.0.content.parts")
	if !parts.IsArray() {
		parts = root.Get("candidates.0.content.parts")
	}
	if parts.IsArray() {
		for _, part := range parts.Array() {
			if text := part.Get("text"); text.Exists() {
				textBuilder.WriteString(text.String())
			}
		}
	}
	return textBuilder.String()
}

func antigravityWebSearchQueryFromGrounding(groundingMetadata gjson.Result) string {
	if queries := groundingMetadata.Get("webSearchQueries"); queries.IsArray() && len(queries.Array()) > 0 {
		return queries.Array()[0].String()
	}
	return ""
}

func antigravityWebSearchResultsFromGrounding(groundingMetadata gjson.Result) []WebSearchResult {
	groundingChunks := groundingMetadata.Get("groundingChunks")
	if !groundingChunks.IsArray() {
		return nil
	}
	seenURLs := make(map[string]struct{})
	results := make([]WebSearchResult, 0, len(groundingChunks.Array()))
	for _, chunk := range groundingChunks.Array() {
		web := chunk.Get("web")
		if !web.Exists() {
			continue
		}
		uri := strings.TrimSpace(web.Get("uri").String())
		if uri == "" {
			continue
		}
		if _, ok := seenURLs[uri]; ok {
			continue
		}
		seenURLs[uri] = struct{}{}
		title := ""
		titlePresent := false
		if titleResult := web.Get("title"); titleResult.Exists() {
			title = titleResult.String()
			titlePresent = true
		}
		results = append(results, WebSearchResult{
			URL:          uri,
			Title:        title,
			titlePresent: titlePresent,
		})
	}
	return results
}

func parseAntigravityWebSearchGroundingSupports(groundingMetadata gjson.Result) []antigravityWebSearchGroundingSupport {
	groundingChunks := groundingMetadata.Get("groundingChunks")
	if !groundingChunks.IsArray() {
		return nil
	}
	chunks := groundingChunks.Array()
	chunkData := make([]struct {
		URL   string
		Title string
	}, len(chunks))
	for i, chunk := range chunks {
		web := chunk.Get("web")
		if web.Exists() {
			chunkData[i].URL = web.Get("uri").String()
			chunkData[i].Title = web.Get("title").String()
		}
	}

	groundingSupports := groundingMetadata.Get("groundingSupports")
	if !groundingSupports.IsArray() {
		return nil
	}
	supports := make([]antigravityWebSearchGroundingSupport, 0, len(groundingSupports.Array()))
	for _, support := range groundingSupports.Array() {
		segment := support.Get("segment")
		if !segment.Exists() {
			continue
		}
		parsed := antigravityWebSearchGroundingSupport{
			StartIndex: segment.Get("startIndex").Int(),
			EndIndex:   segment.Get("endIndex").Int(),
			Text:       segment.Get("text").String(),
		}
		if chunkIndices := support.Get("groundingChunkIndices"); chunkIndices.IsArray() {
			for _, idx := range chunkIndices.Array() {
				chunkIndex := int(idx.Int())
				if chunkIndex < 0 || chunkIndex >= len(chunkData) {
					continue
				}
				parsed.ChunkURLs = append(parsed.ChunkURLs, chunkData[chunkIndex].URL)
				if parsed.ChunkTitle == "" {
					parsed.ChunkTitle = chunkData[chunkIndex].Title
				}
			}
		}
		supports = append(supports, parsed)
	}
	return supports
}

func buildAntigravityWebSearchCitedTextBlocks(textContent string, supports []antigravityWebSearchGroundingSupport) []WebSearchAnnotationBlock {
	if len(supports) == 0 {
		if textContent == "" {
			return nil
		}
		return []WebSearchAnnotationBlock{{Text: textContent}}
	}

	textBytes := []byte(textContent)
	blocks := make([]WebSearchAnnotationBlock, 0, len(supports)+1)
	lastEnd := int64(0)
	for _, support := range supports {
		if support.EndIndex <= lastEnd {
			continue
		}
		if support.StartIndex > lastEnd {
			start := int(lastEnd)
			end := min(int(support.StartIndex), len(textBytes))
			if start < end {
				blocks = append(blocks, WebSearchAnnotationBlock{Text: string(textBytes[start:end])})
			}
		}

		citedStart := support.StartIndex
		if citedStart < lastEnd {
			citedStart = lastEnd
		}
		citedText := ""
		if citedStart < support.EndIndex {
			start := min(int(citedStart), len(textBytes))
			end := min(int(support.EndIndex), len(textBytes))
			if start < end {
				citedText = string(textBytes[start:end])
			}
		}
		if citedText != "" && len(support.ChunkURLs) > 0 {
			blocks = append(blocks, WebSearchAnnotationBlock{
				Text: citedText,
				Citations: []WebSearchCitation{{
					URL:   support.ChunkURLs[0],
					Title: support.ChunkTitle,
				}},
			})
		}
		if support.EndIndex > lastEnd {
			lastEnd = support.EndIndex
		}
	}
	if int(lastEnd) < len(textBytes) {
		blocks = append(blocks, WebSearchAnnotationBlock{Text: string(textBytes[lastEnd:])})
	}
	return blocks
}

func buildAntigravityWebSearchContent(toolUseID string, textContent string, groundingMetadata gjson.Result) []ContentBlock {
	content := []ContentBlock{
		WebSearchInvocationBlock{
			ID:    toolUseID,
			Name:  "web_search",
			Query: antigravityWebSearchQueryFromGrounding(groundingMetadata),
			Input: codexWebSearchInput(antigravityWebSearchQueryFromGrounding(groundingMetadata)),
		},
		WebSearchResultSetBlock{
			ToolUseID:       toolUseID,
			Results:         antigravityWebSearchResultsFromGrounding(groundingMetadata),
			noTitleFallback: true,
		},
	}
	for _, block := range buildAntigravityWebSearchCitedTextBlocks(textContent, parseAntigravityWebSearchGroundingSupports(groundingMetadata)) {
		if block.Text != "" {
			content = append(content, block)
		}
	}
	return content
}

func antigravityWebSearchUsage(root gjson.Result, stream bool) *UnifiedUsage {
	usage := root.Get("response.usageMetadata")
	if !usage.Exists() {
		return nil
	}

	promptTokens := int(usage.Get("promptTokenCount").Int())
	cachedTokens := int(usage.Get("cachedContentTokenCount").Int())
	inputTokens := promptTokens
	if stream {
		inputTokens = promptTokens - cachedTokens
	}
	candidateTokens := int(usage.Get("candidatesTokenCount").Int())
	thoughtTokens := int(usage.Get("thoughtsTokenCount").Int())
	totalTokens := int(usage.Get("totalTokenCount").Int())
	if stream {
		if candidateTokens == 0 && totalTokens > 0 {
			candidateTokens = totalTokens - inputTokens - thoughtTokens
			if candidateTokens < 0 {
				candidateTokens = 0
			}
		}
	} else if candidateTokens+thoughtTokens == 0 && totalTokens > 0 {
		candidateTokens = totalTokens - inputTokens
		if candidateTokens < 0 {
			candidateTokens = 0
		}
	}
	outputTokens := candidateTokens + thoughtTokens

	u := newUsage(FormatAnthropic)
	u.PromptTokens = inputTokens
	u.CompletionTokens = outputTokens
	u.TotalTokens = totalTokens
	u.CacheReadInputTokens = cachedTokens
	u.CachedTokens = cachedTokens
	u.usagePresence.Prompt = true
	u.usagePresence.Completion = true
	u.usagePresence.Total = usage.Get("totalTokenCount").Exists()
	u.usagePresence.CacheRead = cachedTokens > 0
	u.usagePresence.Cached = u.usagePresence.CacheRead
	return u
}

func antigravityWebSearchMessageStartUsage(rawJSON []byte) *UnifiedUsage {
	root := gjson.ParseBytes(rawJSON)
	usage := root.Get("response.cpaUsageMetadata")
	if !usage.Exists() {
		return nil
	}
	u := newUsage(FormatAnthropic)
	setUsageInt(&u.PromptTokens, &u.usagePresence.Prompt, usage.Get("promptTokenCount"))
	u.usagePresence.Prompt = u.usagePresence.Prompt || usage.Get("promptTokenCount").Exists()
	return u
}

func newClaudeWebSearchToolUseID() string {
	return fmt.Sprintf("srvtoolu_%d", time.Now().UnixNano())
}

func antigravityWebSearchResultContentJSON(results []WebSearchResult) string {
	content := make([]map[string]any, 0, len(results))
	for _, result := range results {
		url := strings.TrimSpace(result.URL)
		if url == "" {
			continue
		}
		item := map[string]any{
			"type":     "web_search_result",
			"url":      url,
			"page_age": nil,
		}
		if result.titlePresent {
			item["title"] = result.Title
		}
		content = append(content, item)
	}
	raw, _ := json.Marshal(content)
	return string(raw)
}

func antigravityWebSearchStreamDeltas(blocks []ContentBlock) []StreamDelta {
	var deltas []StreamDelta
	for _, block := range blocks {
		switch b := block.(type) {
		case WebSearchInvocationBlock:
			deltas = append(deltas, StreamDelta{
				Type:       EventToolStart,
				ToolCallID: b.ID,
				ToolName:   "web_search",
				ToolType:   streamToolTypeServerWebSearch,
			})
			if queryDelta := codexWebSearchQueryDelta(b.Query); queryDelta != "" {
				deltas = append(deltas, StreamDelta{
					Type:       EventToolDelta,
					ToolCallID: b.ID,
					ToolName:   "web_search",
					ToolType:   streamToolTypeServerWebSearch,
					ToolArgs:   queryDelta,
				})
			}
			deltas = append(deltas, StreamDelta{
				Type:       EventToolDone,
				ToolCallID: b.ID,
				ToolName:   "web_search",
				ToolType:   streamToolTypeServerWebSearch,
			})
		case WebSearchResultSetBlock:
			deltas = append(deltas, StreamDelta{
				Type:       EventToolStart,
				ToolCallID: b.ToolUseID,
				ToolName:   "web_search",
				ToolType:   streamToolTypeServerWebSearchResult,
				ToolArgs:   antigravityWebSearchResultContentJSON(b.Results),
			})
			deltas = append(deltas, StreamDelta{
				Type:       EventToolDone,
				ToolCallID: b.ToolUseID,
				ToolName:   "web_search",
				ToolType:   streamToolTypeServerWebSearchResult,
			})
		case WebSearchAnnotationBlock:
			extra := map[string]any{
				"anthropic_force_text_block": true,
				"anthropic_close_text_block": true,
				"anthropic_text_chunk_runes": 50,
			}
			if len(b.Citations) > 0 {
				extra["anthropic_citations"] = b.Citations
			}
			deltas = append(deltas, StreamDelta{
				Type:    EventTextDelta,
				Content: b.Text,
				Extra:   extra,
			})
		}
	}
	return deltas
}

func splitRunesForWebSearch(text string, chunkSize int) []string {
	if chunkSize <= 0 || text == "" {
		return nil
	}
	runes := []rune(text)
	chunks := make([]string, 0, (len(runes)+chunkSize-1)/chunkSize)
	for start := 0; start < len(runes); start += chunkSize {
		end := start + chunkSize
		if end > len(runes) {
			end = len(runes)
		}
		chunks = append(chunks, string(runes[start:end]))
	}
	return chunks
}
