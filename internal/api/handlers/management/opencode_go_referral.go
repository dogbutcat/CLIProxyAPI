package management

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"time"
	"unicode"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v7/sdk/proxyutil"
	log "github.com/sirupsen/logrus"
)

const (
	openCodeGoReferralHashSummary = "2a0b2fef5fd2ec9eff0cb5d4955e4ada4eece21fac85591ed4c09630168d4844"
	openCodeGoReferralHashPreview = "46625df0aecf05f270f7ae4612cde374d11350c8abaf8649027572228b8af150"
	openCodeGoReferralHashApply   = "f386778c1b78eade3e6acff87c9284e02fcd86826463c080526143c4fe8fff23"

	openCodeGoReferralUserAgent  = "Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/149.0.0.0 Safari/537.36"
	openCodeGoReferralMaxBodyLen = int64(2 << 20)
)

// OpenCodeGoReferralRuntime is the narrow runtime surface exposed to referral Management APIs.
type OpenCodeGoReferralRuntime interface {
	ResolveOpenCodeGoReferralWorkspace(ctx context.Context, workspaceID string) (authCookie string, proxyURL string, runtimeAvailable bool, found bool, err error)
}

type openCodeGoReferralHTTPDoer interface {
	Do(req *http.Request) (*http.Response, error)
}

var (
	openCodeGoReferralGatewayURL  = "https://opencode.ai/_server"
	newOpenCodeGoReferralHTTPDoer = func(proxyURL string) (openCodeGoReferralHTTPDoer, error) {
		return newOpenCodeGoReferralHTTPClient(proxyURL)
	}
)

// GetOpenCodeGoReferralSummary proxies the OpenCode referral summary RPC.
func (h *Handler) GetOpenCodeGoReferralSummary(c *gin.Context) {
	workspace, ok := h.resolveOpenCodeGoReferralWorkspace(c)
	if !ok {
		return
	}
	h.proxyOpenCodeGoReferral(c, http.MethodGet, openCodeGoReferralHashSummary, workspace.authCookie, workspace.proxyURL, workspace.workspaceID)
}

// GetOpenCodeGoReferralRewardPreview proxies a reward preview RPC.
func (h *Handler) GetOpenCodeGoReferralRewardPreview(c *gin.Context) {
	workspace, ok := h.resolveOpenCodeGoReferralWorkspace(c)
	if !ok {
		return
	}
	rewardID := strings.TrimSpace(c.Param("reward"))
	if !validOpenCodeGoReferralID(rewardID) {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid reward id"})
		return
	}
	h.proxyOpenCodeGoReferral(c, http.MethodGet, openCodeGoReferralHashPreview, workspace.authCookie, workspace.proxyURL, workspace.workspaceID, rewardID)
}

// PostOpenCodeGoReferralRewardApply proxies a reward redemption RPC.
func (h *Handler) PostOpenCodeGoReferralRewardApply(c *gin.Context) {
	workspace, ok := h.resolveOpenCodeGoReferralWorkspace(c)
	if !ok {
		return
	}
	rewardID := strings.TrimSpace(c.Param("reward"))
	if !validOpenCodeGoReferralID(rewardID) {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid reward id"})
		return
	}
	h.proxyOpenCodeGoReferral(c, http.MethodPost, openCodeGoReferralHashApply, workspace.authCookie, workspace.proxyURL, workspace.workspaceID, rewardID)
}

type openCodeGoReferralWorkspace struct {
	workspaceID string
	authCookie  string
	proxyURL    string
}

func (h *Handler) resolveOpenCodeGoReferralWorkspace(c *gin.Context) (openCodeGoReferralWorkspace, bool) {
	workspaceID := strings.TrimSpace(c.Param("workspace"))
	if !validOpenCodeGoReferralID(workspaceID) {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid workspace id"})
		return openCodeGoReferralWorkspace{}, false
	}
	runtime := h.openCodeGoReferralRuntimeSnapshot()
	if runtime == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "opencode-go referral runtime unavailable"})
		return openCodeGoReferralWorkspace{}, false
	}
	ctx := c.Request.Context()
	if err := ctx.Err(); err != nil {
		writeOpenCodeGoQuotaCanceled(c, err)
		return openCodeGoReferralWorkspace{}, false
	}
	authCookie, proxyURL, runtimeAvailable, found, errResolve := runtime.ResolveOpenCodeGoReferralWorkspace(ctx, workspaceID)
	if errResolve != nil {
		if errors.Is(errResolve, context.Canceled) || errors.Is(errResolve, context.DeadlineExceeded) {
			writeOpenCodeGoQuotaCanceled(c, errResolve)
			return openCodeGoReferralWorkspace{}, false
		}
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to resolve opencode-go referral workspace"})
		return openCodeGoReferralWorkspace{}, false
	}
	if !runtimeAvailable {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "opencode-go referral runtime unavailable"})
		return openCodeGoReferralWorkspace{}, false
	}
	if !found || strings.TrimSpace(authCookie) == "" {
		c.JSON(http.StatusNotFound, gin.H{"error": "workspace not found"})
		return openCodeGoReferralWorkspace{}, false
	}
	return openCodeGoReferralWorkspace{workspaceID: workspaceID, authCookie: strings.TrimSpace(authCookie), proxyURL: strings.TrimSpace(proxyURL)}, true
}

func (h *Handler) openCodeGoReferralRuntimeSnapshot() OpenCodeGoReferralRuntime {
	if h == nil {
		return nil
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.openCodeGoReferralRuntime
}

func (h *Handler) proxyOpenCodeGoReferral(c *gin.Context, method, hashID, authCookie, proxyURL string, args ...string) {
	statusCode, body, errProxy := proxyOpenCodeGoReferralRPC(c.Request.Context(), method, hashID, authCookie, proxyURL, args...)
	if errProxy != nil {
		if errors.Is(errProxy, context.Canceled) || errors.Is(errProxy, context.DeadlineExceeded) {
			writeOpenCodeGoQuotaCanceled(c, errProxy)
			return
		}
		c.JSON(http.StatusBadGateway, gin.H{"error": "upstream request failed"})
		return
	}
	if statusCode != http.StatusOK {
		c.Data(statusCode, "application/json; charset=utf-8", body)
		return
	}

	jsonBody, errConvert := openCodeGoSerovalToJSON(string(body))
	if errConvert != nil {
		if method == http.MethodPost {
			c.JSON(http.StatusOK, gin.H{"success": true})
			return
		}
		c.JSON(http.StatusBadGateway, gin.H{"error": "failed to parse upstream response"})
		return
	}
	c.Data(http.StatusOK, "application/json; charset=utf-8", []byte(jsonBody))
}

func proxyOpenCodeGoReferralRPC(ctx context.Context, method, hashID, authCookie, proxyURL string, args ...string) (int, []byte, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return 0, nil, err
	}
	payload, errPayload := buildOpenCodeGoReferralSerovalPayload(args...)
	if errPayload != nil {
		return 0, nil, errPayload
	}
	req, errRequest := buildOpenCodeGoReferralRequest(ctx, method, hashID, authCookie, payload, args...)
	if errRequest != nil {
		return 0, nil, errRequest
	}
	doer, errClient := newOpenCodeGoReferralHTTPDoer(proxyURL)
	if errClient != nil {
		return 0, nil, errClient
	}
	resp, errDo := doer.Do(req)
	if errDo != nil {
		return 0, nil, errDo
	}
	defer func() {
		if errClose := resp.Body.Close(); errClose != nil {
			log.WithError(errClose).Warn("opencode-go referral: failed to close upstream response body")
		}
	}()
	body, tooLarge, errRead := readOpenCodeGoReferralBounded(resp.Body, openCodeGoReferralMaxBodyLen)
	if errRead != nil {
		return 0, nil, fmt.Errorf("read upstream body: %w", errRead)
	}
	if tooLarge {
		return 0, nil, fmt.Errorf("upstream response exceeded %d bytes", openCodeGoReferralMaxBodyLen)
	}
	return resp.StatusCode, body, nil
}

func buildOpenCodeGoReferralRequest(ctx context.Context, method, hashID, authCookie, payload string, args ...string) (*http.Request, error) {
	gateway, errParse := url.Parse(openCodeGoReferralGatewayURL)
	if errParse != nil || gateway.Scheme == "" || gateway.Host == "" {
		return nil, fmt.Errorf("invalid referral gateway URL")
	}
	var req *http.Request
	var errRequest error
	switch method {
	case http.MethodGet:
		q := gateway.Query()
		q.Set("id", hashID)
		q.Set("args", payload)
		gateway.RawQuery = q.Encode()
		req, errRequest = http.NewRequestWithContext(ctx, http.MethodGet, gateway.String(), nil)
	case http.MethodPost:
		gateway.RawQuery = ""
		req, errRequest = http.NewRequestWithContext(ctx, http.MethodPost, gateway.String(), strings.NewReader(payload))
		if errRequest == nil {
			req.Header.Set("Content-Type", "application/json")
			req.Header.Set("x-single-flight", "true")
		}
	default:
		return nil, fmt.Errorf("unsupported referral method %q", method)
	}
	if errRequest != nil {
		return nil, fmt.Errorf("create referral request: %w", errRequest)
	}

	workspaceID := ""
	if len(args) > 0 {
		workspaceID = strings.TrimSpace(args[0])
	}
	req.Header.Set("User-Agent", openCodeGoReferralUserAgent)
	req.Header.Set("Accept", "*/*")
	req.Header.Set("Cookie", authCookie)
	req.Header.Set("x-server-instance", "server-fn:1")
	req.Header.Set("x-server-id", hashID)
	req.Header.Set("Origin", "https://opencode.ai")
	if workspaceID != "" {
		req.Header.Set("Referer", "https://opencode.ai/workspace/"+url.PathEscape(workspaceID)+"/go")
	} else {
		req.Header.Set("Referer", "https://opencode.ai/")
	}
	return req, nil
}

func newOpenCodeGoReferralHTTPClient(proxyURL string) (*http.Client, error) {
	client := &http.Client{}
	proxyURL = strings.TrimSpace(proxyURL)
	if proxyURL == "" {
		return client, nil
	}
	transport, _, errBuild := proxyutil.BuildHTTPTransport(proxyURL)
	if errBuild != nil {
		return nil, fmt.Errorf("build referral proxy transport: %w", errBuild)
	}
	if transport != nil {
		client.Transport = transport
	}
	return client, nil
}

func buildOpenCodeGoReferralSerovalPayload(args ...string) (string, error) {
	type serovalArg struct {
		T int    `json:"t"`
		S string `json:"s"`
	}
	type serovalTuple struct {
		T int          `json:"t"`
		I int          `json:"i"`
		O int          `json:"o"`
		L int          `json:"l"`
		A []serovalArg `json:"a"`
	}
	type serovalPayload struct {
		T serovalTuple `json:"t"`
		F int          `json:"f"`
		M []any        `json:"m"`
	}
	serializedArgs := make([]serovalArg, 0, len(args))
	for _, arg := range args {
		serializedArgs = append(serializedArgs, serovalArg{T: 1, S: arg})
	}
	payload := serovalPayload{
		T: serovalTuple{T: 9, I: 0, O: 0, L: len(serializedArgs), A: serializedArgs},
		F: 31,
		M: []any{},
	}
	data, errMarshal := json.Marshal(payload)
	if errMarshal != nil {
		return "", fmt.Errorf("marshal referral payload: %w", errMarshal)
	}
	return string(data), nil
}

func readOpenCodeGoReferralBounded(r io.Reader, maxBytes int64) ([]byte, bool, error) {
	if maxBytes < 0 {
		maxBytes = 0
	}
	body, errRead := io.ReadAll(io.LimitReader(r, maxBytes+1))
	if errRead != nil {
		return nil, false, errRead
	}
	if int64(len(body)) > maxBytes {
		return body[:maxBytes], true, nil
	}
	return body, false, nil
}

func validOpenCodeGoReferralID(id string) bool {
	id = strings.TrimSpace(id)
	if id == "" || len(id) > 256 {
		return false
	}
	for _, r := range id {
		if unicode.IsControl(r) || unicode.IsSpace(r) {
			return false
		}
		switch r {
		case '/', '\\', '?', '#', '&', '%':
			return false
		}
	}
	return true
}

func openCodeGoSerovalToJSON(raw string) (string, error) {
	startMarker := "$R[0]="
	startIdx := strings.Index(raw, startMarker)
	if startIdx < 0 {
		return "", fmt.Errorf("seroval: missing result marker")
	}
	objStart := startIdx + len(startMarker)
	depth := 0
	inString := false
	var stringChar byte
	objEnd := -1
	for i := objStart; i < len(raw); i++ {
		ch := raw[i]
		if inString {
			if ch == '\\' {
				i++
				continue
			}
			if ch == stringChar {
				inString = false
			}
			continue
		}
		if ch == '"' || ch == '\'' {
			inString = true
			stringChar = ch
			continue
		}
		if ch == '{' || ch == '[' {
			depth++
			continue
		}
		if ch == '}' || ch == ']' {
			depth--
			if depth == 0 {
				objEnd = i + 1
				break
			}
		}
	}
	if objEnd < 0 {
		return "", fmt.Errorf("seroval: unmatched result object")
	}

	js := raw[objStart:objEnd]
	js = reOpenCodeGoSerovalRef.ReplaceAllString(js, "")
	js = reOpenCodeGoSerovalDate.ReplaceAllStringFunc(js, func(match string) string {
		groups := reOpenCodeGoSerovalDate.FindStringSubmatch(match)
		if len(groups) != 2 {
			return match
		}
		parsed, errParse := time.Parse(time.RFC3339Nano, groups[1])
		if errParse != nil {
			parsed, errParse = time.Parse("2006-01-02T15:04:05.000Z", groups[1])
			if errParse != nil {
				return match
			}
		}
		return fmt.Sprintf("%d", parsed.UnixMilli())
	})
	js = strings.ReplaceAll(js, "!0", "true")
	js = strings.ReplaceAll(js, "!1", "false")
	js = reOpenCodeGoSerovalUnquotedKey.ReplaceAllString(js, `${1}"${2}":`)
	if !json.Valid([]byte(js)) {
		return "", fmt.Errorf("seroval: converted JSON is invalid")
	}
	return js, nil
}

var (
	reOpenCodeGoSerovalRef         = regexp.MustCompile(`\$R\[\d+\]=`)
	reOpenCodeGoSerovalDate        = regexp.MustCompile(`new Date\("([^"]+)"\)`)
	reOpenCodeGoSerovalUnquotedKey = regexp.MustCompile(`(^|[{,\[])([A-Za-z_][A-Za-z0-9_]*):`)
)
