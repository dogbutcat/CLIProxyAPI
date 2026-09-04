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
