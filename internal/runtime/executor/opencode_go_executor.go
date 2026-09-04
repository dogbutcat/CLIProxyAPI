package executor

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"strings"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/runtime/executor/helps"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/thinking"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/executor"
	"github.com/router-for-me/CLIProxyAPI/v7/sdk/oagmsg"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v7/sdk/translator"
	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

const (
	openCodeGoProtocolOpenAI = "openai"
	openCodeGoProtocolClaude = "claude"
)

type openCodeGoDelegate interface {
	PrepareRequest(req *http.Request, auth *cliproxyauth.Auth) error
	Execute(ctx context.Context, auth *cliproxyauth.Auth, req cliproxyexecutor.Request, opts cliproxyexecutor.Options) (cliproxyexecutor.Response, error)
	ExecuteStream(ctx context.Context, auth *cliproxyauth.Auth, req cliproxyexecutor.Request, opts cliproxyexecutor.Options) (*cliproxyexecutor.StreamResult, error)
	Refresh(ctx context.Context, auth *cliproxyauth.Auth) (*cliproxyauth.Auth, error)
	CountTokens(ctx context.Context, auth *cliproxyauth.Auth, req cliproxyexecutor.Request, opts cliproxyexecutor.Options) (cliproxyexecutor.Response, error)
	HttpRequest(ctx context.Context, auth *cliproxyauth.Auth, req *http.Request) (*http.Response, error)
}

type openCodeGoModelMetadata struct {
	protocol string
}

type openCodeGoModelIndex struct {
	entries   map[string]openCodeGoModelMetadata
	ambiguous map[string]struct{}
}

// OpenCodeGoExecutor delegates OpenCode Go requests to protocol-specific
// executors after resolving configured model and auth protocol metadata.
type OpenCodeGoExecutor struct {
	providerKey string
	cfg         *config.Config
	models      openCodeGoModelIndex
	claude      openCodeGoDelegate
	compat      openCodeGoDelegate
}

// NewOpenCodeGoExecutor creates the OpenCode Go executor facade.
func NewOpenCodeGoExecutor(providerKey string, cfg *config.Config) *OpenCodeGoExecutor {
	return &OpenCodeGoExecutor{
		providerKey: strings.TrimSpace(providerKey),
		cfg:         cfg,
		models:      buildOpenCodeGoModelIndex(cfg),
		claude:      NewClaudeExecutor(cfg),
		compat:      NewOpenAICompatExecutor(providerKey, cfg),
	}
}

// Identifier implements cliproxyauth.ProviderExecutor.
func (e *OpenCodeGoExecutor) Identifier() string {
	if e == nil {
		return ""
	}
	return e.providerKey
}

// RequestToFormat resolves the delegate request protocol for interceptors.
func (e *OpenCodeGoExecutor) RequestToFormat(req cliproxyexecutor.Request, _ cliproxyexecutor.Options) sdktranslator.Format {
	protocol, err := e.resolveModelProtocol(req.Model)
	if err != nil {
		return ""
	}
	if protocol == openCodeGoProtocolClaude {
		return sdktranslator.FormatClaude
	}
	return sdktranslator.FormatOpenAI
}

// PrepareRequest injects credentials using the selected auth protocol metadata.
func (e *OpenCodeGoExecutor) PrepareRequest(req *http.Request, auth *cliproxyauth.Auth) error {
	routeAuth, err := e.routeAuthForHTTPRequest(auth, req)
	if err != nil {
		return err
	}
	delegate, err := e.delegateForSelectedAuth(routeAuth)
	if err != nil {
		return err
	}
	return delegate.PrepareRequest(req, routeAuth)
}

func (e *OpenCodeGoExecutor) Execute(ctx context.Context, auth *cliproxyauth.Auth, req cliproxyexecutor.Request, opts cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
	ctx = helps.WithUsageProvider(ctx, e.Identifier())
	delegate, routeAuth, preparedReq, preparedOpts, err := e.prepareExecution(ctx, auth, req, opts)
	if err != nil {
		return cliproxyexecutor.Response{}, err
	}
	resp, err := delegate.Execute(ctx, routeAuth, preparedReq, preparedOpts)
	if err != nil {
		return resp, err
	}
	resp.Payload = rewriteOpenCodeGoResponseModel(resp.Payload, openCodeGoResponseAlias(req, opts, preparedReq))
	return resp, nil
}

func (e *OpenCodeGoExecutor) ExecuteStream(ctx context.Context, auth *cliproxyauth.Auth, req cliproxyexecutor.Request, opts cliproxyexecutor.Options) (*cliproxyexecutor.StreamResult, error) {
	ctx = helps.WithUsageProvider(ctx, e.Identifier())
	delegate, routeAuth, preparedReq, preparedOpts, err := e.prepareExecution(ctx, auth, req, opts)
	if err != nil {
		return nil, err
	}
	stream, err := delegate.ExecuteStream(ctx, routeAuth, preparedReq, preparedOpts)
	if err != nil {
		return nil, err
	}
	return wrapOpenCodeGoResponseStream(stream, openCodeGoResponseAlias(req, opts, preparedReq)), nil
}

func (e *OpenCodeGoExecutor) Refresh(ctx context.Context, auth *cliproxyauth.Auth) (*cliproxyauth.Auth, error) {
	if _, err := selectedOpenCodeGoProtocol(auth); err != nil {
		if !isCanonicalOpenCodeGoAuth(auth) {
			return nil, err
		}
		return auth.Clone(), nil
	}
	delegate, err := e.delegateForSelectedAuth(auth)
	if err != nil {
		return nil, err
	}
	return delegate.Refresh(ctx, auth)
}

func (e *OpenCodeGoExecutor) CountTokens(ctx context.Context, auth *cliproxyauth.Auth, req cliproxyexecutor.Request, opts cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
	ctx = helps.WithUsageProvider(ctx, e.Identifier())
	delegate, routeAuth, preparedReq, preparedOpts, err := e.prepareExecution(ctx, auth, req, opts)
	if err != nil {
		return cliproxyexecutor.Response{}, err
	}
	return delegate.CountTokens(ctx, routeAuth, preparedReq, preparedOpts)
}

func (e *OpenCodeGoExecutor) HttpRequest(ctx context.Context, auth *cliproxyauth.Auth, req *http.Request) (*http.Response, error) {
	ctx = helps.WithUsageProvider(ctx, e.Identifier())
	routeAuth, err := e.routeAuthForHTTPRequest(auth, req)
	if err != nil {
		return nil, err
	}
	delegate, err := e.delegateForSelectedAuth(routeAuth)
	if err != nil {
		return nil, err
	}
	return delegate.HttpRequest(ctx, routeAuth, req)
}

func (e *OpenCodeGoExecutor) prepareExecution(ctx context.Context, auth *cliproxyauth.Auth, req cliproxyexecutor.Request, opts cliproxyexecutor.Options) (openCodeGoDelegate, *cliproxyauth.Auth, cliproxyexecutor.Request, cliproxyexecutor.Options, error) {
	delegate, routeAuth, err := e.delegateForModelAndAuth(req.Model, auth)
	if err != nil {
		return nil, nil, req, opts, err
	}
	req = rewriteOpenCodeGoRequestModel(routeAuth, req)
	req, opts, err = applyOpenCodeGoTruncation(routeAuth, req, opts)
	if err != nil {
		return nil, nil, req, opts, err
	}
	if rewritten, changed, errRewrite := helps.ApplyVisionRewrite(ctx, e.cfg, opts.SourceFormat.String(), baseOpenCodeGoModel(req.Model), req.Payload, opts.Headers, opts.Metadata, e.Identifier()); errRewrite != nil {
		return nil, nil, req, opts, newOpenCodeGoRequestError(http.StatusBadRequest, "%v", errRewrite)
	} else if changed {
		req.Payload = rewritten
		opts.OriginalRequest = rewritten
	}
	if protocol, errProtocol := selectedOpenCodeGoProtocol(routeAuth); errProtocol == nil && protocol == openCodeGoProtocolOpenAI {
		req = applyOpenCodeGoOpenAICacheControl(req, opts)
	}
	return delegate, routeAuth, req, opts, nil
}

func applyOpenCodeGoOpenAICacheControl(req cliproxyexecutor.Request, opts cliproxyexecutor.Options) cliproxyexecutor.Request {
	if !openCodeGoOpenAICacheControlSourceFormat(opts.SourceFormat) {
		return req
	}
	req.Payload = helps.EnsureOpenCodeGoOpenAICacheControl(req.Payload, baseOpenCodeGoModel(req.Model))
	return req
}

func openCodeGoOpenAICacheControlSourceFormat(format sdktranslator.Format) bool {
	switch format {
	case "", sdktranslator.FormatOpenAI:
		return true
	default:
		return false
	}
}

func (e *OpenCodeGoExecutor) delegateForModelAndAuth(model string, auth *cliproxyauth.Auth) (openCodeGoDelegate, *cliproxyauth.Auth, error) {
	modelProtocol, err := e.resolveModelProtocol(model)
	if err != nil {
		return nil, nil, err
	}
	routeAuth, err := e.routeAuthForProtocol(auth, modelProtocol)
	if err != nil {
		return nil, nil, err
	}
	authProtocol, err := selectedOpenCodeGoProtocol(routeAuth)
	if err != nil {
		return nil, nil, err
	}
	if modelProtocol != authProtocol {
		return nil, nil, newOpenCodeGoRequestError(http.StatusBadRequest, "opencode-go executor: selected auth protocol %q is inconsistent with model %q protocol %q", authProtocol, baseOpenCodeGoModel(model), modelProtocol)
	}
	delegate, err := e.delegateForProtocol(authProtocol)
	return delegate, routeAuth, err
}

func (e *OpenCodeGoExecutor) delegateForSelectedAuth(auth *cliproxyauth.Auth) (openCodeGoDelegate, error) {
	protocol, err := selectedOpenCodeGoProtocol(auth)
	if err != nil {
		return nil, err
	}
	return e.delegateForProtocol(protocol)
}

func (e *OpenCodeGoExecutor) routeAuthForHTTPRequest(auth *cliproxyauth.Auth, req *http.Request) (*cliproxyauth.Auth, error) {
	if _, err := selectedOpenCodeGoProtocol(auth); err == nil {
		return auth, nil
	} else if !isCanonicalOpenCodeGoAuth(auth) {
		return nil, err
	}
	if req == nil || req.URL == nil {
		return nil, newOpenCodeGoRequestError(http.StatusBadRequest, "opencode-go executor: request URL is required to resolve protocol")
	}
	group := e.resolveOpenCodeGoAuthGroup(auth)
	if group == nil {
		return nil, newOpenCodeGoRequestError(http.StatusBadRequest, "opencode-go executor: selected auth %q has no key-group metadata", authID(auth))
	}
	matched := ""
	matchedBaseURL := ""
	for _, candidate := range []struct {
		protocol string
		cfg      *config.OpenCodeGoProtocolConfig
	}{
		{protocol: openCodeGoProtocolOpenAI, cfg: group.OpenAI},
		{protocol: openCodeGoProtocolClaude, cfg: group.Anthropic},
	} {
		if candidate.cfg == nil {
			continue
		}
		baseURL := strings.TrimRight(strings.TrimSpace(candidate.cfg.BaseURL), "/")
		if baseURL == "" || !openCodeGoRequestURLMatchesBase(req.URL, baseURL) {
			continue
		}
		if len(baseURL) < len(matchedBaseURL) {
			continue
		}
		if len(baseURL) == len(matchedBaseURL) && matched != "" && matched != candidate.protocol {
			return nil, newOpenCodeGoRequestError(http.StatusBadRequest, "opencode-go executor: request URL matches multiple protocols")
		}
		matched = candidate.protocol
		matchedBaseURL = baseURL
	}
	if matched == "" {
		return nil, newOpenCodeGoRequestError(http.StatusBadRequest, "opencode-go executor: request URL does not match a configured protocol")
	}
	return e.routeAuthForProtocol(auth, matched)
}

func openCodeGoRequestURLMatchesBase(target *url.URL, baseURL string) bool {
	if target == nil {
		return false
	}
	base, errParse := url.Parse(strings.TrimSpace(baseURL))
	if errParse != nil || base.Scheme == "" || base.Host == "" {
		return false
	}
	if !strings.EqualFold(target.Scheme, base.Scheme) || !strings.EqualFold(target.Host, base.Host) {
		return false
	}
	basePath := strings.TrimRight(base.EscapedPath(), "/")
	if basePath == "" {
		return true
	}
	targetPath := strings.TrimRight(target.EscapedPath(), "/")
	return targetPath == basePath || strings.HasPrefix(targetPath, basePath+"/")
}

func (e *OpenCodeGoExecutor) routeAuthForProtocol(auth *cliproxyauth.Auth, protocol string) (*cliproxyauth.Auth, error) {
	if auth == nil {
		return nil, newOpenCodeGoRequestError(http.StatusUnauthorized, "opencode-go executor: selected auth is nil")
	}
	protocol = normalizeOpenCodeGoProtocol(protocol)
	if protocol == "" {
		return nil, newOpenCodeGoRequestError(http.StatusBadRequest, "opencode-go executor: unsupported protocol")
	}
	if existing, err := selectedOpenCodeGoProtocol(auth); err == nil {
		if existing != protocol {
			return nil, newOpenCodeGoRequestError(http.StatusBadRequest, "opencode-go executor: selected auth protocol %q is inconsistent with requested protocol %q", existing, protocol)
		}
		return auth, nil
	} else if !isCanonicalOpenCodeGoAuth(auth) {
		return nil, err
	}
	group := e.resolveOpenCodeGoAuthGroup(auth)
	if group == nil {
		return nil, newOpenCodeGoRequestError(http.StatusBadRequest, "opencode-go executor: selected auth %q has no key-group metadata", auth.ID)
	}
	var route *config.OpenCodeGoProtocolConfig
	switch protocol {
	case openCodeGoProtocolOpenAI:
		route = group.OpenAI
	case openCodeGoProtocolClaude:
		route = group.Anthropic
	}
	if route == nil || strings.TrimSpace(route.BaseURL) == "" {
		return nil, newOpenCodeGoRequestError(http.StatusBadRequest, "opencode-go executor: selected auth %q has no %s route", auth.ID, protocol)
	}

	projected := auth.Clone()
	if projected.Attributes == nil {
		projected.Attributes = make(map[string]string)
	}
	// Rebase guard: OpenCode Go's Anthropic-compatible route is represented by
	// the executor's canonical "claude" protocol, but it still keeps
	// OpenCode Go wire-auth rules in the Claude delegate.
	projected.Attributes["protocol"] = protocol
	projected.Attributes["base_url"] = strings.TrimSpace(route.BaseURL)
	projected.Attributes["name_suffix"] = strings.TrimSpace(route.NameSuffix)
	if route.Priority != 0 {
		projected.Attributes["priority"] = fmt.Sprint(route.Priority)
	} else {
		delete(projected.Attributes, "priority")
	}
	delete(projected.Attributes, "model_aliases")
	cliproxyauth.SetOAuthModelAliasesAttribute(projected, openCodeGoRouteAliases(route.Models))
	projected.Prefix = strings.TrimSpace(route.Prefix)
	return projected, nil
}

func (e *OpenCodeGoExecutor) resolveOpenCodeGoAuthGroup(auth *cliproxyauth.Auth) *config.OpenCodeGoKeyGroup {
	if e == nil || e.cfg == nil || auth == nil || auth.Attributes == nil {
		return nil
	}
	namePrefix := strings.TrimSpace(auth.Attributes["name_prefix"])
	keyName := strings.TrimSpace(auth.Attributes["key_name"])
	apiKey := strings.TrimSpace(auth.Attributes[cliproxyauth.AttributeAPIKey])
	if namePrefix == "" || keyName == "" {
		return nil
	}
	for groupIndex := range e.cfg.OpenCodeGo.KeyGroups {
		group := &e.cfg.OpenCodeGo.KeyGroups[groupIndex]
		if group.Disabled || !strings.EqualFold(strings.TrimSpace(group.NamePrefix), namePrefix) {
			continue
		}
		for keyIndex := range group.Keys {
			key := &group.Keys[keyIndex]
			if !strings.EqualFold(strings.TrimSpace(key.KeyName), keyName) {
				continue
			}
			if apiKey != "" && strings.TrimSpace(key.APIKey) != apiKey {
				continue
			}
			return group
		}
	}
	return nil
}

func openCodeGoRouteAliases(models []config.OpenCodeGoModelEntry) []config.OAuthModelAlias {
	out := make([]config.OAuthModelAlias, 0, len(models))
	for _, model := range models {
		name := strings.TrimSpace(model.Name)
		alias := strings.TrimSpace(model.Alias)
		if name == "" || alias == "" || strings.EqualFold(name, alias) {
			continue
		}
		out = append(out, config.OAuthModelAlias{Name: name, Alias: alias})
	}
	return out
}

func authID(auth *cliproxyauth.Auth) string {
	if auth == nil {
		return ""
	}
	return auth.ID
}

func isCanonicalOpenCodeGoAuth(auth *cliproxyauth.Auth) bool {
	if auth == nil || auth.Attributes == nil {
		return false
	}
	return strings.TrimSpace(auth.Attributes["protocols"]) != "" &&
		strings.TrimSpace(auth.Attributes["name_prefix"]) != "" &&
		strings.TrimSpace(auth.Attributes["key_name"]) != ""
}

func (e *OpenCodeGoExecutor) delegateForProtocol(protocol string) (openCodeGoDelegate, error) {
	if e == nil {
		return nil, newOpenCodeGoRequestError(http.StatusInternalServerError, "opencode-go executor: executor is nil")
	}
	switch protocol {
	case openCodeGoProtocolClaude:
		if e.claude == nil {
			return nil, newOpenCodeGoRequestError(http.StatusInternalServerError, "opencode-go executor: claude delegate is nil")
		}
		return e.claude, nil
	case openCodeGoProtocolOpenAI:
		if e.compat == nil {
			return nil, newOpenCodeGoRequestError(http.StatusInternalServerError, "opencode-go executor: openai-compatible delegate is nil")
		}
		return e.compat, nil
	default:
		return nil, newOpenCodeGoRequestError(http.StatusBadRequest, "opencode-go executor: unsupported protocol %q", protocol)
	}
}

func (e *OpenCodeGoExecutor) resolveModelProtocol(model string) (string, error) {
	if e == nil {
		return "", newOpenCodeGoRequestError(http.StatusInternalServerError, "opencode-go executor: executor is nil")
	}
	key := strings.ToLower(baseOpenCodeGoModel(model))
	if key == "" {
		return "", newOpenCodeGoRequestError(http.StatusBadRequest, "opencode-go executor: model is required")
	}
	if _, ok := e.models.ambiguous[key]; ok {
		return "", newOpenCodeGoRequestError(http.StatusBadRequest, "opencode-go executor: model %q resolves to multiple protocols", key)
	}
	entry, ok := e.models.entries[key]
	if !ok {
		return "", newOpenCodeGoRequestError(http.StatusBadRequest, "opencode-go executor: model %q has no OpenCode Go protocol metadata", key)
	}
	return entry.protocol, nil
}

func applyOpenCodeGoTruncation(auth *cliproxyauth.Auth, req cliproxyexecutor.Request, opts cliproxyexecutor.Options) (cliproxyexecutor.Request, cliproxyexecutor.Options, error) {
	limits := helps.ReadOpenCodeGoTruncationLimits(auth)
	payload, changed, err := helps.TruncateOpenCodeGoPayload(req.Payload, limits)
	if err != nil {
		return req, opts, newOpenCodeGoRequestError(http.StatusBadRequest, "%v", err)
	}
	if !changed {
		return req, opts, nil
	}
	req.Payload = payload
	if len(opts.OriginalRequest) > 0 {
		opts.OriginalRequest = payload
	}
	return req, opts, nil
}

func rewriteOpenCodeGoRequestModel(auth *cliproxyauth.Auth, req cliproxyexecutor.Request) cliproxyexecutor.Request {
	if auth == nil || strings.TrimSpace(req.Model) == "" {
		return req
	}
	req.Model = stripOpenCodeGoRoutePrefix(auth, req.Model)
	if resolved := resolveOpenCodeGoAliasModel(auth, req.Model); resolved != "" {
		req.Model = resolved
	}
	return req
}

func stripOpenCodeGoRoutePrefix(auth *cliproxyauth.Auth, model string) string {
	if auth == nil {
		return model
	}
	prefix := strings.Trim(strings.TrimSpace(auth.Prefix), "/")
	parsed := thinking.ParseSuffix(model)
	baseModel := strings.TrimSpace(parsed.ModelName)
	needle := prefix + "/"
	if prefix == "" || len(baseModel) <= len(needle) || !strings.EqualFold(baseModel[:len(needle)], needle) {
		return model
	}
	return preserveOpenCodeGoModelSuffix(strings.TrimSpace(baseModel[len(needle):]), parsed)
}

func resolveOpenCodeGoAliasModel(auth *cliproxyauth.Auth, requestedModel string) string {
	aliases := cliproxyauth.OAuthModelAliasesFromAttributes(openCodeGoAuthAttributes(auth))
	if len(aliases) == 0 {
		return ""
	}
	requestResult := thinking.ParseSuffix(requestedModel)
	baseModel := strings.TrimSpace(requestResult.ModelName)
	if baseModel == "" {
		baseModel = strings.TrimSpace(requestedModel)
	}
	for _, alias := range aliases {
		upstream := strings.TrimSpace(alias.Name)
		client := strings.TrimSpace(alias.Alias)
		if upstream == "" || client == "" || !strings.EqualFold(client, baseModel) {
			continue
		}
		return preserveOpenCodeGoModelSuffix(upstream, requestResult)
	}
	return ""
}

func openCodeGoAuthAttributes(auth *cliproxyauth.Auth) map[string]string {
	if auth == nil {
		return nil
	}
	return auth.Attributes
}

func preserveOpenCodeGoModelSuffix(upstream string, requestResult thinking.SuffixResult) string {
	upstream = strings.TrimSpace(upstream)
	if upstream == "" {
		return ""
	}
	if thinking.ParseSuffix(upstream).HasSuffix {
		return upstream
	}
	if requestResult.HasSuffix && requestResult.RawSuffix != "" {
		return upstream + "(" + requestResult.RawSuffix + ")"
	}
	return upstream
}

func openCodeGoResponseAlias(originalReq cliproxyexecutor.Request, opts cliproxyexecutor.Options, preparedReq cliproxyexecutor.Request) string {
	if strings.EqualFold(strings.TrimSpace(originalReq.Model), strings.TrimSpace(preparedReq.Model)) {
		return ""
	}
	if requested := requestedOpenCodeGoModel(opts); requested != "" {
		return requested
	}
	return strings.TrimSpace(originalReq.Model)
}

func requestedOpenCodeGoModel(opts cliproxyexecutor.Options) string {
	if len(opts.Metadata) == 0 {
		return ""
	}
	raw, ok := opts.Metadata[cliproxyexecutor.RequestedModelMetadataKey]
	if !ok || raw == nil {
		return ""
	}
	switch value := raw.(type) {
	case string:
		return strings.TrimSpace(value)
	case fmt.Stringer:
		return strings.TrimSpace(value.String())
	default:
		return strings.TrimSpace(fmt.Sprint(value))
	}
}

var openCodeGoResponseModelPaths = []string{"model", "modelVersion", "response.model", "response.modelVersion", "message.model"}

func rewriteOpenCodeGoResponseModel(payload []byte, responseModel string) []byte {
	out := oagmsg.SanitizeOpenAICompatibleResponse(payload)
	responseModel = strings.TrimSpace(responseModel)
	if responseModel == "" || !gjson.ValidBytes(out) {
		return out
	}
	for _, path := range openCodeGoResponseModelPaths {
		if !gjson.GetBytes(out, path).Exists() {
			continue
		}
		updated, errSet := sjson.SetBytes(out, path, responseModel)
		if errSet != nil {
			continue
		}
		out = updated
	}
	return out
}

func wrapOpenCodeGoResponseStream(stream *cliproxyexecutor.StreamResult, responseModel string) *cliproxyexecutor.StreamResult {
	responseModel = strings.TrimSpace(responseModel)
	if stream == nil || stream.Chunks == nil || responseModel == "" {
		return stream
	}
	rewriter := cliproxyauth.NewStreamRewriter(cliproxyauth.StreamRewriteOptions{RewriteModel: responseModel})
	out := make(chan cliproxyexecutor.StreamChunk)
	go func() {
		defer close(out)
		for chunk := range stream.Chunks {
			if len(chunk.Payload) > 0 {
				chunk.Payload = rewriter.RewriteChunk(chunk.Payload)
			}
			out <- chunk
		}
		if flushed := rewriter.Finish(); len(flushed) > 0 {
			out <- cliproxyexecutor.StreamChunk{Payload: flushed}
		}
	}()
	return &cliproxyexecutor.StreamResult{
		Headers: stream.Headers,
		Chunks:  out,
	}
}

func selectedOpenCodeGoProtocol(auth *cliproxyauth.Auth) (string, error) {
	if auth == nil {
		return "", newOpenCodeGoRequestError(http.StatusUnauthorized, "opencode-go executor: selected auth is nil")
	}
	protocol := ""
	baseURL := ""
	if auth.Attributes != nil {
		protocol = normalizeOpenCodeGoProtocol(auth.Attributes["protocol"])
		baseURL = strings.TrimSpace(auth.Attributes["base_url"])
	}
	if protocol == "" {
		return "", newOpenCodeGoRequestError(http.StatusBadRequest, "opencode-go executor: selected auth %q is missing protocol metadata", auth.ID)
	}
	if baseURL == "" {
		return "", newOpenCodeGoRequestError(http.StatusBadRequest, "opencode-go executor: selected auth %q is missing base_url metadata", auth.ID)
	}
	return protocol, nil
}

func buildOpenCodeGoModelIndex(cfg *config.Config) openCodeGoModelIndex {
	index := openCodeGoModelIndex{
		entries:   map[string]openCodeGoModelMetadata{},
		ambiguous: map[string]struct{}{},
	}
	if cfg == nil {
		return index
	}
	for groupIndex := range cfg.OpenCodeGo.KeyGroups {
		group := &cfg.OpenCodeGo.KeyGroups[groupIndex]
		if group.Disabled {
			continue
		}
		addOpenCodeGoProtocolModels(&index, openCodeGoProtocolOpenAI, group.OpenAI)
		addOpenCodeGoProtocolModels(&index, openCodeGoProtocolClaude, group.Anthropic)
	}
	return index
}

func addOpenCodeGoProtocolModels(index *openCodeGoModelIndex, protocol string, cfg *config.OpenCodeGoProtocolConfig) {
	if index == nil || cfg == nil {
		return
	}
	prefix := strings.Trim(strings.TrimSpace(cfg.Prefix), "/")
	for _, model := range cfg.Models {
		for _, candidate := range []string{model.Name, model.Alias} {
			candidate = strings.TrimSpace(candidate)
			if candidate == "" {
				continue
			}
			addOpenCodeGoModelIndexEntry(index, candidate, protocol)
			if prefix != "" {
				addOpenCodeGoModelIndexEntry(index, prefix+"/"+candidate, protocol)
			}
		}
	}
}

func addOpenCodeGoModelIndexEntry(index *openCodeGoModelIndex, name string, protocol string) {
	name = strings.ToLower(strings.TrimSpace(name))
	if name == "" {
		return
	}
	if existing, ok := index.entries[name]; ok {
		if existing.protocol != protocol {
			index.ambiguous[name] = struct{}{}
		}
		return
	}
	if _, ambiguous := index.ambiguous[name]; ambiguous {
		return
	}
	index.entries[name] = openCodeGoModelMetadata{protocol: protocol}
}

func normalizeOpenCodeGoProtocol(protocol string) string {
	switch strings.ToLower(strings.TrimSpace(protocol)) {
	case openCodeGoProtocolOpenAI:
		return openCodeGoProtocolOpenAI
	case openCodeGoProtocolClaude, "anthropic":
		return openCodeGoProtocolClaude
	default:
		return ""
	}
}

func baseOpenCodeGoModel(model string) string {
	return strings.TrimSpace(thinking.ParseSuffix(model).ModelName)
}

type openCodeGoRequestError struct {
	statusErr
}

func (openCodeGoRequestError) IsRequestScoped() bool {
	return true
}

func newOpenCodeGoRequestError(status int, format string, args ...any) error {
	return openCodeGoRequestError{statusErr{code: status, msg: fmt.Sprintf(format, args...)}}
}
