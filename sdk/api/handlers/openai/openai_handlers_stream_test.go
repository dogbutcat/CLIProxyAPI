package openai

import (
	"bytes"
	"strings"
	"testing"
)

func TestWriteOpenAIChatStreamChunkAvoidsDoubleDataPrefix(t *testing.T) {
	tests := []struct {
		name  string
		chunk []byte
		want  string
	}{
		{
			name:  "raw json",
			chunk: []byte(`{"choices":[{"delta":{"content":"pong"}}]}`),
			want:  "data: {\"choices\":[{\"delta\":{\"content\":\"pong\"}}]}\n\n",
		},
		{
			name:  "data frame",
			chunk: []byte("data: {\"choices\":[{\"delta\":{\"content\":\"pong\"}}]}\n\n"),
			want:  "data: {\"choices\":[{\"delta\":{\"content\":\"pong\"}}]}\n\n",
		},
		{
			name:  "done frame",
			chunk: []byte("data: [DONE]\n\n"),
			want:  "",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var got bytes.Buffer
			writeOpenAIChatStreamChunk(&got, tt.chunk)
			if got.String() != tt.want {
				t.Fatalf("stream chunk = %q, want %q", got.String(), tt.want)
			}
			if strings.Contains(got.String(), "data: data:") {
				t.Fatalf("stream chunk has duplicate SSE data prefix: %q", got.String())
			}
		})
	}
}

func TestOpenAIChatStreamTerminalFilterDropsPostTerminalBusinessChunks(t *testing.T) {
	filter := &openAIChatStreamTerminalFilter{}
	chunks := []struct {
		name  string
		chunk []byte
		want  bool
	}{
		{
			name:  "start",
			chunk: []byte(`{"choices":[{"delta":{"role":"assistant"},"finish_reason":null}]}`),
			want:  true,
		},
		{
			name:  "terminal with usage",
			chunk: []byte(`{"choices":[{"delta":{},"finish_reason":"stop"}],"usage":{"prompt_tokens":4,"completion_tokens":72,"total_tokens":76}}`),
			want:  true,
		},
		{
			name:  "late role",
			chunk: []byte(`{"choices":[{"delta":{"role":"assistant"},"finish_reason":null}]}`),
			want:  false,
		},
		{
			name:  "late empty thinking",
			chunk: []byte(`{"choices":[{"delta":{"reasoning_content":""},"finish_reason":null}]}`),
			want:  false,
		},
		{
			name:  "late usage with choice",
			chunk: []byte(`{"choices":[{"delta":{},"finish_reason":null}],"usage":{"prompt_tokens":4,"completion_tokens":72,"total_tokens":76}}`),
			want:  false,
		},
		{
			name:  "done marker",
			chunk: []byte(`data: [DONE]`),
			want:  true,
		},
	}
	for _, tt := range chunks {
		t.Run(tt.name, func(t *testing.T) {
			if got := filter.Allow(tt.chunk); got != tt.want {
				t.Fatalf("Allow() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestOpenAIChatStreamTerminalFilterAllowsTrailingUsageOnlyWhenTerminalLackedUsage(t *testing.T) {
	filter := &openAIChatStreamTerminalFilter{}
	if !filter.Allow([]byte(`{"choices":[{"delta":{},"finish_reason":"stop"}]}`)) {
		t.Fatal("terminal chunk was dropped")
	}
	if !filter.Allow([]byte(`{"choices":[],"usage":{"prompt_tokens":1,"completion_tokens":2,"total_tokens":3}}`)) {
		t.Fatal("usage-only chunk after terminal without usage was dropped")
	}
	if filter.Allow([]byte(`{"choices":[],"usage":{"prompt_tokens":1,"completion_tokens":2,"total_tokens":3}}`)) {
		t.Fatal("second usage-only chunk after terminal was allowed")
	}
	if filter.Allow([]byte(`{"choices":[{"delta":{"content":"late"},"finish_reason":null}]}`)) {
		t.Fatal("post-terminal content chunk was allowed")
	}
}
