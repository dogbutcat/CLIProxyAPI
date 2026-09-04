package oagmsg

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"os"
	"runtime"
	"strings"
	"testing"

	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

const geminiCarrierSig1 = "EjQKMgEMOdbHO0Gd+c9Mxk4ELwPGbpCEcp2mFfYYLix2UVtBH3fL8GECc4+JITVnHF4qZDsA"

var geminiResponsesCarrierG6FixtureManifest = []struct {
	upstream  string
	local     string
	subtest   string
	exclusion string
}{
	{"ConsecutiveSignedVisibleTextPreservesEverySignature", "TestGeminiSignatureCarrierDirectG6Fixtures", "stream_consecutive_signed_visible_text", ""},
	{"NonStream_ConsecutiveSignedVisibleTextPreservesEverySignature", "TestGeminiSignatureCarrierDirectG6Fixtures", "nonstream_consecutive_signed_visible_text", ""},
	{"SignedVisibleThenUnsignedPreservesBoundary", "TestGeminiSignatureCarrierDirectG6Fixtures", "stream_signed_visible_then_unsigned", ""},
	{"LeadingCarrierDoesNotCrossSignedThought", "TestGeminiSignatureCarrierLeadingCarrierDoesNotCrossSignedThought", "", ""},
	{"NonStream_SignedVisibleThenUnsignedPreservesBoundary", "TestGeminiSignatureCarrierConsecutiveVisibleAndUnsignedBoundary", "", ""},
	{"NonStream_TrailingCarrierDirectionDoesNotDependOnID", "TestGeminiSignatureCarrierStrippedReasoningIDsKeepCarrierBinding", "", ""},
	{"TrailingCarrierDirectionSurvivesStrippedIDs", "TestGeminiSignatureCarrierStrippedReasoningIDsKeepCarrierBinding", "", ""},
	{"VisibleSignatureDoesNotOverwriteSignedThought", "TestGeminiSignatureCarrierSignedThoughtThenDifferentlySignedVisible", "", ""},
	{"FlushesVisibleSignatureBeforeLaterThought", "TestGeminiSignatureCarrierDirectG6Fixtures", "stream_visible_signature_before_later_thought", ""},
	{"FunctionAndTrailingSignaturesRoundTrip", "TestGeminiSignatureCarrierStreamAndNonStreamTerminalFunctionParity", "", ""},
	{"NonStream_FunctionAndTrailingSignaturesPreserveOrder", "TestGeminiSignatureCarrierStreamAndNonStreamTerminalFunctionParity", "", ""},
	{"FunctionThenTrailingSignatureHasStreamParity", "TestGeminiSignatureCarrierStreamAndNonStreamTerminalFunctionParity", "", ""},
	{"NonStream_TrailingSignatureFollowsPendingReasoning", "TestGeminiSignatureCarrierDirectG6Fixtures", "nonstream_trailing_signature_follows_pending_reasoning", ""},
	{"NonStream_UnsignedThoughtDoesNotStealFunctionSignature", "TestGeminiSignatureCarrierDirectG6Fixtures", "nonstream_unsigned_thought_does_not_steal_function_signature", ""},
	{"InterleavedThoughtAndTextPreservesOrder", "TestGeminiSignatureCarrierInterleavedThoughtTextFunctionOrdering", "", ""},
	{"NonStream_InterleavedThoughtAndTextPreservesOrder", "TestGeminiSignatureCarrierDirectG6Fixtures", "nonstream_interleaved_thought_and_text", ""},
	{"LeadingEmptyAndSignedTextRoundTripInOrder", "TestGeminiSignatureCarrierDirectG6Fixtures", "stream_leading_empty_then_signed_text", ""},
	{"SignedTextAndTrailingSignatureRoundTripInOrder", "TestGeminiSignatureCarrierStrippedReasoningIDsKeepCarrierBinding", "", ""},
	{"PreservesMultipleLeadingEmptySignatures", "TestGeminiSignatureCarrierDirectG6Fixtures", "stream_multiple_leading_empty_signatures", ""},
	{"NonStream_SignedTextAndTrailingSignatureRoundTripInOrder", "TestGeminiSignatureCarrierStrippedReasoningIDsKeepCarrierBinding", "", ""},
	{"DistinctSignedThoughtsUseDistinctItems", "TestGeminiSignatureCarrierDistinctSignedThoughtsUseStablePairs", "", ""},
	{"NonStream_DistinctSignedThoughtsUseDistinctItems", "TestGeminiSignatureCarrierDirectG6Fixtures", "nonstream_distinct_signed_thoughts", ""},
	{"VisibleSignatureCompletesActiveReasoning", "TestGeminiSignatureCarrierVisibleSignatureCompletesActiveReasoning", "", ""},
	{"LateThoughtSignatureIsImmutable", "TestGeminiSignatureCarrierLateThoughtSignatureImmutable", "", ""},
	{"DoneFinalizesStartedStreamExactlyOnce", "TestResponsesCarrierStreamFinalizationOnceAndBareDone", "", ""},
	{"FinishReasonThenDoneDoesNotDuplicateCompletion", "TestResponsesCarrierStreamFinalizationOnceAndBareDone", "", ""},
	{"BareDoneBeforeStartEmitsNothing", "TestResponsesCarrierStreamFinalizationOnceAndBareDone", "", ""},
	{"NonStream_VisibleSignatureCompletesReasoning", "TestGeminiSignatureCarrierVisibleSignatureCompletesActiveReasoning", "", ""},
	{"PreservesTextAroundFunction", "TestGeminiSignatureCarrierInterleavedThoughtTextFunctionOrdering", "", ""},
	{"PendingSignatureBeforeFunctionRoundTrips", "TestGeminiSignatureCarrierPendingSignatureAndSignedFunctionBoundaries", "", ""},
	{"SignedTextBeforeSignedFunctionRoundTrips", "TestGeminiSignatureCarrierDirectG6Fixtures", "stream_signed_text_before_signed_function", ""},
	{"NonStream_PreservesTextAroundSignedFunction", "TestGeminiSignatureCarrierDirectG6Fixtures", "nonstream_text_around_signed_function", ""},
	{"DetachedSignatureAfterVisibleText", "TestGeminiSignatureCarrierDirectG6Fixtures", "stream_detached_signature_after_visible_text", ""},
	{"GeminiToolSignature", "TestGeminiSignatureCarrierStreamAndNonStreamTerminalFunctionParity", "", ""},
	{"NonStream_DetachedSignature", "TestGeminiSignatureCarrierNonStreamMissingRepeatedTerminal", "", ""},
	{"ReasoningEncryptedContent", "TestGeminiSignatureCarrierLateThoughtSignatureImmutable", "", ""},
	{"ResponseOutputOrdering", "TestGeminiSignatureCarrierInterleavedThoughtTextFunctionOrdering", "", ""},
	{"UnwrapAndAggregateText", "", "", "non-G6 aggregate text/tool baseline without signature carrier"},
	{"FunctionCallEventOrder", "", "", "non-G6 ordinary tool-call lifecycle baseline without signature carrier"},
}

func TestGeminiSignatureCarrierG6FixtureManifest(t *testing.T) {
	if len(geminiResponsesCarrierG6FixtureManifest) != 39 {
		t.Fatalf("manifest count = %d, want 39", len(geminiResponsesCarrierG6FixtureManifest))
	}
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("cannot locate test source")
	}
	sourceBytes, err := os.ReadFile(file)
	if err != nil {
		t.Fatal(err)
	}
	source := string(sourceBytes)
	for _, entry := range geminiResponsesCarrierG6FixtureManifest {
		if entry.upstream == "" {
			t.Fatal("manifest entry missing upstream fixture")
		}
		if entry.local == "" && entry.exclusion == "" {
			t.Fatalf("manifest entry %q has no local test or exclusion", entry.upstream)
		}
		if entry.local != "" && entry.exclusion != "" {
			t.Fatalf("manifest entry %q has both local test and exclusion", entry.upstream)
		}
		if entry.local != "" && !strings.Contains(source, "func "+entry.local+"(") {
			t.Fatalf("manifest entry %q points to missing local test %q", entry.upstream, entry.local)
		}
		if entry.subtest != "" && !strings.Contains(source, `"`+entry.subtest+`"`) {
			t.Fatalf("manifest entry %q points to missing subtest %q", entry.upstream, entry.subtest)
		}
	}
}

func TestGeminiSignatureCarrierNoGlobalSideTable(t *testing.T) {
	source, err := os.ReadFile("signature_carrier.go")
	if err != nil {
		t.Fatal(err)
	}
	for _, forbidden := range []string{
		"sync.Map",
		"geminiResponsesOutputItemsByResponse",
		"setGeminiResponsesOutputItems",
		"geminiResponsesOutputItems(",
	} {
		if strings.Contains(string(source), forbidden) {
			t.Fatalf("global carrier side table implementation still present: %q", forbidden)
		}
	}
}

func TestGeminiSignatureCarrierStreamRequestLocalFunctionCallIDs(t *testing.T) {
	session, err := NewStreamSession(FormatGemini, FormatOpenAIResponse, "gemini-test")
	if err != nil {
		t.Fatal(err)
	}

	first, err := session.Translate([]byte(`data: {"responseId":"resp-call-session","candidates":[{"content":{"parts":[{"functionCall":{"id":"native-a","name":"run_command","args":{"command":"one"}}}],"role":"model"}}]}`))
	if err != nil {
		t.Fatal(err)
	}
	second, err := session.Translate([]byte(`data: {"candidates":[{"content":{"parts":[{"functionCall":{"id":"native-b","name":"run_command","args":{"command":"two"}}}],"role":"model"}}]}`))
	if err != nil {
		t.Fatal(err)
	}

	items := geminiFunctionCallAddedItems(t, first, second)
	if len(items) != 2 {
		t.Fatalf("function_call added items = %d, want 2; first=%q second=%q", len(items), first, second)
	}
	callID1 := items[0].Get("call_id").String()
	callID2 := items[1].Get("call_id").String()
	if callID1 != "call_resp_call_session_1" || callID2 != "call_resp_call_session_2" {
		t.Fatalf("call IDs = %q, %q; want request-local response ID seed and counter", callID1, callID2)
	}
	if callID1 == callID2 || strings.Contains(callID1+callID2, "native-") {
		t.Fatalf("call IDs are not distinct/generated: %q, %q", callID1, callID2)
	}
	if items[0].Get("id").String() != "fc_"+callID1 || items[1].Get("id").String() != "fc_"+callID2 {
		t.Fatalf("function item IDs not tied to call IDs: %s / %s", items[0].Raw, items[1].Raw)
	}
}

func TestGeminiSignatureCarrierToolFirstChunkUsesGeminiParityMode(t *testing.T) {
	session, err := NewStreamSession(FormatGemini, FormatOpenAIResponse, "gemini-test")
	if err != nil {
		t.Fatal(err)
	}

	lines, err := session.Translate([]byte(`data: {"candidates":[{"content":{"parts":[{"functionCall":{"id":"native-tool","name":"run_command","args":{"command":"true"}}}],"role":"model"}}]}`))
	if err != nil {
		t.Fatal(err)
	}

	created := lastEventDataByType(t, lines, "response.created")
	if got := created.Get("response.output").Raw; got != "[]" {
		t.Fatalf("tool-first response.created output = %q, want []", got)
	}
	items := geminiFunctionCallAddedItems(t, lines)
	if len(items) != 1 {
		t.Fatalf("function_call added items = %d, want 1; lines=%q", len(items), lines)
	}
	item := items[0]
	if !item.Get("arguments").Exists() || item.Get("arguments").String() != "" {
		t.Fatalf("tool-first function item missing Gemini parity arguments field: %s", item.Raw)
	}
	if item.Get("call_id").String() != "call_stream_1" || item.Get("id").String() != "fc_call_stream_1" {
		t.Fatalf("tool-first generated IDs malformed: %s", item.Raw)
	}
}

func TestGeminiSignatureCarrierNonStreamMissingRepeatedTerminal(t *testing.T) {
	sig := geminiCarrierSig1
	terminalSig := differentGeminiCarrierSignature(t, sig)
	raw := []byte(`{"responseId":"sig-nonstream","candidates":[{"content":{"parts":[{"text":"plain"},{"text":"signed","thoughtSignature":"` + sig + `"},{"text":"repeat","thoughtSignature":"` + sig + `"},{"text":"","thoughtSignature":"terminal-sig"}]},"finishReason":"STOP"}]}`)
	raw = []byte(strings.ReplaceAll(string(raw), "terminal-sig", terminalSig))
	out := TranslateNonStream(context.Background(), FormatGemini, FormatOpenAIResponse, "gemini-test", nil, nil, raw, nil)
	output := gjson.GetBytes(out, "output").Array()
	if len(output) != 3 {
		t.Fatalf("output item count = %d, want 3: %s", len(output), out)
	}
	firstSig, firstDirection, firstTarget := mustDecodeCarrier(t, output[0].Get("encrypted_content").String())
	if firstSig != sig || firstDirection != geminiResponsesCarrierNext || firstTarget != geminiResponsesCarrierText {
		t.Fatalf("first carrier = %q/%q/%q", firstSig, firstDirection, firstTarget)
	}
	if output[1].Get("type").String() != "message" || output[1].Get("content.0.text").String() != "plainsignedrepeat" {
		t.Fatalf("message item = %s", output[1].Raw)
	}
	gotTerminalSig, terminalDirection, terminalTarget := mustDecodeCarrier(t, output[2].Get("encrypted_content").String())
	if gotTerminalSig != terminalSig || terminalDirection != geminiResponsesCarrierPrevious || terminalTarget != geminiResponsesCarrierText {
		t.Fatalf("terminal carrier = %q/%q/%q", gotTerminalSig, terminalDirection, terminalTarget)
	}
	if strings.Count(gjson.GetBytes(out, "output").Raw, sig) > 0 {
		t.Fatalf("raw signature leaked without carrier envelope: %s", out)
	}
}

func TestGeminiSignatureCarrierStreamAndNonStreamTerminalFunctionParity(t *testing.T) {
	functionSig := geminiCarrierSig1
	terminalSig := differentGeminiCarrierSignature(t, functionSig)
	raw := []byte(`{"responseId":"sig-function","candidates":[{"content":{"parts":[{"functionCall":{"name":"run","args":{"ok":true}},"thoughtSignature":"` + functionSig + `"},{"thoughtSignature":"` + terminalSig + `"}]},"finishReason":"STOP"}]}`)
	nonStream := TranslateNonStream(context.Background(), FormatGemini, FormatOpenAIResponse, "gemini-test", nil, nil, raw, nil)
	nonStreamOutput := gjson.GetBytes(nonStream, "output").Array()
	if len(nonStreamOutput) != 3 {
		t.Fatalf("non-stream output = %s", nonStream)
	}
	nsFuncSig, nsFuncDirection, nsFuncTarget := mustDecodeCarrier(t, nonStreamOutput[0].Get("encrypted_content").String())
	nsTerminalSig, nsTerminalDirection, nsTerminalTarget := mustDecodeCarrier(t, nonStreamOutput[2].Get("encrypted_content").String())
	if nsFuncSig != functionSig || nsFuncDirection != geminiResponsesCarrierNext || nsFuncTarget != geminiResponsesCarrierFunction || nonStreamOutput[1].Get("type").String() != "function_call" {
		t.Fatalf("non-stream function carrier order malformed: %s", nonStream)
	}

	var state any
	var streamReasoning []gjson.Result
	for _, line := range TranslateStream(context.Background(), FormatGemini, FormatOpenAIResponse, "gemini-test", nil, nil, append([]byte("data: "), raw...), &state) {
		event, data := parseSignatureSSE(t, line)
		if event == "response.output_item.done" && data.Get("item.type").String() == "reasoning" {
			streamReasoning = append(streamReasoning, data.Get("item"))
		}
	}
	if len(streamReasoning) != 2 {
		t.Fatalf("stream reasoning items = %d, want 2", len(streamReasoning))
	}
	streamFuncSig, streamFuncDirection, streamFuncTarget := mustDecodeCarrier(t, streamReasoning[0].Get("encrypted_content").String())
	streamTerminalSig, streamTerminalDirection, streamTerminalTarget := mustDecodeCarrier(t, streamReasoning[1].Get("encrypted_content").String())
	if streamFuncSig != nsFuncSig || streamFuncDirection != nsFuncDirection || streamFuncTarget != nsFuncTarget {
		t.Fatalf("stream function carrier = %q/%q/%q, non-stream = %q/%q/%q", streamFuncSig, streamFuncDirection, streamFuncTarget, nsFuncSig, nsFuncDirection, nsFuncTarget)
	}
	if streamTerminalSig != nsTerminalSig || streamTerminalDirection != nsTerminalDirection || streamTerminalTarget != nsTerminalTarget {
		t.Fatalf("stream terminal carrier = %q/%q/%q, non-stream = %q/%q/%q", streamTerminalSig, streamTerminalDirection, streamTerminalTarget, nsTerminalSig, nsTerminalDirection, nsTerminalTarget)
	}
	if streamTerminalSig != terminalSig || streamTerminalDirection != geminiResponsesCarrierPrevious || streamTerminalTarget != geminiResponsesCarrierFunction {
		t.Fatalf("terminal function carrier direction = %q/%q/%q", streamTerminalSig, streamTerminalDirection, streamTerminalTarget)
	}
}

func TestGeminiSignatureCarrierStreamPureSignatureBeforeText(t *testing.T) {
	sig := geminiCarrierSig1
	lines := [][]byte{
		[]byte(`data: {"responseId":"sig-stream","candidates":[{"content":{"parts":[{"thoughtSignature":"` + sig + `"}]}}]}`),
		[]byte(`data: {"responseId":"sig-stream","candidates":[{"content":{"parts":[{"text":"answer"}]},"finishReason":"STOP"}]}`),
	}
	var state any
	var reasoning gjson.Result
	for _, raw := range lines {
		for _, line := range TranslateStream(context.Background(), FormatGemini, FormatOpenAIResponse, "gemini-test", nil, nil, raw, &state) {
			event, data := parseSignatureSSE(t, line)
			if event == "response.output_item.done" && data.Get("item.type").String() == "reasoning" {
				reasoning = data.Get("item")
			}
		}
	}
	gotSig, direction, target := mustDecodeCarrier(t, reasoning.Get("encrypted_content").String())
	if gotSig != sig || direction != geminiResponsesCarrierPrevious || target != geminiResponsesCarrierText {
		t.Fatalf("leading stream carrier = %q/%q/%q", gotSig, direction, target)
	}
}

func TestGeminiSignatureCarrierSignedThoughtThenDifferentlySignedVisible(t *testing.T) {
	sig2 := differentGeminiCarrierSignature(t, geminiCarrierSig1)
	raw := []byte(`{"responseId":"sig-thought-visible","candidates":[{"content":{"parts":[{"text":"plan","thought":true,"thoughtSignature":"` + geminiCarrierSig1 + `"},{"text":"answer","thoughtSignature":"` + sig2 + `"}]},"finishReason":"STOP"}]}`)
	nonStream := gjson.GetBytes(TranslateNonStream(context.Background(), FormatGemini, FormatOpenAIResponse, "gemini-test", nil, nil, raw, nil), "output").Array()
	if strings.Join(outputItemTypes(nonStream), ",") != "reasoning,reasoning,message" {
		t.Fatalf("non-stream output item order malformed: %v", nonStream)
	}
	assertCarrier(t, nonStream[0], geminiCarrierSig1, geminiResponsesCarrierStandalone, geminiResponsesCarrierText)
	assertCarrier(t, nonStream[1], sig2, geminiResponsesCarrierNext, geminiResponsesCarrierText)
	if nonStream[2].Get("content.0.text").String() != "answer" {
		t.Fatalf("non-stream message item = %s", nonStream[2].Raw)
	}
	stream := completedStreamOutput(t, raw)
	if strings.Join(outputItemTypes(stream), ",") != "reasoning,message,reasoning" {
		t.Fatalf("stream output item order malformed: %v", stream)
	}
	assertCarrier(t, stream[0], geminiCarrierSig1, geminiResponsesCarrierStandalone, geminiResponsesCarrierText)
	assertCarrier(t, stream[2], sig2, geminiResponsesCarrierPrevious, geminiResponsesCarrierText)
	if stream[1].Get("content.0.text").String() != "answer" {
		t.Fatalf("stream message item = %s", stream[1].Raw)
	}
}

func TestGeminiSignatureCarrierLeadingCarrierDoesNotCrossSignedThought(t *testing.T) {
	sig2 := differentGeminiCarrierSignature(t, geminiCarrierSig1)
	raw := []byte(`{"responseId":"sig-leading-thought","candidates":[{"content":{"parts":[{"thoughtSignature":"` + geminiCarrierSig1 + `"},{"text":"plan","thought":true,"thoughtSignature":"` + sig2 + `"},{"text":"answer"}]},"finishReason":"STOP"}]}`)
	for name, output := range map[string][]gjson.Result{
		"non-stream": gjson.GetBytes(TranslateNonStream(context.Background(), FormatGemini, FormatOpenAIResponse, "gemini-test", nil, nil, raw, nil), "output").Array(),
		"stream":     completedStreamOutput(t, raw),
	} {
		if len(output) != 3 {
			t.Fatalf("%s output item count = %d: %v", name, len(output), output)
		}
		leadingSig, leadingDirection, leadingTarget := mustDecodeCarrier(t, output[0].Get("encrypted_content").String())
		thoughtSig, thoughtDirection, thoughtTarget := mustDecodeCarrier(t, output[1].Get("encrypted_content").String())
		if leadingSig != geminiCarrierSig1 || leadingDirection != geminiResponsesCarrierStandalone || leadingTarget != geminiResponsesCarrierAny {
			t.Fatalf("%s leading carrier = %q/%q/%q", name, leadingSig, leadingDirection, leadingTarget)
		}
		if thoughtSig != sig2 || thoughtDirection != geminiResponsesCarrierStandalone || thoughtTarget != geminiResponsesCarrierText {
			t.Fatalf("%s thought carrier = %q/%q/%q", name, thoughtSig, thoughtDirection, thoughtTarget)
		}
	}
}

func TestGeminiSignatureCarrierConsecutiveVisibleAndUnsignedBoundary(t *testing.T) {
	sig2 := differentGeminiCarrierSignature(t, geminiCarrierSig1)
	raw := []byte(`{"responseId":"sig-visible-boundary","candidates":[{"content":{"parts":[{"text":"a"},{"text":"b","thoughtSignature":"` + geminiCarrierSig1 + `"},{"text":"c","thoughtSignature":"` + sig2 + `"},{"text":"d"}]},"finishReason":"STOP"}]}`)
	output := gjson.GetBytes(TranslateNonStream(context.Background(), FormatGemini, FormatOpenAIResponse, "gemini-test", nil, nil, raw, nil), "output").Array()
	if len(output) != 5 {
		t.Fatalf("output item count = %d: %v", len(output), output)
	}
	firstSig, firstDirection, firstTarget := mustDecodeCarrier(t, output[0].Get("encrypted_content").String())
	secondSig, secondDirection, secondTarget := mustDecodeCarrier(t, output[2].Get("encrypted_content").String())
	if firstSig != geminiCarrierSig1 || firstDirection != geminiResponsesCarrierNext || firstTarget != geminiResponsesCarrierText {
		t.Fatalf("first visible carrier = %q/%q/%q", firstSig, firstDirection, firstTarget)
	}
	if output[1].Get("content.0.text").String() != "ab" {
		t.Fatalf("first message = %s", output[1].Raw)
	}
	if secondSig != sig2 || secondDirection != geminiResponsesCarrierNext || secondTarget != geminiResponsesCarrierText {
		t.Fatalf("second visible carrier = %q/%q/%q", secondSig, secondDirection, secondTarget)
	}
	if output[3].Get("content.0.text").String() != "c" || output[4].Get("content.0.text").String() != "d" {
		t.Fatalf("signed/unsigned boundary changed: %v", output)
	}
}

func TestGeminiSignatureCarrierStrippedReasoningIDsKeepCarrierBinding(t *testing.T) {
	sig2 := differentGeminiCarrierSignature(t, geminiCarrierSig1)
	raw := []byte(`{"responseId":"sig-stripped-id","candidates":[{"content":{"parts":[{"text":"signed","thoughtSignature":"` + geminiCarrierSig1 + `"},{"text":"","thoughtSignature":"` + sig2 + `"}]},"finishReason":"STOP"}]}`)
	out := TranslateNonStream(context.Background(), FormatGemini, FormatOpenAIResponse, "gemini-test", nil, nil, raw, nil)
	output := []byte(gjson.GetBytes(out, "output").Raw)
	output, _ = sjson.DeleteBytes(output, "0.id")
	output, _ = sjson.DeleteBytes(output, "2.id")
	stripped := gjson.ParseBytes(output).Array()
	firstSig, firstDirection, firstTarget := mustDecodeCarrier(t, stripped[0].Get("encrypted_content").String())
	secondSig, secondDirection, secondTarget := mustDecodeCarrier(t, stripped[2].Get("encrypted_content").String())
	if firstSig != geminiCarrierSig1 || firstDirection != geminiResponsesCarrierNext || firstTarget != geminiResponsesCarrierText {
		t.Fatalf("stripped first carrier = %q/%q/%q", firstSig, firstDirection, firstTarget)
	}
	if secondSig != sig2 || secondDirection != geminiResponsesCarrierPrevious || secondTarget != geminiResponsesCarrierText {
		t.Fatalf("stripped terminal carrier = %q/%q/%q", secondSig, secondDirection, secondTarget)
	}
}

func TestGeminiSignatureCarrierLateThoughtSignatureImmutable(t *testing.T) {
	sig2 := differentGeminiCarrierSignature(t, geminiCarrierSig1)
	lines := []string{
		`data: {"response":{"responseId":"late-thought","candidates":[{"content":{"parts":[{"text":"one","thought":true}]}}]}}`,
		`data: {"response":{"responseId":"late-thought","candidates":[{"content":{"parts":[{"text":"two","thought":true,"thoughtSignature":"` + sig2 + `"}]},"finishReason":"STOP"}]}}`,
	}
	events := translateSignatureStreamEvents(t, lines...)
	var addedID, addedEnc, doneID, doneEnc, doneText string
	var deltas []string
	for _, evt := range events {
		switch evt.event {
		case "response.output_item.added":
			if evt.data.Get("item.type").String() == "reasoning" {
				addedID = evt.data.Get("item.id").String()
				addedEnc = evt.data.Get("item.encrypted_content").String()
			}
		case "response.reasoning_summary_text.delta":
			deltas = append(deltas, evt.data.Get("delta").String())
		case "response.output_item.done":
			if evt.data.Get("item.type").String() == "reasoning" {
				doneID = evt.data.Get("item.id").String()
				doneEnc = evt.data.Get("item.encrypted_content").String()
				doneText = evt.data.Get("item.summary.0.text").String()
			}
		}
	}
	gotSig, _, _ := mustDecodeCarrier(t, addedEnc)
	if addedID == "" || addedID != doneID || gotSig != sig2 || doneEnc != addedEnc || doneText != "onetwo" {
		t.Fatalf("late thought pair changed: added=(%q,%q) done=(%q,%q,%q)", addedID, addedEnc, doneID, doneEnc, doneText)
	}
	if strings.Join(deltas, "") != "onetwo" {
		t.Fatalf("reasoning deltas = %q, want onetwo", deltas)
	}
}

func TestGeminiSignatureCarrierVisibleSignatureCompletesActiveReasoning(t *testing.T) {
	lines := []string{
		`data: {"response":{"responseId":"active-reasoning","candidates":[{"content":{"parts":[{"text":"hidden thought","thought":true}]}}]}}`,
		`data: {"response":{"responseId":"active-reasoning","candidates":[{"content":{"parts":[{"text":"visible answer","thoughtSignature":"` + geminiCarrierSig1 + `"}]},"finishReason":"STOP"}]}}`,
	}
	for name, output := range map[string][]gjson.Result{
		"stream": signatureStreamCompletedOutput(t, lines...),
		"non-stream": gjson.GetBytes(TranslateNonStream(context.Background(), FormatGemini, FormatOpenAIResponse, "gemini-test", nil, nil,
			[]byte(`{"responseId":"active-reasoning-ns","candidates":[{"content":{"parts":[{"text":"hidden thought","thought":true},{"text":"visible answer","thoughtSignature":"`+geminiCarrierSig1+`"}]},"finishReason":"STOP"}]}`), nil), "output").Array(),
	} {
		if len(output) != 2 {
			t.Fatalf("%s output count = %d: %v", name, len(output), output)
		}
		if output[0].Get("type").String() != "reasoning" || output[0].Get("encrypted_content").String() == "" {
			t.Fatalf("%s reasoning carrier missing: %v", name, output)
		}
		sig, _, _ := mustDecodeCarrier(t, output[0].Get("encrypted_content").String())
		if output[0].Get("type").String() != "reasoning" || sig != geminiCarrierSig1 || output[0].Get("summary.0.text").String() != "hidden thought" {
			t.Fatalf("%s reasoning item malformed: %s", name, output[0].Raw)
		}
		if output[1].Get("type").String() != "message" || output[1].Get("content.0.text").String() != "visible answer" || output[1].Get("encrypted_content").Exists() {
			t.Fatalf("%s visible message malformed: %s", name, output[1].Raw)
		}
	}
}

func TestGeminiSignatureCarrierMultipleLeadingEmptyAndSignedText(t *testing.T) {
	sig2 := differentGeminiCarrierSignature(t, geminiCarrierSig1)
	output := signatureStreamCompletedOutput(t,
		`data: {"response":{"responseId":"leading-empty","candidates":[{"content":{"parts":[{"text":"","thoughtSignature":"`+geminiCarrierSig1+`"},{"text":"","thoughtSignature":"`+sig2+`"}]},"finishReason":"STOP"}]}}`,
	)
	if len(output) != 2 {
		t.Fatalf("output count = %d: %v", len(output), output)
	}
	firstSig, firstDirection, firstTarget := mustDecodeCarrier(t, output[0].Get("encrypted_content").String())
	secondSig, secondDirection, secondTarget := mustDecodeCarrier(t, output[1].Get("encrypted_content").String())
	if firstSig != geminiCarrierSig1 || firstDirection != geminiResponsesCarrierStandalone || firstTarget != geminiResponsesCarrierAny {
		t.Fatalf("first leading carrier = %q/%q/%q", firstSig, firstDirection, firstTarget)
	}
	if secondSig != sig2 || secondDirection != geminiResponsesCarrierStandalone || secondTarget != geminiResponsesCarrierAny {
		t.Fatalf("second leading carrier = %q/%q/%q", secondSig, secondDirection, secondTarget)
	}
}

func TestGeminiSignatureCarrierDistinctSignedThoughtsUseStablePairs(t *testing.T) {
	sig2 := differentGeminiCarrierSignature(t, geminiCarrierSig1)
	events := translateSignatureStreamEvents(t,
		`data: {"response":{"responseId":"distinct-thoughts","candidates":[{"content":{"parts":[{"text":"one","thought":true,"thoughtSignature":"`+geminiCarrierSig1+`"}]}}]}}`,
		`data: {"response":{"responseId":"distinct-thoughts","candidates":[{"content":{"parts":[{"text":"two","thought":true,"thoughtSignature":"`+sig2+`"}]},"finishReason":"STOP"}]}}`,
	)
	added := map[string]string{}
	done := map[string]string{}
	for _, evt := range events {
		if evt.data.Get("item.type").String() != "reasoning" {
			continue
		}
		switch evt.event {
		case "response.output_item.added":
			added[evt.data.Get("item.id").String()] = evt.data.Get("item.encrypted_content").String()
		case "response.output_item.done":
			done[evt.data.Get("item.id").String()] = evt.data.Get("item.encrypted_content").String()
		}
	}
	if len(added) != 2 || len(done) != 2 {
		t.Fatalf("reasoning pairs = %d/%d, want 2/2", len(added), len(done))
	}
	for id, enc := range added {
		if done[id] != enc {
			t.Fatalf("reasoning item %s changed encrypted_content from %q to %q", id, enc, done[id])
		}
	}
}

func TestGeminiSignatureCarrierInterleavedThoughtTextFunctionOrdering(t *testing.T) {
	sig2 := differentGeminiCarrierSignature(t, geminiCarrierSig1)
	output := signatureStreamCompletedOutput(t,
		`data: {"response":{"responseId":"interleaved-order","candidates":[{"content":{"parts":[{"text":"thought-a","thought":true,"thoughtSignature":"`+geminiCarrierSig1+`"},{"text":"answer-a"},{"text":"thought-b","thought":true,"thoughtSignature":"`+sig2+`"},{"functionCall":{"id":"native-call","name":"run_command","args":{"command":"true"}}},{"text":"answer-b"}]},"finishReason":"STOP"}]}}`,
	)
	gotTypes := outputItemTypes(output)
	if strings.Join(gotTypes, ",") != "reasoning,message,reasoning,function_call,message" {
		t.Fatalf("completed output types = %q output=%v", gotTypes, output)
	}
	if output[0].Get("summary.0.text").String() != "thought-a" || output[1].Get("content.0.text").String() != "answer-a" || output[2].Get("summary.0.text").String() != "thought-b" || output[3].Get("name").String() != "run_command" || output[4].Get("content.0.text").String() != "answer-b" {
		t.Fatalf("completed output ordering malformed: %v", output)
	}
}

func TestGeminiSignatureCarrierPendingSignatureAndSignedFunctionBoundaries(t *testing.T) {
	toolSig := differentGeminiCarrierSignature(t, geminiCarrierSig1)
	pendingOutput := signatureStreamCompletedOutput(t,
		`data: {"response":{"responseId":"pending-function","candidates":[{"content":{"parts":[{"text":"","thoughtSignature":"`+toolSig+`"}]}}]}}`,
		`data: {"response":{"responseId":"pending-function","candidates":[{"content":{"parts":[{"functionCall":{"id":"pending-call","name":"run_command","args":{"command":"true"}}}]},"finishReason":"STOP"}]}}`,
	)
	if len(pendingOutput) != 2 || pendingOutput[1].Get("type").String() != "function_call" {
		t.Fatalf("pending function output malformed: %v", pendingOutput)
	}
	gotSig, direction, target := mustDecodeCarrier(t, pendingOutput[0].Get("encrypted_content").String())
	if gotSig != toolSig || direction != geminiResponsesCarrierNext || target != geminiResponsesCarrierFunction {
		t.Fatalf("pending function carrier = %q/%q/%q", gotSig, direction, target)
	}

	mixedOutput := gjson.GetBytes(TranslateNonStream(context.Background(), FormatGemini, FormatOpenAIResponse, "gemini-test", nil, nil,
		[]byte(`{"responseId":"signed-function","candidates":[{"content":{"parts":[{"text":"preface"},{"thoughtSignature":"`+toolSig+`","functionCall":{"name":"run_command","args":{"command":"true"}}},{"text":"after"}]},"finishReason":"STOP"}]}`), nil), "output").Array()
	if strings.Join(outputItemTypes(mixedOutput), ",") != "message,reasoning,function_call,message" {
		t.Fatalf("signed function non-stream order malformed: %v", mixedOutput)
	}
}

func TestResponsesCarrierStreamFinalizationOnceAndBareDone(t *testing.T) {
	var state any
	if out := TranslateStream(context.Background(), FormatGemini, FormatOpenAIResponse, "gemini-test", nil, nil, []byte(`data: [DONE]`), &state); len(out) != 0 {
		t.Fatalf("bare DONE emitted %d events", len(out))
	}

	state = nil
	TranslateStream(context.Background(), FormatGemini, FormatOpenAIResponse, "gemini-test", nil, nil,
		[]byte(`data: {"response":{"responseId":"done-once","candidates":[{"content":{"parts":[{"text":"unsigned thought","thought":true}]}}]}}`), &state)
	out := TranslateStream(context.Background(), FormatGemini, FormatOpenAIResponse, "gemini-test", nil, nil, []byte(`data: [DONE]`), &state)
	if strings.Count(joinLines(out), `"type":"response.completed"`) != 1 || strings.Count(joinLines(out), `"type":"response.output_item.done"`) != 1 {
		t.Fatalf("DONE finalization malformed: %s", joinLines(out))
	}
	if duplicate := TranslateStream(context.Background(), FormatGemini, FormatOpenAIResponse, "gemini-test", nil, nil, []byte(`data: [DONE]`), &state); len(duplicate) != 0 {
		t.Fatalf("duplicate DONE emitted %d events", len(duplicate))
	}

	state = nil
	out = TranslateStream(context.Background(), FormatGemini, FormatOpenAIResponse, "gemini-test", nil, nil,
		[]byte(`data: {"response":{"responseId":"finish-once","candidates":[{"content":{"parts":[{"text":"answer"}]},"finishReason":"STOP"}]}}`), &state)
	if strings.Count(joinLines(out), `"type":"response.completed"`) != 1 {
		t.Fatalf("finishReason did not complete once: %s", joinLines(out))
	}
	if duplicate := TranslateStream(context.Background(), FormatGemini, FormatOpenAIResponse, "gemini-test", nil, nil, []byte(`data: [DONE]`), &state); len(duplicate) != 0 {
		t.Fatalf("post-finish DONE emitted %d events", len(duplicate))
	}
	if late := TranslateStream(context.Background(), FormatGemini, FormatOpenAIResponse, "gemini-test", nil, nil,
		[]byte(`data: {"response":{"responseId":"finish-once","candidates":[{"content":{"parts":[{"text":"late"}]}}]}}`), &state); len(late) != 0 {
		t.Fatalf("post-completion input emitted %d events", len(late))
	}
}

func TestGeminiSignatureCarrierDirectG6Fixtures(t *testing.T) {
	t.Run("stream_consecutive_signed_visible_text", func(t *testing.T) {
		sig2 := differentGeminiCarrierSignature(t, geminiCarrierSig1)
		events := translateSignatureStreamEvents(t,
			`data: {"response":{"responseId":"stream-consecutive-visible","candidates":[{"content":{"parts":[{"text":"a"},{"text":"b","thoughtSignature":"`+geminiCarrierSig1+`"}]}}]}}`,
			`data: {"response":{"responseId":"stream-consecutive-visible","candidates":[{"content":{"parts":[{"text":"c","thoughtSignature":"`+sig2+`"}]},"finishReason":"STOP"}]}}`,
		)
		assertStreamLifecycleContiguousAndStable(t, events)
		output := completedOutputFromEvents(t, events)
		assertUniqueCompletedItemIDs(t, output)
		if strings.Join(outputItemTypes(output), ",") != "message,reasoning,message,reasoning" {
			t.Fatalf("stream consecutive visible order malformed: %v", output)
		}
		assertCarrier(t, output[1], geminiCarrierSig1, geminiResponsesCarrierPrevious, geminiResponsesCarrierText)
		assertCarrier(t, output[3], sig2, geminiResponsesCarrierPrevious, geminiResponsesCarrierText)
		if output[0].Get("content.0.text").String() != "ab" || output[2].Get("content.0.text").String() != "c" {
			t.Fatalf("stream consecutive visible text malformed: %v", output)
		}
	})

	t.Run("nonstream_consecutive_signed_visible_text", func(t *testing.T) {
		sig2 := differentGeminiCarrierSignature(t, geminiCarrierSig1)
		raw := []byte(`{"responseId":"nonstream-consecutive-visible","candidates":[{"content":{"parts":[{"text":"a"},{"text":"b","thoughtSignature":"` + geminiCarrierSig1 + `"},{"text":"c","thoughtSignature":"` + sig2 + `"}]},"finishReason":"STOP"}]}`)
		output := gjson.GetBytes(TranslateNonStream(context.Background(), FormatGemini, FormatOpenAIResponse, "gemini-test", nil, nil, raw, nil), "output").Array()
		if strings.Join(outputItemTypes(output), ",") != "reasoning,message,reasoning,message" {
			t.Fatalf("non-stream consecutive visible order malformed: %v", output)
		}
		assertCarrier(t, output[0], geminiCarrierSig1, geminiResponsesCarrierNext, geminiResponsesCarrierText)
		assertCarrier(t, output[2], sig2, geminiResponsesCarrierNext, geminiResponsesCarrierText)
		if output[1].Get("content.0.text").String() != "ab" || output[3].Get("content.0.text").String() != "c" {
			t.Fatalf("non-stream consecutive visible text malformed: %v", output)
		}
	})

	t.Run("stream_signed_visible_then_unsigned", func(t *testing.T) {
		events := translateSignatureStreamEvents(t,
			`data: {"response":{"responseId":"stream-signed-unsigned","candidates":[{"content":{"parts":[{"text":"signed","thoughtSignature":"`+geminiCarrierSig1+`"}]}}]}}`,
			`data: {"response":{"responseId":"stream-signed-unsigned","candidates":[{"content":{"parts":[{"text":"unsigned"}]},"finishReason":"STOP"}]}}`,
		)
		assertStreamLifecycleContiguousAndStable(t, events)
		output := completedOutputFromEvents(t, events)
		assertUniqueCompletedItemIDs(t, output)
		if strings.Join(outputItemTypes(output), ",") != "message,reasoning,message" {
			t.Fatalf("stream signed/unsigned order malformed: %v", output)
		}
		if output[0].Get("content.0.text").String() != "signed" || output[2].Get("content.0.text").String() != "unsigned" {
			t.Fatalf("stream signed/unsigned text malformed: %v", output)
		}
		assertCarrier(t, output[1], geminiCarrierSig1, geminiResponsesCarrierPrevious, geminiResponsesCarrierText)
	})

	t.Run("nonstream_trailing_signature_follows_pending_reasoning", func(t *testing.T) {
		sig2 := differentGeminiCarrierSignature(t, geminiCarrierSig1)
		raw := []byte(`{"responseId":"reasoning-trailing-nonstream","candidates":[{"content":{"parts":[{"text":"thought","thought":true,"thoughtSignature":"` + geminiCarrierSig1 + `"},{"text":"","thoughtSignature":"` + sig2 + `"}]},"finishReason":"STOP"}]}`)
		output := gjson.GetBytes(TranslateNonStream(context.Background(), FormatGemini, FormatOpenAIResponse, "gemini-test", nil, nil, raw, nil), "output").Array()
		if strings.Join(outputItemTypes(output), ",") != "reasoning,reasoning" {
			t.Fatalf("non-stream reasoning/trailing order malformed: %v", output)
		}
		assertCarrier(t, output[0], geminiCarrierSig1, geminiResponsesCarrierStandalone, geminiResponsesCarrierText)
		assertCarrier(t, output[1], sig2, geminiResponsesCarrierPrevious, geminiResponsesCarrierText)
		if output[0].Get("summary.0.text").String() != "thought" {
			t.Fatalf("pending reasoning summary malformed: %s", output[0].Raw)
		}
	})

	t.Run("nonstream_unsigned_thought_does_not_steal_function_signature", func(t *testing.T) {
		raw := []byte(`{"responseId":"function-unsigned-thought","candidates":[{"content":{"parts":[{"thoughtSignature":"` + geminiCarrierSig1 + `","functionCall":{"name":"run_command","args":{"command":"true"}}},{"text":"later thought","thought":true}]},"finishReason":"STOP"}]}`)
		output := gjson.GetBytes(TranslateNonStream(context.Background(), FormatGemini, FormatOpenAIResponse, "gemini-test", nil, nil, raw, nil), "output").Array()
		if strings.Join(outputItemTypes(output), ",") != "reasoning,function_call,reasoning" {
			t.Fatalf("function/unsigned thought order malformed: %v", output)
		}
		assertCarrier(t, output[0], geminiCarrierSig1, geminiResponsesCarrierNext, geminiResponsesCarrierFunction)
		if output[1].Get("name").String() != "run_command" || output[2].Get("summary.0.text").String() != "later thought" || output[2].Get("encrypted_content").String() != "" {
			t.Fatalf("unsigned thought stole function signature: %v", output)
		}
	})

	t.Run("nonstream_interleaved_thought_and_text", func(t *testing.T) {
		sig2 := differentGeminiCarrierSignature(t, geminiCarrierSig1)
		raw := []byte(`{"responseId":"interleaved-nonstream","candidates":[{"content":{"parts":[{"text":"thought-a","thought":true,"thoughtSignature":"` + geminiCarrierSig1 + `"},{"text":"answer-a"},{"text":"thought-b","thought":true,"thoughtSignature":"` + sig2 + `"},{"text":"answer-b"}]},"finishReason":"STOP"}]}`)
		output := gjson.GetBytes(TranslateNonStream(context.Background(), FormatGemini, FormatOpenAIResponse, "gemini-test", nil, nil, raw, nil), "output").Array()
		if strings.Join(outputItemTypes(output), ",") != "reasoning,message,reasoning,message" {
			t.Fatalf("non-stream interleaved order malformed: %v", output)
		}
		assertCarrier(t, output[0], geminiCarrierSig1, geminiResponsesCarrierStandalone, geminiResponsesCarrierText)
		assertCarrier(t, output[2], sig2, geminiResponsesCarrierStandalone, geminiResponsesCarrierText)
		if output[0].Get("summary.0.text").String() != "thought-a" || output[1].Get("content.0.text").String() != "answer-a" || output[2].Get("summary.0.text").String() != "thought-b" || output[3].Get("content.0.text").String() != "answer-b" {
			t.Fatalf("non-stream interleaved content malformed: %v", output)
		}
	})

	t.Run("stream_leading_empty_then_signed_text", func(t *testing.T) {
		sig2 := differentGeminiCarrierSignature(t, geminiCarrierSig1)
		output := signatureStreamCompletedOutput(t,
			`data: {"response":{"responseId":"leading-empty-signed-text","candidates":[{"content":{"parts":[{"text":"","thoughtSignature":"`+geminiCarrierSig1+`"}]}}]}}`,
			`data: {"response":{"responseId":"leading-empty-signed-text","candidates":[{"content":{"parts":[{"text":"answer","thoughtSignature":"`+sig2+`"}]},"finishReason":"STOP"}]}}`,
		)
		assertUniqueCompletedItemIDs(t, output)
		if strings.Join(outputItemTypes(output), ",") != "reasoning,message,reasoning" {
			t.Fatalf("leading empty/signed text order malformed: %v", output)
		}
		assertCarrier(t, output[0], geminiCarrierSig1, geminiResponsesCarrierStandalone, geminiResponsesCarrierAny)
		assertCarrier(t, output[2], sig2, geminiResponsesCarrierPrevious, geminiResponsesCarrierText)
		if output[1].Get("content.0.text").String() != "answer" {
			t.Fatalf("signed text message malformed: %v", output)
		}
	})

	t.Run("stream_multiple_leading_empty_signatures", func(t *testing.T) {
		sig2 := differentGeminiCarrierSignature(t, geminiCarrierSig1)
		output := signatureStreamCompletedOutput(t,
			`data: {"response":{"responseId":"leading-empty","candidates":[{"content":{"parts":[{"text":"","thoughtSignature":"`+geminiCarrierSig1+`"},{"text":"","thoughtSignature":"`+sig2+`"}]},"finishReason":"STOP"}]}}`,
		)
		if strings.Join(outputItemTypes(output), ",") != "reasoning,reasoning" {
			t.Fatalf("multiple leading empty order malformed: %v", output)
		}
		assertCarrier(t, output[0], geminiCarrierSig1, geminiResponsesCarrierStandalone, geminiResponsesCarrierAny)
		assertCarrier(t, output[1], sig2, geminiResponsesCarrierStandalone, geminiResponsesCarrierAny)
	})

	t.Run("nonstream_distinct_signed_thoughts", func(t *testing.T) {
		sig2 := differentGeminiCarrierSignature(t, geminiCarrierSig1)
		raw := []byte(`{"responseId":"signed-thoughts-nonstream","candidates":[{"content":{"parts":[{"text":"one","thought":true,"thoughtSignature":"` + geminiCarrierSig1 + `"},{"text":"two","thought":true,"thoughtSignature":"` + sig2 + `"}]},"finishReason":"STOP"}]}`)
		output := gjson.GetBytes(TranslateNonStream(context.Background(), FormatGemini, FormatOpenAIResponse, "gemini-test", nil, nil, raw, nil), "output").Array()
		if strings.Join(outputItemTypes(output), ",") != "reasoning,reasoning" {
			t.Fatalf("non-stream distinct thought order malformed: %v", output)
		}
		assertCarrier(t, output[0], geminiCarrierSig1, geminiResponsesCarrierStandalone, geminiResponsesCarrierText)
		assertCarrier(t, output[1], sig2, geminiResponsesCarrierStandalone, geminiResponsesCarrierText)
		if output[0].Get("id").String() == output[1].Get("id").String() || output[0].Get("summary.0.text").String() != "one" || output[1].Get("summary.0.text").String() != "two" {
			t.Fatalf("non-stream distinct thoughts malformed: %v", output)
		}
	})

	t.Run("stream_visible_signature_before_later_thought", func(t *testing.T) {
		sig2 := differentGeminiCarrierSignature(t, geminiCarrierSig1)
		sig3 := mutateGeminiCarrierSignatureAt(t, geminiCarrierSig1, len(mustDecodeSignatureBytes(t, geminiCarrierSig1))-2)
		events := translateSignatureStreamEvents(t,
			`data: {"response":{"responseId":"visible-before-thought","candidates":[{"content":{"parts":[{"text":"thought-a","thought":true,"thoughtSignature":"`+geminiCarrierSig1+`"}]}}]}}`,
			`data: {"response":{"responseId":"visible-before-thought","candidates":[{"content":{"parts":[{"text":"answer","thoughtSignature":"`+sig2+`"}]}}]}}`,
			`data: {"response":{"responseId":"visible-before-thought","candidates":[{"content":{"parts":[{"text":"thought-c","thought":true,"thoughtSignature":"`+sig3+`"}]},"finishReason":"STOP"}]}}`,
		)
		assertStreamLifecycleContiguousAndStable(t, events)
		output := completedOutputFromEvents(t, events)
		assertUniqueCompletedItemIDs(t, output)
		if strings.Join(outputItemTypes(output), ",") != "reasoning,message,reasoning,reasoning" {
			t.Fatalf("visible-before-thought order malformed: %v", output)
		}
		assertCarrier(t, output[0], geminiCarrierSig1, geminiResponsesCarrierStandalone, geminiResponsesCarrierText)
		assertCarrier(t, output[2], sig2, geminiResponsesCarrierPrevious, geminiResponsesCarrierText)
		assertCarrier(t, output[3], sig3, geminiResponsesCarrierStandalone, geminiResponsesCarrierText)
		if output[1].Get("content.0.text").String() != "answer" || output[3].Get("summary.0.text").String() != "thought-c" {
			t.Fatalf("visible signature crossed later thought: %v", output)
		}
	})

	t.Run("stream_signed_text_before_signed_function", func(t *testing.T) {
		toolSig := differentGeminiCarrierSignature(t, geminiCarrierSig1)
		events := translateSignatureStreamEvents(t,
			`data: {"response":{"responseId":"signed-text-function","candidates":[{"content":{"parts":[{"text":"before "}]}}]}}`,
			`data: {"response":{"responseId":"signed-text-function","candidates":[{"content":{"parts":[{"text":"tool","thoughtSignature":"`+geminiCarrierSig1+`"}]}}]}}`,
			`data: {"response":{"responseId":"signed-text-function","candidates":[{"content":{"parts":[{"functionCall":{"id":"signed-func","name":"run_command","args":{"command":"true"}},"thoughtSignature":"`+toolSig+`"}]},"finishReason":"STOP"}]}}`,
		)
		assertStreamLifecycleContiguousAndStable(t, events)
		output := completedOutputFromEvents(t, events)
		assertUniqueCompletedItemIDs(t, output)
		if strings.Join(outputItemTypes(output), ",") != "message,reasoning,reasoning,function_call" {
			t.Fatalf("signed text/function order malformed: %v", output)
		}
		assertCarrier(t, output[1], geminiCarrierSig1, geminiResponsesCarrierPrevious, geminiResponsesCarrierText)
		assertCarrier(t, output[2], toolSig, geminiResponsesCarrierNext, geminiResponsesCarrierFunction)
		callID := output[3].Get("call_id").String()
		if output[0].Get("content.0.text").String() != "before tool" ||
			!strings.HasPrefix(callID, "call_") ||
			output[3].Get("id").String() != "fc_"+callID ||
			output[3].Get("name").String() != "run_command" ||
			output[3].Get("arguments").String() != `{"command":"true"}` {
			t.Fatalf("signed text/function fields malformed: %v", output)
		}
	})

	t.Run("nonstream_text_around_signed_function", func(t *testing.T) {
		raw := []byte(`{"responseId":"resp_nonstream_order","candidates":[{"content":{"parts":[{"text":"preface"},{"thoughtSignature":"` + geminiCarrierSig1 + `","functionCall":{"name":"run_command","args":{"command":"true"}}},{"text":"after"}]},"finishReason":"STOP"}]}`)
		output := gjson.GetBytes(TranslateNonStream(context.Background(), FormatGemini, FormatOpenAIResponse, "gemini-test", nil, nil, raw, nil), "output").Array()
		if strings.Join(outputItemTypes(output), ",") != "message,reasoning,function_call,message" {
			t.Fatalf("non-stream signed function order malformed: %v", output)
		}
		assertCarrier(t, output[1], geminiCarrierSig1, geminiResponsesCarrierNext, geminiResponsesCarrierFunction)
		if output[0].Get("content.0.text").String() != "preface" || output[2].Get("name").String() != "run_command" || output[2].Get("arguments").String() != `{"command":"true"}` || output[3].Get("content.0.text").String() != "after" {
			t.Fatalf("non-stream signed function fields malformed: %v", output)
		}
	})

	t.Run("stream_detached_signature_after_visible_text", func(t *testing.T) {
		events := translateSignatureStreamEvents(t,
			`data: {"response":{"responseId":"detached-after-visible","candidates":[{"content":{"parts":[{"text":"visible answer"}]}}]}}`,
			`data: {"response":{"responseId":"detached-after-visible","candidates":[{"content":{"parts":[{"text":"","thoughtSignature":"`+geminiCarrierSig1+`"}]},"finishReason":"STOP"}]}}`,
		)
		var doneTypes []string
		var completed []gjson.Result
		for _, evt := range events {
			if evt.event == "response.output_item.done" {
				doneTypes = append(doneTypes, evt.data.Get("item.type").String())
			}
			if evt.event == "response.completed" {
				completed = evt.data.Get("response.output").Array()
			}
		}
		if strings.Join(doneTypes, ",") != "message,reasoning" || strings.Join(outputItemTypes(completed), ",") != "message,reasoning" {
			t.Fatalf("detached stream order malformed: done=%v completed=%v", doneTypes, completed)
		}
		if completed[0].Get("content.0.text").String() != "visible answer" {
			t.Fatalf("detached visible message malformed: %v", completed)
		}
		assertCarrier(t, completed[1], geminiCarrierSig1, geminiResponsesCarrierPrevious, geminiResponsesCarrierText)
	})
}

func TestGeminiSignatureCarrierRejectsMalformedNestedBypassAndIncompatible(t *testing.T) {
	nested := encodeGeminiResponsesCarrier(encodeGeminiResponsesCarrier(geminiCarrierSig1, geminiResponsesCarrierNext, geminiResponsesCarrierText), geminiResponsesCarrierNext, geminiResponsesCarrierText)
	for name, sig := range map[string]string{
		"malformed":    geminiResponsesCarrierPrefix + "next:text:not-base64!",
		"nested":       nested,
		"bypass":       "skip_thought_signature_validator",
		"incompatible": "claude#" + geminiCarrierSig1,
	} {
		t.Run(name, func(t *testing.T) {
			if _, _, _, marked, ok := decodeGeminiResponsesCarrier(sig); marked && ok {
				t.Fatalf("decode accepted invalid carrier %q", sig)
			}
			raw := []byte(`{"responseId":"sig-invalid","candidates":[{"content":{"parts":[{"text":"answer","thoughtSignature":"` + sig + `"}]},"finishReason":"STOP"}]}`)
			out := TranslateNonStream(context.Background(), FormatGemini, FormatOpenAIResponse, "gemini-test", nil, nil, raw, nil)
			if strings.Contains(string(out), geminiResponsesCarrierPrefix) || strings.Contains(string(out), oagmsgResponsesOutputItemMarker) {
				t.Fatalf("invalid signature produced carrier or marker: %s", out)
			}
			if got := gjson.GetBytes(out, "output.0.content.0.text").String(); got != "answer" {
				t.Fatalf("answer dropped for invalid signature: %s", out)
			}
		})
	}
}

func TestResponsesCarrierRawMarkerDoesNotDropOrdinaryToolCallsOrLeak(t *testing.T) {
	resp := &UnifiedResponse{
		ID:      "resp_marker",
		Content: "ignored because raw output includes message",
		ToolCalls: []map[string]any{
			markedResponsesOutputItem([]byte(`{"type":"message","role":"assistant","content":[{"type":"output_text","text":"raw"}]}`)),
			{"id": "call_1", "type": "function", "function": map[string]any{"name": "calc", "arguments": `{"x":1}`}},
		},
	}
	out, err := (&InteractionsHandler{}).FormatResponse(resp, "model")
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(out), oagmsgResponsesOutputItemMarker) {
		t.Fatalf("internal marker leaked: %s", out)
	}
	if gjson.GetBytes(out, "output.0.type").String() != "message" || gjson.GetBytes(out, "output.1.type").String() != "function_call" {
		t.Fatalf("raw marker dropped ordinary tool call: %s", out)
	}

	spoofed := &UnifiedResponse{
		ID:      "resp_spoof",
		Content: "visible",
		ToolCalls: []map[string]any{{
			"type":                          "function_call",
			"call_id":                       "call_spoof",
			"name":                          "calc",
			"arguments":                     "{}",
			oagmsgResponsesOutputItemMarker: `{"type":"reasoning","encrypted_content":"spoofed","summary":[]}`,
		}},
	}
	out, err = (&InteractionsHandler{}).FormatResponse(spoofed, "model")
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(out), oagmsgResponsesOutputItemMarker) || gjson.GetBytes(out, "output.1.type").String() != "function_call" || gjson.GetBytes(out, "output.1.encrypted_content").Exists() {
		t.Fatalf("spoofed marker affected output: %s", out)
	}
}

func TestResponsesCarrierInvalidSingleKeyMarkerFallsBackWithoutEmptyOutput(t *testing.T) {
	for name, markerValue := range map[string]any{
		"empty":        "",
		"non-string":   123,
		"invalid-json": "{not-json",
	} {
		t.Run(name, func(t *testing.T) {
			resp := &UnifiedResponse{
				ID:      "resp_invalid_marker",
				Content: "visible fallback",
				ToolCalls: []map[string]any{
					{oagmsgResponsesOutputItemMarker: markerValue},
					{"id": "call_1", "type": "function", "function": map[string]any{"name": "calc", "arguments": `{"x":1}`}},
				},
			}
			out, err := (&InteractionsHandler{}).FormatResponse(resp, "model")
			if err != nil {
				t.Fatal(err)
			}
			if strings.Contains(string(out), oagmsgResponsesOutputItemMarker) {
				t.Fatalf("internal marker leaked: %s", out)
			}
			output := gjson.GetBytes(out, "output").Array()
			if len(output) != 2 {
				t.Fatalf("output count = %d, want message + tool: %s", len(output), out)
			}
			if output[0].Raw == "{}" || output[1].Raw == "{}" {
				t.Fatalf("empty object output emitted: %s", out)
			}
			if output[0].Get("type").String() != "message" || output[0].Get("content.0.text").String() != "visible fallback" {
				t.Fatalf("fallback assistant message missing: %s", out)
			}
			if output[1].Get("type").String() != "function_call" || output[1].Get("name").String() != "calc" {
				t.Fatalf("ordinary tool call missing: %s", out)
			}
		})
	}
}

func TestGeminiSignatureCarrierPrivateOutputDoesNotLeakToNonResponsesTargets(t *testing.T) {
	raw := []byte(`{"responseId":"private-carrier","candidates":[{"content":{"parts":[{"text":"answer","thoughtSignature":"` + geminiCarrierSig1 + `"}]},"finishReason":"STOP"}]}`)
	for _, target := range []Format{FormatOpenAI, FormatAnthropic, FormatGemini, FormatInteractions} {
		t.Run(string(target), func(t *testing.T) {
			out := TranslateNonStream(context.Background(), FormatGemini, target, "gemini-test", nil, nil, raw, nil)
			if strings.Contains(string(out), oagmsgResponsesOutputItemMarker) || strings.Contains(string(out), geminiResponsesCarrierPrefix) {
				t.Fatalf("private carrier leaked to %s: %s", target, out)
			}
			switch target {
			case FormatOpenAI:
				if gjson.GetBytes(out, "choices.0.message.tool_calls").Exists() {
					t.Fatalf("OpenAI Chat received synthetic tool call: %s", out)
				}
				if got := gjson.GetBytes(out, "choices.0.message.content").String(); got != "answer" {
					t.Fatalf("OpenAI Chat content = %q, want answer: %s", got, out)
				}
			case FormatAnthropic:
				if strings.Contains(string(out), `"tool_use"`) {
					t.Fatalf("Anthropic received synthetic tool use: %s", out)
				}
				if got := gjson.GetBytes(out, "content.0.text").String(); got != "answer" {
					t.Fatalf("Anthropic content = %q, want answer: %s", got, out)
				}
			case FormatGemini:
				if strings.Contains(string(out), `"functionCall"`) {
					t.Fatalf("Gemini received synthetic function call: %s", out)
				}
				if got := gjson.GetBytes(out, "candidates.0.content.parts.0.text").String(); got != "answer" {
					t.Fatalf("Gemini text = %q, want answer: %s", got, out)
				}
			case FormatInteractions:
				if strings.Contains(string(out), `"function_call"`) {
					t.Fatalf("Google Interactions received synthetic function call: %s", out)
				}
				if got := gjson.GetBytes(out, "steps.0.content.0.text").String(); got != "answer" {
					t.Fatalf("Google Interactions text = %q, want answer: %s", got, out)
				}
			}
		})
	}
}

func TestGeminiSignatureCarrierResponsesPreservesCarrierAndGenuineFunctionCall(t *testing.T) {
	functionSig := differentGeminiCarrierSignature(t, geminiCarrierSig1)
	raw := []byte(`{"responseId":"carrier-function","candidates":[{"content":{"parts":[{"text":"answer","thoughtSignature":"` + geminiCarrierSig1 + `"},{"functionCall":{"id":"native-call","name":"run_command","args":{"command":"true"}},"thoughtSignature":"` + functionSig + `"}]},"finishReason":"STOP"}]}`)

	resp, err := (&GeminiHandler{}).ParseResponse(raw)
	if err != nil {
		t.Fatal(err)
	}
	if len(resp.ToolCalls) != 1 {
		t.Fatalf("ToolCalls count = %d, want only genuine function call: %#v", len(resp.ToolCalls), resp.ToolCalls)
	}
	if _, ok := resp.ToolCalls[0][oagmsgResponsesOutputItemMarker]; ok {
		t.Fatalf("private marker stored in ToolCalls: %#v", resp.ToolCalls[0])
	}
	if got := gjson.Parse(mustMarshalMap(t, resp.ToolCalls[0])).Get("functionCall.name").String(); got != "run_command" {
		t.Fatalf("genuine function call missing: %#v", resp.ToolCalls[0])
	}

	out, err := (&InteractionsHandler{Mode: InteractionsModeResponsesAPI}).FormatResponse(resp, "gemini-test")
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(out), oagmsgResponsesOutputItemMarker) {
		t.Fatalf("private marker leaked in Responses output: %s", out)
	}
	output := gjson.GetBytes(out, "output").Array()
	if strings.Join(outputItemTypes(output), ",") != "reasoning,message,reasoning,function_call" {
		t.Fatalf("Responses carrier/function order malformed: %v", output)
	}
	assertCarrier(t, output[0], geminiCarrierSig1, geminiResponsesCarrierNext, geminiResponsesCarrierText)
	assertCarrier(t, output[2], functionSig, geminiResponsesCarrierNext, geminiResponsesCarrierFunction)
	if output[1].Get("content.0.text").String() != "answer" || output[3].Get("call_id").String() != "native-call" || output[3].Get("name").String() != "run_command" {
		t.Fatalf("Responses function call fields malformed: %v", output)
	}
}

func TestGeminiSignatureCarrierNoCarrierLeavesOrdinaryToolCalls(t *testing.T) {
	raw := []byte(`{"responseId":"no-carrier","candidates":[{"content":{"parts":[{"text":"answer"},{"functionCall":{"id":"call_1","name":"calc","args":{"x":1}}}]},"finishReason":"STOP"}]}`)
	out := TranslateNonStream(context.Background(), FormatGemini, FormatOpenAIResponse, "gemini-test", nil, nil, raw, nil)
	if strings.Contains(string(out), geminiResponsesCarrierPrefix) || strings.Contains(string(out), oagmsgResponsesOutputItemMarker) {
		t.Fatalf("carrier marker appeared without source carrier need: %s", out)
	}
	if gjson.GetBytes(out, "output.0.type").String() != "message" || gjson.GetBytes(out, "output.1.type").String() != "function_call" {
		t.Fatalf("ordinary output changed without carrier: %s", out)
	}
}

func completedStreamOutput(t *testing.T, raw []byte) []gjson.Result {
	t.Helper()
	var state any
	var completed gjson.Result
	for _, line := range TranslateStream(context.Background(), FormatGemini, FormatOpenAIResponse, "gemini-test", nil, nil, append([]byte("data: "), raw...), &state) {
		event, data := parseSignatureSSE(t, line)
		if event == "response.completed" {
			completed = data.Get("response.output")
		}
	}
	if !completed.Exists() {
		t.Fatalf("stream completed output missing for %s", raw)
	}
	return completed.Array()
}

type signatureStreamEvent struct {
	event string
	data  gjson.Result
}

func translateSignatureStreamEvents(t *testing.T, lines ...string) []signatureStreamEvent {
	t.Helper()
	var state any
	var events []signatureStreamEvent
	for _, raw := range lines {
		for _, line := range TranslateStream(context.Background(), FormatGemini, FormatOpenAIResponse, "gemini-test", nil, nil, []byte(raw), &state) {
			event, data := parseSignatureSSE(t, line)
			if event != "" {
				events = append(events, signatureStreamEvent{event: event, data: data})
			}
		}
	}
	return events
}

func signatureStreamCompletedOutput(t *testing.T, lines ...string) []gjson.Result {
	t.Helper()
	return completedOutputFromEvents(t, translateSignatureStreamEvents(t, lines...))
}

func completedOutputFromEvents(t *testing.T, events []signatureStreamEvent) []gjson.Result {
	t.Helper()
	var completed gjson.Result
	for _, evt := range events {
		if evt.event == "response.completed" {
			completed = evt.data.Get("response.output")
		}
	}
	if !completed.Exists() {
		t.Fatal("stream completed output missing")
	}
	return completed.Array()
}

func outputItemTypes(output []gjson.Result) []string {
	types := make([]string, 0, len(output))
	for _, item := range output {
		types = append(types, item.Get("type").String())
	}
	return types
}

func mustMarshalMap(t *testing.T, value map[string]any) string {
	t.Helper()
	raw, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	return string(raw)
}

func assertStreamLifecycleContiguousAndStable(t *testing.T, events []signatureStreamEvent) {
	t.Helper()
	type lifecycleItem struct {
		id        string
		itemType  string
		encrypted string
	}
	added := map[int]lifecycleItem{}
	done := map[int]lifecycleItem{}
	for _, evt := range events {
		if evt.event != "response.output_item.added" && evt.event != "response.output_item.done" {
			continue
		}
		idx := int(evt.data.Get("output_index").Int())
		item := lifecycleItem{
			id:        evt.data.Get("item.id").String(),
			itemType:  evt.data.Get("item.type").String(),
			encrypted: evt.data.Get("item.encrypted_content").String(),
		}
		if evt.event == "response.output_item.added" {
			added[idx] = item
		} else {
			done[idx] = item
		}
	}
	if len(added) == 0 || len(added) != len(done) {
		t.Fatalf("output_item added/done count mismatch: %d/%d", len(added), len(done))
	}
	for idx := 0; idx < len(added); idx++ {
		add, ok := added[idx]
		if !ok {
			t.Fatalf("missing output_item.added index %d; added=%v", idx, added)
		}
		fin, ok := done[idx]
		if !ok {
			t.Fatalf("missing output_item.done index %d; done=%v", idx, done)
		}
		if add.id == "" || add.id != fin.id || add.itemType != fin.itemType {
			t.Fatalf("unstable output item %d: added=%+v done=%+v", idx, add, fin)
		}
		if add.encrypted != fin.encrypted {
			t.Fatalf("unstable encrypted_content at index %d: added=%q done=%q", idx, add.encrypted, fin.encrypted)
		}
	}
}

func assertUniqueCompletedItemIDs(t *testing.T, output []gjson.Result) {
	t.Helper()
	seen := map[string]bool{}
	for _, item := range output {
		id := item.Get("id").String()
		if id == "" {
			t.Fatalf("completed item missing id: %s", item.Raw)
		}
		if seen[id] {
			t.Fatalf("duplicate completed item id %q in %v", id, output)
		}
		seen[id] = true
	}
}

func assertCarrier(t *testing.T, item gjson.Result, wantSignature, wantDirection, wantTarget string) {
	t.Helper()
	gotSignature, gotDirection, gotTarget := mustDecodeCarrier(t, item.Get("encrypted_content").String())
	if gotSignature != wantSignature || gotDirection != wantDirection || gotTarget != wantTarget {
		t.Fatalf("carrier = %q/%q/%q, want %q/%q/%q; item=%s", gotSignature, gotDirection, gotTarget, wantSignature, wantDirection, wantTarget, item.Raw)
	}
}

func joinLines(lines [][]byte) string {
	parts := make([]string, 0, len(lines))
	for _, line := range lines {
		parts = append(parts, string(line))
	}
	return strings.Join(parts, "\n")
}

func geminiFunctionCallAddedItems(t *testing.T, batches ...[][]byte) []gjson.Result {
	t.Helper()
	var items []gjson.Result
	for _, lines := range batches {
		for _, line := range lines {
			text := string(line)
			if !strings.HasPrefix(text, "event: response.output_item.added\n") {
				continue
			}
			item := gjson.ParseBytes(extractSSEData(line)).Get("item")
			if item.Get("type").String() == "function_call" {
				items = append(items, item)
			}
		}
	}
	return items
}

func differentGeminiCarrierSignature(t *testing.T, signature string) string {
	t.Helper()
	return mutateGeminiCarrierSignatureAt(t, signature, len(mustDecodeSignatureBytes(t, signature))-1)
}

func mutateGeminiCarrierSignatureAt(t *testing.T, signature string, offset int) string {
	t.Helper()
	raw := mustDecodeSignatureBytes(t, signature)
	if offset < 0 || offset >= len(raw) {
		t.Fatalf("signature mutation offset %d out of range", offset)
	}
	raw[offset] ^= 1
	return base64.StdEncoding.EncodeToString(raw)
}

func mustDecodeSignatureBytes(t *testing.T, signature string) []byte {
	t.Helper()
	raw, err := base64.StdEncoding.DecodeString(signature)
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

func mustDecodeCarrier(t *testing.T, encrypted string) (string, string, string) {
	t.Helper()
	signature, direction, target, marked, ok := decodeGeminiResponsesCarrier(encrypted)
	if !marked || !ok {
		t.Fatalf("invalid carrier %q marked=%v ok=%v", encrypted, marked, ok)
	}
	return signature, direction, target
}

func parseSignatureSSE(t *testing.T, line []byte) (string, gjson.Result) {
	t.Helper()
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
