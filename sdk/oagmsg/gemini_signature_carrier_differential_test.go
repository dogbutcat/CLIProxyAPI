package oagmsg_test

import (
	"context"
	"encoding/base64"
	"strings"
	"testing"

	upstream "github.com/router-for-me/CLIProxyAPI/v7/internal/translator/gemini/openai/responses"
	"github.com/router-for-me/CLIProxyAPI/v7/sdk/oagmsg"
	"github.com/tidwall/gjson"
)

const differentialGeminiSignature = "EjQKMgEMOdbHO0Gd+c9Mxk4ELwPGbpCEcp2mFfYYLix2UVtBH3fL8GECc4+JITVnHF4qZDsA"

type normalizedResponsesEvent struct {
	Event     string
	Index     int
	ItemType  string
	Direction string
	Target    string
	Signature string
}

type normalizedCompletedItem struct {
	ItemType  string
	Text      string
	Direction string
	Target    string
	Signature string
}

func TestGeminiResponsesCarrierStreamDifferentialOracle(t *testing.T) {
	sig2 := mutateDifferentialSignature(t, differentialGeminiSignature, -1)
	sig3 := mutateDifferentialSignature(t, differentialGeminiSignature, -2)
	cases := []struct {
		name  string
		lines []string
	}{
		{
			name: "consecutive_signed_visible_stream",
			lines: []string{
				`data: {"response":{"responseId":"diff-consecutive","candidates":[{"content":{"parts":[{"text":"a"},{"text":"b","thoughtSignature":"` + differentialGeminiSignature + `"}]}}]}}`,
				`data: {"response":{"responseId":"diff-consecutive","candidates":[{"content":{"parts":[{"text":"c","thoughtSignature":"` + sig2 + `"}]},"finishReason":"STOP"}]}}`,
			},
		},
		{
			name: "signed_visible_then_unsigned_stream",
			lines: []string{
				`data: {"response":{"responseId":"diff-signed-unsigned","candidates":[{"content":{"parts":[{"text":"signed","thoughtSignature":"` + differentialGeminiSignature + `"}]}}]}}`,
				`data: {"response":{"responseId":"diff-signed-unsigned","candidates":[{"content":{"parts":[{"text":"unsigned"}]},"finishReason":"STOP"}]}}`,
			},
		},
		{
			name: "visible_signature_before_later_thought",
			lines: []string{
				`data: {"response":{"responseId":"diff-visible-before-thought","candidates":[{"content":{"parts":[{"text":"thought-a","thought":true,"thoughtSignature":"` + differentialGeminiSignature + `"}]}}]}}`,
				`data: {"response":{"responseId":"diff-visible-before-thought","candidates":[{"content":{"parts":[{"text":"answer","thoughtSignature":"` + sig2 + `"}]}}]}}`,
				`data: {"response":{"responseId":"diff-visible-before-thought","candidates":[{"content":{"parts":[{"text":"thought-c","thought":true,"thoughtSignature":"` + sig3 + `"}]},"finishReason":"STOP"}]}}`,
			},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			upstreamEvents, upstreamCompleted := translateUpstreamResponsesEvents(t, tc.lines)
			oagmsgEvents, oagmsgCompleted := translateOAGMsgResponsesEvents(t, tc.lines)
			if got, want := formatNormalizedEvents(oagmsgEvents), formatNormalizedEvents(upstreamEvents); got != want {
				t.Fatalf("normalized lifecycle mismatch\noagmsg:   %s\nupstream: %s", got, want)
			}
			if got, want := formatNormalizedCompleted(oagmsgCompleted), formatNormalizedCompleted(upstreamCompleted); got != want {
				t.Fatalf("normalized completed output mismatch\noagmsg:   %s\nupstream: %s", got, want)
			}
		})
	}
}

func translateUpstreamResponsesEvents(t *testing.T, lines []string) ([]normalizedResponsesEvent, []normalizedCompletedItem) {
	t.Helper()
	var state any
	var rawEvents [][]byte
	for _, line := range lines {
		rawEvents = append(rawEvents, upstream.ConvertGeminiResponseToOpenAIResponses(context.Background(), "gemini-test", nil, nil, []byte(line), &state)...)
	}
	return normalizeResponsesEvents(t, rawEvents)
}

func translateOAGMsgResponsesEvents(t *testing.T, lines []string) ([]normalizedResponsesEvent, []normalizedCompletedItem) {
	t.Helper()
	var state any
	var rawEvents [][]byte
	for _, line := range lines {
		rawEvents = append(rawEvents, oagmsg.TranslateStream(context.Background(), oagmsg.FormatGemini, oagmsg.FormatOpenAIResponse, "gemini-test", nil, nil, []byte(line), &state)...)
	}
	return normalizeResponsesEvents(t, rawEvents)
}

func normalizeResponsesEvents(t *testing.T, rawEvents [][]byte) ([]normalizedResponsesEvent, []normalizedCompletedItem) {
	t.Helper()
	var events []normalizedResponsesEvent
	var completed []normalizedCompletedItem
	for _, raw := range rawEvents {
		event, data := parseDifferentialSSE(raw)
		switch event {
		case "response.output_item.added", "response.output_item.done":
			item := data.Get("item")
			direction, target, signature := decodeDifferentialCarrier(t, item.Get("encrypted_content").String())
			events = append(events, normalizedResponsesEvent{
				Event:     event,
				Index:     int(data.Get("output_index").Int()),
				ItemType:  item.Get("type").String(),
				Direction: direction,
				Target:    target,
				Signature: signature,
			})
		case "response.completed":
			for _, item := range data.Get("response.output").Array() {
				direction, target, signature := decodeDifferentialCarrier(t, item.Get("encrypted_content").String())
				completed = append(completed, normalizedCompletedItem{
					ItemType:  item.Get("type").String(),
					Text:      firstDifferentialText(item),
					Direction: direction,
					Target:    target,
					Signature: signature,
				})
			}
		}
	}
	return events, completed
}

func firstDifferentialText(item gjson.Result) string {
	if text := item.Get("content.0.text"); text.Exists() {
		return text.String()
	}
	if text := item.Get("summary.0.text"); text.Exists() {
		return text.String()
	}
	return ""
}

func formatNormalizedEvents(events []normalizedResponsesEvent) string {
	parts := make([]string, 0, len(events))
	for _, event := range events {
		parts = append(parts, event.Event+"#"+itoa(event.Index)+":"+event.ItemType+":"+event.Direction+":"+event.Target+":"+event.Signature)
	}
	return strings.Join(parts, "|")
}

func formatNormalizedCompleted(items []normalizedCompletedItem) string {
	parts := make([]string, 0, len(items))
	for _, item := range items {
		parts = append(parts, item.ItemType+":"+item.Text+":"+item.Direction+":"+item.Target+":"+item.Signature)
	}
	return strings.Join(parts, "|")
}

func parseDifferentialSSE(line []byte) (string, gjson.Result) {
	text := strings.TrimSpace(string(line))
	event := ""
	for _, part := range strings.Split(text, "\n") {
		if strings.HasPrefix(part, "event: ") {
			event = strings.TrimPrefix(part, "event: ")
			continue
		}
		if strings.HasPrefix(part, "data: ") {
			return event, gjson.Parse(strings.TrimPrefix(part, "data: "))
		}
	}
	return event, gjson.Result{}
}

func decodeDifferentialCarrier(t *testing.T, encrypted string) (string, string, string) {
	t.Helper()
	if encrypted == "" {
		return "", "", ""
	}
	const prefix = "cpa-gemini-responses-carrier-v1:"
	if !strings.HasPrefix(encrypted, prefix) {
		t.Fatalf("unexpected raw encrypted_content %q", encrypted)
	}
	fields := strings.SplitN(strings.TrimPrefix(encrypted, prefix), ":", 3)
	if len(fields) != 3 {
		t.Fatalf("malformed carrier %q", encrypted)
	}
	decoded, err := base64.RawStdEncoding.DecodeString(fields[2])
	if err != nil {
		t.Fatalf("malformed carrier payload %q: %v", encrypted, err)
	}
	return fields[0], fields[1], string(decoded)
}

func mutateDifferentialSignature(t *testing.T, signature string, offsetFromEnd int) string {
	t.Helper()
	raw, err := base64.StdEncoding.DecodeString(signature)
	if err != nil {
		t.Fatal(err)
	}
	offset := len(raw) + offsetFromEnd
	if offset < 0 || offset >= len(raw) {
		t.Fatalf("mutation offset %d out of range", offset)
	}
	raw[offset] ^= 1
	return base64.StdEncoding.EncodeToString(raw)
}

func itoa(v int) string {
	if v == 0 {
		return "0"
	}
	var buf [20]byte
	i := len(buf)
	for v > 0 {
		i--
		buf[i] = byte('0' + v%10)
		v /= 10
	}
	return string(buf[i:])
}
