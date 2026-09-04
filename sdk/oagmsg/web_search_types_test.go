package oagmsg

import (
	"encoding/json"
	"testing"
)

func TestWebSearchContentBlocksModelConstruction(t *testing.T) {
	eventIndex := 0
	contentIndex := 3
	start := 12

	blocks := []ContentBlock{
		WebSearchInvocationBlock{
			ID:    "srvtoolu_1",
			Name:  "web_search",
			Query: "oagmsg web search parity",
			Input: map[string]any{"query": "oagmsg web search parity"},
			Order: &WebSearchOrder{EventIndex: &eventIndex, ContentIndex: &contentIndex},
			RawMetadata: map[string]any{
				"provider": "codex",
			},
		},
		WebSearchResultSetBlock{
			ToolUseID: "srvtoolu_1",
			Results: []WebSearchResult{
				{
					URL:   "https://example.test/oagmsg",
					Title: "OAGMSG",
					SourceSpans: []WebSearchSourceSpan{
						{Start: &start},
					},
				},
				{
					Snippet: "snippet-only result",
				},
			},
		},
		WebSearchAnnotationBlock{
			Text: "cited text",
			Citations: []WebSearchCitation{
				{
					URL:     "https://example.test/oagmsg",
					Snippet: "cited text",
				},
			},
		},
	}

	expectedTypes := []string{
		"web_search_invocation",
		"web_search_result_set",
		"web_search_annotation",
	}
	for i, block := range blocks {
		if block.blockType() != expectedTypes[i] {
			t.Fatalf("block %d: blockType() = %q, want %q", i, block.blockType(), expectedTypes[i])
		}
		if _, err := json.Marshal(block); err != nil {
			t.Fatalf("block %d (%T): json.Marshal error: %v", i, block, err)
		}
	}
}

func TestWebSearchContentBlocksModelOrderCarrier(t *testing.T) {
	first := 0
	second := 1
	third := 2

	content := []ContentBlock{
		WebSearchInvocationBlock{ID: "srvtoolu_1", Order: &WebSearchOrder{EventIndex: &first}},
		WebSearchResultSetBlock{ToolUseID: "srvtoolu_1", Order: &WebSearchOrder{EventIndex: &second}},
		TextBlock{Text: "between web search phases"},
		WebSearchAnnotationBlock{Text: "cited text", Order: &WebSearchOrder{EventIndex: &third}},
	}

	if _, ok := content[0].(WebSearchInvocationBlock); !ok {
		t.Fatalf("content[0] = %T, want WebSearchInvocationBlock", content[0])
	}
	if _, ok := content[1].(WebSearchResultSetBlock); !ok {
		t.Fatalf("content[1] = %T, want WebSearchResultSetBlock", content[1])
	}
	if _, ok := content[2].(TextBlock); !ok {
		t.Fatalf("content[2] = %T, want TextBlock", content[2])
	}
	if _, ok := content[3].(WebSearchAnnotationBlock); !ok {
		t.Fatalf("content[3] = %T, want WebSearchAnnotationBlock", content[3])
	}

	invocation := content[0].(WebSearchInvocationBlock)
	resultSet := content[1].(WebSearchResultSetBlock)
	annotation := content[3].(WebSearchAnnotationBlock)
	if *invocation.Order.EventIndex != 0 || *resultSet.Order.EventIndex != 1 || *annotation.Order.EventIndex != 2 {
		t.Fatalf("event order = [%d, %d, %d], want [0, 1, 2]",
			*invocation.Order.EventIndex,
			*resultSet.Order.EventIndex,
			*annotation.Order.EventIndex,
		)
	}
}

func TestWebSearchContentBlocksModelJSONRoundTrip(t *testing.T) {
	eventIndex := 7
	contentIndex := 4
	start := 15
	end := 27

	original := WebSearchAnnotationBlock{
		Text: "OpenAI released docs",
		Citations: []WebSearchCitation{
			{
				Title: "OpenAI Docs",
				SourceSpans: []WebSearchSourceSpan{
					{Start: &start, End: &end, Text: "released docs"},
				},
				RawMetadata: map[string]any{"groundingChunkIndex": float64(2)},
			},
			{
				URL:     "https://example.test/docs",
				Snippet: "snippet without title",
			},
		},
		Order: &WebSearchOrder{EventIndex: &eventIndex, ContentIndex: &contentIndex},
		RawMetadata: map[string]any{
			"delta_type": "citations_delta",
		},
	}

	raw, err := json.Marshal(original)
	if err != nil {
		t.Fatalf("json.Marshal error: %v", err)
	}

	var decoded WebSearchAnnotationBlock
	if err := json.Unmarshal(raw, &decoded); err != nil {
		t.Fatalf("json.Unmarshal error: %v", err)
	}

	if decoded.Text != original.Text {
		t.Fatalf("Text = %q, want %q", decoded.Text, original.Text)
	}
	if decoded.Order == nil || decoded.Order.EventIndex == nil || *decoded.Order.EventIndex != eventIndex {
		t.Fatalf("decoded event index = %#v, want %d", decoded.Order, eventIndex)
	}
	if decoded.Order.ContentIndex == nil || *decoded.Order.ContentIndex != contentIndex {
		t.Fatalf("decoded content index = %#v, want %d", decoded.Order, contentIndex)
	}
	if len(decoded.Citations) != 2 {
		t.Fatalf("decoded citations = %d, want 2", len(decoded.Citations))
	}
	if decoded.Citations[0].URL != "" || decoded.Citations[0].Title != "OpenAI Docs" {
		t.Fatalf("citation[0] optional fields = %#v", decoded.Citations[0])
	}
	if decoded.Citations[0].SourceSpans[0].Start == nil || *decoded.Citations[0].SourceSpans[0].Start != start {
		t.Fatalf("citation[0] start span = %#v, want %d", decoded.Citations[0].SourceSpans[0].Start, start)
	}
	if decoded.Citations[0].SourceSpans[0].End == nil || *decoded.Citations[0].SourceSpans[0].End != end {
		t.Fatalf("citation[0] end span = %#v, want %d", decoded.Citations[0].SourceSpans[0].End, end)
	}
	if decoded.Citations[1].Title != "" || decoded.Citations[1].URL == "" || decoded.Citations[1].Snippet == "" {
		t.Fatalf("citation[1] optional fields = %#v", decoded.Citations[1])
	}
	if decoded.RawMetadata["delta_type"] != "citations_delta" {
		t.Fatalf("raw metadata = %#v", decoded.RawMetadata)
	}
	if decoded.Citations[0].RawMetadata["groundingChunkIndex"] != float64(2) {
		t.Fatalf("citation raw metadata = %#v", decoded.Citations[0].RawMetadata)
	}
}
