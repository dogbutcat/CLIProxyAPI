package management

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/watcher/synthesizer"
	coreauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
)

func TestGetOpenCodeGoReturnsSafeIdentityAndUsesAuthIndexes(t *testing.T) {
	idGen := synthesizer.NewStableIDGenerator()
	authID, _ := idGen.Next("opencode-go", "opencode-go", "acct-a")

	manager := coreauth.NewManager(nil, nil, nil)
	if _, errRegister := manager.Register(context.Background(), &coreauth.Auth{ID: authID, Provider: "opencode-go", Index: "key-index"}); errRegister != nil {
		t.Fatalf("register key auth: %v", errRegister)
	}

	h := NewHandlerWithoutConfigFilePath(&config.Config{
		LegacyOpenCodeGoKeyGroups: []config.OpenCodeGoKeyGroup{{
			Keys: []config.OpenCodeGoKeyEntry{{
				KeyName:     " acct-a ",
				APIKey:      "secret-api-key",
				WorkspaceID: " workspace-a ",
				AuthCookie:  "secret-cookie",
			}},
			OpenAI:    &config.OpenCodeGoProtocolConfig{BaseURL: " https://openai.example.com/v1 ", Models: []config.OpenCodeGoModelEntry{{Name: "gpt-5"}}},
			Anthropic: &config.OpenCodeGoProtocolConfig{BaseURL: " https://anthropic.example.com ", Models: []config.OpenCodeGoModelEntry{{Name: "claude-sonnet"}}},
		}},
	}, manager)

	rec := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(rec)
	ctx.Request = httptest.NewRequest(http.MethodGet, "/v0/management/opencode-go", nil)
	h.GetOpenCodeGo(ctx)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d; body=%s", rec.Code, http.StatusOK, rec.Body.String())
	}
	var body struct {
		OpenCodeGo struct {
			KeyGroups []struct {
				NamePrefix string `json:"name-prefix"`
				Keys       []struct {
					KeyName     string            `json:"key-name"`
					APIKey      string            `json:"api-key"`
					WorkspaceID string            `json:"workspace-id"`
					AuthCookie  string            `json:"auth-cookie"`
					AuthIndexes map[string]string `json:"auth-indexes"`
					AuthIndices map[string]string `json:"auth-indices"`
				} `json:"keys"`
			} `json:"key-groups"`
		} `json:"opencode-go"`
		KeyGroups []struct {
			NamePrefix string `json:"name-prefix"`
		} `json:"key-groups"`
	}
	if errUnmarshal := json.Unmarshal(rec.Body.Bytes(), &body); errUnmarshal != nil {
		t.Fatalf("decode response: %v", errUnmarshal)
	}
	if len(body.OpenCodeGo.KeyGroups) != 1 || len(body.OpenCodeGo.KeyGroups[0].Keys) != 1 {
		t.Fatalf("unexpected response shape: %#v", body)
	}
	if len(body.KeyGroups) != 1 {
		t.Fatalf("legacy key-groups count = %d, want 1; body=%s", len(body.KeyGroups), rec.Body.String())
	}
	key := body.OpenCodeGo.KeyGroups[0].Keys[0]
	if key.KeyName != "acct-a" || key.WorkspaceID != "workspace-a" {
		t.Fatalf("normalized key view = %#v", key)
	}
	if key.APIKey != "" || key.AuthCookie != "" || strings.Contains(rec.Body.String(), "secret-api-key") || strings.Contains(rec.Body.String(), "secret-cookie") {
		t.Fatalf("secret key material leaked in management response: %s", rec.Body.String())
	}
	if key.AuthIndexes["openai"] != "key-index" || key.AuthIndexes["anthropic"] != "key-index" {
		t.Fatalf("auth indexes = %#v", key.AuthIndexes)
	}
	if key.AuthIndices["openai"] != "key-index" || key.AuthIndices["anthropic"] != "key-index" {
		t.Fatalf("auth indices = %#v", key.AuthIndices)
	}
}

func TestPatchOpenCodeGoNormalizesLegacyPersistsCanonicalAndRedactsResponse(t *testing.T) {
	path := writeTestConfigFile(t)
	legacy := `routing:
  opencode-go-poll-interval: 10m
  opencode-go-poll-threshold: 5
key-groups:
  - name-prefix: opencode-go
    openai:
      base-url: https://openai.example.com/v1
      models: [gpt-5]
    keys:
      - key-name: acct-a
        api-key: secret-api-key
        workspace-id: workspace-a
        auth-cookie: secret-cookie
`
	if errWrite := os.WriteFile(path, []byte(legacy), 0o600); errWrite != nil {
		t.Fatalf("write legacy config: %v", errWrite)
	}
	threshold := 5.0
	h := &Handler{
		cfg: &config.Config{
			Routing: config.RoutingConfig{OpenCodeGoPollInterval: "10m", OpenCodeGoPollThreshold: &threshold},
			LegacyOpenCodeGoKeyGroups: []config.OpenCodeGoKeyGroup{{
				OpenAI: &config.OpenCodeGoProtocolConfig{BaseURL: "https://openai.example.com/v1", Models: []config.OpenCodeGoModelEntry{{Name: "gpt-5"}}},
				Keys:   []config.OpenCodeGoKeyEntry{{KeyName: "acct-a", APIKey: "secret-api-key", WorkspaceID: "workspace-a", AuthCookie: "secret-cookie"}},
			}},
		},
		configFilePath: path,
	}

	rec := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(rec)
	ctx.Request = httptest.NewRequest(http.MethodPatch, "/v0/management/opencode-go", strings.NewReader(`{"quota":{"poll-interval":"15m","threshold":7}}`))
	ctx.Request.Header.Set("Content-Type", "application/json")
	h.PatchOpenCodeGo(ctx)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d; body=%s", rec.Code, http.StatusOK, rec.Body.String())
	}
	for _, secret := range []string{"secret-api-key", "secret-cookie"} {
		if strings.Contains(rec.Body.String(), secret) {
			t.Fatalf("response leaked key material %q: %s", secret, rec.Body.String())
		}
	}
	if h.cfg.OpenCodeGo.Quota.PollInterval != "15m" || h.cfg.OpenCodeGo.Quota.Threshold == nil || *h.cfg.OpenCodeGo.Quota.Threshold != 7 {
		t.Fatalf("quota not patched: %#v", h.cfg.OpenCodeGo.Quota)
	}
	if len(h.cfg.LegacyOpenCodeGoKeyGroups) != 0 || h.cfg.Routing.OpenCodeGoPollInterval != "" || h.cfg.Routing.OpenCodeGoPollThreshold != nil {
		t.Fatalf("legacy fields not cleared: %#v %#v", h.cfg.LegacyOpenCodeGoKeyGroups, h.cfg.Routing)
	}

	savedBytes, errRead := os.ReadFile(path)
	if errRead != nil {
		t.Fatalf("read saved config: %v", errRead)
	}
	saved := string(savedBytes)
	if strings.Contains(saved, "\nkey-groups:") || strings.Contains(saved, "opencode-go-poll-") {
		t.Fatalf("saved config retained legacy OpenCodeGo keys:\n%s", saved)
	}
	if !strings.Contains(saved, "opencode-go:") || !strings.Contains(saved, "poll-interval: 15m") || !strings.Contains(saved, "threshold: 7") {
		t.Fatalf("saved config missing canonical OpenCodeGo block:\n%s", saved)
	}
}

func TestOpenCodeGoLegacyCRUDShapesRemainSupported(t *testing.T) {
	h := &Handler{
		cfg:            &config.Config{},
		configFilePath: writeTestConfigFile(t),
	}

	putRec := httptest.NewRecorder()
	putCtx, _ := gin.CreateTestContext(putRec)
	putCtx.Request = httptest.NewRequest(http.MethodPut, "/v0/management/opencode-go", strings.NewReader(`[
		{
			"name-prefix":"old-prefix",
			"openai":{"base-url":"https://openai.example.com/v1","models":["gpt-5"]},
			"keys":[{"key-name":"acct-a","api-key":"secret-a","workspace-id":"workspace-a"}]
		}
	]`))
	putCtx.Request.Header.Set("Content-Type", "application/json")
	h.PutOpenCodeGo(putCtx)
	if putRec.Code != http.StatusOK {
		t.Fatalf("put status = %d, want %d; body=%s", putRec.Code, http.StatusOK, putRec.Body.String())
	}
	if len(h.cfg.OpenCodeGo.KeyGroups) != 1 || h.cfg.OpenCodeGo.KeyGroups[0].NamePrefix != "old-prefix" {
		t.Fatalf("put key groups = %#v", h.cfg.OpenCodeGo.KeyGroups)
	}
	var putBody struct {
		KeyGroups []struct {
			NamePrefix string `json:"name-prefix"`
		} `json:"key-groups"`
	}
	if errUnmarshal := json.Unmarshal(putRec.Body.Bytes(), &putBody); errUnmarshal != nil {
		t.Fatalf("decode put response: %v", errUnmarshal)
	}
	if len(putBody.KeyGroups) != 1 || putBody.KeyGroups[0].NamePrefix != "old-prefix" {
		t.Fatalf("legacy put response = %#v; body=%s", putBody, putRec.Body.String())
	}
	if strings.Contains(putRec.Body.String(), "secret-a") {
		t.Fatalf("legacy put response leaked key material: %s", putRec.Body.String())
	}

	patchRec := httptest.NewRecorder()
	patchCtx, _ := gin.CreateTestContext(patchRec)
	patchCtx.Request = httptest.NewRequest(http.MethodPatch, "/v0/management/opencode-go", strings.NewReader(`{
		"name-prefix":"new-prefix",
		"value":{
			"name-prefix":"new-prefix",
			"anthropic":{"base-url":"https://anthropic.example.com","models":["claude-sonnet"]},
			"keys":[{"key-name":"acct-b","api-key":"secret-b","workspace-id":"workspace-b"}]
		}
	}`))
	patchCtx.Request.Header.Set("Content-Type", "application/json")
	h.PatchOpenCodeGo(patchCtx)
	if patchRec.Code != http.StatusOK {
		t.Fatalf("patch status = %d, want %d; body=%s", patchRec.Code, http.StatusOK, patchRec.Body.String())
	}
	if len(h.cfg.OpenCodeGo.KeyGroups) != 2 || h.cfg.OpenCodeGo.KeyGroups[1].NamePrefix != "new-prefix" {
		t.Fatalf("patch key groups = %#v", h.cfg.OpenCodeGo.KeyGroups)
	}
	if strings.Contains(patchRec.Body.String(), "secret-b") {
		t.Fatalf("legacy patch response leaked key material: %s", patchRec.Body.String())
	}

	deleteRec := httptest.NewRecorder()
	deleteCtx, _ := gin.CreateTestContext(deleteRec)
	deleteCtx.Request = httptest.NewRequest(http.MethodDelete, "/v0/management/opencode-go?name=old-prefix", nil)
	h.DeleteOpenCodeGo(deleteCtx)
	if deleteRec.Code != http.StatusOK {
		t.Fatalf("delete status = %d, want %d; body=%s", deleteRec.Code, http.StatusOK, deleteRec.Body.String())
	}
	if len(h.cfg.OpenCodeGo.KeyGroups) != 1 || h.cfg.OpenCodeGo.KeyGroups[0].NamePrefix != "new-prefix" {
		t.Fatalf("delete key groups = %#v", h.cfg.OpenCodeGo.KeyGroups)
	}
}
