package oagmsg

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"sync"

	"github.com/tidwall/gjson"
)

// ProtocolHandler is the strategy interface for protocol-specific parse/serialize operations.
// Each supported protocol implements this interface.
//
// Aligned with oag_server handlers/base.py BaseProtocolHandler.
type ProtocolHandler interface {
	// Format returns the protocol format this handler handles.
	Format() Format

	// ParseRequest parses raw protocol JSON into a UnifiedRequest.
	ParseRequest(rawJSON []byte) (*UnifiedRequest, error)

	// ParseMessages extracts just the messages from raw protocol JSON.
	ParseMessages(rawJSON []byte) ([]OagMessage, error)

	// SerializeMessages converts OagMessages back to protocol-specific JSON messages array.
	SerializeMessages(msgs []OagMessage) ([]byte, error)

	// SerializeRequest converts a full UnifiedRequest to protocol-specific JSON.
	SerializeRequest(req *UnifiedRequest) ([]byte, error)

	// ParseResponse parses a non-streaming upstream response into a UnifiedResponse.
	ParseResponse(rawJSON []byte) (*UnifiedResponse, error)

	// FormatResponse formats a UnifiedResponse into protocol-specific JSON.
	FormatResponse(resp *UnifiedResponse, model string) ([]byte, error)

	// FormatError formats a UnifiedError into protocol-specific error JSON.
	FormatError(err *UnifiedError) ([]byte, error)

	// HasToolsDefined checks if the raw JSON payload contains tool definitions.
	HasToolsDefined(rawJSON []byte) bool
}

type contextResponseParser interface {
	parseResponseWithContext(rawJSON []byte, ctx *TranslationContext) (*UnifiedResponse, error)
}

// HandlerRegistry manages ProtocolHandler instances by Format.
// It is the factory/lookup mechanism, replacing oag_server's get_handler().
type HandlerRegistry struct {
	mu       sync.RWMutex
	handlers map[Format]ProtocolHandler
}

// NewRegistry creates an empty HandlerRegistry.
func NewRegistry() *HandlerRegistry {
	return &HandlerRegistry{
		handlers: make(map[Format]ProtocolHandler),
	}
}

// Register adds a handler to the registry, keyed by its Format().
func (r *HandlerRegistry) Register(h ProtocolHandler) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.handlers[h.Format()] = h
}

// RegisterAs adds a handler under an explicit format key.
// Used for strategy variants that share the same handler type but differ in behavior.
func (r *HandlerRegistry) RegisterAs(f Format, h ProtocolHandler) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.handlers[f] = h
}

// Get retrieves the handler for the given format.
// Returns nil, false if not registered.
func (r *HandlerRegistry) Get(f Format) (ProtocolHandler, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	h, ok := r.handlers[f]
	return h, ok
}

// MustGet retrieves the handler for the given format or panics.
func (r *HandlerRegistry) MustGet(f Format) ProtocolHandler {
	h, ok := r.Get(f)
	if !ok {
		panic(fmt.Sprintf("oagmsg: no handler registered for format %q", f))
	}
	return h
}

// Translate is a convenience method that parses with the source handler
// and serializes with the target handler. Format aliases (e.g. "openai-response")
// are resolved to their canonical handler format before lookup.
func (r *HandlerRegistry) Translate(from, to Format, rawJSON []byte) ([]byte, error) {
	targetFormat := resolveFormat(to)
	fromHandler, ok := r.Get(resolveFormat(from))
	if !ok {
		return nil, fmt.Errorf("oagmsg: no handler for source format %q", from)
	}
	toHandler, ok := r.Get(targetFormat)
	if !ok {
		return nil, fmt.Errorf("oagmsg: no handler for target format %q", to)
	}

	req, err := fromHandler.ParseRequest(rawJSON)
	if err != nil {
		return nil, fmt.Errorf("oagmsg: parse %s request: %w", from, err)
	}

	output, err := toHandler.SerializeRequest(req)
	if err != nil {
		return nil, fmt.Errorf("oagmsg: serialize %s request: %w", to, err)
	}
	output = preserveUnknownFieldsForSource(req.SourceFormat, rawJSON, output)
	return finalizeRequestForTarget(targetFormat, output, req.Stream), nil
}

// SerializeRequestPreserving serializes req with the handler for f and merges
// unknown non-structural top-level fields from sourceJSON into the result.
func (r *HandlerRegistry) SerializeRequestPreserving(f Format, req *UnifiedRequest, sourceJSON []byte) ([]byte, error) {
	targetFormat := resolveFormat(f)
	handler, ok := r.Get(targetFormat)
	if !ok {
		return nil, fmt.Errorf("oagmsg: no handler for format %q", f)
	}
	output, err := handler.SerializeRequest(req)
	if err != nil {
		return nil, fmt.Errorf("oagmsg: serialize %s request: %w", f, err)
	}
	output = preserveUnknownFieldsForSource(req.SourceFormat, sourceJSON, output)
	return finalizeRequestForTarget(targetFormat, output, req.Stream), nil
}

// TranslateResponse translates a non-streaming response from one format to another.
// from = upstream provider format, to = client-facing format.
func (r *HandlerRegistry) TranslateResponse(from, to Format, model string, rawJSON []byte) ([]byte, error) {
	return r.TranslateResponseWithContext(from, to, model, rawJSON, nil)
}

// TranslateResponseWithContext translates a response and applies request-scoped
// metadata such as restored tool names before formatting the client payload.
func (r *HandlerRegistry) TranslateResponseWithContext(from, to Format, model string, rawJSON []byte, ctx *TranslationContext) ([]byte, error) {
	fromHandler, ok := r.Get(resolveFormat(from))
	if !ok {
		return nil, fmt.Errorf("oagmsg: no handler for source format %q", from)
	}
	toHandler, ok := r.Get(resolveFormat(to))
	if !ok {
		return nil, fmt.Errorf("oagmsg: no handler for target format %q", to)
	}
	if unifiedErr := parseUnifiedError(rawJSON); unifiedErr != nil {
		return toHandler.FormatError(unifiedErr)
	}

	var resp *UnifiedResponse
	var err error
	if parser, ok := fromHandler.(contextResponseParser); ok {
		resp, err = parser.parseResponseWithContext(rawJSON, ctx)
	} else {
		resp, err = fromHandler.ParseResponse(rawJSON)
	}
	if err != nil {
		return nil, fmt.Errorf("oagmsg: parse %s response: %w", from, err)
	}
	restoreUnifiedResponseToolNames(resp, ctx)

	formatModel := model
	forceModel := responsesModelSelection{}
	omitEmptyModel := false
	if ctx != nil && resolveFormat(to) == FormatOpenAIResponse && ctx.responsesModelSet {
		formatModel = ctx.responsesModel
		forceModel = responsesModelSelection{model: ctx.responsesModel, set: true}
	} else if provider := providerModelSelection(rawJSON, from); resolveFormat(to) == FormatOpenAIResponse && provider.set {
		formatModel = provider.model
	} else if ctx != nil && resolveFormat(to) == FormatOpenAIResponse && ctx.responsesModelNoRuntime {
		formatModel = ""
		omitEmptyModel = true
	}
	output, err := toHandler.FormatResponse(resp, formatModel)
	if err != nil {
		return nil, fmt.Errorf("oagmsg: format %s response: %w", to, err)
	}
	if forceModel.set {
		output = overrideResponsesModelSelection(output, forceModel, false)
	}
	if omitEmptyModel {
		output = omitEmptyResponsesModel(output)
	}
	if resp.skipUnknownResponseFields {
		return output, nil
	}
	return preserveUnknownResponseFields(rawJSON, output), nil
}

func parseUnifiedError(rawJSON []byte) *UnifiedError {
	root := gjson.ParseBytes(rawJSON)
	errValue := root.Get("error")
	if !errValue.Exists() {
		return nil
	}
	message := errValue.Get("message").String()
	errorType := errValue.Get("type").String()
	if message == "" && errValue.Type == gjson.String {
		message = errValue.String()
	}
	if errorType == "" {
		errorType = root.Get("type").String()
	}
	return &UnifiedError{
		StatusCode: int(errValue.Get("code").Int()),
		Message:    message,
		ErrorType:  errorType,
	}
}

// defaultRegistry is the singleton registry with all standard protocol handlers.
var (
	defaultRegistryOnce sync.Once
	defaultRegistryInst *HandlerRegistry
)

// DefaultRegistry returns the singleton HandlerRegistry pre-populated with all
// standard protocol handlers (OpenAI, Anthropic, Gemini, Google Interactions, Codex, Antigravity).
func DefaultRegistry() *HandlerRegistry {
	defaultRegistryOnce.Do(func() {
		defaultRegistryInst = NewRegistry()
		defaultRegistryInst.Register(&OpenAIHandler{})
		defaultRegistryInst.Register(&AnthropicHandler{})
		defaultRegistryInst.Register(&GeminiHandler{})
		defaultRegistryInst.Register(&GoogleInteractionsHandler{})
		defaultRegistryInst.Register(&CodexHandler{})
		defaultRegistryInst.Register(&AntigravityHandler{})
		// Deprecated compatibility name for the Google Interactions wire format.
		defaultRegistryInst.RegisterAs(FormatInteractionsSteps, &GoogleInteractionsHandler{})
		// OpenAI Responses API uses full lifecycle events (output_item/content_part envelopes).
		defaultRegistryInst.RegisterAs(FormatOpenAIResponse, &InteractionsHandler{Mode: InteractionsModeResponsesAPI})
	})
	return defaultRegistryInst
}

func validateJSONObject(rawJSON []byte) error {
	if len(bytes.TrimSpace(rawJSON)) == 0 {
		return fmt.Errorf("empty JSON payload")
	}
	var decoded any
	dec := json.NewDecoder(bytes.NewReader(rawJSON))
	dec.UseNumber()
	if err := dec.Decode(&decoded); err != nil {
		return err
	}
	var trailing any
	if err := dec.Decode(&trailing); err != io.EOF {
		if err == nil {
			return fmt.Errorf("multiple JSON values")
		}
		return err
	}
	if _, ok := decoded.(map[string]any); !ok {
		return fmt.Errorf("top-level JSON value must be an object")
	}
	return nil
}
