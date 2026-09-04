package cliproxy

import (
	"context"

	"github.com/router-for-me/CLIProxyAPI/v7/sdk/oagmsg"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v7/sdk/translator"
)

type oagmsgPluginHooksAdapter struct {
	hooks sdktranslator.PluginHooks
}

type oagmsgPluginHooksProvider interface {
	OAGMsgHooks() oagmsg.PluginHooks
}

func setTranslationPluginHooks(hooks sdktranslator.PluginHooks) {
	if hooks == nil {
		oagmsg.SetPluginHooks(nil)
		return
	}
	if provider, ok := hooks.(oagmsgPluginHooksProvider); ok {
		oagmsg.SetPluginHooks(provider.OAGMsgHooks())
		return
	}
	oagmsg.SetPluginHooks(oagmsgPluginHooksAdapter{hooks: hooks})
}

func (a oagmsgPluginHooksAdapter) NormalizeRequest(ctx context.Context, from, to oagmsg.Format, model string, body []byte, stream bool) []byte {
	return a.hooks.NormalizeRequest(ctx, sdktranslator.Format(from), sdktranslator.Format(to), model, body, stream)
}

func (a oagmsgPluginHooksAdapter) TranslateRequest(ctx context.Context, from, to oagmsg.Format, model string, body []byte, stream bool) ([]byte, bool) {
	return a.hooks.TranslateRequest(ctx, sdktranslator.Format(from), sdktranslator.Format(to), model, body, stream)
}

func (a oagmsgPluginHooksAdapter) NormalizeResponseBefore(ctx context.Context, from, to oagmsg.Format, model string, originalRequestRawJSON, requestRawJSON, body []byte, stream bool) []byte {
	return a.hooks.NormalizeResponseBefore(ctx, sdktranslator.Format(from), sdktranslator.Format(to), model, originalRequestRawJSON, requestRawJSON, body, stream)
}

func (a oagmsgPluginHooksAdapter) TranslateResponse(ctx context.Context, from, to oagmsg.Format, model string, originalRequestRawJSON, requestRawJSON, body []byte, stream bool) ([]byte, bool) {
	return a.hooks.TranslateResponse(ctx, sdktranslator.Format(from), sdktranslator.Format(to), model, originalRequestRawJSON, requestRawJSON, body, stream)
}

func (a oagmsgPluginHooksAdapter) NormalizeResponseAfter(ctx context.Context, from, to oagmsg.Format, model string, originalRequestRawJSON, requestRawJSON, body []byte, stream bool) []byte {
	return a.hooks.NormalizeResponseAfter(ctx, sdktranslator.Format(from), sdktranslator.Format(to), model, originalRequestRawJSON, requestRawJSON, body, stream)
}
