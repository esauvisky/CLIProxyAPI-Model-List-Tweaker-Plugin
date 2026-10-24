package main

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/url"
	"path"
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
	ModelRouter         bool `json:"model_router"`
	RequestInterceptor  bool `json:"request_interceptor"`
	ResponseInterceptor bool `json:"response_interceptor"`
	ManagementAPI       bool `json:"management_api"`
}

type modelRule struct {
	ID          string `yaml:"id" json:"id"`
	NewID       string `yaml:"new_id,omitempty" json:"new_id,omitempty"`
	DisplayName string `yaml:"display_name,omitempty" json:"display_name,omitempty"`
	Provider    string `yaml:"provider,omitempty" json:"provider,omitempty"`
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
		Match       string `yaml:"match"`
		NewID       string `yaml:"new_id"`
		NewIDDash   string `yaml:"new-id"`
		Alias       string `yaml:"alias"`
		DisplayName string `yaml:"display_name"`
		DisplayDash string `yaml:"display-name"`
		Name        string `yaml:"name"`
		Provider    string `yaml:"provider"`
		Hidden      bool   `yaml:"hidden"`
		Hide        bool   `yaml:"hide"`
	}
	if err := value.Decode(&raw); err != nil {
		return err
	}
	r.ID = firstNonEmpty(raw.ID, raw.Match)
	r.NewID = firstNonEmpty(raw.NewID, raw.NewIDDash, raw.Alias)
	r.DisplayName = firstNonEmpty(raw.DisplayName, raw.DisplayDash, raw.Name)
	r.Provider = strings.TrimSpace(raw.Provider)
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
		Match       string `json:"match"`
		NewID       string `json:"new_id"`
		NewIDDash   string `json:"new-id"`
		Alias       string `json:"alias"`
		DisplayName string `json:"display_name"`
		DisplayDash string `json:"display-name"`
		Name        string `json:"name"`
		Provider    string `json:"provider"`
		Hidden      bool   `json:"hidden"`
		Hide        bool   `json:"hide"`
	}
	if err := json.Unmarshal(trimmed, &raw); err != nil {
		return err
	}
	r.ID = firstNonEmpty(raw.ID, raw.Match)
	r.NewID = firstNonEmpty(raw.NewID, raw.NewIDDash, raw.Alias)
	r.DisplayName = firstNonEmpty(raw.DisplayName, raw.DisplayDash, raw.Name)
	r.Provider = strings.TrimSpace(raw.Provider)
	r.Hidden = raw.Hidden || raw.Hide
	return nil
}

type pluginConfig struct {
	Enabled      bool        `yaml:"enabled" json:"enabled"`
	Priority     int         `yaml:"priority" json:"priority"`
	HideUnlisted bool        `yaml:"hide_unlisted" json:"hide_unlisted"`
	Hide         []string    `yaml:"hide" json:"hide"`
	Models       []modelRule `yaml:"models" json:"models"`
}

type compiledConfig struct {
	raw           pluginConfig
	aliasToTarget map[string]modelRule
}

type observedModelMeta struct {
	ID          string `json:"id"`
	DisplayName string `json:"display_name"`
	OwnedBy     string `json:"owned_by,omitempty"`
}

var (
	activeConfig atomic.Value

	observedMu     sync.RWMutex
	observedModels = make(map[string]observedModelMeta)

	hostModelsResolver = fetchHostAuthModels
)

func init() {
	activeConfig.Store(compileConfig(defaultPluginConfig()))
}

func defaultPluginConfig() pluginConfig {
	return pluginConfig{
		Enabled:      true,
		Priority:     100,
		HideUnlisted: false,
		Hide:         []string{},
		Models:       []modelRule{},
	}
}

func compileConfig(cfg pluginConfig) compiledConfig {
	cleanedModels := make([]modelRule, 0, len(cfg.Models))
	aliasToTarget := make(map[string]modelRule, len(cfg.Models))
	for _, rule := range cfg.Models {
		rule.ID = strings.TrimSpace(rule.ID)
		rule.NewID = strings.TrimSpace(rule.NewID)
		rule.DisplayName = strings.TrimSpace(rule.DisplayName)
		rule.Provider = strings.ToLower(strings.TrimSpace(rule.Provider))
		if rule.ID == "" {
			continue
		}
		cleanedModels = append(cleanedModels, rule)
		if rule.NewID != "" && !strings.EqualFold(rule.NewID, rule.ID) {
			aliasToTarget[rule.NewID] = rule
			aliasToTarget[strings.ToLower(rule.NewID)] = rule
		}
	}
	cleanedHide := make([]string, 0, len(cfg.Hide))
	for _, pattern := range cfg.Hide {
		if trimmed := strings.TrimSpace(pattern); trimmed != "" {
			cleanedHide = append(cleanedHide, trimmed)
		}
	}
	cfg.Models = cleanedModels
	cfg.Hide = cleanedHide
	return compiledConfig{
		raw:           cfg,
		aliasToTarget: aliasToTarget,
	}
}

func currentCompiledConfig() compiledConfig {
	if loaded, ok := activeConfig.Load().(compiledConfig); ok {
		return loaded
	}
	return compileConfig(defaultPluginConfig())
}

func setPluginConfig(cfg pluginConfig) {
	activeConfig.Store(compileConfig(cfg))
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
	var node yaml.Node
	if errUnmarshal := yaml.Unmarshal(rawYAML, &node); errUnmarshal != nil {
		return pluginConfig{}, errUnmarshal
	}
	if errDecode := node.Decode(&cfg); errDecode != nil {
		return pluginConfig{}, errDecode
	}
	var dash checkDashFields
	if errDash := node.Decode(&dash); errDash == nil && dash.HideUnlisted != nil {
		cfg.HideUnlisted = *dash.HideUnlisted
	}
	return cfg, nil
}

type checkDashFields struct {
	HideUnlisted *bool `yaml:"hide-unlisted"`
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
					Description: "Ordered list of model rules ({id, new_id, display_name, provider, hidden}) or model IDs.",
				},
				{
					Name:        "hide",
					Type:        "array",
					Default:     []any{},
					Description: "Model IDs, display names, or glob patterns (e.g. gpt-4o-mini*, *-preview) to hide from the global model list.",
				},
				{
					Name:        "hide_unlisted",
					Type:        "boolean",
					Default:     false,
					Description: "When true, hide any model not explicitly listed in models.",
				},
			},
		},
		Capabilities: registrationCapabilities{
			ModelRouter:         true,
			RequestInterceptor:  true,
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

type requestInterceptRequest struct {
	RequestID      string         `json:"RequestID"`
	TraceID        string         `json:"TraceID"`
	SourceFormat   string         `json:"SourceFormat"`
	ToFormat       string         `json:"ToFormat"`
	Model          string         `json:"Model"`
	RequestedModel string         `json:"RequestedModel"`
	Stream         bool           `json:"Stream"`
	Headers        http.Header    `json:"Headers"`
	Body           []byte         `json:"Body"`
	Metadata       map[string]any `json:"Metadata"`
	HostCallbackID string         `json:"host_callback_id,omitempty"`
}

type requestInterceptResponse struct {
	Path            string      `json:"path,omitempty"`
	Headers         http.Header `json:"Headers,omitempty"`
	Body            []byte      `json:"Body,omitempty"`
	ClearHeaders    []string    `json:"ClearHeaders,omitempty"`
	Terminate       bool        `json:"Terminate,omitempty"`
	StatusCode      int         `json:"StatusCode,omitempty"`
	ResponseHeaders http.Header `json:"ResponseHeaders,omitempty"`
	ResponseBody    []byte      `json:"ResponseBody,omitempty"`
}

type modelRouteRequest struct {
	PluginID           string         `json:"PluginID"`
	SourceFormat       string         `json:"SourceFormat"`
	RequestedModel     string         `json:"RequestedModel"`
	Stream             bool           `json:"Stream"`
	Headers            http.Header    `json:"Headers"`
	Query              url.Values     `json:"Query"`
	Body               []byte         `json:"Body"`
	Metadata           map[string]any `json:"Metadata"`
	AvailableProviders []string       `json:"AvailableProviders"`
	HostCallbackID     string         `json:"host_callback_id,omitempty"`
}

type modelRouteResponse struct {
	Handled     bool   `json:"Handled"`
	TargetKind  string `json:"TargetKind,omitempty"`
	Target      string `json:"Target,omitempty"`
	TargetModel string `json:"TargetModel,omitempty"`
	Reason      string `json:"Reason,omitempty"`
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

func handleResponseInterceptAfter(raw []byte) ([]byte, error) {
	var req responseInterceptRequest
	if errUnmarshal := json.Unmarshal(raw, &req); errUnmarshal != nil {
		return nil, errUnmarshal
	}
	if req.Model != "" || req.RequestedModel != "" || len(req.OriginalRequest) > 0 || len(req.Body) == 0 {
		return okEnvelope(responseInterceptResponse{})
	}
	compiled := currentCompiledConfig()
	if !compiled.raw.Enabled {
		return okEnvelope(responseInterceptResponse{})
	}
	updated, changed := transformCatalogBody(req.SourceFormat, req.Body, compiled.raw)
	if !changed {
		return okEnvelope(responseInterceptResponse{})
	}
	return okEnvelope(responseInterceptResponse{Body: updated})
}

func handleModelRoute(raw []byte) ([]byte, error) {
	var req modelRouteRequest
	if errUnmarshal := json.Unmarshal(raw, &req); errUnmarshal != nil {
		return nil, errUnmarshal
	}
	compiled := currentCompiledConfig()
	if !compiled.raw.Enabled || len(compiled.aliasToTarget) == 0 {
		return okEnvelope(modelRouteResponse{Handled: false})
	}
	rule, targetModel, matched := resolveAliasModel(req.RequestedModel, compiled)
	if !matched {
		return okEnvelope(modelRouteResponse{Handled: false})
	}
	baseOriginal, _, _ := splitThinkingSuffix(targetModel)
	provider := resolveProviderForModel(baseOriginal, rule.Provider, req.AvailableProviders, req.HostCallbackID)
	if provider == "" {
		return okEnvelope(modelRouteResponse{Handled: false})
	}
	return okEnvelope(modelRouteResponse{
		Handled:     true,
		TargetKind:  "provider",
		Target:      provider,
		TargetModel: targetModel,
		Reason:      "model_list_tweaker_alias",
	})
}

func handleRequestInterceptBefore(raw []byte) ([]byte, error) {
	var req requestInterceptRequest
	if errUnmarshal := json.Unmarshal(raw, &req); errUnmarshal != nil {
		return nil, errUnmarshal
	}
	compiled := currentCompiledConfig()
	if !compiled.raw.Enabled || len(compiled.aliasToTarget) == 0 || len(req.Body) == 0 {
		return okEnvelope(requestInterceptResponse{})
	}
	updatedBody, changed := rewriteRequestBodyModel(req.Body, req.RequestedModel, compiled)
	if !changed {
		return okEnvelope(requestInterceptResponse{})
	}
	return okEnvelope(requestInterceptResponse{Body: updatedBody})
}

func resolveAliasModel(requested string, compiled compiledConfig) (modelRule, string, bool) {
	trimmed := strings.TrimSpace(requested)
	if trimmed == "" {
		return modelRule{}, "", false
	}
	trimmed = strings.TrimPrefix(trimmed, "models/")
	base, suffix, hasSuffix := splitThinkingSuffix(trimmed)
	uncloaked := resolveClaudeModelIDPrefix(base)

	for _, candidate := range []string{uncloaked, base, strings.ToLower(uncloaked), strings.ToLower(base)} {
		if rule, ok := compiled.aliasToTarget[candidate]; ok {
			target := rule.ID
			if hasSuffix {
				target = target + "(" + suffix + ")"
			}
			return rule, target, true
		}
	}
	return modelRule{}, "", false
}

func rewriteRequestBodyModel(body []byte, requestedModel string, compiled compiledConfig) ([]byte, bool) {
	var payload map[string]any
	dec := json.NewDecoder(bytes.NewReader(body))
	dec.UseNumber()
	if errDecode := dec.Decode(&payload); errDecode != nil || payload == nil {
		return nil, false
	}
	rawModel, _ := payload["model"].(string)
	lookup := rawModel
	if strings.TrimSpace(lookup) == "" {
		lookup = requestedModel
	}
	_, targetModel, matched := resolveAliasModel(lookup, compiled)
	if !matched {
		return nil, false
	}
	if rawModel == targetModel {
		return nil, false
	}
	payload["model"] = targetModel
	updated, errMarshal := marshalJSONNoEscape(payload)
	if errMarshal != nil {
		return nil, false
	}
	return updated, true
}

func resolveProviderForModel(modelID, configuredProvider string, availableProviders []string, hostCallbackID string) string {
	if configuredProvider != "" && isProviderAvailable(configuredProvider, availableProviders) {
		return configuredProvider
	}
	if credentials, errFetch := hostModelsResolver(hostCallbackID); errFetch == nil {
		for _, cred := range credentials {
			prov := strings.ToLower(strings.TrimSpace(cred.Provider))
			if prov == "" || !isProviderAvailable(prov, availableProviders) {
				continue
			}
			for _, m := range cred.Models {
				if strings.EqualFold(strings.TrimSpace(m), modelID) {
					return prov
				}
			}
		}
	}
	observedMu.RLock()
	meta, hasMeta := observedModels[modelID]
	observedMu.RUnlock()
	if hasMeta && meta.OwnedBy != "" {
		if prov := matchOwnedByToProvider(meta.OwnedBy, availableProviders); prov != "" {
			return prov
		}
	}
	if prov := inferProviderFromModelID(modelID, availableProviders); prov != "" {
		return prov
	}
	if configuredProvider != "" {
		return configuredProvider
	}
	if len(availableProviders) == 1 {
		return strings.ToLower(strings.TrimSpace(availableProviders[0]))
	}
	return ""
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

func isProviderAvailable(provider string, available []string) bool {
	if len(available) == 0 {
		return true
	}
	for _, p := range available {
		if strings.EqualFold(strings.TrimSpace(p), provider) {
			return true
		}
	}
	return false
}

func matchOwnedByToProvider(ownedBy string, available []string) string {
	lower := strings.ToLower(strings.TrimSpace(ownedBy))
	candidates := []string{lower}
	switch lower {
	case "anthropic":
		candidates = []string{"claude", "antigravity", "anthropic"}
	case "openai":
		candidates = []string{"codex", "openai"}
	case "google":
		candidates = []string{"gemini", "gemini-cli", "antigravity", "vertex", "aistudio"}
	case "xai":
		candidates = []string{"xai", "grok"}
	}
	for _, c := range candidates {
		if isProviderAvailable(c, available) && len(available) > 0 {
			return c
		}
	}
	return ""
}

func inferProviderFromModelID(modelID string, available []string) string {
	lower := strings.ToLower(strings.TrimSpace(modelID))
	var candidates []string
	switch {
	case strings.HasPrefix(lower, "claude-"):
		candidates = []string{"claude", "antigravity"}
	case strings.HasPrefix(lower, "gpt-"), strings.HasPrefix(lower, "o1"), strings.HasPrefix(lower, "o3"), strings.HasPrefix(lower, "o4"), strings.HasPrefix(lower, "codex"):
		candidates = []string{"codex", "openai"}
	case strings.HasPrefix(lower, "gemini-"):
		candidates = []string{"gemini", "gemini-cli", "antigravity", "vertex"}
	case strings.HasPrefix(lower, "grok-"):
		candidates = []string{"xai", "grok"}
	}
	for _, c := range candidates {
		if len(available) == 0 || isProviderAvailable(c, available) {
			return c
		}
	}
	return ""
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

	if len(cfg.Models) == 0 && len(cfg.Hide) == 0 && !cfg.HideUnlisted {
		return nil, false
	}

	isClaudeFormat := strings.EqualFold(strings.TrimSpace(sourceFormat), "claude") || hasClaudePaginationFields(root)
	claudeCloakingActive := detectClaudeCloaking(items, isClaudeFormat)

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
				if !rule.Hidden && !matchesExactHide(item, cfg.Hide) {
					ordered = append(ordered, applyRuleToItem(item, rule, isClaudeFormat, claudeCloakingActive))
				}
				break
			}
		}
	}

	if !cfg.HideUnlisted {
		for itemIdx, item := range items {
			if matchedRuleIdx[itemIdx] != -1 {
				continue
			}
			if matchesAnyHidePattern(item, cfg.Hide) {
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
		if hasGlobMeta(target) && matchGlob(target, candidate) {
			return true
		}
	}
	return false
}

func matchesExactHide(item map[string]any, hidePatterns []string) bool {
	canonicalID, rawID, displayName := extractItemIdentifiers(item)
	for _, pattern := range hidePatterns {
		pattern = strings.TrimSpace(pattern)
		if pattern == "" || hasGlobMeta(pattern) {
			continue
		}
		uncloaked := resolveClaudeModelIDPrefix(strings.TrimPrefix(pattern, "models/"))
		for _, candidate := range []string{canonicalID, rawID, displayName} {
			if candidate != "" && (strings.EqualFold(candidate, pattern) || strings.EqualFold(candidate, uncloaked)) {
				return true
			}
		}
	}
	return false
}

func matchesAnyHidePattern(item map[string]any, hidePatterns []string) bool {
	canonicalID, rawID, displayName := extractItemIdentifiers(item)
	for _, pattern := range hidePatterns {
		pattern = strings.TrimSpace(pattern)
		if pattern == "" {
			continue
		}
		uncloaked := resolveClaudeModelIDPrefix(strings.TrimPrefix(pattern, "models/"))
		for _, candidate := range []string{canonicalID, rawID, displayName} {
			if candidate == "" {
				continue
			}
			if strings.EqualFold(candidate, pattern) || strings.EqualFold(candidate, uncloaked) || matchGlob(pattern, candidate) || matchGlob(uncloaked, candidate) {
				return true
			}
		}
	}
	return false
}

func applyRuleToItem(item map[string]any, rule modelRule, isClaudeFormat, claudeCloakingActive bool) map[string]any {
	out := cloneMap(item)
	canonicalID, rawID, _ := extractItemIdentifiers(out)

	if rule.DisplayName != "" {
		if _, hasCamel := out["displayName"]; hasCamel {
			out["displayName"] = rule.DisplayName
		} else {
			out["display_name"] = rule.DisplayName
		}
	} else if rule.NewID != "" {
		if dn, ok := out["display_name"].(string); ok && (dn == "" || dn == canonicalID || dn == rawID) {
			out["display_name"] = rule.NewID
		} else if dn, ok := out["displayName"].(string); ok && (dn == "" || dn == canonicalID || dn == rawID) {
			out["displayName"] = rule.NewID
		}
	}

	if rule.NewID != "" {
		newID := rule.NewID
		if _, hasSlug := out["slug"]; hasSlug {
			out["slug"] = newID
			if _, hasID := out["id"]; hasID {
				out["id"] = newID
			}
		} else if nameVal, hasName := out["name"].(string); hasName && isGeminiModelObject(out) {
			trimmedNew := strings.TrimPrefix(newID, "models/")
			if strings.HasPrefix(nameVal, "models/") {
				out["name"] = "models/" + trimmedNew
			} else {
				out["name"] = newID
			}
		} else {
			if isClaudeFormat && (claudeCloakingActive || strings.HasPrefix(rawID, claudeDDModelPrefix)) {
				out["id"] = ensureClaudeModelIDPrefix(newID)
			} else {
				out["id"] = newID
			}
		}
	} else if isClaudeFormat && (claudeCloakingActive || strings.HasPrefix(rawID, claudeDDModelPrefix)) {
		if id, ok := out["id"].(string); ok && id != "" {
			out["id"] = ensureClaudeModelIDPrefix(id)
		}
	}
	return out
}

func isGeminiModelObject(item map[string]any) bool {
	if _, hasID := item["id"]; hasID {
		return false
	}
	if _, hasSlug := item["slug"]; hasSlug {
		return false
	}
	_, hasName := item["name"].(string)
	return hasName
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
	hasNonClaudeUncloaked := false
	for _, item := range items {
		id, _ := item["id"].(string)
		if strings.HasPrefix(id, claudeDDModelPrefix) {
			return true
		}
		if id != "" && !strings.HasPrefix(id, "claude-") {
			hasNonClaudeUncloaked = true
		}
	}
	return !hasNonClaudeUncloaked
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
	if id == "" {
		return id
	}
	if strings.HasPrefix(id, claudeDDModelPrefix) {
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

func hasGlobMeta(pattern string) bool {
	return strings.ContainsAny(pattern, "*?[]")
}

func matchGlob(pattern, candidate string) bool {
	matched, err := path.Match(strings.ToLower(pattern), strings.ToLower(candidate))
	if err == nil && matched {
		return true
	}
	if strings.Contains(pattern, "*") {
		return wildcardMatch(strings.ToLower(pattern), strings.ToLower(candidate))
	}
	return false
}

func wildcardMatch(pattern, str string) bool {
	parts := strings.Split(pattern, "*")
	if len(parts) == 1 {
		return pattern == str
	}
	if !strings.HasPrefix(str, parts[0]) {
		return false
	}
	str = str[len(parts[0]):]
	for i := 1; i < len(parts)-1; i++ {
		if parts[i] == "" {
			continue
		}
		idx := strings.Index(str, parts[i])
		if idx < 0 {
			return false
		}
		str = str[idx+len(parts[i]):]
	}
	last := parts[len(parts)-1]
	return strings.HasSuffix(str, last)
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
