package main

import (
	"bytes"
	"encoding/json"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"

	"gopkg.in/yaml.v3"
)

const claudeDDModelPrefix = "claude-fable-5-dd-"

type lifecycleRequest struct {
	ConfigYAML    []byte `json:"config_yaml"`
	SchemaVersion uint32 `json:"schema_version"`
}

type registration struct {
	SchemaVersion uint32                   `json:"schema_version"`
	Metadata      pluginMetadata           `json:"metadata"`
	Capabilities  registrationCapabilities `json:"capabilities"`
}

type pluginMetadata struct {
	Name             string        `json:"Name"`
	Version          string        `json:"Version"`
	Author           string        `json:"Author"`
	GitHubRepository string        `json:"GitHubRepository"`
	Logo             string        `json:"Logo,omitempty"`
	ConfigFields     []configField `json:"ConfigFields"`
}

type configField struct {
	Name        string   `json:"Name"`
	Type        string   `json:"Type"`
	Default     any      `json:"Default,omitempty"`
	EnumValues  []string `json:"EnumValues,omitempty"`
	Description string   `json:"Description"`
}

type registrationCapabilities struct {
	ResponseInterceptor bool `json:"response_interceptor"`
	ManagementAPI       bool `json:"management_api"`
}

type modelRule struct {
	ID          string `yaml:"id" json:"id"`
	DisplayName string `yaml:"display_name,omitempty" json:"display_name,omitempty"`
	Hidden      bool   `yaml:"hidden,omitempty" json:"hidden,omitempty"`
}

func (r *modelRule) UnmarshalYAML(value *yaml.Node) error {
	if value == nil {
		return nil
	}
	if value.Kind == yaml.ScalarNode {
		r.ID = strings.TrimSpace(value.Value)
		return nil
	}
	var raw struct {
		ID          string `yaml:"id"`
		DisplayName string `yaml:"display_name"`
		DisplayDash string `yaml:"display-name"`
		Name        string `yaml:"name"`
		Hidden      bool   `yaml:"hidden"`
		Hide        bool   `yaml:"hide"`
	}
	if err := value.Decode(&raw); err != nil {
		return err
	}
	r.ID = strings.TrimSpace(raw.ID)
	r.DisplayName = firstNonEmpty(raw.DisplayName, raw.DisplayDash, raw.Name)
	r.Hidden = raw.Hidden || raw.Hide
	return nil
}

func (r *modelRule) UnmarshalJSON(data []byte) error {
	trimmed := bytes.TrimSpace(data)
	if len(trimmed) == 0 || string(trimmed) == "null" {
		return nil
	}
	if trimmed[0] == '"' {
		var id string
		if err := json.Unmarshal(trimmed, &id); err != nil {
			return err
		}
		r.ID = strings.TrimSpace(id)
		return nil
	}
	var raw struct {
		ID          string `json:"id"`
		DisplayName string `json:"display_name"`
		DisplayDash string `json:"display-name"`
		Name        string `json:"name"`
		Hidden      bool   `json:"hidden"`
		Hide        bool   `json:"hide"`
	}
	if err := json.Unmarshal(trimmed, &raw); err != nil {
		return err
	}
	r.ID = strings.TrimSpace(raw.ID)
	r.DisplayName = firstNonEmpty(raw.DisplayName, raw.DisplayDash, raw.Name)
	r.Hidden = raw.Hidden || raw.Hide
	return nil
}

type pluginConfig struct {
	Enabled  bool        `yaml:"enabled" json:"enabled"`
	Priority int         `yaml:"priority" json:"priority"`
	Models   []modelRule `yaml:"models" json:"models"`
}

type observedModelMeta struct {
	ID          string `json:"id"`
	DisplayName string `json:"display_name"`
	OwnedBy     string `json:"owned_by,omitempty"`
}

var (
	activeConfig atomic.Value

	observedMu            sync.RWMutex
	observedModels        = make(map[string]observedModelMeta)
	lastClaudeCloakingVal atomic.Bool

	hostModelsResolver = fetchHostAuthModels
)

func init() {
	lastClaudeCloakingVal.Store(true)
	activeConfig.Store(normalizeConfig(defaultPluginConfig()))
}

func defaultPluginConfig() pluginConfig {
	return pluginConfig{
		Enabled:  true,
		Priority: 100,
		Models:   []modelRule{},
	}
}

func normalizeConfig(cfg pluginConfig) pluginConfig {
	cleanedModels := make([]modelRule, 0, len(cfg.Models))
	for _, rule := range cfg.Models {
		rule.ID = strings.TrimSpace(rule.ID)
		rule.DisplayName = strings.TrimSpace(rule.DisplayName)
		if rule.ID == "" {
			continue
		}
		cleanedModels = append(cleanedModels, rule)
	}
	cfg.Models = cleanedModels
	return cfg
}

func currentConfig() pluginConfig {
	if loaded, ok := activeConfig.Load().(pluginConfig); ok {
		return loaded
	}
	return defaultPluginConfig()
}

func setPluginConfig(cfg pluginConfig) {
	activeConfig.Store(normalizeConfig(cfg))
}

func configurePlugin(raw []byte) error {
	var req lifecycleRequest
	if len(raw) > 0 {
		if errUnmarshal := json.Unmarshal(raw, &req); errUnmarshal != nil {
			return errUnmarshal
		}
	}
	cfg := defaultPluginConfig()
	if len(req.ConfigYAML) > 0 {
		decoded, errDecode := decodePluginConfigYAML(req.ConfigYAML)
		if errDecode != nil {
			return errDecode
		}
		cfg = decoded
	}
	setPluginConfig(cfg)
	return nil
}

func decodePluginConfigYAML(rawYAML []byte) (pluginConfig, error) {
	cfg := defaultPluginConfig()
	if errUnmarshal := yaml.Unmarshal(rawYAML, &cfg); errUnmarshal != nil {
		return pluginConfig{}, errUnmarshal
	}
	return normalizeConfig(cfg), nil
}

func pluginRegistration() registration {
	return registration{
		SchemaVersion: schemaVersion,
		Metadata: pluginMetadata{
			Name:             pluginID,
			Version:          pluginVersion,
			Author:           pluginAuthor,
			GitHubRepository: pluginRepo,
			ConfigFields: []configField{
				{
					Name:        "models",
					Type:        "array",
					Default:     []any{},
					Description: "Ordered list of models ({id, display_name, hidden}).",
				},
			},
		},
		Capabilities: registrationCapabilities{
			ResponseInterceptor: true,
			ManagementAPI:       true,
		},
	}
}

type responseInterceptRequest struct {
	RequestID       string         `json:"RequestID"`
	SourceFormat    string         `json:"SourceFormat"`
	Model           string         `json:"Model"`
	RequestedModel  string         `json:"RequestedModel"`
	Stream          bool           `json:"Stream"`
	RequestHeaders  http.Header    `json:"RequestHeaders"`
	ResponseHeaders http.Header    `json:"ResponseHeaders"`
	OriginalRequest []byte         `json:"OriginalRequest"`
	RequestBody     []byte         `json:"RequestBody"`
	Body            []byte         `json:"Body"`
	StatusCode      int            `json:"StatusCode"`
	Metadata        map[string]any `json:"Metadata"`
	HostCallbackID  string         `json:"host_callback_id,omitempty"`
}

type responseInterceptResponse struct {
	Headers      http.Header `json:"Headers,omitempty"`
	Body         []byte      `json:"Body,omitempty"`
	ClearHeaders []string    `json:"ClearHeaders,omitempty"`
}

type hostAuthModelsRequest struct {
	AuthIndex      string `json:"auth_index,omitempty"`
	HostCallbackID string `json:"host_callback_id,omitempty"`
}

type hostAuthModelsResponse struct {
	Credentials []hostAuthModelsEntry `json:"credentials"`
}

type hostAuthModelsEntry struct {
	ID        string   `json:"id"`
	AuthIndex string   `json:"auth_index"`
	Provider  string   `json:"provider"`
	Models    []string `json:"models"`
}

func fetchHostAuthModels(hostCallbackID string) ([]hostAuthModelsEntry, error) {
	raw, errCall := callHost(methodHostAuthModels, hostAuthModelsRequest{
		HostCallbackID: hostCallbackID,
	})
	if errCall != nil {
		return nil, errCall
	}
	var resp hostAuthModelsResponse
	if errUnmarshal := json.Unmarshal(raw, &resp); errUnmarshal != nil {
		return nil, errUnmarshal
	}
	return resp.Credentials, nil
}

func handleResponseInterceptAfter(raw []byte) ([]byte, error) {
	var req responseInterceptRequest
	if errUnmarshal := json.Unmarshal(raw, &req); errUnmarshal != nil {
		return nil, errUnmarshal
	}
	if req.Model != "" || req.RequestedModel != "" || len(req.OriginalRequest) > 0 || len(req.Body) == 0 {
		return okEnvelope(responseInterceptResponse{})
	}
	cfg := currentConfig()
	if !cfg.Enabled {
		return okEnvelope(responseInterceptResponse{})
	}
	updated, changed := transformCatalogBody(req.SourceFormat, req.Body, cfg)
	if !changed {
		return okEnvelope(responseInterceptResponse{})
	}
	return okEnvelope(responseInterceptResponse{Body: updated})
}

func transformCatalogBody(sourceFormat string, body []byte, cfg pluginConfig) ([]byte, bool) {
	var root map[string]any
	dec := json.NewDecoder(bytes.NewReader(body))
	dec.UseNumber()
	if errDecode := dec.Decode(&root); errDecode != nil || root == nil {
		return nil, false
	}

	listKey := ""
	var rawItems []any
	if dataSlice, ok := root["data"].([]any); ok {
		listKey = "data"
		rawItems = dataSlice
	} else if modelsSlice, ok := root["models"].([]any); ok {
		listKey = "models"
		rawItems = modelsSlice
	} else {
		return nil, false
	}

	items := make([]map[string]any, 0, len(rawItems))
	for _, rawItem := range rawItems {
		itemMap, ok := rawItem.(map[string]any)
		if !ok || itemMap == nil {
			return nil, false
		}
		items = append(items, cloneMap(itemMap))
	}

	recordObservedModels(items)

	isClaudeFormat := strings.EqualFold(strings.TrimSpace(sourceFormat), "claude") || hasClaudePaginationFields(root)
	claudeCloakingActive := detectClaudeCloaking(items, isClaudeFormat)
	if isClaudeFormat {
		lastClaudeCloakingVal.Store(claudeCloakingActive)
	}

	if len(cfg.Models) == 0 {
		return nil, false
	}

	matchedRuleIdx := make([]int, len(items))
	for i := range matchedRuleIdx {
		matchedRuleIdx[i] = -1
	}

	ordered := make([]map[string]any, 0, len(items))
	for ruleIdx, rule := range cfg.Models {
		for itemIdx, item := range items {
			if matchedRuleIdx[itemIdx] != -1 {
				continue
			}
			if matchesRule(item, rule.ID) {
				matchedRuleIdx[itemIdx] = ruleIdx
				if !rule.Hidden {
					ordered = append(ordered, applyRuleToItem(item, rule, isClaudeFormat, claudeCloakingActive))
				}
				break
			}
		}
	}

	for itemIdx, item := range items {
		if matchedRuleIdx[itemIdx] != -1 {
			continue
		}
		if isClaudeFormat && claudeCloakingActive {
			item = cloneMap(item)
			if id, ok := item["id"].(string); ok && id != "" {
				item["id"] = ensureClaudeModelIDPrefix(id)
			}
		}
		ordered = append(ordered, item)
	}

	if listKey == "models" {
		reindexCodexPriorities(ordered)
	}

	root[listKey] = ordered
	if isClaudeFormat {
		firstID := ""
		lastID := ""
		if len(ordered) > 0 {
			firstID, _ = ordered[0]["id"].(string)
			lastID, _ = ordered[len(ordered)-1]["id"].(string)
		}
		if _, hasFirst := root["first_id"]; hasFirst || strings.EqualFold(sourceFormat, "claude") {
			root["first_id"] = firstID
		}
		if _, hasLast := root["last_id"]; hasLast || strings.EqualFold(sourceFormat, "claude") {
			root["last_id"] = lastID
		}
	}

	out, errMarshal := marshalJSONNoEscape(root)
	if errMarshal != nil {
		return nil, false
	}
	return out, true
}

func recordObservedModels(items []map[string]any) {
	if len(items) == 0 {
		return
	}
	observedMu.Lock()
	defer observedMu.Unlock()
	for _, item := range items {
		canonicalID, _, displayName := extractItemIdentifiers(item)
		if canonicalID == "" {
			continue
		}
		ownedBy, _ := item["owned_by"].(string)
		existing := observedModels[canonicalID]
		if displayName != "" && displayName != canonicalID {
			existing.DisplayName = displayName
		} else if existing.DisplayName == "" {
			existing.DisplayName = canonicalID
		}
		if ownedBy != "" {
			existing.OwnedBy = ownedBy
		}
		existing.ID = canonicalID
		observedModels[canonicalID] = existing
	}
}

func extractItemIdentifiers(item map[string]any) (canonicalID, rawID, displayName string) {
	if id, ok := item["id"].(string); ok && strings.TrimSpace(id) != "" {
		rawID = strings.TrimSpace(id)
		canonicalID = resolveClaudeModelIDPrefix(rawID)
	} else if slug, ok := item["slug"].(string); ok && strings.TrimSpace(slug) != "" {
		rawID = strings.TrimSpace(slug)
		canonicalID = rawID
	} else if name, ok := item["name"].(string); ok && strings.TrimSpace(name) != "" {
		rawID = strings.TrimSpace(name)
		canonicalID = strings.TrimPrefix(rawID, "models/")
	}
	if dn, ok := item["display_name"].(string); ok && strings.TrimSpace(dn) != "" {
		displayName = strings.TrimSpace(dn)
	} else if dn, ok := item["displayName"].(string); ok && strings.TrimSpace(dn) != "" {
		displayName = strings.TrimSpace(dn)
	}
	return canonicalID, rawID, displayName
}

func matchesRule(item map[string]any, target string) bool {
	target = strings.TrimSpace(target)
	if target == "" {
		return false
	}
	canonicalID, rawID, displayName := extractItemIdentifiers(item)
	targetUncloaked := resolveClaudeModelIDPrefix(strings.TrimPrefix(target, "models/"))
	for _, candidate := range []string{canonicalID, rawID, displayName} {
		if candidate == "" {
			continue
		}
		if candidate == target || candidate == targetUncloaked || strings.EqualFold(candidate, target) || strings.EqualFold(candidate, targetUncloaked) {
			return true
		}
	}
	return false
}

func applyRuleToItem(item map[string]any, rule modelRule, isClaudeFormat, claudeCloakingActive bool) map[string]any {
	out := cloneMap(item)
	if rule.DisplayName != "" {
		if _, hasCamel := out["displayName"]; hasCamel {
			out["displayName"] = rule.DisplayName
		} else {
			out["display_name"] = rule.DisplayName
		}
	}
	if isClaudeFormat && claudeCloakingActive {
		if id, ok := out["id"].(string); ok && id != "" {
			out["id"] = ensureClaudeModelIDPrefix(id)
		}
	}
	return out
}

func hasClaudePaginationFields(root map[string]any) bool {
	_, hasFirst := root["first_id"]
	_, hasLast := root["last_id"]
	return hasFirst || hasLast
}

func detectClaudeCloaking(items []map[string]any, isClaudeFormat bool) bool {
	if !isClaudeFormat {
		return false
	}
	for _, item := range items {
		id, _ := item["id"].(string)
		if strings.HasPrefix(id, claudeDDModelPrefix) {
			return true
		}
		if id != "" && !strings.HasPrefix(id, "claude-") {
			return false
		}
	}
	return true
}

func reindexCodexPriorities(items []map[string]any) {
	hasPriority := false
	for _, item := range items {
		if _, ok := item["priority"]; ok {
			hasPriority = true
			break
		}
	}
	if !hasPriority {
		return
	}
	for idx, item := range items {
		item["priority"] = idx
	}
}

func ensureClaudeModelIDPrefix(id string) string {
	if id == "" || strings.HasPrefix(id, claudeDDModelPrefix) {
		return id
	}
	return claudeDDModelPrefix + reverseString(id)
}

func resolveClaudeModelIDPrefix(id string) string {
	if id == "" {
		return id
	}
	base, suffix, hasSuffix := splitThinkingSuffix(id)
	if !strings.HasPrefix(base, claudeDDModelPrefix) {
		return id
	}
	encoded := base[len(claudeDDModelPrefix):]
	if encoded == "" {
		return id
	}
	resolved := reverseString(encoded)
	if hasSuffix {
		return resolved + "(" + suffix + ")"
	}
	return resolved
}

func splitThinkingSuffix(model string) (base, suffix string, hasSuffix bool) {
	lastOpen := strings.LastIndex(model, "(")
	if lastOpen == -1 || !strings.HasSuffix(model, ")") {
		return model, "", false
	}
	return model[:lastOpen], model[lastOpen+1 : len(model)-1], true
}

func reverseString(s string) string {
	runes := []rune(s)
	for i, j := 0, len(runes)-1; i < j; i, j = i+1, j-1 {
		runes[i], runes[j] = runes[j], runes[i]
	}
	return string(runes)
}

func cloneMap(src map[string]any) map[string]any {
	if src == nil {
		return nil
	}
	dst := make(map[string]any, len(src))
	for k, v := range src {
		dst[k] = v
	}
	return dst
}

func firstNonEmpty(values ...string) string {
	for _, v := range values {
		if trimmed := strings.TrimSpace(v); trimmed != "" {
			return trimmed
		}
	}
	return ""
}

func marshalJSONNoEscape(v any) ([]byte, error) {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(v); err != nil {
		return nil, err
	}
	return bytes.TrimRight(buf.Bytes(), "\n"), nil
}
