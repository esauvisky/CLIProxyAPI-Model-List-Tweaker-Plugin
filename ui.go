package main

import (
	"encoding/json"
	"fmt"
	"math"
	"net/http"
	"net/url"
	"sort"
	"strings"
)

const (
	uiResourcePath     = "/ui"
	mgmtStateRoutePath = "/plugins/model-list-tweaker/state"
)

type managementRegistration struct {
	Routes    []managementRoute    `json:"routes,omitempty"`
	Resources []managementResource `json:"resources,omitempty"`
}

type managementRoute struct {
	Method      string `json:"Method"`
	Path        string `json:"Path"`
	Description string `json:"Description,omitempty"`
}

type managementResource struct {
	Path        string `json:"Path"`
	Menu        string `json:"Menu"`
	Description string `json:"Description"`
}

type managementRequest struct {
	Method         string      `json:"Method"`
	Path           string      `json:"Path"`
	Headers        http.Header `json:"Headers"`
	Query          url.Values  `json:"Query"`
	Body           []byte      `json:"Body"`
	HostCallbackID string      `json:"host_callback_id,omitempty"`
}

type managementResponse struct {
	StatusCode int         `json:"StatusCode"`
	Headers    http.Header `json:"Headers"`
	Body       []byte      `json:"Body"`
}

type discoveredModel struct {
	ID          string   `json:"id"`
	DisplayName string   `json:"display_name"`
	Providers   []string `json:"providers"`
}

type uiStatePayload struct {
	Config         pluginConfig      `json:"config"`
	Available      []discoveredModel `json:"available"`
	ClaudeCloaking bool              `json:"claude_cloaking"`
	QuotaByModel   map[string]int    `json:"quota_by_model,omitempty"`
}

func managementRegistrationPayload() managementRegistration {
	return managementRegistration{
		Routes: []managementRoute{
			{
				Method:      http.MethodGet,
				Path:        mgmtStateRoutePath,
				Description: "Returns discovered models and active Model List Tweaker config.",
			},
			{
				Method:      http.MethodPost,
				Path:        mgmtStateRoutePath,
				Description: "Updates active in-memory Model List Tweaker config.",
			},
		},
		Resources: []managementResource{
			{
				Path:        uiResourcePath,
				Menu:        "Model List Tweaker",
				Description: "Reorder, rename, and hide models in the global model list.",
			},
		},
	}
}

func handleManagement(raw []byte) ([]byte, error) {
	var req managementRequest
	if len(raw) > 0 {
		if errUnmarshal := json.Unmarshal(raw, &req); errUnmarshal != nil {
			return nil, errUnmarshal
		}
	}

	cleanedPath := strings.TrimRight(strings.TrimSpace(req.Path), "/")
	if strings.HasSuffix(cleanedPath, mgmtStateRoutePath) {
		if strings.EqualFold(req.Method, http.MethodPost) {
			if len(req.Body) > 0 {
				var nextCfg pluginConfig
				if errUnmarshal := json.Unmarshal(req.Body, &nextCfg); errUnmarshal != nil {
					return okEnvelope(jsonManagementResponse(http.StatusBadRequest, map[string]any{
						"error":   "invalid_config",
						"message": errUnmarshal.Error(),
					}))
				}
				if !nextCfg.Enabled {
					nextCfg.Enabled = true
				}
				if nextCfg.Priority == 0 {
					nextCfg.Priority = currentConfig().Priority
				}
				nextCfg = normalizeConfig(nextCfg)
				if errQuota := validateQuotaConfig(nextCfg.Quota); errQuota != nil {
					return okEnvelope(jsonManagementResponse(http.StatusBadRequest, map[string]any{
						"error":   "invalid_config",
						"message": errQuota.Error(),
					}))
				}
				setPluginConfig(nextCfg)
				if nextCfg.Quota.Enabled {
					maybeRefreshQuota(nextCfg.Quota)
				}
			}
			return okEnvelope(jsonManagementResponse(http.StatusOK, buildUIState(req.HostCallbackID)))
		}
		return okEnvelope(jsonManagementResponse(http.StatusOK, buildUIState(req.HostCallbackID)))
	}

	// /v0/resource/plugins/model-list-tweaker/ui serves only the static UI asset.
	return okEnvelope(managementResponse{
		StatusCode: http.StatusOK,
		Headers: http.Header{
			"Content-Type":  []string{"text/html; charset=utf-8"},
			"Cache-Control": []string{"no-store"},
		},
		Body: []byte(editorHTMLTemplate),
	})
}

func buildUIState(hostCallbackID string) uiStatePayload {
	cfg := currentConfig()
	if cfg.Quota.Enabled {
		maybeRefreshQuota(cfg.Quota)
	}
	byID := make(map[string]*discoveredModel)

	if credentials, errFetch := hostModelsResolver(hostCallbackID); errFetch == nil {
		for _, cred := range credentials {
			prov := strings.ToLower(strings.TrimSpace(cred.Provider))
			for _, m := range cred.Models {
				id := strings.TrimSpace(m)
				if id == "" {
					continue
				}
				entry := byID[id]
				if entry == nil {
					entry = &discoveredModel{
						ID:          id,
						DisplayName: id,
					}
					byID[id] = entry
				}
				if prov != "" && !containsString(entry.Providers, prov) {
					entry.Providers = append(entry.Providers, prov)
				}
			}
		}
	}

	observedMu.RLock()
	for id, meta := range observedModels {
		entry := byID[id]
		if entry == nil {
			entry = &discoveredModel{
				ID:          id,
				DisplayName: meta.DisplayName,
			}
			byID[id] = entry
		} else if meta.DisplayName != "" && (entry.DisplayName == "" || entry.DisplayName == entry.ID) {
			entry.DisplayName = meta.DisplayName
		}
		if meta.OwnedBy != "" && len(entry.Providers) == 0 {
			entry.Providers = []string{meta.OwnedBy}
		}
	}
	observedMu.RUnlock()

	available := make([]discoveredModel, 0, len(byID))
	for _, item := range byID {
		sort.Strings(item.Providers)
		if item.DisplayName == "" {
			item.DisplayName = item.ID
		}
		available = append(available, *item)
	}
	sort.Slice(available, func(i, j int) bool {
		if !strings.EqualFold(available[i].DisplayName, available[j].DisplayName) {
			return strings.ToLower(available[i].DisplayName) < strings.ToLower(available[j].DisplayName)
		}
		return available[i].ID < available[j].ID
	})

	var quotaPct map[string]int
	quotaMu.RLock()
	if len(quotaByModel) > 0 {
		quotaPct = make(map[string]int, len(quotaByModel))
		for k, v := range quotaByModel {
			quotaPct[k] = int(math.Round(v * 100))
		}
	}
	quotaMu.RUnlock()

	return uiStatePayload{
		Config:         cfg,
		Available:      available,
		ClaudeCloaking: lastClaudeCloakingVal.Load(),
		QuotaByModel:   quotaPct,
	}
}

func containsString(list []string, target string) bool {
	for _, s := range list {
		if strings.EqualFold(s, target) {
			return true
		}
	}
	return false
}

func jsonManagementResponse(status int, payload any) managementResponse {
	raw, _ := marshalJSONNoEscape(payload)
	return managementResponse{
		StatusCode: status,
		Headers: http.Header{
			"Content-Type":  []string{"application/json; charset=utf-8"},
			"Cache-Control": []string{"no-store"},
		},
		Body: raw,
	}
}

var _ = fmt.Sprint

const editorHTMLTemplate = `<!doctype html>
<html lang="en" data-theme="dark">
<head>
<meta charset="utf-8">
<meta name="viewport" content="width=device-width, initial-scale=1">
<title>Model List Tweaker</title>
<style>
  /* CPA Dark Theme (Default) */
  :root, [data-theme='dark'] {
    --bg-secondary: #151412;
    --bg-primary: #1d1b18;
    --bg-tertiary: #262320;
    --bg-hover: #2e2a26;
    --bg-quinary: #191714;
    --text-primary: #f6f4f1;
    --text-secondary: #c9c3bb;
    --text-tertiary: #9c958d;
    --text-quaternary: #6f6962;
    --border-color: #3a3530;
    --border-primary: #4a453f;
    --border-hover: #5a544d;
    --primary-color: #8b8680;
    --primary-hover: #9a948e;
    --primary-active: #a6a099;
    --primary-contrast: #ffffff;
    --success-color: #10b981;
    --quota-medium-color: #ffd862;
    --warning-color: #c65746;
    --error-color: #c65746;
    --viz-success: #0fae78;
    --viz-failure: #cf4b3a;
    --success-badge-bg: rgba(6, 78, 59, 0.3);
    --success-badge-text: #6ee7b7;
    --success-badge-border: #059669;
    --failure-badge-bg: rgba(198, 87, 70, 0.24);
    --failure-badge-text: #f1b0a6;
    --failure-badge-border: rgba(198, 87, 70, 0.5);
    --count-badge-bg: rgba(139, 134, 128, 0.28);
    --count-badge-text: #a6a099;
    --shadow-sm: 0 1px 3px 0 rgb(0 0 0 / 0.3);
    --radius-sm: 4px;
    --radius-md: 8px;
    --radius-lg: 12px;
    --radius-full: 9999px;
    --ease-out-strong: cubic-bezier(0.23, 1, 0.32, 1);
  }

  /* CPA Warm-Stone Light Theme */
  [data-theme='light'] {
    --bg-secondary: #faf9f5;
    --bg-primary: #f0eee8;
    --bg-tertiary: #e9e6df;
    --bg-hover: #e9e6df;
    --bg-quinary: #f6f4ee;
    --text-primary: #2d2a26;
    --text-secondary: #6d6760;
    --text-tertiary: #a29c95;
    --text-quaternary: #c0bab3;
    --border-color: #e3e1db;
    --border-primary: #d5d2cb;
    --border-hover: #cecac4;
    --primary-color: #8b8680;
    --primary-hover: #7f7a74;
    --primary-active: #726d67;
    --primary-contrast: #ffffff;
    --quota-medium-color: #e0aa14;
    --viz-success: #10b981;
    --viz-failure: #c65746;
    --success-badge-bg: #d1fae5;
    --success-badge-text: #065f46;
    --success-badge-border: #6ee7b7;
    --failure-badge-bg: rgba(198, 87, 70, 0.14);
    --failure-badge-text: #8a3a30;
    --failure-badge-border: rgba(198, 87, 70, 0.35);
    --count-badge-bg: rgba(139, 134, 128, 0.18);
    --count-badge-text: #726d67;
    --shadow-sm: 0 1px 2px 0 rgb(0 0 0 / 0.05);
  }

  /* CPA Pure White Theme */
  [data-theme='white'] {
    --bg-secondary: #ffffff;
    --bg-primary: #ffffff;
    --bg-tertiary: #f6f6f6;
    --bg-hover: #f6f6f6;
    --bg-quinary: #ffffff;
    --text-primary: #2d2a26;
    --text-secondary: #6d6760;
    --text-tertiary: #a29c95;
    --text-quaternary: #c0bab3;
    --border-color: #e5e5e5;
    --border-primary: #d9d9d9;
    --border-hover: #cccccc;
    --primary-color: #8b8680;
    --primary-hover: #7f7a74;
    --primary-active: #726d67;
    --primary-contrast: #ffffff;
    --quota-medium-color: #e0aa14;
    --viz-success: #10b981;
    --viz-failure: #c65746;
    --success-badge-bg: #d1fae5;
    --success-badge-text: #065f46;
    --success-badge-border: #6ee7b7;
    --failure-badge-bg: rgba(198, 87, 70, 0.14);
    --failure-badge-text: #8a3a30;
    --failure-badge-border: rgba(198, 87, 70, 0.35);
    --count-badge-bg: rgba(139, 134, 128, 0.18);
    --count-badge-text: #726d67;
    --shadow-sm: 0 1px 2px 0 rgb(0 0 0 / 0.05);
  }

  * { box-sizing: border-box; }
  html, body {
    margin: 0;
    padding: 0;
    background-color: var(--bg-secondary);
    color: var(--text-primary);
    font-family: -apple-system, BlinkMacSystemFont, 'Segoe UI', 'Roboto', 'Oxygen', 'Ubuntu', 'Cantarell', sans-serif;
    font-size: 14px;
    line-height: 1.5;
    -webkit-font-smoothing: antialiased;
  }

  ::-webkit-scrollbar { width: 8px; height: 8px; }
  ::-webkit-scrollbar-track { background: var(--bg-secondary); }
  ::-webkit-scrollbar-thumb { background: var(--border-color); border-radius: 9999px; }
  ::-webkit-scrollbar-thumb:hover { background: var(--border-hover); }

  /* Matches CPA .main-content + PluginsPage.module.scss */
  .page {
    padding: 70px clamp(20px, 3vw, 48px) 40px;
    display: flex;
    flex-direction: column;
    gap: 20px;
    width: 100%;
  }

  .page-header {
    display: flex;
    flex-direction: column;
    gap: 8px;
    padding-right: 180px; /* Keep clear of CPA's floating top-right header-actions pill */
  }
  .page-title {
    margin: 0;
    color: var(--text-primary);
    font-size: 28px;
    font-weight: 700;
    line-height: 1.2;
  }
  .page-desc {
    margin: 0;
    color: var(--text-secondary);
    font-size: 14px;
    line-height: 1.5;
  }

  /* Matches CPA PluginsPage .statusBar */
  .status-bar {
    display: flex;
    align-items: center;
    gap: 10px;
    padding: 10px 14px;
    border-radius: 10px;
    border: 1px solid color-mix(in srgb, var(--border-color) 60%, transparent);
    background: color-mix(in srgb, var(--bg-secondary) 60%, transparent);
    flex-wrap: wrap;
  }
  .status-pill {
    display: inline-flex;
    align-items: center;
    gap: 6px;
    padding: 5px 12px;
    border-radius: var(--radius-full);
    border: 1px solid color-mix(in srgb, var(--border-color) 50%, transparent);
    background: color-mix(in srgb, var(--bg-primary) 56%, transparent);
    font-size: 12px;
    font-weight: 600;
    white-space: nowrap;
  }
  .status-dot {
    width: 7px;
    height: 7px;
    border-radius: 50%;
    background: var(--success-color);
    box-shadow: 0 0 6px rgba(16, 185, 129, 0.5);
    flex-shrink: 0;
  }
  .status-divider {
    width: 1px;
    height: 20px;
    background: color-mix(in srgb, var(--border-color) 60%, transparent);
    flex-shrink: 0;
  }
  .status-label { color: var(--text-secondary); }
  .status-value { color: var(--text-primary); font-weight: 700; }

  /* Matches CPA PluginsPage .toolbar: left-aligned search + buttons, never in top-right */
  .toolbar {
    display: flex;
    align-items: center;
    gap: 8px;
    flex-wrap: wrap;
  }
  .toolbar .search-input {
    flex: 1 1 260px;
    max-width: 420px;
  }

  .btn {
    display: inline-flex;
    align-items: center;
    justify-content: center;
    gap: 6px;
    border: 1px solid var(--border-color);
    border-radius: var(--radius-md);
    padding: 8px 12px;
    font-size: 13px;
    font-weight: 600;
    cursor: pointer;
    transition: all 150ms ease;
    background-color: var(--bg-tertiary);
    color: var(--text-primary);
    white-space: nowrap;
  }
  .btn:hover {
    background-color: var(--bg-hover);
    border-color: var(--border-hover);
  }
  .btn-primary {
    background-color: var(--primary-color);
    color: var(--primary-contrast);
    border-color: var(--primary-color);
  }
  .btn-primary:hover {
    background-color: var(--primary-hover);
    border-color: var(--primary-hover);
  }
  .btn-sm {
    padding: 5px 10px;
    font-size: 12px;
  }

  .input, textarea {
    width: 100%;
    border: 1px solid var(--border-color);
    border-radius: var(--radius-md);
    padding: 7px 11px;
    font-size: 13px;
    background-color: var(--bg-secondary);
    color: var(--text-primary);
    transition: border-color 150ms ease, box-shadow 150ms ease;
  }
  .input:focus, textarea:focus {
    outline: none;
    border-color: var(--primary-color);
    box-shadow: 0 0 0 3px rgba(139, 134, 128, 0.18);
  }

  .notice {
    padding: 10px 14px;
    border-radius: var(--radius-md);
    font-size: 13px;
    font-weight: 500;
    display: none;
  }
  .notice.ok {
    display: block;
    background: var(--success-badge-bg);
    color: var(--success-badge-text);
    border: 1px solid var(--success-badge-border);
  }
  .notice.err {
    display: block;
    background: var(--failure-badge-bg);
    color: var(--failure-badge-text);
    border: 1px solid var(--failure-badge-border);
  }

  .grid {
    display: grid;
    grid-template-columns: max-content minmax(0, 1fr);
    gap: 20px;
    align-items: start;
  }
  @media (max-width: 1024px) {
    .grid { grid-template-columns: 1fr; }
  }

  /* Matches CPA OAuthConfigPanels .panel */
  .panel {
    display: flex;
    flex-direction: column;
    gap: 12px;
    padding: clamp(16px, 2vw, 20px);
    border-radius: 16px;
    border: 1px solid var(--border-color);
    background: color-mix(in srgb, var(--bg-primary) 82%, transparent);
  }
  .panel-head {
    display: flex;
    align-items: center;
    justify-content: space-between;
    gap: 10px;
    padding-bottom: 10px;
    border-bottom: 1px solid color-mix(in srgb, var(--border-color) 65%, transparent);
  }
  .panel-title {
    margin: 0;
    font-size: 15px;
    font-weight: 650;
    letter-spacing: -0.01em;
    color: var(--text-primary);
  }

  table {
    width: auto;
    border-collapse: collapse;
    font-size: 13px;
  }
  th, td {
    text-align: left;
    padding: 6px 8px;
    border-bottom: 1px solid color-mix(in srgb, var(--border-color) 65%, transparent);
    vertical-align: middle;
    white-space: nowrap;
  }
  th {
    color: var(--text-tertiary);
    font-weight: 700;
    font-size: 11px;
    letter-spacing: 0.06em;
    text-transform: uppercase;
  }
  tbody tr {
    transition: background-color 120ms ease, opacity 120ms ease;
  }
  tbody tr:hover {
    background-color: var(--bg-hover);
  }
  tr.dragging { opacity: 0.4; }
  tr.hidden-row { opacity: 0.48; }

  .drag-handle {
    cursor: grab;
    color: var(--text-tertiary);
    user-select: none;
    font-size: 14px;
    padding-right: 6px;
    display: inline-block;
  }
  .drag-handle:hover { color: var(--text-primary); }
  .row-index {
    color: var(--text-tertiary);
    font-variant-numeric: tabular-nums;
    font-size: 12px;
  }
  .display-input {
    width: 205px !important;
  }

  /* CPA ToggleSwitch */
  .switch {
    position: relative;
    display: inline-flex;
    align-items: center;
    cursor: pointer;
    vertical-align: middle;
  }
  .switch input {
    width: 0;
    height: 0;
    opacity: 0;
    position: absolute;
  }
  .switch-track {
    width: 38px;
    height: 20px;
    background: var(--border-primary);
    border-radius: var(--radius-full);
    position: relative;
    transition: background-color 180ms var(--ease-out-strong);
  }
  .switch-thumb {
    position: absolute;
    top: 2px;
    left: 2px;
    width: 16px;
    height: 16px;
    background: #ffffff;
    border-radius: var(--radius-full);
    box-shadow: var(--shadow-sm);
    transition: transform 180ms var(--ease-out-strong);
  }
  .switch input:checked + .switch-track {
    background: var(--primary-color);
  }
  .switch input:checked + .switch-track .switch-thumb {
    transform: translateX(18px);
  }

  .preview-list {
    list-style: none;
    padding: 0;
    margin: 0;
    font-size: 13px;
    overflow-x: auto;
  }
  .preview-list li {
    display: flex;
    align-items: center;
    gap: 8px;
    padding: 7px 10px;
    border-radius: 8px;
    border: 1px solid color-mix(in srgb, var(--border-color) 65%, transparent);
    background: color-mix(in srgb, var(--bg-secondary) 45%, transparent);
    margin-bottom: 5px;
    white-space: nowrap;
  }
  .preview-num {
    width: 24px;
    color: var(--text-tertiary);
    font-variant-numeric: tabular-nums;
    font-size: 12px;
    font-weight: 600;
    flex-shrink: 0;
  }
  .preview-list li.builtin-item {
    color: var(--text-secondary);
    background: transparent;
    border-style: dashed;
  }

  .badge {
    display: inline-flex;
    min-height: 20px;
    align-items: center;
    padding: 2px 8px;
    font-size: 11px;
    font-weight: 600;
    border-radius: 6px;
    background: color-mix(in srgb, var(--bg-secondary) 82%, transparent);
    border: 1px solid color-mix(in srgb, var(--border-color) 50%, transparent);
    color: var(--text-secondary);
    white-space: nowrap;
  }
  .badge.builtin {
    color: var(--text-tertiary);
  }
  .badge.quota-high {
    background: rgba(16, 185, 129, 0.12);
    border-color: rgba(16, 185, 129, 0.25);
    color: var(--success-color);
  }
  .badge.quota-low {
    background: rgba(198, 87, 70, 0.12);
    border-color: rgba(198, 87, 70, 0.25);
    color: var(--warning-color);
  }

  .mono {
    font-family: ui-monospace, 'SF Mono', 'Cascadia Mono', 'JetBrains Mono', Menlo, Consolas, monospace;
    font-size: 12px;
  }
  .subtle { color: var(--text-tertiary); }
</style>
</head>
<body>
<main class="page">
  <header class="page-header">
    <h1 class="page-title">Model List Tweaker</h1>
    <p class="page-desc">Drag rows to reorder, customize display names, and toggle visibility across the global model catalog.</p>
  </header>

  <div class="status-bar">
    <div class="status-pill">
      <span class="status-dot"></span>
      <span class="status-label">Visible</span>
      <span class="status-value" id="visibleCount">0</span>
    </div>
    <span class="status-divider"></span>
    <div class="status-pill">
      <span class="status-label">Hidden</span>
      <span class="status-value" id="hiddenCount">0</span>
    </div>
    <span class="status-divider"></span>
    <div class="status-pill">
      <span class="status-label">Total</span>
      <span class="status-value" id="totalCount">0</span>
    </div>
    <span class="status-divider"></span>
    <div class="status-pill">
      <span class="status-label">Claude Catalog</span>
      <span class="status-value" id="cloakBadge">Cloaked</span>
    </div>
  </div>

  <div class="toolbar">
    <input type="search" id="filterInput" class="input search-input" placeholder="Search model ID or display name..." oninput="renderRulesTable()">
    <input type="password" id="mgmtKey" class="input" placeholder="Management Key" style="width: 180px; display: none;">
    <button class="btn" id="toggleAllBtn" onclick="toggleHideAll()">Hide All</button>
    <button class="btn" onclick="refreshFromServer(false)">Refresh</button>
    <button class="btn btn-primary" onclick="saveConfig()">Save Changes</button>
  </div>

  <div id="statusBar" class="notice"></div>

  <div class="grid">
    <section class="panel">
      <div class="panel-head">
        <h2 class="panel-title">Model Catalog</h2>
      </div>
      <table>
        <thead>
          <tr>
            <th>#</th>
            <th>Model ID</th>
            <th>Display Name</th>
            <th style="text-align:center;">Hide</th>
          </tr>
        </thead>
        <tbody id="rulesBody"></tbody>
      </table>
    </section>

    <section class="panel">
      <div class="panel-head">
        <h2 class="panel-title">Final Order Preview (Claude <code>/model</code>)</h2>
      </div>
      <ol id="previewList" class="preview-list"></ol>
    </section>
  </div>

  <section class="panel">
    <div class="panel-head">
      <h2 class="panel-title">Configuration YAML (<code>config.yaml</code>)</h2>
      <button class="btn btn-sm" onclick="copyYAML()">Copy YAML</button>
    </div>
    <textarea id="yamlPreview" rows="12" class="mono" readonly></textarea>
  </section>
</main>

<script>
const STATE_API_URL = '/v0/management/plugins/model-list-tweaker/state';
let state = { config: { enabled: true, priority: 100, models: [] }, available: [], claude_cloaking: true, quota_by_model: {} };
let dragIndex = null;

function syncThemeWithCPA() {
  const apply = (mode) => {
    if (mode === 'white') {
      document.documentElement.setAttribute('data-theme', 'white');
    } else if (mode === 'light') {
      document.documentElement.setAttribute('data-theme', 'light');
    } else {
      document.documentElement.setAttribute('data-theme', 'dark');
    }
  };
  try {
    if (window.parent && window.parent !== window && window.parent.document) {
      const parentDoc = window.parent.document.documentElement;
      const syncFromParent = () => {
        const attr = parentDoc.getAttribute('data-theme');
        if (attr === 'dark' || attr === 'white') {
          apply(attr);
        } else {
          apply('light');
        }
      };
      syncFromParent();
      new MutationObserver(syncFromParent).observe(parentDoc, { attributes: true, attributeFilter: ['data-theme'] });
      return;
    }
  } catch (_) {}
  try {
    const raw = localStorage.getItem('cli-proxy-theme');
    if (raw) {
      const parsed = JSON.parse(raw);
      const t = parsed?.state?.theme;
      const resolved = parsed?.state?.resolvedTheme;
      if (t === 'dark' || (t === 'auto' && resolved === 'dark')) {
        apply('dark');
        return;
      }
      if (t === 'white' || (t === 'auto' && resolved !== 'dark')) {
        apply('white');
        return;
      }
      if (t === 'light') {
        apply('light');
        return;
      }
    }
  } catch (_) {}
  apply(window.matchMedia && window.matchMedia('(prefers-color-scheme: light)').matches ? 'light' : 'dark');
}

function deobfuscateCPASecret(payload) {
  const prefix = 'enc::v1::';
  if (!payload || !payload.startsWith(prefix)) return payload;
  try {
    const bin = atob(payload.slice(prefix.length));
    const data = new Uint8Array(bin.length);
    for (let i = 0; i < bin.length; i++) data[i] = bin.charCodeAt(i);
    const keyStr = 'cli-proxy-api-webui::secure-storage|' + window.location.host + '|' + navigator.userAgent;
    const keyBytes = new TextEncoder().encode(keyStr);
    const out = new Uint8Array(data.length);
    for (let i = 0; i < data.length; i++) out[i] = data[i] ^ keyBytes[i % keyBytes.length];
    return new TextDecoder().decode(out);
  } catch (_) {
    return payload;
  }
}

function detectStoredManagementKey() {
  try {
    const authRaw = localStorage.getItem('cli-proxy-auth');
    if (authRaw) {
      const parsed = JSON.parse(authRaw);
      const k = parsed?.state?.managementKey;
      if (k && typeof k === 'string' && k.trim()) return k.trim();
    }
    const legacy = localStorage.getItem('managementKey');
    if (legacy) {
      const dec = deobfuscateCPASecret(legacy);
      try {
        const parsed = JSON.parse(dec);
        if (typeof parsed === 'string' && parsed.trim()) return parsed.trim();
      } catch (_) {
        if (typeof dec === 'string' && dec.trim()) return dec.trim();
      }
    }
  } catch (_) {}
  return '';
}

function hydrateState() {
  if (!state.config) state.config = { enabled: true, priority: 100, models: [] };
  if (!Array.isArray(state.config.models)) state.config.models = [];
  if (!Array.isArray(state.available)) state.available = [];

  const existing = new Set(state.config.models.map(r => (r.id || '').toLowerCase()));
  for (const item of state.available) {
    if (!existing.has(item.id.toLowerCase())) {
      state.config.models.push({
        id: item.id,
        display_name: '',
        hidden: false
      });
      existing.add(item.id.toLowerCase());
    }
  }
  renderAll();
}

function initUI() {
  syncThemeWithCPA();
  const keyInput = document.getElementById('mgmtKey');
  const detectedKey = detectStoredManagementKey();
  if (detectedKey) {
    keyInput.value = detectedKey;
    keyInput.style.display = 'none';
  } else if (!keyInput.value) {
    keyInput.style.display = 'inline-block';
  }
  hydrateState();
  void refreshFromServer(true);
}

function renderAll() {
  renderCounts();
  renderRulesTable();
  renderPreview();
  renderYAML();
}

function renderCounts() {
  const total = state.config.models.length;
  const hidden = state.config.models.filter(r => !!r.hidden).length;
  document.getElementById('totalCount').textContent = String(total);
  document.getElementById('hiddenCount').textContent = String(hidden);
  document.getElementById('visibleCount').textContent = String(total - hidden);
}

function defaultDisplayNameFor(id) {
  const found = state.available.find(a => a.id.toLowerCase() === (id || '').toLowerCase());
  return found && found.display_name ? found.display_name : id;
}

function visibleRules() {
  const filter = (document.getElementById('filterInput').value || '').toLowerCase().trim();
  if (!filter) return state.config.models;
  return state.config.models.filter(rule => {
    const defName = defaultDisplayNameFor(rule.id);
    const text = ((rule.id || '') + ' ' + (rule.display_name || '') + ' ' + defName).toLowerCase();
    return text.includes(filter);
  });
}

function updateToggleAllControl() {
  const vis = visibleRules();
  const allHidden = vis.length > 0 && vis.every(r => !!r.hidden);
  const btn = document.getElementById('toggleAllBtn');
  if (btn) {
    btn.textContent = allHidden ? 'Unhide All' : 'Hide All';
  }
}

function toggleHideAll(forceVal) {
  const vis = visibleRules();
  if (vis.length === 0) return;
  const allHidden = vis.every(r => !!r.hidden);
  const nextHidden = typeof forceVal === 'boolean' ? forceVal : !allHidden;
  for (const r of vis) {
    r.hidden = nextHidden;
  }
  renderAll();
}

function renderRulesTable() {
  const tbody = document.getElementById('rulesBody');
  const filter = (document.getElementById('filterInput').value || '').toLowerCase().trim();
  tbody.innerHTML = '';

  state.config.models.forEach((rule, idx) => {
    const defName = defaultDisplayNameFor(rule.id);
    const text = ((rule.id || '') + ' ' + (rule.display_name || '') + ' ' + defName).toLowerCase();
    if (filter && !text.includes(filter)) return;

    const tr = document.createElement('tr');
    if (rule.hidden) tr.className = 'hidden-row';
    tr.draggable = true;
    tr.ondragstart = () => { dragIndex = idx; tr.classList.add('dragging'); };
    tr.ondragend = () => { dragIndex = null; tr.classList.remove('dragging'); };
    tr.ondragover = (e) => { e.preventDefault(); };
    tr.ondrop = (e) => {
      e.preventDefault();
      if (dragIndex === null || dragIndex === idx) return;
      const moved = state.config.models.splice(dragIndex, 1)[0];
      state.config.models.splice(idx, 0, moved);
      dragIndex = null;
      renderAll();
    };

    tr.innerHTML =
      '<td><span class="drag-handle" title="Drag to reorder">⋮⋮</span><span class="row-index">' + (idx + 1) + '</span></td>' +
      '<td class="mono">' + escHTML(rule.id || '') + '</td>' +
      '<td><input type="text" class="input display-input" value="' + escAttr(rule.display_name || '') + '" placeholder="' + escAttr(defName || '') + '" oninput="updateRule(' + idx + ', \'display_name\', this.value)"></td>' +
      '<td style="text-align:center;">' +
        '<label class="switch" title="Hide this model">' +
          '<input type="checkbox" ' + (rule.hidden ? 'checked' : '') + ' onchange="updateRule(' + idx + ', \'hidden\', this.checked); renderAll();">' +
          '<span class="switch-track"><span class="switch-thumb"></span></span>' +
        '</label>' +
      '</td>';
    tbody.appendChild(tr);
  });
  updateToggleAllControl();
}

function updateRule(idx, field, val) {
  if (!state.config.models[idx]) return;
  state.config.models[idx][field] = typeof val === 'string' ? val.trim() : val;
  renderCounts();
  renderPreview();
  renderYAML();
}

function cloakClaudeID(id) {
  if (!id || id.startsWith('claude-fable-5-dd-')) return id;
  return 'claude-fable-5-dd-' + Array.from(id).reverse().join('');
}

function renderPreview() {
  const list = document.getElementById('previewList');
  const cloak = state.claude_cloaking !== false;
  const cloakBadge = document.getElementById('cloakBadge');
  if (cloakBadge) {
    cloakBadge.textContent = cloak ? 'Cloaked' : 'Uncloaked';
  }
  list.innerHTML = '';

  const topBuiltins = [
    { label: 'Default (recommended)', desc: 'Use the default model (currently Opus 5.5) · $4/$20 per Mtok', builtin: true },
    { label: 'Opus', desc: 'Opus 5.5 · Best for everyday, complex tasks · $4/$20 per Mtok', builtin: true }
  ];
  const bottomBuiltins = [
    { label: 'Fable', desc: 'Fable 5.1 · Most capable for your hardest and longest-running tasks · $10/$50 per Mtok', builtin: true },
    { label: 'Sonnet', desc: 'Sonnet 5.5 · Efficient for routine tasks · $2/$10 per Mtok', builtin: true },
    { label: 'Haiku', desc: 'Haiku 5.5 · Fastest for quick answers · $0.10/$0.50 per Mtok', builtin: true }
  ];

  const topGateway = [];
  const bottomGateway = [];
  const quotaMap = state.quota_by_model || {};
  const quotaEnabled = !!(state.config.quota && state.config.quota.enabled);

  for (const r of state.config.models) {
    const id = (r.id || '').trim();
    if (!id || r.hidden) continue;
    const defName = defaultDisplayNameFor(id);
    const effectiveID = cloak ? cloakClaudeID(id) : id;
    let effectiveName = (r.display_name || '').trim() || defName;
    const pct = quotaEnabled && typeof quotaMap[id] === 'number' ? quotaMap[id] : null;
    if (pct !== null && !/\(\d+%\)$/.test(effectiveName)) {
      effectiveName = effectiveName + ' (' + pct + '%)';
    }

    if (!cloak && !/(claude|anthropic)/i.test(effectiveID)) {
      continue;
    }

    const entry = {
      label: effectiveName,
      id: id,
      pct: pct,
      builtin: false
    };
    if (effectiveID.toLowerCase().startsWith('claude-fable-5')) {
      topGateway.push(entry);
    } else {
      bottomGateway.push(entry);
    }
  }

  const finalRows = [...topBuiltins, ...topGateway, ...bottomBuiltins, ...bottomGateway];
  finalRows.forEach((item, index) => {
    const li = document.createElement('li');
    const numSpan = '<span class="preview-num">' + (index + 1) + '.</span>';
    if (item.builtin) {
      li.className = 'builtin-item';
      li.innerHTML = numSpan + '<strong>' + escHTML(item.label) + '</strong> <span class="badge builtin">Built-in</span> <span class="subtle" style="font-size:12px;">' + escHTML(item.desc) + '</span>';
    } else {
      let qBadge = '';
      if (item.pct !== null && item.pct !== undefined) {
        const cls = item.pct >= 70 ? 'quota-high' : (item.pct < 30 ? 'quota-low' : '');
        qBadge = ' <span class="badge ' + cls + '">' + item.pct + '%</span>';
      }
      li.innerHTML = numSpan + '<strong>' + escHTML(item.label) + '</strong> <span class="mono subtle">' + escHTML(item.id) + '</span>' + qBadge;
    }
    list.appendChild(li);
  });
}

function buildConfigObject() {
  const models = state.config.models
    .filter(r => (r.id || '').trim() !== '')
    .map(r => {
      const out = { id: r.id.trim() };
      if (r.display_name && r.display_name.trim()) out.display_name = r.display_name.trim();
      if (r.hidden) out.hidden = true;
      return out;
    });
  const out = {
    enabled: true,
    priority: state.config.priority || 100,
    models: models
  };
  if (state.config.quota && state.config.quota.enabled) {
    out.quota = state.config.quota;
  }
  return out;
}

function renderYAML() {
  const cfg = buildConfigObject();
  const lines = [
    'plugins:',
    '  enabled: true',
    '  configs:',
    '    model-list-tweaker:',
    '      enabled: true',
    '      priority: ' + cfg.priority,
    '      models:'
  ];
  for (const m of cfg.models) {
    lines.push('        - id: ' + JSON.stringify(m.id));
    if (m.display_name) lines.push('          display_name: ' + JSON.stringify(m.display_name));
    if (m.hidden) lines.push('          hidden: true');
  }
  if (state.config.quota && state.config.quota.enabled) {
    lines.push('      quota:');
    lines.push('        enabled: true');
    lines.push('        management_url: ' + JSON.stringify(state.config.quota.management_url));
    lines.push('        management_key: ' + JSON.stringify(state.config.quota.management_key));
    lines.push('        refresh_minutes: ' + state.config.quota.refresh_minutes);
  }
  document.getElementById('yamlPreview').value = lines.join('\n');
}

async function copyYAML() {
  const el = document.getElementById('yamlPreview');
  el.select();
  await navigator.clipboard.writeText(el.value);
  showStatus('Copied YAML snippet to clipboard.', true);
}

function authHeaders() {
  const key = (document.getElementById('mgmtKey').value || detectStoredManagementKey() || '').trim();
  const headers = { 'Content-Type': 'application/json' };
  if (key) {
    headers['Authorization'] = 'Bearer ' + key;
    headers['X-Management-Key'] = key;
  }
  return headers;
}

async function refreshFromServer(silent) {
  try {
    const resp = await fetch(STATE_API_URL, {
      method: 'GET',
      headers: authHeaders(),
      cache: 'no-store'
    });
    if (resp.status === 401 || resp.status === 403) {
      document.getElementById('mgmtKey').style.display = 'inline-block';
      throw new Error('Management key required (HTTP ' + resp.status + ')');
    }
    if (!resp.ok) throw new Error('HTTP ' + resp.status);
    state = await resp.json();
    hydrateState();
    if (!silent) showStatus('Refreshed model catalog from server.', true);
  } catch (err) {
    showStatus('Failed to load state: ' + err.message, false);
  }
}

async function saveConfig() {
  const cfg = buildConfigObject();
  const headers = authHeaders();

  try {
    const runtimeResp = await fetch(STATE_API_URL, {
      method: 'POST',
      headers: headers,
      body: JSON.stringify(cfg),
      cache: 'no-store'
    });
    if (runtimeResp.status === 401 || runtimeResp.status === 403) {
      document.getElementById('mgmtKey').style.display = 'inline-block';
      throw new Error('Management key required (HTTP ' + runtimeResp.status + ')');
    }
    if (!runtimeResp.ok) throw new Error('Runtime apply failed: HTTP ' + runtimeResp.status);
    state = await runtimeResp.json();
    hydrateState();

    let persisted = false;
    for (const endpoint of ['/v8/management/config/plugins/configs/model-list-tweaker', '/v0/management/plugins/model-list-tweaker/config']) {
      const pResp = await fetch(endpoint, {
        method: 'PUT',
        headers: headers,
        body: JSON.stringify(cfg)
      });
      if (pResp.ok) {
        persisted = true;
        break;
      }
    }
    if (persisted) {
      showStatus('Saved to config.yaml and applied immediately.', true);
    } else {
      document.getElementById('mgmtKey').style.display = 'inline-block';
      showStatus('Applied to active runtime! Enter Management Key to also persist to config.yaml.', true);
    }
  } catch (err) {
    showStatus('Save error: ' + err.message, false);
  }
}

function showStatus(msg, ok) {
  const bar = document.getElementById('statusBar');
  bar.className = 'notice ' + (ok ? 'ok' : 'err');
  bar.textContent = msg;
}

function escHTML(s) {
  return String(s).replace(/&/g, '&amp;').replace(/</g, '&lt;').replace(/>/g, '&gt;').replace(/"/g, '&quot;');
}

function escAttr(s) {
  return escHTML(s).replace(/'/g, '&#39;');
}

initUI();
</script>
</body>
</html>
`
