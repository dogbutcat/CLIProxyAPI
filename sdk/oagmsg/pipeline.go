package oagmsg

import (
	"fmt"

	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

// Builder provides a fluent API for explicit protocol translation operations.
// Created via the package-level From() function. Each call to From() creates a
// new Builder instance; the underlying HandlerRegistry is a singleton.
//
// Usage:
//
//	// Translate request
//	translated, err := oagmsg.From(FormatClaude).Request(rawData).To(FormatOpenAI)
//
//	// Translate response
//	resp, err := oagmsg.From(FormatClaude).Response(rawData).To(FormatOpenAI, "gpt-4o")
type Builder struct {
	registry *HandlerRegistry
	source   Format
	rawJSON  []byte              // original raw JSON for field preservation
	request  *UnifiedRequest     // populated after Request()
	response *UnifiedResponse    // populated after Response()
	mode     builderMode         // request or response
	ctx      *TranslationContext // optional cross-phase context
	err      error
}

type builderMode int

const (
	modeNone     builderMode = iota
	modeRequest              // Request() was called
	modeResponse             // Response() was called
)

// From creates a new Builder bound to the given source format.
// Format aliases (e.g. "openai-response") are resolved automatically.
func From(format Format) *Builder {
	return &Builder{
		registry: DefaultRegistry(),
		source:   resolveFormat(format),
	}
}

// Request parses raw request JSON using the source format handler.
// The raw JSON is retained for field preservation during translation.
func (b *Builder) Request(rawJSON []byte) *Builder {
	if b.err != nil {
		return b
	}
	b.mode = modeRequest
	b.rawJSON = rawJSON

	handler, ok := b.registry.Get(b.source)
	if !ok {
		b.err = fmt.Errorf("oagmsg: no handler for source format %q", b.source)
		return b
	}
	b.request, b.err = handler.ParseRequest(rawJSON)

	// Populate context with request metadata if attached.
	if b.ctx != nil {
		b.ctx.OriginalRequestJSON = rawJSON
		b.ctx.SourceFormat = b.source
		if b.request != nil {
			b.ctx.IsStreaming = b.request.Stream
			b.ctx.ModelName = b.request.Model
		}
	}
	return b
}

// WithTranslationContext attaches a TranslationContext to the builder.
// The context is populated during Request() and To() phases, and can be
// reused in subsequent response/stream translation to carry tool name maps etc.
func (b *Builder) WithTranslationContext(ctx *TranslationContext) *Builder {
	b.ctx = ctx
	return b
}

// Response parses raw response JSON using the source format handler.
func (b *Builder) Response(rawJSON []byte) *Builder {
	if b.err != nil {
		return b
	}
	b.mode = modeResponse
	b.rawJSON = rawJSON

	handler, ok := b.registry.Get(b.source)
	if !ok {
		b.err = fmt.Errorf("oagmsg: no handler for source format %q", b.source)
		return b
	}
	if parser, ok := handler.(contextResponseParser); ok {
		b.response, b.err = parser.parseResponseWithContext(rawJSON, b.ctx)
	} else {
		b.response, b.err = handler.ParseResponse(rawJSON)
	}
	return b
}

// To translates the parsed request or response to the target format.
//
// For request mode: To(targetFormat)
// For response mode: To(targetFormat, model)  - model is optional
//
// Field preservation: top-level fields from the source JSON that are not
// present in the serialized output are automatically merged back. This
// preserves fields like presence_penalty, seed, logprobs, etc. without
// requiring changes to individual handlers.
func (b *Builder) To(target Format, args ...string) ([]byte, error) {
	if b.err != nil {
		return nil, b.err
	}

	targetFormat := resolveFormat(target)
	handler, ok := b.registry.Get(targetFormat)
	if !ok {
		return nil, fmt.Errorf("oagmsg: no handler for target format %q", target)
	}

	switch b.mode {
	case modeRequest:
		if b.request == nil {
			return nil, fmt.Errorf("oagmsg: Request() not called before To()")
		}
		output, err := handler.SerializeRequest(b.request)
		if err != nil {
			return nil, fmt.Errorf("oagmsg: serialize %s request: %w", target, err)
		}
		// Central field preservation
		output = preserveUnknownFieldsForSource(b.source, b.rawJSON, output)
		output = finalizeRequestForTarget(targetFormat, output, b.request.Stream)
		// Record target format in context.
		if b.ctx != nil {
			b.ctx.TargetFormat = targetFormat
		}
		return output, nil

	case modeResponse:
		if b.response == nil {
			return nil, fmt.Errorf("oagmsg: Response() not called before To()")
		}
		model := ""
		if len(args) > 0 {
			model = args[0]
		}
		output, err := handler.FormatResponse(b.response, model)
		if err != nil {
			return nil, fmt.Errorf("oagmsg: format %s response: %w", target, err)
		}
		if b.response.skipUnknownResponseFields {
			return output, nil
		}
		return preserveUnknownResponseFields(b.rawJSON, output), nil

	default:
		return nil, fmt.Errorf("oagmsg: call Request() or Response() before To()")
	}
}

// ToWithModel translates the parsed data to the target format, ensuring the
// model field in the output matches the given model name.
func (b *Builder) ToWithModel(target Format, model string) ([]byte, error) {
	output, err := b.To(target)
	if err != nil {
		return nil, err
	}
	if model != "" && gjson.GetBytes(output, "model").String() != model {
		if updated, errModel := sjson.SetBytes(output, "model", model); errModel == nil {
			output = updated
		}
	}
	if b.request != nil {
		output = finalizeRequestForTarget(target, output, b.request.Stream)
	} else {
		output = finalizeCodexRequestForTarget(target, output)
	}
	return output, nil
}

// HasTools returns whether the parsed request contains tool definitions.
func (b *Builder) HasTools() bool {
	if b.err != nil || b.request == nil {
		return false
	}
	return len(b.request.Tools) > 0
}

// Messages returns the parsed messages (protocol-agnostic OagMessage format).
func (b *Builder) Messages() []OagMessage {
	if b.err != nil || b.request == nil {
		return nil
	}
	return b.request.Messages
}

// Err returns any error that occurred during the builder chain.
func (b *Builder) Err() error {
	return b.err
}
