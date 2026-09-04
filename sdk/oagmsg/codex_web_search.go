package oagmsg

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/tidwall/gjson"
)

const (
	streamToolTypeServerWebSearch        = "server_web_search"
	streamToolTypeServerWebSearchResult  = "server_web_search_result"
	streamToolTypeResponsesRawItem       = "responses_raw_item"
	codexWebSearchDeferredFallbackID     = "__oagmsg_codex_web_search_fallback__"
	codexWebSearchDeferredFallbackPrefix = codexWebSearchDeferredFallbackID + ":"
)

type codexWebSearchState struct {
	resultIDs  map[string]struct{}
	lastID     string
	fallback   int
	fallbackID func() string
	noFallback bool
}

func (s *codexWebSearchState) rememberFromEvent(root, item gjson.Result) {
	if id := firstCodexWebSearchID(root, item, []string{"id", "output_item_id", "call_id", "item_id"}); id != "" {
		s.lastID = id
	}
}

func (s *codexWebSearchState) resolveID(root, item gjson.Result) string {
	if id := firstCodexWebSearchID(root, item, []string{"id", "output_item_id", "call_id"}); id != "" {
		return id
	}
	if s.lastID != "" {
		return s.lastID
	}
	if id := firstCodexWebSearchID(root, item, []string{"item_id"}); id != "" {
		return id
	}
	if s.fallbackID != nil {
		return s.fallbackID()
	}
	if s.noFallback {
		return ""
	}
	id := fmt.Sprintf("web_search_%d", s.fallback)
	s.fallback++
	s.lastID = id
	return id
}

func firstCodexWebSearchID(root, item gjson.Result, paths []string) string {
	for _, path := range paths {
		if value := strings.TrimSpace(item.Get(path).String()); value != "" {
			return value
		}
		if value := strings.TrimSpace(root.Get(path).String()); value != "" {
			return value
		}
	}
	return ""
}

func codexWebSearchQuery(root, item gjson.Result) string {
	for _, path := range []string{"action.query", "query", "input.query"} {
		if value := strings.TrimSpace(item.Get(path).String()); value != "" {
			return value
		}
		if value := strings.TrimSpace(root.Get(path).String()); value != "" {
			return value
		}
	}
	return ""
}

func codexWebSearchResults(root, item gjson.Result) []WebSearchResult {
	results := item.Get("results")
	if !results.IsArray() {
		results = root.Get("results")
	}
	if !results.IsArray() {
		return nil
	}
	out := make([]WebSearchResult, 0, len(results.Array()))
	results.ForEach(func(_, result gjson.Result) bool {
		url := strings.TrimSpace(result.Get("url").String())
		if url == "" {
			return true
		}
		title := strings.TrimSpace(result.Get("title").String())
		if title == "" {
			title = url
		}
		out = append(out, WebSearchResult{URL: url, Title: title})
		return true
	})
	return out
}

func codexWebSearchHasAction(root, item gjson.Result) bool {
	return item.Get("action").Exists() || root.Get("action").Exists()
}

func codexWebSearchHasProjection(root, item gjson.Result, includeAction bool) bool {
	if codexWebSearchQuery(root, item) != "" || len(codexWebSearchResults(root, item)) > 0 {
		return true
	}
	return includeAction && codexWebSearchHasAction(root, item)
}

func codexWebSearchBlocks(root, item gjson.Result, state *codexWebSearchState, includeAction bool) []ContentBlock {
	if state == nil {
		state = &codexWebSearchState{}
	}
	id := state.resolveID(root, item)
	if id == "" || !codexWebSearchHasProjection(root, item, includeAction) {
		return nil
	}
	if state.resultIDs == nil {
		state.resultIDs = make(map[string]struct{})
	}
	if !isCodexWebSearchDeferredFallbackID(id) {
		if _, ok := state.resultIDs[id]; ok {
			return nil
		}
	}
	query := codexWebSearchQuery(root, item)
	results := codexWebSearchResults(root, item)
	if !isCodexWebSearchDeferredFallbackID(id) {
		state.resultIDs[id] = struct{}{}
	}
	if id == state.lastID {
		state.lastID = ""
	}
	return []ContentBlock{
		WebSearchInvocationBlock{
			ID:    id,
			Name:  "web_search",
			Query: query,
			Input: codexWebSearchInput(query),
		},
		WebSearchResultSetBlock{
			ToolUseID: id,
			Results:   results,
		},
	}
}

func codexWebSearchInput(query string) map[string]any {
	if query == "" {
		return map[string]any{}
	}
	return map[string]any{"query": query}
}

func codexWebSearchResultContentJSON(results []WebSearchResult) string {
	content := make([]map[string]any, 0, len(results))
	for _, result := range results {
		url := strings.TrimSpace(result.URL)
		if url == "" {
			continue
		}
		title := strings.TrimSpace(result.Title)
		if title == "" {
			title = url
		}
		content = append(content, map[string]any{
			"type":     "web_search_result",
			"title":    title,
			"url":      url,
			"page_age": nil,
		})
	}
	raw, _ := json.Marshal(content)
	return string(raw)
}

func codexWebSearchQueryDelta(query string) string {
	if query == "" {
		return ""
	}
	raw, _ := json.Marshal(map[string]string{"query": query})
	return string(raw)
}

func isServerWebSearchToolType(toolType string) bool {
	return toolType == streamToolTypeServerWebSearch || toolType == streamToolTypeServerWebSearchResult
}

func isCodexWebSearchDeferredFallbackID(id string) bool {
	return id == codexWebSearchDeferredFallbackID || strings.HasPrefix(id, codexWebSearchDeferredFallbackPrefix)
}

func isInternalResponseToolType(toolType string) bool {
	return isServerWebSearchToolType(toolType) || toolType == streamToolTypeResponsesRawItem
}

func responsesOutputToolUseBlock(item gjson.Result) ContentBlock {
	switch item.Get("type").String() {
	case "function_call":
		input := map[string]any{}
		if args := item.Get("arguments").String(); args != "" && gjson.Valid(args) {
			if err := json.Unmarshal([]byte(args), &input); err != nil {
				input = map[string]any{}
			}
		}
		return ToolUseBlock{
			ID:    item.Get("call_id").String(),
			Name:  item.Get("name").String(),
			Input: input,
		}
	case "custom_tool_call":
		input := item.Get("input").String()
		if input == "" && item.Get("input").Exists() {
			input = item.Get("input").Raw
		}
		return CustomToolUseBlock{
			ID:    item.Get("call_id").String(),
			Name:  item.Get("name").String(),
			Input: input,
		}
	default:
		return nil
	}
}

func codexWebSearchStreamDeltas(blocks []ContentBlock) []StreamDelta {
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
				ToolArgs:   codexWebSearchResultContentJSON(b.Results),
			})
			deltas = append(deltas, StreamDelta{
				Type:       EventToolDone,
				ToolCallID: b.ToolUseID,
				ToolName:   "web_search",
				ToolType:   streamToolTypeServerWebSearchResult,
			})
		}
	}
	return deltas
}
