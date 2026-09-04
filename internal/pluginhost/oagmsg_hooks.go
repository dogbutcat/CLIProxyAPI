package pluginhost

import (
	"context"

	"github.com/router-for-me/CLIProxyAPI/v7/sdk/oagmsg"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v7/sdk/translator"
)

type oagmsgHooksAdapter struct {
	host *Host
}

// OAGMsgHooks exposes plugin translation hooks to the oagmsg runtime.
func (h *Host) OAGMsgHooks() oagmsg.PluginHooks {
	if h == nil {
		return nil
	}
	return oagmsgHooksAdapter{host: h}
}

func (a oagmsgHooksAdapter) NormalizeRequest(ctx context.Context, from, to oagmsg.Format, model string, body []byte, stream bool) []byte {
	return a.host.NormalizeRequest(ctx, sdktranslator.Format(from), sdktranslator.Format(to), model, body, stream)
}

func (a oagmsgHooksAdapter) TranslateRequest(ctx context.Context, from, to oagmsg.Format, model string, body []byte, stream bool) ([]byte, bool) {
	return a.host.TranslateRequest(ctx, sdktranslator.Format(from), sdktranslator.Format(to), model, body, stream)
}

func (a oagmsgHooksAdapter) NormalizeResponseBefore(ctx context.Context, from, to oagmsg.Format, model string, originalRequestRawJSON, requestRawJSON, body []byte, stream bool) []byte {
	return a.host.NormalizeResponseBefore(ctx, sdktranslator.Format(from), sdktranslator.Format(to), model, originalRequestRawJSON, requestRawJSON, body, stream)
}

func (a oagmsgHooksAdapter) TranslateResponse(ctx context.Context, from, to oagmsg.Format, model string, originalRequestRawJSON, requestRawJSON, body []byte, stream bool) ([]byte, bool) {
	return a.host.TranslateResponse(ctx, sdktranslator.Format(from), sdktranslator.Format(to), model, originalRequestRawJSON, requestRawJSON, body, stream)
}

func (a oagmsgHooksAdapter) NormalizeResponseAfter(ctx context.Context, from, to oagmsg.Format, model string, originalRequestRawJSON, requestRawJSON, body []byte, stream bool) []byte {
	return a.host.NormalizeResponseAfter(ctx, sdktranslator.Format(from), sdktranslator.Format(to), model, originalRequestRawJSON, requestRawJSON, body, stream)
}
