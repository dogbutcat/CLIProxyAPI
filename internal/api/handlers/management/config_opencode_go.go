package management

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/watcher/synthesizer"
)

type openCodeGoConfigView struct {
	KeyGroups []openCodeGoKeyGroupView     `json:"key-groups,omitempty"`
	Quota     config.OpenCodeGoQuotaConfig `json:"quota,omitempty"`
}

type openCodeGoKeyGroupView struct {
	NamePrefix     string                           `json:"name-prefix"`
	Disabled       bool                             `json:"disabled,omitempty"`
	DisableCooling bool                             `json:"disable-cooling,omitempty"`
	Headers        map[string]string                `json:"headers,omitempty"`
	OpenAI         *config.OpenCodeGoProtocolConfig `json:"openai,omitempty"`
	Anthropic      *config.OpenCodeGoProtocolConfig `json:"anthropic,omitempty"`
	Keys           []openCodeGoKeyEntryView         `json:"keys,omitempty"`
	AuthIndexes    map[string]map[string]string     `json:"auth-indexes,omitempty"`
}

type openCodeGoKeyEntryView struct {
	KeyName     string            `json:"key-name"`
	APIKey      string            `json:"api-key,omitempty"`
	ProxyURL    string            `json:"proxy-url,omitempty"`
	WorkspaceID string            `json:"workspace-id,omitempty"`
	AuthCookie  string            `json:"auth-cookie,omitempty"`
	AuthIndexes map[string]string `json:"auth-indexes,omitempty"`
	AuthIndices map[string]string `json:"auth-indices,omitempty"`
}

type openCodeGoProtocolPatch struct {
	NameSuffix *string                        `json:"name-suffix"`
	BaseURL    *string                        `json:"base-url"`
	Prefix     *string                        `json:"prefix"`
	Priority   *int                           `json:"priority"`
	Models     *[]config.OpenCodeGoModelEntry `json:"models"`
}

type openCodeGoKeyGroupPatch struct {
	NamePrefix     *string                      `json:"name-prefix"`
	Disabled       *bool                        `json:"disabled"`
	DisableCooling *bool                        `json:"disable-cooling"`
	Headers        *map[string]string           `json:"headers"`
	OpenAI         *openCodeGoProtocolPatch     `json:"openai"`
	Anthropic      *openCodeGoProtocolPatch     `json:"anthropic"`
	Keys           *[]config.OpenCodeGoKeyEntry `json:"keys"`
}

type openCodeGoQuotaPatch struct {
	PollInterval *string  `json:"poll-interval"`
	Threshold    *float64 `json:"threshold"`
}

// GetOpenCodeGo returns the canonical OpenCode Go config without secrets.
func (h *Handler) GetOpenCodeGo(c *gin.Context) {
	cfgView := h.openCodeGoConfigWithAuthIndex()
	c.JSON(http.StatusOK, openCodeGoConfigResponse(cfgView))
}

// PutOpenCodeGo replaces the canonical OpenCode Go provider config.
func (h *Handler) PutOpenCodeGo(c *gin.Context) {
	cfgValue, ok := decodeOpenCodeGoConfigBody(c)
	if !ok {
		return
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.cfg == nil {
		h.cfg = &config.Config{}
	}
	candidate := h.cfg.CloneForRuntime()
	if candidate == nil {
		candidate = &config.Config{}
	}
	candidate.OpenCodeGo = cfgValue
	candidate.NormalizeOpenCodeGo()
	if errValidate := candidate.ValidateOpenCodeGoConfig(); errValidate != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": errValidate.Error()})
		return
	}
	h.cfg.OpenCodeGo = candidate.OpenCodeGo
	h.cfg.LegacyOpenCodeGoKeyGroups = nil
	h.cfg.Routing.OpenCodeGoPollInterval = ""
	h.cfg.Routing.OpenCodeGoPollThreshold = nil
	h.saveOpenCodeGoLocked(c)
}

// PatchOpenCodeGo updates quota settings or one key group in the canonical config.
func (h *Handler) PatchOpenCodeGo(c *gin.Context) {
	var body struct {
		Index      *int                     `json:"index"`
		NamePrefix *string                  `json:"name-prefix"`
		Value      *openCodeGoKeyGroupPatch `json:"value"`
		Quota      *openCodeGoQuotaPatch    `json:"quota"`
	}
	if errBind := c.ShouldBindJSON(&body); errBind != nil || (body.Value == nil && body.Quota == nil) {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid body"})
		return
	}

	h.mu.Lock()
	defer h.mu.Unlock()
	if h.cfg == nil {
		h.cfg = &config.Config{}
	}
	candidate := h.cfg.CloneForRuntime()
	if candidate == nil {
		candidate = &config.Config{}
	}
	candidate.NormalizeOpenCodeGo()
	if body.Quota != nil {
		applyOpenCodeGoQuotaPatch(&candidate.OpenCodeGo.Quota, body.Quota)
	}
	if body.Value != nil {
		targetIndex := findOpenCodeGoGroupIndex(candidate.OpenCodeGo.KeyGroups, body.Index, body.NamePrefix)
		if targetIndex < 0 {
			entry := config.OpenCodeGoKeyGroup{}
			applyOpenCodeGoGroupPatch(&entry, body.Value)
			candidate.OpenCodeGo.KeyGroups = append(candidate.OpenCodeGo.KeyGroups, entry)
		} else {
			entry := candidate.OpenCodeGo.KeyGroups[targetIndex]
			applyOpenCodeGoGroupPatch(&entry, body.Value)
			candidate.OpenCodeGo.KeyGroups[targetIndex] = entry
		}
	}
	candidate.NormalizeOpenCodeGo()
	if errValidate := candidate.ValidateOpenCodeGoConfig(); errValidate != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": errValidate.Error()})
		return
	}
	h.cfg.OpenCodeGo = candidate.OpenCodeGo
	h.cfg.LegacyOpenCodeGoKeyGroups = nil
	h.cfg.Routing.OpenCodeGoPollInterval = ""
	h.cfg.Routing.OpenCodeGoPollThreshold = nil
	h.saveOpenCodeGoLocked(c)
}

// DeleteOpenCodeGo deletes one OpenCode Go key group or clears the provider config.
func (h *Handler) DeleteOpenCodeGo(c *gin.Context) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.cfg == nil {
		h.cfg = &config.Config{}
	}
	candidate := h.cfg.CloneForRuntime()
	if candidate == nil {
		candidate = &config.Config{}
	}
	candidate.NormalizeOpenCodeGo()

	if indexText := strings.TrimSpace(c.Query("index")); indexText != "" {
		index, errParse := strconv.Atoi(indexText)
		if errParse != nil || index < 0 || index >= len(candidate.OpenCodeGo.KeyGroups) {
			c.JSON(http.StatusBadRequest, gin.H{"error": "invalid index"})
			return
		}
		candidate.OpenCodeGo.KeyGroups = append(candidate.OpenCodeGo.KeyGroups[:index], candidate.OpenCodeGo.KeyGroups[index+1:]...)
	} else if namePrefix := strings.TrimSpace(c.Query("name-prefix")); namePrefix != "" {
		if !deleteOpenCodeGoGroupByNamePrefix(&candidate.OpenCodeGo.KeyGroups, namePrefix) {
			c.JSON(http.StatusNotFound, gin.H{"error": "item not found"})
			return
		}
	} else if name := strings.TrimSpace(c.Query("name")); name != "" {
		if !deleteOpenCodeGoGroupByNamePrefix(&candidate.OpenCodeGo.KeyGroups, name) {
			c.JSON(http.StatusNotFound, gin.H{"error": "item not found"})
			return
		}
	} else {
		candidate.OpenCodeGo = config.OpenCodeGoConfig{}
	}

	candidate.NormalizeOpenCodeGo()
	if errValidate := candidate.ValidateOpenCodeGoConfig(); errValidate != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": errValidate.Error()})
		return
	}
	h.cfg.OpenCodeGo = candidate.OpenCodeGo
	h.cfg.LegacyOpenCodeGoKeyGroups = nil
	h.cfg.Routing.OpenCodeGoPollInterval = ""
	h.cfg.Routing.OpenCodeGoPollThreshold = nil
	h.saveOpenCodeGoLocked(c)
}

func deleteOpenCodeGoGroupByNamePrefix(groups *[]config.OpenCodeGoKeyGroup, namePrefix string) bool {
	if groups == nil {
		return false
	}
	out := make([]config.OpenCodeGoKeyGroup, 0, len(*groups))
	for _, group := range *groups {
		if strings.TrimSpace(group.NamePrefix) != namePrefix {
			out = append(out, group)
		}
	}
	if len(out) == len(*groups) {
		return false
	}
	*groups = out
	return true
}

func decodeOpenCodeGoConfigBody(c *gin.Context) (config.OpenCodeGoConfig, bool) {
	data, errRead := c.GetRawData()
	if errRead != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "failed to read body"})
		return config.OpenCodeGoConfig{}, false
	}
	var wrapped struct {
		OpenCodeGo *config.OpenCodeGoConfig `json:"opencode-go"`
	}
	if errUnmarshal := json.Unmarshal(data, &wrapped); errUnmarshal == nil && wrapped.OpenCodeGo != nil {
		return *wrapped.OpenCodeGo, true
	}
	var legacyGroups []config.OpenCodeGoKeyGroup
	if errUnmarshal := json.Unmarshal(data, &legacyGroups); errUnmarshal == nil {
		return config.OpenCodeGoConfig{KeyGroups: legacyGroups}, true
	}
	var direct config.OpenCodeGoConfig
	if errUnmarshal := json.Unmarshal(data, &direct); errUnmarshal != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid body"})
		return config.OpenCodeGoConfig{}, false
	}
	return direct, true
}

func (h *Handler) openCodeGoConfigWithAuthIndex() openCodeGoConfigView {
	if h == nil {
		return openCodeGoConfigView{}
	}
	liveIndexByID := h.liveAuthIndexByID()
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.cfg == nil {
		return openCodeGoConfigView{}
	}
	cfgSnapshot := h.cfg.CloneForRuntime()
	if cfgSnapshot == nil {
		return openCodeGoConfigView{}
	}
	cfgSnapshot.NormalizeOpenCodeGo()
	return openCodeGoConfigViewFor(cfgSnapshot.OpenCodeGo, liveIndexByID)
}

func openCodeGoConfigViewFor(cfgValue config.OpenCodeGoConfig, liveIndexByID map[string]string) openCodeGoConfigView {
	out := openCodeGoConfigView{
		Quota:     cfgValue.Quota,
		KeyGroups: make([]openCodeGoKeyGroupView, 0, len(cfgValue.KeyGroups)),
	}
	idGen := synthesizer.NewStableIDGenerator()
	for groupIndex := range cfgValue.KeyGroups {
		group := cfgValue.KeyGroups[groupIndex]
		groupView := openCodeGoKeyGroupView{
			NamePrefix:     group.NamePrefix,
			Disabled:       group.Disabled,
			DisableCooling: group.DisableCooling,
			Headers:        group.Headers,
			OpenAI:         cloneOpenCodeGoProtocol(group.OpenAI),
			Anthropic:      cloneOpenCodeGoProtocol(group.Anthropic),
			Keys:           make([]openCodeGoKeyEntryView, 0, len(group.Keys)),
			AuthIndexes:    map[string]map[string]string{},
		}
		for keyIndex := range group.Keys {
			key := group.Keys[keyIndex]
			keyIndexes := map[string]string{}
			authID, _ := idGen.Next("opencode-go", group.NamePrefix, key.KeyName)
			authIndex := liveIndexByID[authID]
			for _, protocol := range []struct {
				name string
				cfg  *config.OpenCodeGoProtocolConfig
			}{
				{name: "openai", cfg: group.OpenAI},
				{name: "anthropic", cfg: group.Anthropic},
			} {
				if protocol.cfg == nil {
					continue
				}
				if authIndex != "" {
					keyIndexes[protocol.name] = authIndex
				}
			}
			keyView := openCodeGoKeyEntryView{
				KeyName:     key.KeyName,
				ProxyURL:    key.ProxyURL,
				WorkspaceID: key.WorkspaceID,
			}
			if len(keyIndexes) > 0 {
				keyView.AuthIndexes = keyIndexes
				keyView.AuthIndices = keyIndexes
				groupView.AuthIndexes[key.KeyName] = keyIndexes
			}
			groupView.Keys = append(groupView.Keys, keyView)
		}
		if len(groupView.AuthIndexes) == 0 {
			groupView.AuthIndexes = nil
		}
		out.KeyGroups = append(out.KeyGroups, groupView)
	}
	return out
}

func cloneOpenCodeGoProtocol(protocol *config.OpenCodeGoProtocolConfig) *config.OpenCodeGoProtocolConfig {
	if protocol == nil {
		return nil
	}
	clone := *protocol
	if len(protocol.Models) > 0 {
		clone.Models = append([]config.OpenCodeGoModelEntry(nil), protocol.Models...)
	}
	return &clone
}

func applyOpenCodeGoQuotaPatch(target *config.OpenCodeGoQuotaConfig, patch *openCodeGoQuotaPatch) {
	if target == nil || patch == nil {
		return
	}
	if patch.PollInterval != nil {
		target.PollInterval = strings.TrimSpace(*patch.PollInterval)
	}
	if patch.Threshold != nil {
		threshold := *patch.Threshold
		target.Threshold = &threshold
	}
}

func applyOpenCodeGoGroupPatch(target *config.OpenCodeGoKeyGroup, patch *openCodeGoKeyGroupPatch) {
	if target == nil || patch == nil {
		return
	}
	if patch.NamePrefix != nil {
		target.NamePrefix = strings.TrimSpace(*patch.NamePrefix)
	}
	if patch.Disabled != nil {
		target.Disabled = *patch.Disabled
	}
	if patch.DisableCooling != nil {
		target.DisableCooling = *patch.DisableCooling
	}
	if patch.Headers != nil {
		target.Headers = config.NormalizeHeaders(*patch.Headers)
	}
	if patch.Keys != nil {
		target.Keys = append([]config.OpenCodeGoKeyEntry(nil), (*patch.Keys)...)
	}
	if patch.OpenAI != nil {
		if target.OpenAI == nil {
			target.OpenAI = &config.OpenCodeGoProtocolConfig{}
		}
		applyOpenCodeGoProtocolPatch(target.OpenAI, patch.OpenAI)
	}
	if patch.Anthropic != nil {
		if target.Anthropic == nil {
			target.Anthropic = &config.OpenCodeGoProtocolConfig{}
		}
		applyOpenCodeGoProtocolPatch(target.Anthropic, patch.Anthropic)
	}
}

func applyOpenCodeGoProtocolPatch(target *config.OpenCodeGoProtocolConfig, patch *openCodeGoProtocolPatch) {
	if target == nil || patch == nil {
		return
	}
	if patch.NameSuffix != nil {
		target.NameSuffix = strings.TrimSpace(*patch.NameSuffix)
	}
	if patch.BaseURL != nil {
		target.BaseURL = strings.TrimSpace(*patch.BaseURL)
	}
	if patch.Prefix != nil {
		target.Prefix = strings.TrimSpace(*patch.Prefix)
	}
	if patch.Priority != nil {
		target.Priority = *patch.Priority
	}
	if patch.Models != nil {
		target.Models = append([]config.OpenCodeGoModelEntry(nil), (*patch.Models)...)
	}
}

func findOpenCodeGoGroupIndex(groups []config.OpenCodeGoKeyGroup, index *int, namePrefix *string) int {
	if index != nil {
		if *index >= 0 && *index < len(groups) {
			return *index
		}
		return -1
	}
	if namePrefix == nil {
		return -1
	}
	match := strings.TrimSpace(*namePrefix)
	if match == "" {
		return -1
	}
	for i := range groups {
		if strings.TrimSpace(groups[i].NamePrefix) == match {
			return i
		}
	}
	return -1
}

func (h *Handler) saveOpenCodeGoLocked(c *gin.Context) {
	if errSave := config.SaveConfigPreserveComments(h.configFilePath, h.cfg); errSave != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": fmt.Sprintf("failed to save config: %v", errSave)})
		return
	}
	snapshot := h.reloadSnapshotConfigLocked()
	response := openCodeGoConfigViewFor(h.cfg.OpenCodeGo, liveAuthIndexByIDFromManager(h.authManager))
	c.JSON(http.StatusOK, openCodeGoConfigResponse(response))
	reqCtx := c.Request.Context()
	h.reloadConfigAfterManagementSaveAsync(reqCtx, snapshot)
}

func openCodeGoConfigResponse(cfgView openCodeGoConfigView) gin.H {
	return gin.H{
		"opencode-go": cfgView,
		"key-groups":  cfgView.KeyGroups,
		"quota":       cfgView.Quota,
	}
}
