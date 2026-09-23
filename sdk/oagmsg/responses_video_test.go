package oagmsg

import (
	"encoding/json"
	"fmt"
	"reflect"
	"testing"

	responses_to_chat "github.com/router-for-me/CLIProxyAPI/v7/internal/translator/openai/openai/responses"
	"github.com/tidwall/gjson"
)

func TestOpenAIResponsesVideoInputToChatUpstreamOracle(t *testing.T) {
	tests := []struct {
		name string
		part string
		want string
	}{
		{
			name: "remote URL",
			part: `{"type":"input_video","video_url":"https://example.com/clip.mp4?part=1&name=a%20b"}`,
			want: `{"type":"video_url","video_url":{"url":"https://example.com/clip.mp4?part=1&name=a%20b"}}`,
		},
		{
			name: "base64 data URL",
			part: `{"type":"input_video","video_url":"data:video/mp4;base64,AAECAwQ="}`,
			want: `{"type":"video_url","video_url":{"url":"data:video/mp4;base64,AAECAwQ="}}`,
		},
		{
			name: "processing mode",
			part: `{"type":"input_video","video_url":"https://example.com/clip.webm","processing":"agentic"}`,
			want: `{"type":"video_url","video_url":{"url":"https://example.com/clip.webm","processing":"agentic"}}`,
		},
		{
			name: "object video URL",
			part: `{"type":"input_video","video_url":{"url":"https://example.com/clip.mp4","processing":"static"}}`,
			want: `{"type":"video_url","video_url":{"url":"https://example.com/clip.mp4","processing":"static"}}`,
		},
		{
			name: "chat video part in Responses content",
			part: `{"type":"video_url","video_url":{"url":"data:video/webm;base64,AAECAwQ=","processing":"static"}}`,
			want: `{"type":"video_url","video_url":{"url":"data:video/webm;base64,AAECAwQ=","processing":"static"}}`,
		},
		{
			name: "top-level processing overrides object processing",
			part: `{"type":"input_video","video_url":{"url":"https://example.com/clip.mp4","processing":"static"},"processing":"agentic"}`,
			want: `{"type":"video_url","video_url":{"url":"https://example.com/clip.mp4","processing":"agentic"}}`,
		},
		{
			name: "missing URL remains a video for upstream validation",
			part: `{"type":"input_video"}`,
			want: `{"type":"video_url","video_url":{}}`,
		},
		{
			name: "invalid URL is not coerced to a string",
			part: `{"type":"input_video","video_url":123}`,
			want: `{"type":"video_url","video_url":{"url":123}}`,
		},
	}

	for _, tt := range tests {
		for _, stream := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/stream=%t", tt.name, stream), func(t *testing.T) {
				raw := []byte(`{"input":[{"role":"user","content":[` + tt.part + `]}]}`)
				upstream := responses_to_chat.ConvertOpenAIResponsesRequestToOpenAIChatCompletions("video-model", raw, stream)
				oag := TranslateRequest(FormatOpenAIResponse, FormatOpenAI, "video-model", raw, stream)

				content := gjson.GetBytes(oag, "messages.0.content").Array()
				if len(content) != 1 {
					t.Fatalf("video content was lost: got %d parts, want 1; output=%s", len(content), oag)
				}
				var got any
				if err := json.Unmarshal([]byte(content[0].Raw), &got); err != nil {
					t.Fatal(err)
				}
				var want any
				if err := json.Unmarshal([]byte(tt.want), &want); err != nil {
					t.Fatal(err)
				}
				if !reflect.DeepEqual(got, want) {
					t.Fatalf("video part = %s, want %s", content[0].Raw, tt.want)
				}
				if got, want := normalizedVideoChatContent(oag), normalizedVideoChatContent(upstream); !reflect.DeepEqual(got, want) {
					t.Fatalf("video oracle mismatch\noag:      %#v\nupstream: %#v\noagJSON: %s\nupJSON:  %s", got, want, oag, upstream)
				}
			})
		}
	}
}

func TestOpenAIResponsesMixedVideoInputOrderUpstreamOracle(t *testing.T) {
	raw := []byte(`{"input":[{"type":"message","role":"user","content":[
		{"type":"input_text","text":"Compare these clips and this image."},
		{"type":"input_video","video_url":"https://example.com/first.mp4"},
		{"type":"input_image","image_url":"https://example.com/frame.png","detail":"low"},
		{"type":"input_video","video_url":"data:video/mp4;base64,AAECAwQ=","processing":"static"},
		{"type":"input_text","text":"Describe the differences."}
	]}]}`)

	upstream := responses_to_chat.ConvertOpenAIResponsesRequestToOpenAIChatCompletions("video-model", raw, false)
	oag := TranslateRequest(FormatOpenAIResponse, FormatOpenAI, "video-model", raw, false)
	if got, want := normalizedVideoChatContent(oag), normalizedVideoChatContent(upstream); !reflect.DeepEqual(got, want) {
		t.Fatalf("mixed content order or media changed\noag:      %#v\nupstream: %#v\noagJSON: %s\nupJSON:  %s", got, want, oag, upstream)
	}
}

func TestOpenAIResponsesVideoInputToAntigravityInlineData(t *testing.T) {
	raw := []byte(`{"input":[{"role":"user","content":[{"type":"input_video","video_url":"data:video/mp4;base64,AAECAwQ="}]}]}`)
	out := TranslateRequest(FormatOpenAIResponse, FormatAntigravity, "gemini-3.7-flash-high", raw, false)
	part := gjson.GetBytes(out, "request.contents.0.parts.0.inlineData")
	if got := part.Get("mimeType").String(); got != "video/mp4" {
		t.Fatalf("mimeType = %q, want video/mp4; output=%s", got, out)
	}
	if got := part.Get("data").String(); got != "AAECAwQ=" {
		t.Fatalf("data = %q, want video payload; output=%s", got, out)
	}
}

func normalizedVideoChatContent(raw []byte) any {
	return gjson.GetBytes(raw, "messages.0.content").Value()
}
