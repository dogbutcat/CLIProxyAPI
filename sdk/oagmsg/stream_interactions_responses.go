// Package oagmsg - responsesAPISerializer implements the full OpenAI Responses API
// lifecycle event format, including content_part and output_item envelope events.
//
// This is the third InteractionsMode strategy:
//
//	InteractionsModeCodex        → response.created / response.output_text.delta / response.completed (simplified)
//	InteractionsModeSteps        → interaction.* / step.* lifecycle events
//	InteractionsModeResponsesAPI → full lifecycle with output_item / content_part envelopes
//
// Event sequence for text content:
//
//	response.created → response.in_progress →
//	  response.output_item.added (type=message) →
//	    response.content_part.added (type=output_text) →
//	      response.output_text.delta × N →
//	    response.content_part.done →
//	  response.output_item.done →
//	response.completed
//
// Event sequence for tool calls:
//
//	response.output_item.added (type=function_call) →
//	  response.function_call_arguments.delta × N →
//	  response.function_call_arguments.done →
//	response.output_item.done
package oagmsg

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"sync/atomic"

	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

// responsesSeqCounter provides process-wide unique sequence numbers.
var responsesSeqCounter uint64

func nextResponsesSeq() int {
	return int(atomic.AddUint64(&responsesSeqCounter, 1))
}

// responsesAPISerializer implements StreamSerializer for full Responses API lifecycle.
// It tracks output items (messages, function calls) and content parts within them,
// emitting the envelope events that Codex serializer omits.
type responsesAPISerializer struct {
	model      string
	fixedModel bool
	responseID string
	createdAt  int64
	seq        int // local sequence counter
	started    bool
	completed  bool
	geminiMode bool
	noDoneLine bool

	// Output item tracking.
	nextOutputIdx int
	// Message output item state (text + reasoning share one message item).
	msgOutputIdx    int
	msgItemID       string
	msgItemAdded    bool
	msgContentAdded bool // text content_part.added emitted
	msgText         strings.Builder
	// Reasoning state.
	reasoningContentAdded bool
	reasoningOutputIdx    int
	reasoningItemAdded    bool
	reasoningItemID       string
	reasoningText         strings.Builder
	reasoningDeltaBuffer  []string
	reasoningSignature    string
	pendingSignatures     []string
	seenSignatures        map[string]bool
	lastSemanticKind      string
	messageSignature      string
	// Tool output item tracking.
	toolOutputIdxByCallID map[string]int
	toolItemAdded         map[string]bool
	toolNames             map[string]string
	toolNamespaces        map[string]string
	toolArgs              map[string]*strings.Builder
	toolTypes             map[string]string
	toolItemIDs           map[string]string
	webSearchItems        map[string]*responsesWebSearchState
	// Deferred done/usage for Flush.
	finishReason         string
	usage                *UnifiedUsage
	completedOutputItems map[int]string
}

type responsesWebSearchState struct {
	outputIdx int
	id        string
	query     string
	results   string
	done      bool
}

func (s *responsesAPISerializer) SetResponsesModelOverride(model string) {
	if model == "" {
		return
	}
	s.model = model
	s.fixedModel = true
}

func newResponsesAPISerializer(model string) *responsesAPISerializer {
	return &responsesAPISerializer{
		model:                 model,
		nextOutputIdx:         0,
		msgOutputIdx:          -1,
		reasoningOutputIdx:    -1,
		seenSignatures:        make(map[string]bool),
		toolOutputIdxByCallID: make(map[string]int),
		toolItemAdded:         make(map[string]bool),
		toolNames:             make(map[string]string),
		toolNamespaces:        make(map[string]string),
		toolArgs:              make(map[string]*strings.Builder),
		toolTypes:             make(map[string]string),
		toolItemIDs:           make(map[string]string),
		webSearchItems:        make(map[string]*responsesWebSearchState),
		completedOutputItems:  make(map[int]string),
	}
}

func (s *responsesAPISerializer) nextSeq() int {
	s.seq++
	return s.seq
}

// Serialize converts a StreamDelta into full Responses API SSE event lines.
func (s *responsesAPISerializer) Serialize(delta StreamDelta) [][]byte {
	if boolExtraValue(delta.Extra, "gemini_responses_parity") {
		s.geminiMode = true
	}
	switch delta.Type {
	case EventStart:
		return s.handleStart(delta)
	case EventTextDelta:
		out := s.ensureStarted()
		return append(out, s.handleTextDelta(delta)...)
	case EventThinkingDelta:
		out := s.ensureStarted()
		return append(out, s.handleThinkingDelta(delta)...)
	case EventToolStart:
		out := s.ensureStarted()
		return append(out, s.handleToolStart(delta)...)
	case EventToolDelta:
		out := s.ensureStarted()
		return append(out, s.handleToolDelta(delta)...)
	case EventToolDone:
		out := s.ensureStarted()
		return append(out, s.handleToolDone(delta)...)
	case EventImageDelta:
		out := s.ensureStarted()
		return append(out, s.handleImageDelta(delta)...)
	case EventDone:
		if boolExtraValue(delta.Extra, "gemini_responses_parity_no_done") {
			s.noDoneLine = true
		}
		s.finishReason = delta.FinishReason
		return s.closeOpenItemsWithTools(true)
	case EventUsage:
		mergeUnifiedUsage(&s.usage, delta.Usage)
		return nil // deferred to Flush
	case EventError:
		evt := []byte(`{"type":"error","message":"","code":""}`)
		evt, _ = sjson.SetBytes(evt, "message", delta.ErrorMessage)
		evt, _ = sjson.SetBytes(evt, "code", delta.ErrorType)
		return [][]byte{formatSSEEventData("error", evt)}
	case EventPing:
		return nil
	}
	return nil
}

func (s *responsesAPISerializer) closeOpenItems() [][]byte {
	return s.closeOpenItemsWithTools(true)
}

func (s *responsesAPISerializer) closeOpenItemsWithTools(closeTools bool) [][]byte {
	var out [][]byte

	out = append(out, s.flushPendingTerminalSignature()...)
	out = append(out, s.closeReasoningItem()...)
	out = append(out, s.closeMessageItem()...)

	if !closeTools {
		return out
	}

	// Close any open tool output items in output_index order.
	callIDs := make([]string, 0, len(s.toolOutputIdxByCallID))
	for callID := range s.toolOutputIdxByCallID {
		callIDs = append(callIDs, callID)
	}
	sort.SliceStable(callIDs, func(i, j int) bool {
		return s.toolOutputIdxByCallID[callIDs[i]] < s.toolOutputIdxByCallID[callIDs[j]]
	})
	for _, callID := range callIDs {
		outputIdx := s.toolOutputIdxByCallID[callID]
		if s.toolItemAdded[callID] {
			out = append(out, s.emitCustomToolInputDone(outputIdx, callID)...)
			out = append(out, s.emitToolOutputItemDone(outputIdx, callID, s.outputItemStatus()))
			s.toolItemAdded[callID] = false
		}
	}
	return out
}

func (s *responsesAPISerializer) ensureStarted() [][]byte {
	if s.started {
		return nil
	}
	return s.handleStart(StreamDelta{})
}

// Flush emits any remaining item lifecycle events, then response.completed.
func (s *responsesAPISerializer) Flush() [][]byte {
	if !s.started || s.completed {
		return nil
	}
	partialToolInterrupted := s.finishReason == "" && s.hasOpenPartialTool()
	out := s.closeOpenItemsWithTools(!partialToolInterrupted)
	if !partialToolInterrupted {
		out = append(out, s.emitCompleted())
	}
	if !s.noDoneLine {
		out = append(out, formatDataLine(doneSignal))
	}
	s.completed = true

	return out
}

// ----------------------------------------------------------------
// Event handlers
// ----------------------------------------------------------------

func (s *responsesAPISerializer) handleStart(d StreamDelta) [][]byte {
	if s.started {
		return nil
	}
	s.started = true
	if boolExtraValue(d.Extra, "gemini_responses_parity") {
		s.geminiMode = true
	}
	if d.ID != "" {
		s.responseID = d.ID
	}
	if d.Model != "" && !s.fixedModel {
		s.model = d.Model
	}
	if d.Created > 0 {
		s.createdAt = d.Created
	}

	// response.created
	created := []byte(`{"type":"response.created","sequence_number":0,"response":{"id":"","object":"response","created_at":0,"status":"in_progress","model":""}}`)
	created, _ = sjson.SetBytes(created, "sequence_number", s.nextSeq())
	created, _ = sjson.SetBytes(created, "response.id", s.responseID)
	created, _ = sjson.SetBytes(created, "response.created_at", s.createdAt)
	created, _ = sjson.SetBytes(created, "response.model", s.model)
	if s.geminiMode {
		created, _ = sjson.SetBytes(created, "response.background", false)
		created, _ = sjson.SetRawBytes(created, "response.error", []byte("null"))
		created, _ = sjson.SetRawBytes(created, "response.output", []byte("[]"))
	}

	// response.in_progress
	inProgress := []byte(`{"type":"response.in_progress","sequence_number":0,"response":{"id":"","status":"in_progress","model":""}}`)
	inProgress, _ = sjson.SetBytes(inProgress, "sequence_number", s.nextSeq())
	inProgress, _ = sjson.SetBytes(inProgress, "response.id", s.responseID)
	inProgress, _ = sjson.SetBytes(inProgress, "response.model", s.model)
	if s.geminiMode {
		inProgress, _ = sjson.SetBytes(inProgress, "response.object", "response")
		inProgress, _ = sjson.SetBytes(inProgress, "response.created_at", s.createdAt)
		inProgress, _ = sjson.SetRawBytes(inProgress, "response.output", []byte("[]"))
	}

	return [][]byte{
		formatSSEEventData("response.created", created),
		formatSSEEventData("response.in_progress", inProgress),
	}
}

func (s *responsesAPISerializer) handleTextDelta(d StreamDelta) [][]byte {
	var out [][]byte
	if d.Signature == "" {
		d.Signature = s.popPendingSignature()
	}
	if d.Signature != "" {
		if normalized, compatible := compatibleGeminiResponsesCarrierSignature(d.Signature, geminiResponsesCarrierText); compatible {
			d.Signature = normalized
		} else {
			d.Signature = ""
		}
	}
	if d.Signature != "" && !s.reasoningItemAdded && len(s.reasoningDeltaBuffer) > 0 {
		out = append(out, s.flushPendingSignatures(geminiResponsesCarrierStandalone, geminiResponsesCarrierAny)...)
		out = append(out, s.ensureReasoningItem(d.Signature)...)
		out = append(out, s.flushReasoningDeltaBuffer()...)
		out = append(out, s.closeReasoningItem()...)
		d.Signature = ""
	}
	out = append(out, s.closeReasoningItem()...)
	if d.Signature != "" && d.Signature != s.messageSignature {
		if s.msgItemAdded && s.messageSignature != "" {
			out = append(out, s.closeMessageItem()...)
		}
		out = append(out, s.flushPendingSignatures(geminiResponsesCarrierStandalone, geminiResponsesCarrierAny)...)
		s.messageSignature = d.Signature
	} else if d.Signature == "" && s.messageSignature != "" {
		out = append(out, s.closeMessageItem()...)
		s.messageSignature = ""
	}
	out = append(out, s.ensureMessageItem()...)
	out = append(out, s.ensureTextContentPart()...)

	// response.output_text.delta
	evt := []byte(`{"type":"response.output_text.delta","sequence_number":0,"output_index":0,"content_index":0,"delta":""}`)
	evt, _ = sjson.SetBytes(evt, "sequence_number", s.nextSeq())
	evt, _ = sjson.SetBytes(evt, "output_index", s.msgOutputIdx)
	evt, _ = sjson.SetBytes(evt, "delta", d.Content)
	if s.geminiMode {
		evt, _ = sjson.SetBytes(evt, "item_id", s.msgItemID)
		evt, _ = sjson.SetRawBytes(evt, "logprobs", []byte("[]"))
	}
	out = append(out, formatSSEEventData("response.output_text.delta", evt))
	s.msgText.WriteString(d.Content)
	s.lastSemanticKind = geminiResponsesCarrierText
	return out
}

func (s *responsesAPISerializer) handleThinkingDelta(d StreamDelta) [][]byte {
	var out [][]byte
	if d.Content == "" && d.Signature != "" {
		s.pushPendingSignature(d.Signature)
		return nil
	}
	out = append(out, s.closeMessageItem()...)
	if d.Signature != "" && len(s.pendingSignatures) > 0 && s.pendingSignatures[0] != d.Signature {
		out = append(out, s.flushPendingSignatures(geminiResponsesCarrierStandalone, geminiResponsesCarrierAny)...)
	}
	if d.Signature == "" {
		d.Signature = s.popPendingSignature()
	}
	if d.Signature != "" {
		if normalized, compatible := compatibleGeminiResponsesCarrierSignature(d.Signature, geminiResponsesCarrierText); compatible {
			d.Signature = normalized
		} else {
			d.Signature = ""
		}
	}
	if s.reasoningItemAdded && d.Signature != s.reasoningSignature {
		out = append(out, s.closeReasoningItem()...)
	}
	if d.Signature == "" && !s.reasoningItemAdded {
		s.bufferReasoningDelta(d.Content)
		return out
	}
	out = append(out, s.ensureReasoningItem(d.Signature)...)
	out = append(out, s.flushReasoningDeltaBuffer()...)
	out = append(out, s.emitReasoningDelta(d.Content))
	return out
}

func (s *responsesAPISerializer) handleToolStart(d StreamDelta) [][]byte {
	if d.ToolType == streamToolTypeResponsesRawItem {
		return s.handleRawResponsesItem(d)
	}
	// Close any open message content part before starting tool.
	var out [][]byte
	if d.Signature == "" {
		d.Signature = s.popPendingSignature()
	}
	if d.Signature != "" {
		if normalized, compatible := compatibleGeminiResponsesCarrierSignature(d.Signature, geminiResponsesCarrierFunction); compatible {
			d.Signature = normalized
		} else {
			d.Signature = ""
		}
	}
	out = append(out, s.closeReasoningItem()...)
	if d.Signature != "" {
		out = append(out, s.closeMessageItem()...)
		out = append(out, s.emitDetachedReasoning(d.Signature, geminiResponsesCarrierNext, geminiResponsesCarrierFunction)...)
	} else {
		out = append(out, s.closeMessageContentPart()...)
	}
	if isServerWebSearchToolType(d.ToolType) {
		return append(out, s.handleWebSearchToolStart(d)...)
	}

	outputIdx := s.nextOutputIdx
	s.nextOutputIdx++
	s.toolOutputIdxByCallID[d.ToolCallID] = outputIdx
	s.toolItemAdded[d.ToolCallID] = true

	itemType := "function_call"
	if d.ToolType == "custom" {
		itemType = "custom_tool_call"
	}
	s.toolNames[d.ToolCallID] = d.ToolName
	if d.toolNamespace != "" {
		s.toolNamespaces[d.ToolCallID] = d.toolNamespace
	}
	s.toolTypes[d.ToolCallID] = itemType
	itemIDPrefix := "fc_"
	if itemType == "custom_tool_call" {
		itemIDPrefix = "ctc_"
	}
	s.toolItemIDs[d.ToolCallID] = fmt.Sprintf("%s%s", itemIDPrefix, d.ToolCallID)
	if s.toolArgs[d.ToolCallID] == nil {
		s.toolArgs[d.ToolCallID] = &strings.Builder{}
	}

	evt := []byte(`{"type":"response.output_item.added","sequence_number":0,"output_index":0,"item":{"id":"","type":"","status":"in_progress","call_id":"","name":""}}`)
	evt, _ = sjson.SetBytes(evt, "sequence_number", s.nextSeq())
	evt, _ = sjson.SetBytes(evt, "output_index", outputIdx)
	evt, _ = sjson.SetBytes(evt, "item.id", s.toolItemIDs[d.ToolCallID])
	evt, _ = sjson.SetBytes(evt, "item.type", itemType)
	evt, _ = sjson.SetBytes(evt, "item.call_id", d.ToolCallID)
	evt, _ = sjson.SetBytes(evt, "item.name", d.ToolName)
	if s.geminiMode {
		evt, _ = sjson.SetBytes(evt, "item.arguments", "")
	}
	if d.toolNamespace != "" {
		evt, _ = sjson.SetBytes(evt, "item.namespace", d.toolNamespace)
	}
	if itemType == "custom_tool_call" {
		evt, _ = sjson.SetBytes(evt, "item.input", "")
	}
	out = append(out, formatSSEEventData("response.output_item.added", evt))
	s.lastSemanticKind = geminiResponsesCarrierFunction
	return out
}

func (s *responsesAPISerializer) handleToolDelta(d StreamDelta) [][]byte {
	if d.ToolType == streamToolTypeServerWebSearch {
		state := s.webSearchState(d.ToolCallID, false)
		if d.ToolArgs != "" && gjson.Valid(d.ToolArgs) {
			if query := gjson.Get(d.ToolArgs, "query").String(); query != "" {
				state.query = query
			}
		}
		return nil
	}
	if isInternalResponseToolType(d.ToolType) {
		return nil
	}
	outputIdx, ok := s.toolOutputIdxByCallID[d.ToolCallID]
	if !ok {
		if strings.TrimSpace(d.ToolCallID) == "" && strings.TrimSpace(d.ToolName) == "" && strings.TrimSpace(d.ToolArgs) == "" {
			return nil
		}
		outputIdx = 0
	}

	itemType := s.toolTypes[d.ToolCallID]
	if itemType == "custom_tool_call" {
		if s.toolArgs[d.ToolCallID] == nil {
			s.toolArgs[d.ToolCallID] = &strings.Builder{}
		}
		s.toolArgs[d.ToolCallID].WriteString(d.ToolArgs)
		return nil
	}
	evt := []byte(`{"type":"response.function_call_arguments.delta","sequence_number":0,"output_index":0,"item_id":"","delta":""}`)
	evt, _ = sjson.SetBytes(evt, "sequence_number", s.nextSeq())
	evt, _ = sjson.SetBytes(evt, "output_index", outputIdx)
	evt, _ = sjson.SetBytes(evt, "item_id", s.toolItemIDs[d.ToolCallID])
	evt, _ = sjson.SetBytes(evt, "delta", d.ToolArgs)
	if s.toolArgs[d.ToolCallID] == nil {
		s.toolArgs[d.ToolCallID] = &strings.Builder{}
	}
	s.toolArgs[d.ToolCallID].WriteString(d.ToolArgs)
	return [][]byte{formatSSEEventData("response.function_call_arguments.delta", evt)}
}

func (s *responsesAPISerializer) handleToolDone(d StreamDelta) [][]byte {
	if d.ToolType == streamToolTypeServerWebSearch {
		return nil
	}
	if d.ToolType == streamToolTypeServerWebSearchResult {
		return s.emitWebSearchOutputItemDone(d.ToolCallID)
	}
	if d.ToolType == streamToolTypeResponsesRawItem {
		return nil
	}
	outputIdx, ok := s.toolOutputIdxByCallID[d.ToolCallID]
	if !ok {
		if strings.TrimSpace(d.ToolCallID) == "" && strings.TrimSpace(d.ToolName) == "" && strings.TrimSpace(d.ToolArgs) == "" {
			return nil
		}
		outputIdx = 0
	}

	var out [][]byte

	if s.toolTypes[d.ToolCallID] == "custom_tool_call" {
		if d.ToolArgs != "" {
			if s.toolArgs[d.ToolCallID] == nil {
				s.toolArgs[d.ToolCallID] = &strings.Builder{}
			}
			if s.toolArgs[d.ToolCallID].Len() == 0 {
				s.toolArgs[d.ToolCallID].WriteString(d.ToolArgs)
			}
		}
		out = append(out, s.emitCustomToolInputDone(outputIdx, d.ToolCallID)...)
	} else if d.ToolArgs != "" {
		if s.geminiMode && s.toolArgs[d.ToolCallID] != nil && s.toolArgs[d.ToolCallID].Len() == 0 {
			out = append(out, s.emitToolArgumentsDelta(outputIdx, d.ToolCallID, d.ToolArgs))
			s.toolArgs[d.ToolCallID].WriteString(d.ToolArgs)
		}
		// response.function_call_arguments.done
		done := []byte(`{"type":"response.function_call_arguments.done","sequence_number":0,"output_index":0,"arguments":""}`)
		done, _ = sjson.SetBytes(done, "sequence_number", s.nextSeq())
		done, _ = sjson.SetBytes(done, "output_index", outputIdx)
		done, _ = sjson.SetBytes(done, "item_id", s.toolItemIDs[d.ToolCallID])
		done, _ = sjson.SetBytes(done, "arguments", d.ToolArgs)
		out = append(out, formatSSEEventData("response.function_call_arguments.done", done))
		if s.toolArgs[d.ToolCallID] == nil {
			s.toolArgs[d.ToolCallID] = &strings.Builder{}
		}
		if s.toolArgs[d.ToolCallID].Len() == 0 {
			s.toolArgs[d.ToolCallID].WriteString(d.ToolArgs)
		}
	}

	// response.output_item.done for the function call.
	out = append(out, s.emitToolOutputItemDone(outputIdx, d.ToolCallID, "completed"))
	delete(s.toolItemAdded, d.ToolCallID) // Mark as closed so Flush doesn't re-close.
	return out
}

func (s *responsesAPISerializer) handleImageDelta(d StreamDelta) [][]byte {
	// Image events use the simplified format (same as Codex serializer).
	evt := []byte(`{"type":"response.image_generation_call.partial_image","sequence_number":0,"partial_image_b64":"","output_format":"","item_id":""}`)
	evt, _ = sjson.SetBytes(evt, "sequence_number", s.nextSeq())
	evt, _ = sjson.SetBytes(evt, "partial_image_b64", d.ImageData)
	evt, _ = sjson.SetBytes(evt, "output_format", d.ImageFormat)
	evt, _ = sjson.SetBytes(evt, "item_id", d.ImageItemID)
	return [][]byte{formatSSEEventData("response.image_generation_call.partial_image", evt)}
}

func (s *responsesAPISerializer) emitToolArgumentsDelta(outputIdx int, callID, args string) []byte {
	evt := []byte(`{"type":"response.function_call_arguments.delta","sequence_number":0,"output_index":0,"item_id":"","delta":""}`)
	evt, _ = sjson.SetBytes(evt, "sequence_number", s.nextSeq())
	evt, _ = sjson.SetBytes(evt, "output_index", outputIdx)
	evt, _ = sjson.SetBytes(evt, "item_id", s.toolItemIDs[callID])
	evt, _ = sjson.SetBytes(evt, "delta", args)
	return formatSSEEventData("response.function_call_arguments.delta", evt)
}

func (s *responsesAPISerializer) handleWebSearchToolStart(d StreamDelta) [][]byte {
	state := s.webSearchState(d.ToolCallID, true)
	if d.ToolType == streamToolTypeServerWebSearchResult {
		state.results = d.ToolArgs
	}
	return nil
}

func (s *responsesAPISerializer) webSearchState(id string, allocate bool) *responsesWebSearchState {
	key := id
	if key == "" {
		key = codexWebSearchDeferredFallbackID
	}
	if state := s.webSearchItems[key]; state != nil {
		return state
	}
	state := &responsesWebSearchState{outputIdx: -1, id: id}
	if allocate {
		state.outputIdx = s.nextOutputIdx
		s.nextOutputIdx++
	}
	s.webSearchItems[key] = state
	return state
}

func (s *responsesAPISerializer) emitWebSearchOutputItemDone(id string) [][]byte {
	state := s.webSearchState(id, false)
	if state == nil || state.done {
		return nil
	}
	if state.outputIdx < 0 {
		state.outputIdx = s.nextOutputIdx
		s.nextOutputIdx++
	}
	item := []byte(`{"type":"response.output_item.done","sequence_number":0,"output_index":0,"item":{"type":"web_search_call","status":"completed","action":{"type":"search","query":""}}}`)
	item, _ = sjson.SetBytes(item, "sequence_number", s.nextSeq())
	item, _ = sjson.SetBytes(item, "output_index", state.outputIdx)
	if strings.TrimSpace(state.id) != "" && !isCodexWebSearchDeferredFallbackID(state.id) {
		item, _ = sjson.SetBytes(item, "item.id", state.id)
	}
	if state.query != "" {
		item, _ = sjson.SetBytes(item, "item.action.query", state.query)
	}
	if results := responsesWebSearchResultsFromClaudeContent(state.results); len(results) > 0 {
		raw, _ := json.Marshal(results)
		item, _ = sjson.SetRawBytes(item, "item.results", raw)
	}
	s.rememberCompletedOutputFromEvent(item)
	state.done = true
	return [][]byte{formatSSEEventData("response.output_item.done", item)}
}

func responsesWebSearchResultsFromClaudeContent(raw string) []map[string]any {
	if raw == "" || !gjson.Valid(raw) {
		return nil
	}
	var out []map[string]any
	for _, result := range gjson.Parse(raw).Array() {
		url := strings.TrimSpace(result.Get("url").String())
		if url == "" {
			continue
		}
		entry := map[string]any{"url": url}
		if title := strings.TrimSpace(result.Get("title").String()); title != "" {
			entry["title"] = title
		}
		out = append(out, entry)
	}
	return out
}

func (s *responsesAPISerializer) handleRawResponsesItem(d StreamDelta) [][]byte {
	if !gjson.Valid(d.ToolArgs) {
		return nil
	}
	outputIdx := s.nextOutputIdx
	s.nextOutputIdx++
	evt := []byte(`{"type":"response.output_item.done","sequence_number":0,"output_index":0,"item":{}}`)
	evt, _ = sjson.SetBytes(evt, "sequence_number", s.nextSeq())
	evt, _ = sjson.SetBytes(evt, "output_index", outputIdx)
	evt, _ = sjson.SetRawBytes(evt, "item", []byte(d.ToolArgs))
	s.rememberCompletedOutputFromEvent(evt)
	return [][]byte{formatSSEEventData("response.output_item.done", evt)}
}

// ----------------------------------------------------------------
// Lifecycle envelope helpers
// ----------------------------------------------------------------

// ensureMessageItem emits response.output_item.added for the message if not yet emitted.
func (s *responsesAPISerializer) ensureMessageItem() [][]byte {
	if s.msgItemAdded {
		return nil
	}
	s.msgItemAdded = true
	s.msgOutputIdx = s.nextOutputIdx
	s.nextOutputIdx++
	s.msgItemID = fmt.Sprintf("msg_%s_%d", s.responseID, s.msgOutputIdx)

	evt := []byte(`{"type":"response.output_item.added","sequence_number":0,"output_index":0,"item":{"id":"","type":"message","role":"assistant","status":"in_progress","content":[]}}`)
	evt, _ = sjson.SetBytes(evt, "sequence_number", s.nextSeq())
	evt, _ = sjson.SetBytes(evt, "output_index", s.msgOutputIdx)
	evt, _ = sjson.SetBytes(evt, "item.id", s.msgItemID)
	return [][]byte{formatSSEEventData("response.output_item.added", evt)}
}

// ensureTextContentPart emits response.content_part.added for text content if not yet emitted.
func (s *responsesAPISerializer) ensureTextContentPart() [][]byte {
	if s.msgContentAdded {
		return nil
	}
	s.msgContentAdded = true

	evt := []byte(`{"type":"response.content_part.added","sequence_number":0,"output_index":0,"content_index":0,"part":{"type":"output_text","text":""}}`)
	evt, _ = sjson.SetBytes(evt, "sequence_number", s.nextSeq())
	evt, _ = sjson.SetBytes(evt, "output_index", s.msgOutputIdx)
	if s.geminiMode {
		evt, _ = sjson.SetBytes(evt, "item_id", s.msgItemID)
		evt, _ = sjson.SetRawBytes(evt, "part.annotations", []byte("[]"))
		evt, _ = sjson.SetRawBytes(evt, "part.logprobs", []byte("[]"))
	}
	return [][]byte{formatSSEEventData("response.content_part.added", evt)}
}

func (s *responsesAPISerializer) ensureReasoningItem(signature string) [][]byte {
	if signature != "" {
		if normalized, compatible := compatibleGeminiResponsesCarrierSignature(signature, geminiResponsesCarrierText); compatible {
			signature = normalized
		} else {
			signature = ""
		}
	}
	if s.reasoningItemAdded {
		if signature != "" && s.reasoningSignature == "" {
			s.reasoningSignature = signature
		}
		return nil
	}
	s.reasoningItemAdded = true
	s.reasoningOutputIdx = s.nextOutputIdx
	s.nextOutputIdx++
	s.reasoningItemID = fmt.Sprintf("rs_%s_%d", s.responseID, s.reasoningOutputIdx)
	s.reasoningSignature = signature

	evt := []byte(`{"type":"response.output_item.added","sequence_number":0,"output_index":0,"item":{"id":"","type":"reasoning","status":"in_progress","encrypted_content":"","summary":[]}}`)
	evt, _ = sjson.SetBytes(evt, "sequence_number", s.nextSeq())
	evt, _ = sjson.SetBytes(evt, "output_index", s.reasoningOutputIdx)
	evt, _ = sjson.SetBytes(evt, "item.id", s.reasoningItemID)
	if signature != "" {
		evt, _ = sjson.SetBytes(evt, "item.encrypted_content", encodeGeminiResponsesCarrier(signature, geminiResponsesCarrierStandalone, geminiResponsesCarrierText))
		s.seenSignatures[signature] = true
	}
	out := [][]byte{formatSSEEventData("response.output_item.added", evt)}
	if s.geminiMode {
		partAdded := []byte(`{"type":"response.reasoning_summary_part.added","sequence_number":0,"item_id":"","output_index":0,"summary_index":0,"part":{"type":"summary_text","text":""}}`)
		partAdded, _ = sjson.SetBytes(partAdded, "sequence_number", s.nextSeq())
		partAdded, _ = sjson.SetBytes(partAdded, "item_id", s.reasoningItemID)
		partAdded, _ = sjson.SetBytes(partAdded, "output_index", s.reasoningOutputIdx)
		out = append(out, formatSSEEventData("response.reasoning_summary_part.added", partAdded))
	}
	return out
}

func (s *responsesAPISerializer) closeReasoningItem() [][]byte {
	if !s.reasoningItemAdded {
		if len(s.reasoningDeltaBuffer) == 0 {
			return nil
		}
		out := s.ensureReasoningItem("")
		out = append(out, s.flushReasoningDeltaBuffer()...)
		out = append(out, s.closeReasoningItem()...)
		return out
	}
	var out [][]byte
	if s.reasoningContentAdded {
		doneText := []byte(`{"type":"response.reasoning_summary_text.done","sequence_number":0,"output_index":0,"summary_index":0,"text":""}`)
		doneText, _ = sjson.SetBytes(doneText, "sequence_number", s.nextSeq())
		doneText, _ = sjson.SetBytes(doneText, "output_index", s.reasoningOutputIdx)
		doneText, _ = sjson.SetBytes(doneText, "text", s.reasoningText.String())
		if s.geminiMode {
			doneText, _ = sjson.SetBytes(doneText, "item_id", s.reasoningItemID)
		}
		out = append(out, formatSSEEventData("response.reasoning_summary_text.done", doneText))
		if s.geminiMode {
			partDone := []byte(`{"type":"response.reasoning_summary_part.done","sequence_number":0,"item_id":"","output_index":0,"summary_index":0,"part":{"type":"summary_text","text":""}}`)
			partDone, _ = sjson.SetBytes(partDone, "sequence_number", s.nextSeq())
			partDone, _ = sjson.SetBytes(partDone, "item_id", s.reasoningItemID)
			partDone, _ = sjson.SetBytes(partDone, "output_index", s.reasoningOutputIdx)
			partDone, _ = sjson.SetBytes(partDone, "part.text", s.reasoningText.String())
			out = append(out, formatSSEEventData("response.reasoning_summary_part.done", partDone))
		}
		s.reasoningContentAdded = false
	}
	itemDone := []byte(`{"type":"response.output_item.done","sequence_number":0,"output_index":0,"item":{"id":"","type":"reasoning","status":"completed","encrypted_content":"","summary":[]}}`)
	if s.geminiMode {
		itemDone = []byte(`{"type":"response.output_item.done","sequence_number":0,"output_index":0,"item":{"id":"","type":"reasoning","encrypted_content":"","summary":[]}}`)
	}
	itemDone, _ = sjson.SetBytes(itemDone, "sequence_number", s.nextSeq())
	itemDone, _ = sjson.SetBytes(itemDone, "output_index", s.reasoningOutputIdx)
	itemDone, _ = sjson.SetBytes(itemDone, "item.id", s.reasoningItemID)
	if s.reasoningSignature != "" {
		itemDone, _ = sjson.SetBytes(itemDone, "item.encrypted_content", encodeGeminiResponsesCarrier(s.reasoningSignature, geminiResponsesCarrierStandalone, geminiResponsesCarrierText))
	}
	if s.reasoningText.Len() > 0 {
		itemDone, _ = sjson.SetBytes(itemDone, "item.summary.0.type", "summary_text")
		itemDone, _ = sjson.SetBytes(itemDone, "item.summary.0.text", s.reasoningText.String())
	}
	out = append(out, formatSSEEventData("response.output_item.done", itemDone))
	s.rememberCompletedOutputFromEvent(itemDone)
	s.reasoningItemAdded = false
	s.reasoningOutputIdx = -1
	s.reasoningItemID = ""
	s.reasoningSignature = ""
	s.reasoningText.Reset()
	return out
}

func (s *responsesAPISerializer) bufferReasoningDelta(delta string) {
	if delta == "" {
		return
	}
	s.reasoningDeltaBuffer = append(s.reasoningDeltaBuffer, delta)
	s.lastSemanticKind = geminiResponsesCarrierText
}

func (s *responsesAPISerializer) flushReasoningDeltaBuffer() [][]byte {
	var out [][]byte
	for _, delta := range s.reasoningDeltaBuffer {
		out = append(out, s.emitReasoningDelta(delta))
	}
	s.reasoningDeltaBuffer = nil
	return out
}

func (s *responsesAPISerializer) emitReasoningDelta(delta string) []byte {
	evt := []byte(`{"type":"response.reasoning_summary_text.delta","sequence_number":0,"output_index":0,"summary_index":0,"delta":""}`)
	evt, _ = sjson.SetBytes(evt, "sequence_number", s.nextSeq())
	evt, _ = sjson.SetBytes(evt, "output_index", s.reasoningOutputIdx)
	evt, _ = sjson.SetBytes(evt, "delta", delta)
	if s.geminiMode {
		evt, _ = sjson.SetBytes(evt, "item_id", s.reasoningItemID)
	}
	s.reasoningContentAdded = true
	s.reasoningText.WriteString(delta)
	s.lastSemanticKind = geminiResponsesCarrierText
	return formatSSEEventData("response.reasoning_summary_text.delta", evt)
}

func (s *responsesAPISerializer) closeMessageItem() [][]byte {
	var out [][]byte
	out = append(out, s.closeMessageContentPart()...)
	if s.msgItemAdded {
		signature := s.messageSignature
		out = append(out, s.emitMessageOutputItemDone(s.outputItemStatus()))
		s.msgItemAdded = false
		s.messageSignature = ""
		if signature != "" {
			out = append(out, s.emitDetachedReasoning(signature, geminiResponsesCarrierPrevious, geminiResponsesCarrierText)...)
		}
	}
	s.msgText.Reset()
	return out
}

func (s *responsesAPISerializer) closeMessageContentPart() [][]byte {
	if !s.msgContentAdded {
		return nil
	}
	s.msgContentAdded = false
	if !s.geminiMode {
		return [][]byte{s.emitContentPartDone(s.msgOutputIdx, 0, "output_text")}
	}
	out := [][]byte{s.emitOutputTextDone(s.msgOutputIdx, 0)}
	out = append(out, s.emitContentPartDone(s.msgOutputIdx, 0, "output_text"))
	return out
}

func (s *responsesAPISerializer) emitDetachedReasoning(signature, direction, targetKind string) [][]byte {
	signature = strings.TrimSpace(signature)
	if signature == "" || s.seenSignatures[signature] {
		return nil
	}
	normalized, compatible := compatibleGeminiResponsesCarrierSignature(signature, targetKind)
	if !compatible {
		return nil
	}
	signature = normalized
	if s.seenSignatures[signature] {
		return nil
	}
	s.seenSignatures[signature] = true
	outputIdx := s.nextOutputIdx
	s.nextOutputIdx++
	itemID := fmt.Sprintf("rs_%s_detached_%d", s.responseID, outputIdx)
	if s.geminiMode {
		placement := "before"
		if direction == geminiResponsesCarrierPrevious {
			placement = "after"
		}
		itemID = fmt.Sprintf("rs_%s_detached_%s_%d", s.responseID, placement, outputIdx)
	}
	carrier := encodeGeminiResponsesCarrier(signature, direction, targetKind)

	added := []byte(`{"type":"response.output_item.added","sequence_number":0,"output_index":0,"item":{"id":"","type":"reasoning","status":"in_progress","encrypted_content":"","summary":[]}}`)
	added, _ = sjson.SetBytes(added, "sequence_number", s.nextSeq())
	added, _ = sjson.SetBytes(added, "output_index", outputIdx)
	added, _ = sjson.SetBytes(added, "item.id", itemID)
	added, _ = sjson.SetBytes(added, "item.encrypted_content", carrier)

	done := []byte(`{"type":"response.output_item.done","sequence_number":0,"output_index":0,"item":{"id":"","type":"reasoning","status":"completed","encrypted_content":"","summary":[]}}`)
	if s.geminiMode {
		done = []byte(`{"type":"response.output_item.done","sequence_number":0,"output_index":0,"item":{"id":"","type":"reasoning","encrypted_content":"","summary":[]}}`)
	}
	done, _ = sjson.SetBytes(done, "sequence_number", s.nextSeq())
	done, _ = sjson.SetBytes(done, "output_index", outputIdx)
	done, _ = sjson.SetBytes(done, "item.id", itemID)
	done, _ = sjson.SetBytes(done, "item.encrypted_content", carrier)
	s.rememberCompletedOutputFromEvent(done)
	return [][]byte{
		formatSSEEventData("response.output_item.added", added),
		formatSSEEventData("response.output_item.done", done),
	}
}

func (s *responsesAPISerializer) flushPendingTerminalSignature() [][]byte {
	if len(s.pendingSignatures) == 0 {
		return nil
	}
	signatures := s.pendingSignatures
	s.pendingSignatures = nil
	var out [][]byte
	if s.reasoningItemAdded {
		out = append(out, s.closeReasoningItem()...)
		for _, signature := range signatures {
			out = append(out, s.emitDetachedReasoning(signature, geminiResponsesCarrierPrevious, geminiResponsesCarrierText)...)
		}
		return out
	}
	if s.msgItemAdded {
		out = append(out, s.closeMessageItem()...)
		for _, signature := range signatures {
			out = append(out, s.emitDetachedReasoning(signature, geminiResponsesCarrierPrevious, geminiResponsesCarrierText)...)
		}
		return out
	}
	direction, target := geminiResponsesCarrierStandalone, geminiResponsesCarrierAny
	switch s.lastSemanticKind {
	case geminiResponsesCarrierFunction:
		direction, target = geminiResponsesCarrierPrevious, geminiResponsesCarrierFunction
	case geminiResponsesCarrierText:
		direction, target = geminiResponsesCarrierPrevious, geminiResponsesCarrierText
	}
	for _, signature := range signatures {
		out = append(out, s.emitDetachedReasoning(signature, direction, target)...)
	}
	return out
}

func (s *responsesAPISerializer) pushPendingSignature(signature string) {
	signature = strings.TrimSpace(signature)
	if signature != "" {
		s.pendingSignatures = append(s.pendingSignatures, signature)
	}
}

func (s *responsesAPISerializer) popPendingSignature() string {
	if len(s.pendingSignatures) == 0 {
		return ""
	}
	signature := s.pendingSignatures[0]
	s.pendingSignatures = s.pendingSignatures[1:]
	return signature
}

func (s *responsesAPISerializer) flushPendingSignatures(direction, targetKind string) [][]byte {
	if len(s.pendingSignatures) == 0 {
		return nil
	}
	signatures := s.pendingSignatures
	s.pendingSignatures = nil
	var out [][]byte
	for _, signature := range signatures {
		out = append(out, s.emitDetachedReasoning(signature, direction, targetKind)...)
	}
	return out
}

func (s *responsesAPISerializer) emitMessageOutputItemDone(status string) []byte {
	itemDone := []byte(`{"type":"response.output_item.done","sequence_number":0,"output_index":0,"item":{"id":"","type":"message","status":"completed","role":"assistant","content":[{"type":"output_text","text":"","annotations":[],"logprobs":[]}]}}`)
	itemDone, _ = sjson.SetBytes(itemDone, "sequence_number", s.nextSeq())
	itemDone, _ = sjson.SetBytes(itemDone, "output_index", s.msgOutputIdx)
	itemDone, _ = sjson.SetBytes(itemDone, "item.id", s.msgItemID)
	itemDone, _ = sjson.SetBytes(itemDone, "item.status", status)
	itemDone, _ = sjson.SetBytes(itemDone, "item.content.0.text", s.msgText.String())
	s.rememberCompletedOutputFromEvent(itemDone)
	if s.geminiMode {
		completed := []byte(`{"id":"","type":"message","status":"completed","role":"assistant","content":[{"type":"output_text","annotations":[],"logprobs":[],"text":""}]}`)
		completed, _ = sjson.SetBytes(completed, "id", s.msgItemID)
		completed, _ = sjson.SetBytes(completed, "status", status)
		completed, _ = sjson.SetBytes(completed, "content.0.text", s.msgText.String())
		s.completedOutputItems[s.msgOutputIdx] = string(completed)
	}
	return formatSSEEventData("response.output_item.done", itemDone)
}

func (s *responsesAPISerializer) emitToolOutputItemDone(outputIdx int, callID, status string) []byte {
	itemType := s.toolTypes[callID]
	if itemType == "" {
		itemType = "function_call"
	}
	itemID := s.toolItemIDs[callID]
	if itemID == "" {
		if itemType == "custom_tool_call" {
			itemID = fmt.Sprintf("ctc_%s", callID)
		} else {
			itemID = fmt.Sprintf("fc_%s", callID)
		}
	}
	args := ""
	if s.toolArgs[callID] != nil {
		args = s.toolArgs[callID].String()
	}
	itemDone := []byte(`{"type":"response.output_item.done","sequence_number":0,"output_index":0,"item":{"id":"","type":"","status":"completed","call_id":"","name":""}}`)
	itemDone, _ = sjson.SetBytes(itemDone, "sequence_number", s.nextSeq())
	itemDone, _ = sjson.SetBytes(itemDone, "output_index", outputIdx)
	itemDone, _ = sjson.SetBytes(itemDone, "item.id", itemID)
	itemDone, _ = sjson.SetBytes(itemDone, "item.type", itemType)
	itemDone, _ = sjson.SetBytes(itemDone, "item.status", status)
	itemDone, _ = sjson.SetBytes(itemDone, "item.call_id", callID)
	itemDone, _ = sjson.SetBytes(itemDone, "item.name", s.toolNames[callID])
	if namespace := s.toolNamespaces[callID]; namespace != "" {
		itemDone, _ = sjson.SetBytes(itemDone, "item.namespace", namespace)
	}
	if itemType == "custom_tool_call" {
		itemDone, _ = sjson.SetBytes(itemDone, "item.input", unwrapCustomToolInput(args))
	} else {
		itemDone, _ = sjson.SetBytes(itemDone, "item.arguments", args)
	}
	s.rememberCompletedOutputFromEvent(itemDone)
	return formatSSEEventData("response.output_item.done", itemDone)
}

func (s *responsesAPISerializer) emitCustomToolInputDone(outputIdx int, callID string) [][]byte {
	if s.toolTypes[callID] != "custom_tool_call" {
		return nil
	}
	args := ""
	if s.toolArgs[callID] != nil {
		args = s.toolArgs[callID].String()
	}
	inputDone := []byte(`{"type":"response.custom_tool_call_input.done","sequence_number":0,"item_id":"","output_index":0,"input":""}`)
	inputDone, _ = sjson.SetBytes(inputDone, "sequence_number", s.nextSeq())
	inputDone, _ = sjson.SetBytes(inputDone, "item_id", s.toolItemIDs[callID])
	inputDone, _ = sjson.SetBytes(inputDone, "output_index", outputIdx)
	inputDone, _ = sjson.SetBytes(inputDone, "input", unwrapCustomToolInput(args))
	return [][]byte{formatSSEEventData("response.custom_tool_call_input.done", inputDone)}
}

func (s *responsesAPISerializer) rememberCompletedOutputFromEvent(event []byte) {
	item := gjson.GetBytes(event, "item")
	if item.Exists() {
		s.completedOutputItems[int(gjson.GetBytes(event, "output_index").Int())] = item.Raw
	}
}

// emitContentPartDone emits response.content_part.done.
func (s *responsesAPISerializer) emitContentPartDone(outputIdx, contentIdx int, partType string) []byte {
	evt := []byte(`{"type":"response.content_part.done","sequence_number":0,"output_index":0,"content_index":0,"part":{"type":""}}`)
	evt, _ = sjson.SetBytes(evt, "sequence_number", s.nextSeq())
	evt, _ = sjson.SetBytes(evt, "output_index", outputIdx)
	evt, _ = sjson.SetBytes(evt, "content_index", contentIdx)
	evt, _ = sjson.SetBytes(evt, "part.type", partType)
	if s.geminiMode && partType == "output_text" {
		evt, _ = sjson.SetBytes(evt, "item_id", s.msgItemID)
		evt, _ = sjson.SetRawBytes(evt, "part.annotations", []byte("[]"))
		evt, _ = sjson.SetRawBytes(evt, "part.logprobs", []byte("[]"))
		evt, _ = sjson.SetBytes(evt, "part.text", s.msgText.String())
	}
	return formatSSEEventData("response.content_part.done", evt)
}

func (s *responsesAPISerializer) emitOutputTextDone(outputIdx, contentIdx int) []byte {
	evt := []byte(`{"type":"response.output_text.done","sequence_number":0,"item_id":"","output_index":0,"content_index":0,"text":"","logprobs":[]}`)
	evt, _ = sjson.SetBytes(evt, "sequence_number", s.nextSeq())
	evt, _ = sjson.SetBytes(evt, "item_id", s.msgItemID)
	evt, _ = sjson.SetBytes(evt, "output_index", outputIdx)
	evt, _ = sjson.SetBytes(evt, "content_index", contentIdx)
	evt, _ = sjson.SetBytes(evt, "text", s.msgText.String())
	return formatSSEEventData("response.output_text.done", evt)
}

// emitOutputItemDone emits response.output_item.done.
func (s *responsesAPISerializer) emitOutputItemDone(outputIdx int, itemType string) []byte {
	evt := []byte(`{"type":"response.output_item.done","sequence_number":0,"output_index":0,"item":{"type":"","status":"completed"}}`)
	evt, _ = sjson.SetBytes(evt, "sequence_number", s.nextSeq())
	evt, _ = sjson.SetBytes(evt, "output_index", outputIdx)
	evt, _ = sjson.SetBytes(evt, "item.type", itemType)
	return formatSSEEventData("response.output_item.done", evt)
}

// emitCompleted emits response.completed with aggregated usage.
func (s *responsesAPISerializer) emitCompleted() []byte {
	status, incompleteReason, incomplete := responsesStatusForFinishReason(s.finishReason)
	eventType := "response.completed"
	if incomplete {
		eventType = "response.incomplete"
	}
	evt := []byte(`{"type":"","sequence_number":0,"response":{"id":"","object":"response","created_at":0,"status":"","model":""}}`)
	evt, _ = sjson.SetBytes(evt, "type", eventType)
	evt, _ = sjson.SetBytes(evt, "sequence_number", s.nextSeq())
	evt, _ = sjson.SetBytes(evt, "response.id", s.responseID)
	evt, _ = sjson.SetBytes(evt, "response.created_at", s.createdAt)
	evt, _ = sjson.SetBytes(evt, "response.status", status)
	evt, _ = sjson.SetBytes(evt, "response.model", s.model)
	if incomplete {
		evt, _ = sjson.SetBytes(evt, "response.incomplete_details.reason", incompleteReason)
	}
	if s.geminiMode {
		evt, _ = sjson.SetBytes(evt, "response.background", false)
		evt, _ = sjson.SetRawBytes(evt, "response.error", []byte("null"))
	}

	if s.usage != nil {
		if usageHasPrompt(s.usage) {
			evt, _ = sjson.SetBytes(evt, "response.usage.input_tokens", usagePromptForTarget(s.usage, FormatOpenAIResponse))
		}
		if usageHasCompletion(s.usage) {
			evt, _ = sjson.SetBytes(evt, "response.usage.output_tokens", s.usage.CompletionTokens)
		}
		if total, ok := usageTotalForTarget(s.usage, FormatOpenAIResponse); ok {
			evt, _ = sjson.SetBytes(evt, "response.usage.total_tokens", total)
		}
		if cached, ok := responsesCachedTokensForUsage(s.usage); ok {
			evt, _ = sjson.SetBytes(evt, "response.usage.input_tokens_details.cached_tokens", cached)
		}
		if usageHasCacheCreation(s.usage) {
			evt, _ = sjson.SetBytes(evt, "response.usage.input_tokens_details.cache_write_tokens", s.usage.CacheCreationInputTokens)
		}
		if usageHasReasoning(s.usage) {
			evt, _ = sjson.SetBytes(evt, "response.usage.output_tokens_details.reasoning_tokens", s.usage.ReasoningTokens)
		}
	}
	if len(s.completedOutputItems) > 0 {
		var outputItems []string
		for idx := 0; idx < s.nextOutputIdx; idx++ {
			if item, ok := s.completedOutputItems[idx]; ok {
				outputItems = append(outputItems, item)
			}
		}
		evt, _ = sjson.SetRawBytes(evt, "response.output", []byte("["+strings.Join(outputItems, ",")+"]"))
	}

	return formatSSEEventData(eventType, evt)
}

func (s *responsesAPISerializer) outputItemStatus() string {
	status, _, _ := responsesStatusForFinishReason(s.finishReason)
	return status
}

func (s *responsesAPISerializer) hasOpenPartialTool() bool {
	for callID, added := range s.toolItemAdded {
		if !added || s.toolTypes[callID] == "custom_tool_call" {
			continue
		}
		args := ""
		if s.toolArgs[callID] != nil {
			args = s.toolArgs[callID].String()
		}
		if args == "" || !gjson.Valid(args) {
			return true
		}
	}
	return false
}

// formatSSEEventData is defined in stream_interactions_steps.go and shared
// across all Interactions serializer variants.

func boolExtraValue(extra map[string]any, key string) bool {
	if extra == nil {
		return false
	}
	value, ok := extra[key]
	if !ok {
		return false
	}
	typed, ok := value.(bool)
	return ok && typed
}
