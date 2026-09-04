package oagmsg

// WebSearchOrder carries source ordering metadata when a provider emits web
// search as multiple ordered events. OagMessage.Content order remains the
// primary order; this optional metadata preserves source event and block indexes.
type WebSearchOrder struct {
	EventIndex   *int `json:"event_index,omitempty"`
	ContentIndex *int `json:"content_index,omitempty"`
}

// WebSearchInvocationBlock represents the ordered web search invocation phase.
// It can model audited Codex/Antigravity web_search tool-use starts without
// requiring provider-specific conversion rules at the model layer.
type WebSearchInvocationBlock struct {
	ID          string          `json:"id,omitempty"`
	Name        string          `json:"name,omitempty"`
	Query       string          `json:"query,omitempty"`
	Input       map[string]any  `json:"input,omitempty"`
	Order       *WebSearchOrder `json:"order,omitempty"`
	RawMetadata map[string]any  `json:"raw_metadata,omitempty"`
}

func (WebSearchInvocationBlock) blockType() string { return "web_search_invocation" }

// WebSearchResultSetBlock represents the ordered web search result-set phase.
type WebSearchResultSetBlock struct {
	ToolUseID   string            `json:"tool_use_id,omitempty"`
	Results     []WebSearchResult `json:"results,omitempty"`
	Order       *WebSearchOrder   `json:"order,omitempty"`
	RawMetadata map[string]any    `json:"raw_metadata,omitempty"`

	noTitleFallback bool
}

func (WebSearchResultSetBlock) blockType() string { return "web_search_result_set" }

// WebSearchAnnotationBlock represents an ordered citation/annotation phase for
// web-search-backed text. Text may be empty when a provider emits citation data
// separately from text deltas.
type WebSearchAnnotationBlock struct {
	Text        string              `json:"text,omitempty"`
	Citations   []WebSearchCitation `json:"citations,omitempty"`
	Order       *WebSearchOrder     `json:"order,omitempty"`
	RawMetadata map[string]any      `json:"raw_metadata,omitempty"`
}

func (WebSearchAnnotationBlock) blockType() string { return "web_search_annotation" }

// WebSearchResult is one item in a web search result set. URL, title, snippet,
// and source spans are independently optional because providers do not expose a
// consistent subset of those fields.
type WebSearchResult struct {
	URL         string                `json:"url,omitempty"`
	Title       string                `json:"title,omitempty"`
	Snippet     string                `json:"snippet,omitempty"`
	SourceSpans []WebSearchSourceSpan `json:"source_spans,omitempty"`
	RawMetadata map[string]any        `json:"raw_metadata,omitempty"`

	titlePresent bool
}

// WebSearchCitation points from generated text back to web search evidence.
// URL, title, snippet, and source spans are independently optional.
type WebSearchCitation struct {
	URL         string                `json:"url,omitempty"`
	Title       string                `json:"title,omitempty"`
	Snippet     string                `json:"snippet,omitempty"`
	SourceSpans []WebSearchSourceSpan `json:"source_spans,omitempty"`
	RawMetadata map[string]any        `json:"raw_metadata,omitempty"`
}

// WebSearchSourceSpan preserves an optional provider source span. Start and End
// are pointers so either side of the span can be represented independently.
type WebSearchSourceSpan struct {
	Start       *int           `json:"start,omitempty"`
	End         *int           `json:"end,omitempty"`
	Text        string         `json:"text,omitempty"`
	RawMetadata map[string]any `json:"raw_metadata,omitempty"`
}
