package executor

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/websocket"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/executor"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v7/sdk/translator"
	"github.com/tidwall/gjson"
)

func TestCodexFinalWireConstraintsWebSocketExecute(t *testing.T) {
	executor := NewCodexWebsocketsExecutor(codexHTTPFinalWireConfig())
	auth, frames, closeServer := codexWebSocketFinalWireServer(t)
	defer closeServer()

	if _, err := executor.Execute(context.Background(), auth, codexWebSocketFinalWireRequest(), codexWebSocketFinalWireOptions(false, "")); err != nil {
		t.Fatalf("Execute error: %v", err)
	}
	initialFrame := readCodexWebSocketFinalWireFrame(t, frames)
	assertCodexWebSocketFinalWireFrame(t, initialFrame)

	retrySessionID := "codex-final-wire-ws-execute-retry"
	seedClosedCodexWebSocketSession(t, executor, retrySessionID, auth)
	if _, err := executor.Execute(context.Background(), auth, codexWebSocketFinalWireRequest(), codexWebSocketFinalWireOptions(false, retrySessionID)); err != nil {
		t.Fatalf("retry Execute error: %v", err)
	}
	retryFrame := readCodexWebSocketFinalWireFrame(t, frames)
	assertCodexWebSocketFinalWireFrame(t, retryFrame)
	assertCodexWebSocketFinalWireFramesEqual(t, initialFrame, retryFrame)
}

func TestCodexFinalWireConstraintsWebSocketStream(t *testing.T) {
	executor := NewCodexWebsocketsExecutor(codexHTTPFinalWireConfig())
	auth, frames, closeServer := codexWebSocketFinalWireServer(t)
	defer closeServer()

	result, err := executor.ExecuteStream(context.Background(), auth, codexWebSocketFinalWireRequest(), codexWebSocketFinalWireOptions(true, ""))
	if err != nil {
		t.Fatalf("ExecuteStream error: %v", err)
	}
	drainCodexWebSocketFinalWireStream(t, result)
	initialFrame := readCodexWebSocketFinalWireFrame(t, frames)
	assertCodexWebSocketFinalWireFrame(t, initialFrame)

	retrySessionID := "codex-final-wire-ws-stream-retry"
	seedClosedCodexWebSocketSession(t, executor, retrySessionID, auth)
	result, err = executor.ExecuteStream(context.Background(), auth, codexWebSocketFinalWireRequest(), codexWebSocketFinalWireOptions(true, retrySessionID))
	if err != nil {
		t.Fatalf("retry ExecuteStream error: %v", err)
	}
	drainCodexWebSocketFinalWireStream(t, result)
	retryFrame := readCodexWebSocketFinalWireFrame(t, frames)
	assertCodexWebSocketFinalWireFrame(t, retryFrame)
	assertCodexWebSocketFinalWireFramesEqual(t, initialFrame, retryFrame)
}

func codexWebSocketFinalWireRequest() cliproxyexecutor.Request {
	return cliproxyexecutor.Request{
		Model:   "gpt-5.4",
		Payload: []byte(codexHTTPFinalWirePayload),
	}
}

func codexWebSocketFinalWireOptions(stream bool, executionSessionID string) cliproxyexecutor.Options {
	opts := cliproxyexecutor.Options{
		SourceFormat: sdktranslator.FromString("openai-response"),
		Stream:       stream,
	}
	if executionSessionID != "" {
		opts.Metadata = map[string]any{cliproxyexecutor.ExecutionSessionMetadataKey: executionSessionID}
	}
	return opts
}

func codexWebSocketFinalWireServer(t *testing.T) (*cliproxyauth.Auth, <-chan []byte, func()) {
	t.Helper()

	upgrader := websocket.Upgrader{CheckOrigin: func(*http.Request) bool { return true }}
	frames := make(chan []byte, 4)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/responses" {
			t.Errorf("request path = %s, want /responses", r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
			return
		}
		conn, errUpgrade := upgrader.Upgrade(w, r, nil)
		if errUpgrade != nil {
			t.Errorf("upgrade websocket: %v", errUpgrade)
			return
		}
		defer func() { _ = conn.Close() }()

		_, payload, errRead := conn.ReadMessage()
		if errRead != nil {
			t.Errorf("read upstream websocket message: %v", errRead)
			return
		}
		frames <- bytes.Clone(payload)
		completed := []byte(`{"type":"response.completed","response":{"id":"resp_ws_final","object":"response","status":"completed","output":[],"usage":{"input_tokens":1,"output_tokens":1,"total_tokens":2}}}`)
		if errWrite := conn.WriteMessage(websocket.TextMessage, completed); errWrite != nil {
			t.Errorf("write completed websocket message: %v", errWrite)
		}
	}))

	auth := &cliproxyauth.Auth{
		ID:       "auth-ws-final",
		Provider: "codex",
		Attributes: map[string]string{
			"base_url": server.URL,
			"api_key":  "test",
		},
	}
	return auth, frames, server.Close
}

func readCodexWebSocketFinalWireFrame(t *testing.T, frames <-chan []byte) []byte {
	t.Helper()

	select {
	case frame := <-frames:
		return frame
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for upstream websocket frame")
	}
	return nil
}

func drainCodexWebSocketFinalWireStream(t *testing.T, result *cliproxyexecutor.StreamResult) {
	t.Helper()

	for {
		select {
		case chunk, ok := <-result.Chunks:
			if !ok {
				return
			}
			if chunk.Err != nil {
				t.Fatalf("stream chunk error: %v", chunk.Err)
			}
		case <-time.After(5 * time.Second):
			t.Fatal("timed out waiting for websocket stream completion")
		}
	}
}

func seedClosedCodexWebSocketSession(t *testing.T, executor *CodexWebsocketsExecutor, executionSessionID string, auth *cliproxyauth.Auth) {
	t.Helper()

	staleUpgrader := websocket.Upgrader{CheckOrigin: func(*http.Request) bool { return true }}
	staleServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, errUpgrade := staleUpgrader.Upgrade(w, r, nil)
		if errUpgrade != nil {
			t.Errorf("upgrade stale websocket: %v", errUpgrade)
			return
		}
		defer func() { _ = conn.Close() }()
		<-r.Context().Done()
	}))
	defer staleServer.Close()

	staleConn, _, errDial := websocket.DefaultDialer.Dial("ws"+strings.TrimPrefix(staleServer.URL, "http"), nil)
	if errDial != nil {
		t.Fatalf("dial stale websocket: %v", errDial)
	}
	if errClose := staleConn.Close(); errClose != nil {
		t.Fatalf("close stale websocket: %v", errClose)
	}

	httpURL := strings.TrimSuffix(auth.Attributes["base_url"], "/") + "/responses"
	targetWSURL, errURL := buildCodexResponsesWebsocketURL(httpURL)
	if errURL != nil {
		t.Fatalf("build target websocket URL: %v", errURL)
	}

	sess := executor.getOrCreateSession(executionSessionID)
	sess.connMu.Lock()
	sess.conn = staleConn
	sess.connCloser = newWebsocketConnectionCloser(staleConn)
	sess.authID = auth.ID
	sess.wsURL = targetWSURL
	sess.readerConn = staleConn
	sess.connMu.Unlock()
	t.Cleanup(func() { executor.CloseExecutionSession(executionSessionID) })
}

func assertCodexWebSocketFinalWireFrame(t *testing.T, frame []byte) {
	t.Helper()

	if !json.Valid(frame) {
		t.Fatalf("frame is not valid JSON: %s", string(frame))
	}
	if got := gjson.GetBytes(frame, "type").String(); got != "response.create" {
		t.Fatalf("type = %q, want response.create; frame=%s", got, string(frame))
	}
	if got := gjson.GetBytes(frame, "model").String(); got != "gpt-5.4" {
		t.Fatalf("model = %q, want gpt-5.4; frame=%s", got, string(frame))
	}
	if got := gjson.GetBytes(frame, "store"); got.Type != gjson.False {
		t.Fatalf("store = %s, want false; frame=%s", got.Raw, string(frame))
	}
	if got := gjson.GetBytes(frame, "stream"); got.Type != gjson.True {
		t.Fatalf("stream = %s, want true; frame=%s", got.Raw, string(frame))
	}
	if got := gjson.GetBytes(frame, "parallel_tool_calls"); got.Type != gjson.True {
		t.Fatalf("parallel_tool_calls = %s, want true; frame=%s", got.Raw, string(frame))
	}
	include := gjson.GetBytes(frame, "include").Array()
	if len(include) != 1 || include[0].String() != "reasoning.encrypted_content" {
		t.Fatalf("include = %s, want reasoning.encrypted_content only; frame=%s", gjson.GetBytes(frame, "include").Raw, string(frame))
	}
	for _, field := range []string{
		"max_output_tokens",
		"max_completion_tokens",
		"temperature",
		"top_p",
		"truncation",
		"user",
		"context_management",
		"service_tier",
	} {
		if gjson.GetBytes(frame, field).Exists() {
			t.Fatalf("rejected field %q survived; frame=%s", field, string(frame))
		}
	}
	if got := gjson.GetBytes(frame, "tools.0.type").String(); got != "web_search" {
		t.Fatalf("tools.0.type = %q, want web_search; frame=%s", got, string(frame))
	}
	if got := gjson.GetBytes(frame, "tool_choice.type").String(); got != "web_search" {
		t.Fatalf("tool_choice.type = %q, want web_search; frame=%s", got, string(frame))
	}
	if got := gjson.GetBytes(frame, "input.0.role").String(); got != "developer" {
		t.Fatalf("input.0.role = %q, want developer; frame=%s", got, string(frame))
	}
	expectedPromptCacheKey := codexIdentityConfuseUUID("auth-ws-final", "prompt-cache", "cache-http-final")
	if got := gjson.GetBytes(frame, "prompt_cache_key").String(); got != expectedPromptCacheKey {
		t.Fatalf("prompt_cache_key = %q, want confused key %q; frame=%s", got, expectedPromptCacheKey, string(frame))
	}
	expectedInstallationID := codexIdentityConfuseUUID("auth-ws-final", "installation", "install-http-final")
	if got := gjson.GetBytes(frame, "client_metadata.x-codex-installation-id").String(); got != expectedInstallationID {
		t.Fatalf("installation id = %q, want confused id %q; frame=%s", got, expectedInstallationID, string(frame))
	}
	if got := gjson.GetBytes(frame, "client_metadata.injected").String(); got != "after-translation" {
		t.Fatalf("payload override marker = %q, want after-translation; frame=%s", got, string(frame))
	}
}

func assertCodexWebSocketFinalWireFramesEqual(t *testing.T, initialFrame, retryFrame []byte) {
	t.Helper()

	var initial any
	var retry any
	if err := json.Unmarshal(initialFrame, &initial); err != nil {
		t.Fatalf("unmarshal initial frame: %v", err)
	}
	if err := json.Unmarshal(retryFrame, &retry); err != nil {
		t.Fatalf("unmarshal retry frame: %v", err)
	}
	if !reflect.DeepEqual(initial, retry) {
		t.Fatalf("retry frame differs from initial frame:\ninitial=%s\nretry=%s", string(initialFrame), string(retryFrame))
	}
}
