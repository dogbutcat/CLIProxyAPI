package oagmsg

import (
	"fmt"
	"strings"
	"testing"
)

func TestFormatIdentityDistinctHandlers(t *testing.T) {
	registry := DefaultRegistry()
	tests := []struct {
		format       Format
		expectFormat Format
		expectType   string
	}{
		{FormatOpenAI, FormatOpenAI, "*oagmsg.OpenAIHandler"},
		{FormatAnthropic, FormatAnthropic, "*oagmsg.AnthropicHandler"},
		{FormatGemini, FormatGemini, "*oagmsg.GeminiHandler"},
		{FormatInteractions, FormatInteractions, "*oagmsg.GoogleInteractionsHandler"},
		{FormatInteractionsSteps, FormatInteractions, "*oagmsg.GoogleInteractionsHandler"},
		{FormatCodex, FormatCodex, "*oagmsg.CodexHandler"},
		{FormatOpenAIResponse, FormatOpenAIResponse, "*oagmsg.InteractionsHandler"},
		{FormatAntigravity, FormatAntigravity, "*oagmsg.AntigravityHandler"},
	}
	for _, tt := range tests {
		t.Run(string(tt.format), func(t *testing.T) {
			handler, ok := registry.Get(tt.format)
			if !ok {
				t.Fatalf("no handler registered for %q", tt.format)
			}
			if handler.Format() != tt.expectFormat {
				t.Fatalf("Format() = %q, want %q", handler.Format(), tt.expectFormat)
			}
			if got := typeName(handler); got != tt.expectType {
				t.Fatalf("handler type = %s, want %s", got, tt.expectType)
			}
		})
	}
}

func TestFormatIdentityResponseModes(t *testing.T) {
	registry := DefaultRegistry()
	responses, ok := registry.MustGet(FormatOpenAIResponse).(*InteractionsHandler)
	if !ok {
		t.Fatalf("OpenAI Responses handler type = %T", registry.MustGet(FormatOpenAIResponse))
	}
	if responses.Mode != InteractionsModeResponsesAPI {
		t.Fatalf("OpenAI Responses mode = %d, want %d", responses.Mode, InteractionsModeResponsesAPI)
	}
	if _, ok := registry.MustGet(FormatCodex).(*CodexHandler); !ok {
		t.Fatalf("Codex handler type = %T", registry.MustGet(FormatCodex))
	}
	if _, ok := registry.MustGet(FormatInteractions).(*GoogleInteractionsHandler); !ok {
		t.Fatalf("Google Interactions handler type = %T", registry.MustGet(FormatInteractions))
	}
}

func TestFormatIdentityStreamParsersDoNotConfuseGoogleAndResponses(t *testing.T) {
	registry := DefaultRegistry()
	google := registry.MustGet(FormatInteractions).(StreamHandler)
	codex := registry.MustGet(FormatCodex).(StreamHandler)

	googleEvents := []string{
		`{"event_type":"interaction.created","interaction":{"id":"int_1","model":"gemini-2.0-flash"}}`,
		`{"event_type":"step.delta","index":0,"delta":{"type":"text","text":"hello"}}`,
		`{"event_type":"interaction.completed","interaction":{"id":"int_1","status":"completed"}}`,
	}
	for _, event := range googleEvents {
		deltas, err := google.ParseStreamChunk([]byte(event))
		if err != nil || len(deltas) == 0 {
			t.Fatalf("Google parser failed for %s: deltas=%#v err=%v", event, deltas, err)
		}
		deltas, err = codex.ParseStreamChunk([]byte(event))
		if err != nil || len(deltas) != 0 {
			t.Fatalf("Codex parser accepted Google event %s: deltas=%#v err=%v", event, deltas, err)
		}
	}

	responseEvents := []string{
		`{"type":"response.created","response":{"id":"resp_1","status":"in_progress"}}`,
		`{"type":"response.output_text.delta","delta":"hello"}`,
		`{"type":"response.completed","response":{"id":"resp_1","status":"completed"}}`,
	}
	for _, event := range responseEvents {
		deltas, err := codex.ParseStreamChunk([]byte(event))
		if err != nil || len(deltas) == 0 {
			t.Fatalf("Codex parser failed for %s: deltas=%#v err=%v", event, deltas, err)
		}
		deltas, err = google.ParseStreamChunk([]byte(event))
		if err != nil || len(deltas) != 0 {
			t.Fatalf("Google parser accepted response event %s: deltas=%#v err=%v", event, deltas, err)
		}
	}
}

func TestFormatIdentitySerializerFamilies(t *testing.T) {
	registry := DefaultRegistry()
	start := StreamDelta{Type: EventStart, ID: "test_1", Model: "test-model"}
	textDelta := StreamDelta{Type: EventTextDelta, Content: "hello"}

	googleOutput := serializeIdentityDeltas(registry.MustGet(FormatInteractions).(StreamHandler), start, textDelta)
	if !strings.Contains(googleOutput, "interaction.created") || !strings.Contains(googleOutput, "step.delta") {
		t.Fatalf("Google Interactions output has wrong lifecycle: %s", googleOutput)
	}
	if strings.Contains(googleOutput, "response.created") {
		t.Fatalf("Google Interactions output contains response.* lifecycle: %s", googleOutput)
	}

	codexOutput := serializeIdentityDeltas(registry.MustGet(FormatCodex).(StreamHandler), start, textDelta)
	if !strings.Contains(codexOutput, "response.created") || !strings.Contains(codexOutput, "response.output_text.delta") {
		t.Fatalf("Codex output has wrong lifecycle: %s", codexOutput)
	}
	if strings.Contains(codexOutput, "response.output_item.added") {
		t.Fatalf("Codex output unexpectedly uses full Responses lifecycle: %s", codexOutput)
	}

	responsesOutput := serializeIdentityDeltas(registry.MustGet(FormatOpenAIResponse).(StreamHandler), start, textDelta)
	if !strings.Contains(responsesOutput, "response.output_item.added") || !strings.Contains(responsesOutput, "response.content_part.added") {
		t.Fatalf("OpenAI Responses output lacks full lifecycle: %s", responsesOutput)
	}
}

func TestFormatIdentityConstants(t *testing.T) {
	values := map[Format]string{
		FormatInteractions:      "interactions",
		FormatInteractionsSteps: "interactions-steps",
		FormatCodex:             "codex",
		FormatOpenAIResponse:    "openai-response",
	}
	for format, want := range values {
		if string(format) != want {
			t.Fatalf("format = %q, want %q", format, want)
		}
	}
}

func TestGoogleInteractionsNonStreamResponseShape(t *testing.T) {
	handler := DefaultRegistry().MustGet(FormatInteractions)
	response, err := handler.ParseResponse([]byte(`{
		"id":"int_1","model":"gemini-2.0-flash","status":"completed",
		"steps":[
			{"type":"thought","content":[{"type":"text","text":"plan"}],"signature":"sig_1"},
			{"type":"model_output","content":[{"type":"text","text":"hello"}]},
			{"type":"function_call","name":"lookup","call_id":"call_1","arguments":{"q":"x"}}
		],
		"usage":{"input_tokens":3,"output_tokens":4,"total_tokens":7}
	}`))
	if err != nil {
		t.Fatal(err)
	}
	if response.Content != "hello" || response.ThinkingContent != "plan" || response.ThinkingSignature != "sig_1" {
		t.Fatalf("response content = %#v", response)
	}
	if len(response.ToolCalls) != 1 || response.Usage == nil || response.Usage.TotalTokens != 7 {
		t.Fatalf("response metadata = %#v", response)
	}
	formatted, err := handler.FormatResponse(response, "")
	if err != nil {
		t.Fatal(err)
	}
	formattedString := string(formatted)
	if !strings.Contains(formattedString, `"steps"`) || strings.Contains(formattedString, `"output"`) {
		t.Fatalf("formatted response has wrong shape: %s", formattedString)
	}
}

func serializeIdentityDeltas(handler StreamHandler, deltas ...StreamDelta) string {
	serializer := handler.NewStreamSerializer("test-model")
	var output strings.Builder
	for _, delta := range deltas {
		for _, chunk := range serializer.Serialize(delta) {
			output.Write(chunk)
		}
	}
	return output.String()
}

func typeName(value any) string {
	return fmt.Sprintf("%T", value)
}
