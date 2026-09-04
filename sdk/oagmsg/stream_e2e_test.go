package oagmsg

import (
	"strings"
	"testing"
)

// assertContains is a test helper that checks if s contains substr.
func assertContains(t *testing.T, s, substr string) {
	t.Helper()
	if !strings.Contains(s, substr) {
		t.Errorf("expected %q to contain %q", s, substr)
	}
}

// --- End-to-End Stream Translation Tests ---

func TestE2E_Anthropic_To_OpenAI(t *testing.T) {
	// Simulate a full Anthropic stream being translated to OpenAI format.
	session, err := NewStreamSession(FormatAnthropic, FormatOpenAI, "claude-3")
	if err != nil {
		t.Fatal(err)
	}

	// message_start
	out, err := session.Translate([]byte(`data: {"type":"message_start","message":{"id":"msg_1","model":"claude-3","usage":{"input_tokens":10}}}`))
	if err != nil {
		t.Fatal(err)
	}
	if len(out) == 0 {
		t.Fatal("expected output for message_start")
	}
	// First chunk should have role:assistant.
	assertContains(t, string(out[0]), `"role":"assistant"`)

	// content_block_start (text)
	out, _ = session.Translate([]byte(`data: {"type":"content_block_start","index":0,"content_block":{"type":"text","text":""}}`))
	// text block start doesn't produce meaningful OpenAI output by itself.

	// content_block_delta (text)
	out, _ = session.Translate([]byte(`data: {"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"Hello"}}`))
	if len(out) == 0 {
		t.Fatal("expected output for text_delta")
	}
	assertContains(t, string(out[0]), `"content":"Hello"`)

	// message_delta carries usage; message_stop closes Anthropic streams.
	out, _ = session.Translate([]byte(`data: {"type":"message_delta","delta":{"stop_reason":"end_turn"},"usage":{"output_tokens":5}}`))
	for _, chunk := range out {
		if strings.Contains(string(chunk), "[DONE]") {
			t.Fatalf("message_delta closed stream early: %q", out)
		}
	}
	out, _ = session.Translate([]byte(`data: {"type":"message_stop"}`))
	foundFinish := false
	foundDone := false
	for _, chunk := range out {
		if strings.Contains(string(chunk), `"finish_reason":"stop"`) {
			foundFinish = true
		}
		if strings.Contains(string(chunk), "[DONE]") {
			foundDone = true
		}
	}
	if !foundFinish {
		t.Fatal("expected finish_reason:stop in output")
	}
	if !foundDone {
		t.Fatal("expected automatic [DONE] output")
	}
	if flushOut := session.Flush(); len(flushOut) != 0 {
		t.Fatalf("repeated flush output = %q", flushOut)
	}
}

func TestE2E_OpenAI_To_Anthropic(t *testing.T) {
	session, err := NewStreamSession(FormatOpenAI, FormatAnthropic, "gpt-4")
	if err != nil {
		t.Fatal(err)
	}

	// First chunk with role.
	out, _ := session.Translate([]byte(`data: {"id":"chatcmpl-1","object":"chat.completion.chunk","created":100,"model":"gpt-4","choices":[{"index":0,"delta":{"role":"assistant"},"finish_reason":null}]}`))
	combined := ""
	for _, c := range out {
		combined += string(c)
	}
	assertContains(t, combined, "message_start")

	// Text content.
	out, _ = session.Translate([]byte(`data: {"id":"chatcmpl-1","choices":[{"index":0,"delta":{"content":"Hi"},"finish_reason":null}]}`))
	combined = ""
	for _, c := range out {
		combined += string(c)
	}
	assertContains(t, combined, "text_delta")

	// Finish.
	out, _ = session.Translate([]byte(`data: {"id":"chatcmpl-1","choices":[{"index":0,"delta":{},"finish_reason":"stop"}]}`))

	// [DONE] triggers flush.
	out, _ = session.Translate([]byte(`data: [DONE]`))
	combined = ""
	for _, c := range out {
		combined += string(c)
	}
	assertContains(t, combined, "message_delta")
	assertContains(t, combined, "message_stop")
}

func TestE2E_Codex_To_OpenAI(t *testing.T) {
	session, err := NewStreamSession(FormatCodex, FormatOpenAI, "codex-mini")
	if err != nil {
		t.Fatal(err)
	}

	// response.created
	out, _ := session.Translate([]byte(`data: {"type":"response.created","response":{"id":"resp_1","model":"codex-mini","created_at":100}}`))
	if len(out) == 0 {
		t.Fatal("expected output for response.created")
	}
	assertContains(t, string(out[0]), `"role":"assistant"`)

	// response.output_text.delta
	out, _ = session.Translate([]byte(`data: {"type":"response.output_text.delta","delta":"World"}`))
	if len(out) == 0 {
		t.Fatal("expected output for text delta")
	}
	assertContains(t, string(out[0]), `"content":"World"`)

	// response.completed
	out, _ = session.Translate([]byte(`data: {"type":"response.completed","response":{"status":"completed","usage":{"input_tokens":5,"output_tokens":10,"total_tokens":15}}}`))
	foundFinish := false
	for _, chunk := range out {
		if strings.Contains(string(chunk), `"finish_reason":"stop"`) {
			foundFinish = true
		}
	}
	if !foundFinish {
		t.Fatal("expected finish_reason:stop")
	}
}

func TestE2E_Session_SkipsEventAndEmptyLines(t *testing.T) {
	session, err := NewStreamSession(FormatAnthropic, FormatOpenAI, "claude-3")
	if err != nil {
		t.Fatal(err)
	}

	// event: line should be skipped.
	out, _ := session.Translate([]byte("event: message_start"))
	if len(out) != 0 {
		t.Fatal("expected nil for event: line")
	}

	// Empty line should be skipped.
	out, _ = session.Translate([]byte(""))
	if len(out) != 0 {
		t.Fatal("expected nil for empty line")
	}

	// Comment line should be skipped.
	out, _ = session.Translate([]byte(": keep-alive"))
	if len(out) != 0 {
		t.Fatal("expected nil for comment line")
	}
}

func TestE2E_Session_DoneTriggersFlush(t *testing.T) {
	session, err := NewStreamSession(FormatOpenAI, FormatOpenAI, "gpt-4")
	if err != nil {
		t.Fatal(err)
	}

	// Send a text chunk.
	session.Translate([]byte(`data: {"id":"c1","choices":[{"index":0,"delta":{"role":"assistant"},"finish_reason":null}]}`))

	// Send [DONE].
	out, _ := session.Translate([]byte("data: [DONE]"))
	if len(out) == 0 {
		t.Fatal("expected flush output from [DONE]")
	}
	assertContains(t, string(out[0]), "[DONE]")

	// Subsequent translate should return nil (already flushed).
	out, _ = session.Translate([]byte(`data: {"choices":[]}`))
	if len(out) != 0 {
		t.Fatal("expected nil after flush")
	}
}

func TestE2E_TranslateStreamChunk_Convenience(t *testing.T) {
	var session *StreamTranslateSession

	// First call auto-initializes.
	chunks := TranslateStreamChunk(FormatOpenAI, FormatOpenAI, "gpt-4",
		[]byte(`data: {"id":"c1","choices":[{"index":0,"delta":{"role":"assistant"},"finish_reason":null}]}`),
		&session)
	if session == nil {
		t.Fatal("session should be initialized")
	}
	if len(chunks) == 0 {
		t.Fatal("expected output")
	}

	// Second call reuses session.
	chunks = TranslateStreamChunk(FormatOpenAI, FormatOpenAI, "gpt-4",
		[]byte(`data: {"id":"c1","choices":[{"index":0,"delta":{"content":"hi"},"finish_reason":null}]}`),
		&session)
	if len(chunks) == 0 {
		t.Fatal("expected output from second call")
	}
}
