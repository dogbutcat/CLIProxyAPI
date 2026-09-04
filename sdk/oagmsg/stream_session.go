package oagmsg

import (
	"bytes"
	"fmt"
	"sort"
	"strings"
)

// StreamHandler extends ProtocolHandler with SSE streaming capabilities.
// It is a separate interface (not embedded in ProtocolHandler) to avoid
// breaking existing ProtocolHandler implementors that do not support streaming.
//
// Use a type assertion to check if a handler supports streaming:
//
//	if sh, ok := handler.(StreamHandler); ok { ... }
type StreamHandler interface {
	// ParseStreamChunk parses a JSON body (data: prefix already stripped by session layer)
	// into zero or more StreamDelta events. Returns nil slice for unrecognized events.
	ParseStreamChunk(rawJSON []byte) ([]StreamDelta, error)

	// NewStreamSerializer creates a stateful serializer for the target protocol format.
	// The serializer outputs complete SSE lines (with data: or event:+data: prefix).
	NewStreamSerializer(model string) StreamSerializer
}

// StreamSerializer is a stateful writer that converts StreamDelta events into
// protocol-specific SSE output lines. Each call to Serialize() may produce
// zero or more complete SSE lines ready to write directly to HTTP response.
//
// The serializer maintains internal state (e.g., Anthropic content_block lifecycle)
// across the entire stream lifetime.
type StreamSerializer interface {
	// Serialize converts a single StreamDelta into zero or more SSE output lines.
	// Returns nil if the delta produces no output for this protocol.
	Serialize(delta StreamDelta) [][]byte

	// Flush produces any remaining SSE lines needed to properly close the stream
	// (e.g., Anthropic message_stop, OpenAI [DONE]).
	Flush() [][]byte
}

type responsesModelOverrideSerializer interface {
	SetResponsesModelOverride(model string)
}

type terminalUsageStreamSerializer interface {
	SerializeTerminalUsage(done, usage StreamDelta) [][]byte
}

type streamTerminalPolicy struct {
	deferDoneUntilUsageOrEOF bool
	suppressOpenAIDoneAfter  bool
}

type deferredStreamTerminal struct {
	seenOutput bool
	sawTool    bool
	emitted    bool
	pending    StreamDelta
	hasPending bool
}

// SessionOption configures optional behavior for a StreamTranslateSession.
type SessionOption func(*StreamTranslateSession)

// WithContext attaches a TranslationContext to the session.
// When present, the session applies middleware between parse and serialize:
//   - Tool name restoration via ToolNameReverse
//   - SawToolCall tracking for finish_reason override
//   - Image deduplication via sha256 hash
func WithContext(ctx *TranslationContext) SessionOption {
	return func(s *StreamTranslateSession) {
		s.ctx = ctx
	}
}

// StreamTranslateSession holds the source parser and target serializer state
// for a single streaming request. It handles SSE transport concerns:
//   - Stripping "data: " prefix from raw SSE lines
//   - Skipping "event: " lines and empty lines
//   - Detecting "[DONE]" terminal signal → triggers Flush()
//   - Conditional "data: " prefix handling (Gemini may or may not have it)
//   - Parsing JSON body via source handler's ParseStreamChunk
//   - Serializing each StreamDelta via target serializer's Serialize
//   - (Optional) Context-driven middleware: tool name restore, SawToolCall, image dedup, tool call dedup
type StreamTranslateSession struct {
	sourceHandler       StreamHandler
	serializer          StreamSerializer
	targetFormat        Format
	flushed             bool
	flushOnDone         bool
	terminalPolicy      streamTerminalPolicy
	deferredTerminal    deferredStreamTerminal
	ctx                 *TranslationContext       // nil = no middleware
	toolCallStates      map[string]*toolCallState // keyed by ToolCallID
	pendingFinishReason string
	pendingStopSequence string
	parseState          streamParseState
}

type streamParseState struct {
	webSearch            codexWebSearchState
	antigravityWebSearch antigravityWebSearchStreamState
	textDeltaSeen        bool
	gemini               geminiStreamParseState
}

type geminiStreamParseState struct {
	responseID          string
	functionCallCounter int
}

type statefulStreamParser interface {
	parseStreamChunkWithState(rawJSON []byte, state *streamParseState) ([]StreamDelta, error)
}

type contextStatefulStreamParser interface {
	parseStreamChunkWithContext(rawJSON []byte, state *streamParseState, ctx *TranslationContext) ([]StreamDelta, error)
}

// toolCallState tracks the lifecycle of a single tool call across multiple SSE events.
// Mirrors upstream Codex translator's toolCallStreamState for deduplication.
type toolCallState struct {
	// announced is true once EventToolStart was emitted for this tool.
	announced bool
	// argumentsEmitted is true once any EventToolDelta was emitted (args streamed incrementally).
	argumentsEmitted bool
	// done is true once the tool call lifecycle is complete.
	done      bool
	callID    string
	name      string
	namespace string
	// toolType is "function" or "custom" after request-scoped classification.
	toolType string
	index    int
	// syntheticID is true when Responses metadata middleware created a fallback
	// ID before the upstream sent a stable call ID.
	syntheticID bool
	arguments   strings.Builder
}

var (
	dataPrefix  = []byte("data: ")
	dataPrefix2 = []byte("data:")
	eventPrefix = []byte("event: ")
	doneSignal  = []byte("[DONE]")
)

// NewStreamSession creates a session for translating SSE chunks from source to target format.
// It resolves format aliases and verifies both handlers implement StreamHandler.
// Optional SessionOption values can be passed to attach a TranslationContext.
func NewStreamSession(source, target Format, model string, opts ...SessionOption) (*StreamTranslateSession, error) {
	reg := DefaultRegistry()

	srcHandler, ok := reg.Get(resolveFormat(source))
	if !ok {
		return nil, fmt.Errorf("oagmsg: no handler for source format %q", source)
	}
	srcStream, ok := srcHandler.(StreamHandler)
	if !ok {
		return nil, fmt.Errorf("oagmsg: source handler %q does not support streaming", source)
	}

	tgtHandler, ok := reg.Get(resolveFormat(target))
	if !ok {
		return nil, fmt.Errorf("oagmsg: no handler for target format %q", target)
	}
	tgtStream, ok := tgtHandler.(StreamHandler)
	if !ok {
		return nil, fmt.Errorf("oagmsg: target handler %q does not support streaming", target)
	}

	sourceFormat := resolveFormat(source)
	targetFormat := resolveFormat(target)
	terminalPolicy := selectStreamTerminalPolicy(sourceFormat, targetFormat)
	s := &StreamTranslateSession{
		sourceHandler: srcStream,
		serializer:    tgtStream.NewStreamSerializer(model),
		targetFormat:  targetFormat,
		// OpenAI Chat can emit a usage-only chunk after finish_reason and closes
		// explicitly with [DONE]. Other supported protocols end at EventDone.
		flushOnDone:    sourceFormat != FormatOpenAI && !terminalPolicy.deferDoneUntilUsageOrEOF,
		terminalPolicy: terminalPolicy,
	}
	for _, opt := range opts {
		opt(s)
	}
	if s.ctx != nil && resolveFormat(target) == FormatOpenAIResponse && s.ctx.responsesModel != "" {
		if serializer, ok := s.serializer.(responsesModelOverrideSerializer); ok {
			serializer.SetResponsesModelOverride(s.ctx.responsesModel)
		}
	}
	return s, nil
}

// Translate processes a single raw SSE line (as received from bufio.Scanner in the executor).
// It handles transport-level concerns before delegating to parser and serializer.
func (s *StreamTranslateSession) Translate(rawSSELine []byte) ([][]byte, error) {
	if s.flushed {
		return nil, nil
	}

	line := bytes.TrimSpace(rawSSELine)

	// Skip empty lines (SSE event separator).
	if len(line) == 0 {
		return nil, nil
	}

	// Skip "event: " lines — the event type is inside the JSON "type" field
	// for protocols that use it (Anthropic, Codex).
	if bytes.HasPrefix(line, eventPrefix) {
		return nil, nil
	}

	// Strip "data: " or "data:" prefix.
	jsonBody := line
	if bytes.HasPrefix(line, dataPrefix) {
		jsonBody = bytes.TrimSpace(line[len(dataPrefix):])
	} else if bytes.HasPrefix(line, dataPrefix2) {
		jsonBody = bytes.TrimSpace(line[len(dataPrefix2):])
	}

	// Detect [DONE] terminal signal → flush and close.
	if bytes.Equal(jsonBody, doneSignal) {
		s.flushed = true
		var outputs [][]byte
		outputs = append(outputs, s.flushDeferredTerminal()...)
		if !s.skipSerializerFlushOnDone() {
			outputs = append(outputs, s.serializer.Flush()...)
		}
		return outputs, nil
	}

	// Skip non-JSON lines (e.g., SSE comments starting with ":").
	if len(jsonBody) == 0 || jsonBody[0] != '{' && jsonBody[0] != '[' {
		return nil, nil
	}

	// Parse JSON body into StreamDelta events.
	var deltas []StreamDelta
	var err error
	if parser, ok := s.sourceHandler.(contextStatefulStreamParser); ok {
		deltas, err = parser.parseStreamChunkWithContext(jsonBody, &s.parseState, s.ctx)
	} else if parser, ok := s.sourceHandler.(statefulStreamParser); ok {
		deltas, err = parser.parseStreamChunkWithState(jsonBody, &s.parseState)
	} else {
		deltas, err = s.sourceHandler.ParseStreamChunk(jsonBody)
	}
	if err != nil {
		return nil, fmt.Errorf("oagmsg: stream parse error: %w", err)
	}
	if len(deltas) == 0 {
		return nil, nil
	}

	// Apply context-driven middleware (tool name restore, SawToolCall, image dedup).
	if s.ctx != nil {
		deltas = s.applyMiddleware(deltas)
		if len(deltas) == 0 {
			return nil, nil
		}
	}

	// Serialize each delta through the target serializer.
	var outputs [][]byte
	sawDone := false
	for _, delta := range deltas {
		s.captureStreamMetadata(delta)
		if delta.Type == EventDone {
			delta = s.prepareDoneDelta(delta)
			if s.deferDoneUntilUsageOrEOF() {
				s.deferredTerminal.pending = delta
				s.deferredTerminal.hasPending = true
				continue
			}
			sawDone = true
		}
		if s.deferDoneUntilUsageOrEOF() {
			s.observeDeferredTerminal(delta)
			if delta.Type == EventUsage {
				outputs = append(outputs, s.serializeUsageWithDeferredTerminal(delta)...)
				continue
			}
		}
		chunks := s.serializer.Serialize(delta)
		if len(chunks) > 0 {
			outputs = append(outputs, chunks...)
		}
	}
	if sawDone && s.flushOnDone {
		s.flushed = true
		outputs = append(outputs, s.serializer.Flush()...)
	}
	return outputs, nil
}

func selectStreamTerminalPolicy(source, target Format) streamTerminalPolicy {
	switch source {
	case FormatGemini, FormatAntigravity:
	default:
		return streamTerminalPolicy{}
	}
	if target == FormatOpenAIResponse || target == FormatCodex {
		return streamTerminalPolicy{}
	}
	return streamTerminalPolicy{
		deferDoneUntilUsageOrEOF: true,
		suppressOpenAIDoneAfter:  target == FormatOpenAI,
	}
}

func (s *StreamTranslateSession) deferDoneUntilUsageOrEOF() bool {
	return s.terminalPolicy.deferDoneUntilUsageOrEOF
}

func (s *StreamTranslateSession) skipSerializerFlushOnDone() bool {
	return s.terminalPolicy.suppressOpenAIDoneAfter && s.deferredTerminal.emitted
}

func (s *StreamTranslateSession) prepareDoneDelta(delta StreamDelta) StreamDelta {
	if s.pendingFinishReason != "" && (delta.FinishReason == "" || delta.FinishReason == "stop") {
		delta.FinishReason = s.pendingFinishReason
	}
	if s.pendingStopSequence != "" && delta.StopSequence == "" {
		delta.StopSequence = s.pendingStopSequence
	}
	return delta
}

func (s *StreamTranslateSession) observeDeferredTerminal(delta StreamDelta) {
	switch delta.Type {
	case EventTextDelta, EventThinkingDelta:
		if delta.Content != "" || delta.Signature != "" {
			s.deferredTerminal.seenOutput = true
		}
	case EventToolStart, EventToolDelta, EventToolDone:
		if !isInternalResponseToolType(delta.ToolType) {
			s.deferredTerminal.seenOutput = true
			s.deferredTerminal.sawTool = true
		}
	case EventImageDelta:
		if delta.ImageData != "" {
			s.deferredTerminal.seenOutput = true
		}
	}
}

func (s *StreamTranslateSession) serializeUsageWithDeferredTerminal(usage StreamDelta) [][]byte {
	done, ok := s.nextDeferredTerminal()
	if !ok {
		return s.serializer.Serialize(usage)
	}
	if serializer, ok := s.serializer.(terminalUsageStreamSerializer); ok {
		return serializer.SerializeTerminalUsage(done, usage)
	}
	var outputs [][]byte
	outputs = append(outputs, s.serializer.Serialize(done)...)
	outputs = append(outputs, s.serializer.Serialize(usage)...)
	return outputs
}

func (s *StreamTranslateSession) flushDeferredTerminal() [][]byte {
	done, ok := s.nextDeferredTerminal()
	if !ok {
		return nil
	}
	return s.serializer.Serialize(done)
}

func (s *StreamTranslateSession) nextDeferredTerminal() (StreamDelta, bool) {
	if !s.deferDoneUntilUsageOrEOF() || s.deferredTerminal.emitted {
		return StreamDelta{}, false
	}
	if s.deferredTerminal.hasPending {
		done := s.deferredTerminal.pending
		s.deferredTerminal.pending = StreamDelta{}
		s.deferredTerminal.hasPending = false
		s.deferredTerminal.emitted = true
		return done, true
	}
	if !s.deferredTerminal.seenOutput {
		return StreamDelta{}, false
	}
	finishReason := "stop"
	if s.deferredTerminal.sawTool {
		finishReason = "tool_calls"
	}
	s.deferredTerminal.emitted = true
	return StreamDelta{Type: EventDone, FinishReason: finishReason}, true
}

func (s *StreamTranslateSession) captureStreamMetadata(delta StreamDelta) {
	if delta.Extra == nil {
		return
	}
	if reason := stringValue(delta.Extra["anthropic_stop_reason"]); reason != "" {
		s.pendingFinishReason = reason
	}
	if stopSequence := stringValue(delta.Extra["anthropic_stop_sequence"]); stopSequence != "" {
		s.pendingStopSequence = stopSequence
	}
}

// applyMiddleware processes deltas through context-driven transformations.
// Called only when s.ctx != nil.
func (s *StreamTranslateSession) applyMiddleware(deltas []StreamDelta) []StreamDelta {
	if !s.ctx.responseToolMetadataApplicable() {
		return s.applyLegacyMiddleware(deltas)
	}
	var result []StreamDelta
	for _, d := range deltas {
		switch d.Type {
		case EventStart:
			result = append(result, d)
			continue

		case EventToolStart:
			if isInternalResponseToolType(d.ToolType) {
				result = append(result, d)
				continue
			}
			result = append(result, s.acceptToolStartDelta(d)...)
			continue

		case EventToolDelta:
			if isInternalResponseToolType(d.ToolType) {
				result = append(result, d)
				continue
			}
			result = append(result, s.acceptToolArgumentDelta(d)...)
			continue

		case EventToolDone:
			if isInternalResponseToolType(d.ToolType) {
				result = append(result, d)
				continue
			}
			result = append(result, s.acceptToolDoneDelta(d)...)
			continue

		case EventDone:
			result = append(result, s.flushPendingToolStates()...)
			// Override finish_reason if tool calls were seen.
			d.FinishReason = s.ctx.EffectiveFinishReason(d.FinishReason)

		case EventImageDelta:
			// Deduplicate image data by item ID + sha256 hash.
			if d.ImageItemID != "" && s.ctx.IsImageDuplicate(d.ImageItemID, d.ImageData) {
				continue // skip duplicate
			}
		}
		result = append(result, d)
	}
	return result
}

func (s *StreamTranslateSession) applyLegacyMiddleware(deltas []StreamDelta) []StreamDelta {
	var result []StreamDelta
	for _, d := range deltas {
		switch d.Type {
		case EventToolStart:
			if isInternalResponseToolType(d.ToolType) {
				result = append(result, d)
				continue
			}
			s.ctx.SawToolCall = true
			d.ToolName = s.ctx.RestoreToolName(d.ToolName)
			emitArgsDelta := s.toolArgsRequireSeparateDelta(d)
			if d.ToolCallID != "" {
				state := s.getOrCreateToolState(d.ToolCallID)
				if state.done {
					continue
				}
				state.announced = true
				if emitArgsDelta {
					state.argumentsEmitted = true
				}
			}
			if emitArgsDelta {
				result = append(result, d, toolArgsAsDelta(d))
				continue
			}

		case EventToolDelta:
			if isInternalResponseToolType(d.ToolType) {
				result = append(result, d)
				continue
			}
			d.ToolName = s.ctx.RestoreToolName(d.ToolName)
			if d.ToolCallID != "" {
				state := s.getOrCreateToolState(d.ToolCallID)
				if state.done {
					continue
				}
				state.argumentsEmitted = true
			}

		case EventToolDone:
			if isInternalResponseToolType(d.ToolType) {
				result = append(result, d)
				continue
			}
			d.ToolName = s.ctx.RestoreToolName(d.ToolName)
			if d.ToolCallID != "" {
				state := s.getOrCreateToolState(d.ToolCallID)
				if state.done {
					continue
				}
				state.done = true
				if state.argumentsEmitted {
					continue
				}
				if !state.announced {
					s.ctx.SawToolCall = true
					state.announced = true
					result = append(result, StreamDelta{
						Type:       EventToolStart,
						ToolCallID: d.ToolCallID,
						ToolName:   d.ToolName,
						ToolType:   d.ToolType,
						ToolIndex:  d.ToolIndex,
					})
				}
				if s.toolArgsRequireSeparateDelta(d) {
					state.argumentsEmitted = true
					result = append(result, toolArgsAsDelta(d))
				}
			} else if s.toolArgsRequireSeparateDelta(d) {
				result = append(result, toolArgsAsDelta(d))
			}

		case EventDone:
			d.FinishReason = s.ctx.EffectiveFinishReason(d.FinishReason)

		case EventImageDelta:
			if d.ImageItemID != "" && s.ctx.IsImageDuplicate(d.ImageItemID, d.ImageData) {
				continue
			}
		}
		result = append(result, d)
	}
	return result
}

func (s *StreamTranslateSession) toolArgsRequireSeparateDelta(d StreamDelta) bool {
	return s.targetFormat == FormatAnthropic && d.ToolArgs != "" && (d.ToolType == "" || d.ToolType == "function")
}

func toolArgsAsDelta(d StreamDelta) StreamDelta {
	d.Type = EventToolDelta
	return d
}

func (s *StreamTranslateSession) acceptToolStartDelta(d StreamDelta) []StreamDelta {
	d.ToolName = s.ctx.RestoreToolName(d.ToolName)
	state := s.getOrCreateToolStateForDelta(d)
	state.index = d.ToolIndex
	syntheticID := isSyntheticToolCallID(d)
	if d.ToolCallID != "" {
		if !syntheticID || state.callID == "" {
			state.callID = d.ToolCallID
			state.syntheticID = syntheticID
		}
	} else if state.callID == "" {
		callID, ok := responseSyntheticToolCallID(d)
		if !ok {
			callID = ""
		}
		state.callID = callID
		state.syntheticID = ok
	}
	if d.ToolName != "" {
		state.name = d.ToolName
	}
	if d.toolNamespace != "" {
		state.namespace = d.toolNamespace
	}
	if d.ToolType != "" {
		state.toolType = d.ToolType
	}
	s.applyResponseToolDescriptorToState(state)
	if d.ToolArgs != "" {
		state.arguments.WriteString(d.ToolArgs)
	}
	if state.announced {
		if state.toolType == "function" && d.ToolArgs != "" {
			state.argumentsEmitted = true
			return []StreamDelta{s.stateToolDelta(state, d.ToolArgs)}
		}
		return nil
	}
	if state.done || state.syntheticID {
		return nil
	}
	return s.announceToolState(state, true)
}

func (s *StreamTranslateSession) acceptToolArgumentDelta(d StreamDelta) []StreamDelta {
	d.ToolName = s.ctx.RestoreToolName(d.ToolName)
	state := s.getOrCreateToolStateForDelta(d)
	state.index = d.ToolIndex
	syntheticID := isSyntheticToolCallID(d)
	if d.ToolCallID != "" {
		if !syntheticID || state.callID == "" {
			state.callID = d.ToolCallID
			state.syntheticID = syntheticID
		}
	} else if state.callID == "" {
		callID, ok := responseSyntheticToolCallID(d)
		if !ok {
			callID = ""
		}
		state.callID = callID
		state.syntheticID = ok
	}
	if d.ToolName != "" {
		state.name = d.ToolName
	}
	if d.toolNamespace != "" {
		state.namespace = d.toolNamespace
	}
	if d.ToolType != "" {
		state.toolType = d.ToolType
	}
	s.applyResponseToolDescriptorToState(state)
	if d.ToolArgs != "" {
		state.arguments.WriteString(d.ToolArgs)
	}
	if state.done || state.toolType == "custom" {
		return nil
	}
	if !state.announced {
		return s.announceToolState(state, true)
	}
	if d.ToolArgs == "" {
		return nil
	}
	state.argumentsEmitted = true
	return []StreamDelta{s.stateToolDelta(state, d.ToolArgs)}
}

func (s *StreamTranslateSession) acceptToolDoneDelta(d StreamDelta) []StreamDelta {
	d.ToolName = s.ctx.RestoreToolName(d.ToolName)
	state := s.getOrCreateToolStateForDelta(d)
	state.index = d.ToolIndex
	syntheticID := isSyntheticToolCallID(d)
	if d.ToolCallID != "" {
		if !syntheticID || state.callID == "" {
			state.callID = d.ToolCallID
			state.syntheticID = syntheticID
		}
	} else if state.callID == "" {
		callID, ok := responseSyntheticToolCallID(d)
		if !ok {
			callID = ""
		}
		state.callID = callID
		state.syntheticID = ok
	}
	if d.ToolName != "" {
		state.name = d.ToolName
	}
	if d.toolNamespace != "" {
		state.namespace = d.toolNamespace
	}
	if d.ToolType != "" {
		state.toolType = d.ToolType
	}
	s.applyResponseToolDescriptorToState(state)
	if d.ToolArgs != "" && state.arguments.Len() == 0 {
		state.arguments.WriteString(d.ToolArgs)
	}
	if state.done {
		return nil
	}
	var out []StreamDelta
	if !state.announced {
		out = append(out, s.announceToolState(state, false)...)
	} else if state.toolType == "function" && d.ToolArgs != "" && !state.argumentsEmitted {
		out = append(out, s.stateToolDelta(state, d.ToolArgs))
		state.argumentsEmitted = true
	}
	out = append(out, s.stateToolDone(state))
	state.done = true
	return out
}

func (s *StreamTranslateSession) flushPendingToolStates() []StreamDelta {
	if len(s.toolCallStates) == 0 {
		return nil
	}
	states := make([]*toolCallState, 0, len(s.toolCallStates))
	seen := make(map[*toolCallState]bool, len(s.toolCallStates))
	for _, state := range s.toolCallStates {
		if state.done || seen[state] {
			continue
		}
		seen[state] = true
		states = append(states, state)
	}
	sort.SliceStable(states, func(i, j int) bool {
		return states[i].index < states[j].index
	})
	var out []StreamDelta
	for _, state := range states {
		if !state.announced {
			out = append(out, s.announceToolState(state, false)...)
		}
		out = append(out, s.stateToolDone(state))
		state.done = true
	}
	return out
}

func (s *StreamTranslateSession) announceToolState(state *toolCallState, includeBufferedArgs bool) []StreamDelta {
	if state.callID == "" || state.name == "" && state.toolType == "custom" {
		return nil
	}
	s.ctx.SawToolCall = true
	state.announced = true
	start := StreamDelta{
		Type:          EventToolStart,
		ToolCallID:    state.callID,
		ToolName:      state.name,
		ToolType:      state.toolType,
		ToolIndex:     state.index,
		BlockIndex:    state.index,
		toolNamespace: state.namespace,
	}
	var out []StreamDelta
	out = append(out, start)
	if includeBufferedArgs && state.toolType == "function" && state.arguments.Len() > 0 {
		state.argumentsEmitted = true
		out = append(out, s.stateToolDelta(state, state.arguments.String()))
	}
	return out
}

func (s *StreamTranslateSession) stateToolDelta(state *toolCallState, args string) StreamDelta {
	return StreamDelta{
		Type:          EventToolDelta,
		ToolCallID:    state.callID,
		ToolName:      state.name,
		ToolType:      state.toolType,
		ToolIndex:     state.index,
		BlockIndex:    state.index,
		ToolArgs:      args,
		toolNamespace: state.namespace,
	}
}

func (s *StreamTranslateSession) stateToolDone(state *toolCallState) StreamDelta {
	return StreamDelta{
		Type:          EventToolDone,
		ToolCallID:    state.callID,
		ToolName:      state.name,
		ToolType:      state.toolType,
		ToolIndex:     state.index,
		BlockIndex:    state.index,
		ToolArgs:      state.arguments.String(),
		toolNamespace: state.namespace,
	}
}

func (s *StreamTranslateSession) applyResponseToolDescriptorToState(state *toolCallState) {
	if state == nil {
		return
	}
	call := map[string]any{"name": state.name}
	if state.namespace != "" {
		call["namespace"] = state.namespace
	}
	switch state.toolType {
	case "custom":
		call["type"] = "custom_tool_call"
	default:
		call["type"] = "function_call"
	}
	if descriptor, ok := s.ctx.responseToolDescriptorForCall(call); ok {
		state.name = responseOutputToolName(descriptor)
		state.toolType = descriptor.toolType
		state.namespace = descriptor.namespace
	} else if state.toolType == "" {
		state.toolType = "function"
	}
}

// getOrCreateToolState returns the tool call state for the given ID, creating it if absent.
func (s *StreamTranslateSession) getOrCreateToolState(toolCallID string) *toolCallState {
	if s.toolCallStates == nil {
		s.toolCallStates = make(map[string]*toolCallState)
	}
	state, ok := s.toolCallStates[toolCallID]
	if !ok {
		state = &toolCallState{}
		s.toolCallStates[toolCallID] = state
	}
	return state
}

func (s *StreamTranslateSession) getOrCreateToolStateForDelta(delta StreamDelta) *toolCallState {
	if delta.ToolCallID != "" && !isSyntheticToolCallID(delta) {
		if indexState, ok := s.findToolStateByIndex(delta.ToolIndex); ok {
			indexState.callID = delta.ToolCallID
			indexState.syntheticID = false
			s.toolCallStates["id:"+delta.ToolCallID] = indexState
			return indexState
		}
		state := s.getOrCreateToolState("id:" + delta.ToolCallID)
		s.toolCallStates[fmt.Sprintf("index:%d", delta.ToolIndex)] = state
		return state
	}
	return s.getOrCreateToolState(fmt.Sprintf("index:%d", delta.ToolIndex))
}

func (s *StreamTranslateSession) findToolStateByIndex(index int) (*toolCallState, bool) {
	if s.toolCallStates == nil {
		return nil, false
	}
	state, ok := s.toolCallStates[fmt.Sprintf("index:%d", index)]
	return state, ok
}

func isSyntheticToolCallID(delta StreamDelta) bool {
	if delta.Extra == nil {
		return false
	}
	synthetic, _ := delta.Extra["synthetic_call_id"].(bool)
	return synthetic
}

func responseSyntheticToolCallID(delta StreamDelta) (string, bool) {
	if delta.Extra == nil {
		return "", false
	}
	responseID := stringValue(delta.Extra["response_id"])
	if responseID == "" {
		return "", false
	}
	choiceIndex, okChoice := intExtraValue(delta.Extra["choice_index"])
	toolIndex, okTool := intExtraValue(delta.Extra["tool_index"])
	if !okChoice {
		choiceIndex = 0
	}
	if !okTool {
		toolIndex = delta.ToolIndex
	}
	return fmt.Sprintf("call_%s_%d_%d", responseID, choiceIndex, toolIndex), true
}

func intExtraValue(value any) (int, bool) {
	switch v := value.(type) {
	case int:
		return v, true
	case int64:
		return int(v), true
	case float64:
		return int(v), true
	default:
		return 0, false
	}
}

// Flush produces any remaining SSE lines to properly close the stream.
// Safe to call multiple times; subsequent calls return nil.
func (s *StreamTranslateSession) Flush() [][]byte {
	if s.flushed {
		return nil
	}
	s.flushed = true
	return s.serializer.Flush()
}

// TranslateStreamChunk is the top-level convenience function for SSE stream translation.
// On the first call, *session should be nil; it will be auto-initialized.
// Subsequent calls reuse the same session to maintain state across the stream.
//
// This signature is designed to match the sdktranslator.TranslateStream pattern
// where state is carried via a pointer-to-pointer parameter.
func TranslateStreamChunk(source, target Format, model string, rawSSELine []byte, session **StreamTranslateSession) [][]byte {
	if *session == nil {
		s, err := NewStreamSession(source, target, model)
		if err != nil {
			return nil
		}
		*session = s
	}
	outputs, _ := (*session).Translate(rawSSELine)
	return outputs
}
