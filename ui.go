package main

import (
	"encoding/json"
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
	Config    pluginConfig      `json:"config"`
	Available []discoveredModel `json:"available"`
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

	op := ""
	if req.Query != nil {
		op = strings.ToLower(strings.TrimSpace(req.Query.Get("op")))
	}

	if strings.EqualFold(req.Method, http.MethodPost) || op == "apply" {
		bodyBytes := req.Body
		if len(bodyBytes) == 0 && req.Query != nil {
			if rawCfg := req.Query.Get("config"); rawCfg != "" {
				bodyBytes = []byte(rawCfg)
			}
		}
		if len(bodyBytes) > 0 {
			var nextCfg pluginConfig
			if errUnmarshal := json.Unmarshal(bodyBytes, &nextCfg); errUnmarshal != nil {
				return okEnvelope(jsonManagementResponse(http.StatusBadRequest, map[string]any{
					"error":   "invalid_config",
					"message": errUnmarshal.Error(),
				}))
			}
			if !nextCfg.Enabled {
				nextCfg.Enabled = true
			}
			if nextCfg.Priority == 0 {
				nextCfg.Priority = currentCompiledConfig().raw.Priority
			}
			setPluginConfig(nextCfg)
		}
		return okEnvelope(jsonManagementResponse(http.StatusOK, buildUIState(req.HostCallbackID)))
	}

	if op == "state" || strings.HasSuffix(strings.TrimRight(req.Path, "/"), mgmtStateRoutePath) {
		return okEnvelope(jsonManagementResponse(http.StatusOK, buildUIState(req.HostCallbackID)))
	}

	state := buildUIState(req.HostCallbackID)
	page, errPage := renderEditorHTML(state)
	if errPage != nil {
		return nil, errPage
	}
	return okEnvelope(managementResponse{
		StatusCode: http.StatusOK,
		Headers: http.Header{
			"Content-Type":  []string{"text/html; charset=utf-8"},
			"Cache-Control": []string{"no-store"},
		},
		Body: page,
	})
}

func buildUIState(hostCallbackID string) uiStatePayload {
	compiled := currentCompiledConfig()
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

	return uiStatePayload{
		Config:    compiled.raw,
		Available: available,
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

func renderEditorHTML(initialState uiStatePayload) ([]byte, error) {
	stateJSON, errMarshal := marshalJSONNoEscape(initialState)
	if errMarshal != nil {
		return nil, errMarshal
	}
	htmlDoc := strings.Replace(editorHTMLTemplate, "__INITIAL_STATE_JSON__", string(stateJSON), 1)
	return []byte(htmlDoc), nil
}

const editorHTMLTemplate = `<!doctype html>
<html lang="en">
<head>
<meta charset="utf-8">
<meta name="viewport" content="width=device-width, initial-scale=1">
<title>Model List Tweaker</title>
<style>
  :root {
    --bg: #0f172a;
    --panel: #1e293b;
    --panel-alt: #0b1120;
    --border: #334155;
    --text: #f8fafc;
    --muted: #94a3b8;
    --accent: #38bdf8;
    --accent-hover: #0284c7;
    --danger: #ef4444;
    --success: #22c55e;
  }
  * { box-sizing: border-box; }
  body {
    margin: 0;
    padding: 24px;
    font-family: -apple-system, BlinkMacSystemFont, "Segoe UI", Roboto, sans-serif;
    background: var(--bg);
    color: var(--text);
    line-height: 1.45;
  }
  .container { max-width: 1280px; margin: 0 auto; }
  header {
    display: flex;
    flex-wrap: wrap;
    align-items: center;
    justify-content: space-between;
    gap: 16px;
    margin-bottom: 20px;
    padding-bottom: 16px;
    border-bottom: 1px solid var(--border);
  }
  h1 { margin: 0; font-size: 1.5rem; }
  .subtitle { color: var(--muted); font-size: 0.9rem; margin-top: 4px; }
  .toolbar { display: flex; flex-wrap: wrap; gap: 10px; align-items: center; }
  button {
    background: var(--panel);
    color: var(--text);
    border: 1px solid var(--border);
    border-radius: 6px;
    padding: 8px 14px;
    font-size: 0.875rem;
    font-weight: 500;
    cursor: pointer;
  }
  button:hover { border-color: var(--accent); }
  button.primary {
    background: var(--accent);
    color: #0f172a;
    border-color: var(--accent);
    font-weight: 600;
  }
  button.primary:hover { background: var(--accent-hover); color: #fff; }
  button.sm { padding: 4px 8px; font-size: 0.78rem; }
  input[type="text"], input[type="password"], textarea {
    background: var(--panel-alt);
    color: var(--text);
    border: 1px solid var(--border);
    border-radius: 6px;
    padding: 7px 10px;
    font-size: 0.85rem;
    width: 100%;
  }
  input[type="text"]:focus, input[type="password"]:focus, textarea:focus {
    outline: none;
    border-color: var(--accent);
  }
  .grid {
    display: grid;
    grid-template-columns: 2fr 1fr;
    gap: 20px;
  }
  @media (max-width: 980px) { .grid { grid-template-columns: 1fr; } }
  .card {
    background: var(--panel);
    border: 1px solid var(--border);
    border-radius: 10px;
    padding: 16px;
    margin-bottom: 20px;
  }
  .card h2 {
    margin: 0 0 12px 0;
    font-size: 1.05rem;
    display: flex;
    justify-content: space-between;
    align-items: center;
  }
  table {
    width: 100%;
    border-collapse: collapse;
    font-size: 0.85rem;
  }
  th, td {
    text-align: left;
    padding: 8px;
    border-bottom: 1px solid var(--border);
    vertical-align: middle;
  }
  th { color: var(--muted); font-weight: 600; font-size: 0.78rem; text-transform: uppercase; }
  tr.dragging { opacity: 0.45; }
  tr.hidden-row { opacity: 0.55; }
  .drag-handle {
    cursor: grab;
    color: var(--muted);
    user-select: none;
    font-size: 1.1rem;
    padding: 0 6px;
  }
  .badge {
    display: inline-block;
    padding: 2px 6px;
    font-size: 0.72rem;
    border-radius: 4px;
    background: var(--panel-alt);
    border: 1px solid var(--border);
    color: var(--muted);
    margin-right: 4px;
  }
  .status-bar {
    padding: 10px 14px;
    border-radius: 6px;
    margin-bottom: 16px;
    font-size: 0.875rem;
    display: none;
  }
  .status-bar.ok { display: block; background: rgba(34,197,94,0.15); border: 1px solid var(--success); color: #86efac; }
  .status-bar.err { display: block; background: rgba(239,68,68,0.15); border: 1px solid var(--danger); color: #fca5a5; }
  .preview-list {
    list-style: decimal;
    padding-left: 22px;
    margin: 0;
    font-size: 0.85rem;
  }
  .preview-list li {
    padding: 6px 4px;
    border-bottom: 1px solid rgba(51,65,85,0.5);
  }
  .mono { font-family: ui-monospace, SFMono-Regular, Menlo, Consolas, monospace; font-size: 0.8rem; }
  .muted { color: var(--muted); }
  .row-actions { display: flex; gap: 4px; }
  .top-controls {
    display: flex;
    flex-wrap: wrap;
    gap: 12px;
    align-items: center;
    margin-bottom: 12px;
  }
  label.checkbox {
    display: inline-flex;
    align-items: center;
    gap: 6px;
    font-size: 0.85rem;
    cursor: pointer;
  }
</style>
</head>
<body>
<div class="container">
  <header>
    <div>
      <h1>Model List Tweaker</h1>
      <div class="subtitle">Reorder, rename (ID &amp; Display Name), and hide models across Claude Code (<code>/model</code>), OpenAI, Codex, Gemini, and Grok.</div>
    </div>
    <div class="toolbar">
      <input type="password" id="mgmtKey" placeholder="Management Key (auto-detected if logged in)" style="width: 250px;">
      <button onclick="refreshFromServer()">Refresh Models</button>
      <button class="primary" onclick="saveConfig()">Save Configuration</button>
    </div>
  </header>

  <div id="statusBar" class="status-bar"></div>

  <div class="grid">
    <div>
      <div class="card">
        <h2>
          <span>Ordered Model Rules</span>
          <div class="toolbar">
            <button class="sm" onclick="populateAllAvailable()">+ Add All Active Models</button>
            <button class="sm" onclick="addBlankRule()">+ Add Custom Rule</button>
          </div>
        </h2>
        <div class="top-controls">
          <input type="text" id="filterInput" placeholder="Filter rules by ID or display name..." oninput="renderRulesTable()" style="max-width: 320px;">
          <label class="checkbox">
            <input type="checkbox" id="hideUnlisted" onchange="syncStateFromInputs()">
            Hide models not listed in this table (<code>hide_unlisted</code>)
          </label>
        </div>
        <table>
          <thead>
            <tr>
              <th style="width:36px;">#</th>
              <th>Match Model ID</th>
              <th>New ID (Optional Alias)</th>
              <th>Display Name (Optional)</th>
              <th style="width:70px;">Hide</th>
              <th style="width:120px;">Order / Remove</th>
            </tr>
          </thead>
          <tbody id="rulesBody"></tbody>
        </table>
      </div>

      <div class="card">
        <h2>
          <span>Active Unlisted Models (<span id="unlistedCount">0</span>)</span>
          <span class="muted" style="font-size:0.8rem;font-weight:normal;">Click "+ Pin" to move a model into the ordered list or "Hide" to exclude it</span>
        </h2>
        <table>
          <thead>
            <tr>
              <th>Model ID</th>
              <th>Default Display Name</th>
              <th>Providers</th>
              <th style="width:140px;">Action</th>
            </tr>
          </thead>
          <tbody id="availableBody"></tbody>
        </table>
      </div>
    </div>

    <div>
      <div class="card">
        <h2>Global Hide Patterns</h2>
        <div class="subtitle" style="margin-bottom:8px;">One model ID or wildcard pattern per line (e.g. <code>gpt-4o-mini*</code>, <code>*-preview</code>).</div>
        <textarea id="hidePatterns" rows="5" class="mono" oninput="syncStateFromInputs()" placeholder="gpt-4o-mini*&#10;*-legacy"></textarea>
      </div>

      <div class="card">
        <h2>
          <span>Live Final Order Preview</span>
          <label class="checkbox" style="font-weight:normal;font-size:0.78rem;">
            <input type="checkbox" id="previewClaudeCloak" checked onchange="renderPreview()">
            Claude <code>/model</code> cloaking
          </label>
        </h2>
        <ol id="previewList" class="preview-list"></ol>
      </div>

      <div class="card">
        <h2>
          <span>YAML Snippet (<code>config.yaml</code>)</span>
          <button class="sm" onclick="copyYAML()">Copy YAML</button>
        </h2>
        <textarea id="yamlPreview" rows="12" class="mono" readonly></textarea>
      </div>
    </div>
  </div>
</div>

<script>
let state = __INITIAL_STATE_JSON__;
let dragIndex = null;

function detectStoredManagementKey() {
  try {
    const candidates = ['management_key', 'secret_key', 'cpa_management_key', 'cliproxy_management_key', 'apiKey', 'token', 'auth_token'];
    for (const k of candidates) {
      const v = localStorage.getItem(k) || sessionStorage.getItem(k);
      if (v && typeof v === 'string' && v.trim()) return v.trim().replace(/^"|"$/g, '');
    }
    for (let i = 0; i < localStorage.length; i++) {
      const k = localStorage.key(i);
      if (k && /secret|mgmt|management|key|token/i.test(k)) {
        const v = localStorage.getItem(k);
        if (v && v.length < 256 && !v.startsWith('{')) return v.trim().replace(/^"|"$/g, '');
      }
    }
  } catch (_) {}
  return '';
}

function initUI() {
  if (!state.config) state.config = { enabled: true, priority: 100, hide_unlisted: false, hide: [], models: [] };
  if (!Array.isArray(state.config.models)) state.config.models = [];
  if (!Array.isArray(state.config.hide)) state.config.hide = [];
  if (!Array.isArray(state.available)) state.available = [];

  const detectedKey = detectStoredManagementKey();
  if (detectedKey) document.getElementById('mgmtKey').value = detectedKey;

  document.getElementById('hideUnlisted').checked = !!state.config.hide_unlisted;
  document.getElementById('hidePatterns').value = state.config.hide.join('\n');
  renderAll();
}

function syncStateFromInputs() {
  state.config.hide_unlisted = document.getElementById('hideUnlisted').checked;
  state.config.hide = document.getElementById('hidePatterns').value
    .split('\n')
    .map(s => s.trim())
    .filter(Boolean);
  renderAvailableTable();
  renderPreview();
  renderYAML();
}

function renderAll() {
  renderRulesTable();
  renderAvailableTable();
  renderPreview();
  renderYAML();
}

function renderRulesTable() {
  const tbody = document.getElementById('rulesBody');
  const filter = (document.getElementById('filterInput').value || '').toLowerCase().trim();
  tbody.innerHTML = '';

  state.config.models.forEach((rule, idx) => {
    const text = ((rule.id || '') + ' ' + (rule.new_id || '') + ' ' + (rule.display_name || '')).toLowerCase();
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
      '<td><span class="drag-handle" title="Drag to reorder">☰</span>' + (idx + 1) + '</td>' +
      '<td><input type="text" class="mono" value="' + escAttr(rule.id || '') + '" placeholder="e.g. gpt-5.4" oninput="updateRule(' + idx + ', \'id\', this.value)"></td>' +
      '<td><input type="text" class="mono" value="' + escAttr(rule.new_id || '') + '" placeholder="(keep original ID)" oninput="updateRule(' + idx + ', \'new_id\', this.value)"></td>' +
      '<td><input type="text" value="' + escAttr(rule.display_name || '') + '" placeholder="(keep default name)" oninput="updateRule(' + idx + ', \'display_name\', this.value)"></td>' +
      '<td style="text-align:center;"><input type="checkbox" ' + (rule.hidden ? 'checked' : '') + ' onchange="updateRule(' + idx + ', \'hidden\', this.checked); renderAll();"></td>' +
      '<td><div class="row-actions">' +
        '<button class="sm" onclick="moveRule(' + idx + ', -1)" title="Move up">↑</button>' +
        '<button class="sm" onclick="moveRule(' + idx + ', 1)" title="Move down">↓</button>' +
        '<button class="sm" onclick="removeRule(' + idx + ')" title="Remove">✕</button>' +
      '</div></td>';
    tbody.appendChild(tr);
  });
}

function updateRule(idx, field, val) {
  if (!state.config.models[idx]) return;
  state.config.models[idx][field] = typeof val === 'string' ? val.trim() : val;
  renderAvailableTable();
  renderPreview();
  renderYAML();
}

function moveRule(idx, delta) {
  const target = idx + delta;
  if (target < 0 || target >= state.config.models.length) return;
  const tmp = state.config.models[idx];
  state.config.models[idx] = state.config.models[target];
  state.config.models[target] = tmp;
  renderAll();
}

function removeRule(idx) {
  state.config.models.splice(idx, 1);
  renderAll();
}

function addBlankRule() {
  state.config.models.push({ id: '', new_id: '', display_name: '', hidden: false });
  renderAll();
}

function populateAllAvailable() {
  const existing = new Set(state.config.models.map(r => (r.id || '').toLowerCase()));
  for (const item of state.available) {
    if (!existing.has(item.id.toLowerCase())) {
      state.config.models.push({
        id: item.id,
        new_id: '',
        display_name: item.display_name !== item.id ? item.display_name : '',
        hidden: false
      });
      existing.add(item.id.toLowerCase());
    }
  }
  renderAll();
}

function pinAvailableModel(id, displayName, hidden) {
  state.config.models.push({
    id: id,
    new_id: '',
    display_name: displayName && displayName !== id ? displayName : '',
    hidden: !!hidden
  });
  renderAll();
}

function renderAvailableTable() {
  const tbody = document.getElementById('availableBody');
  tbody.innerHTML = '';
  const listed = new Set(state.config.models.map(r => (r.id || '').toLowerCase()));
  const unlisted = state.available.filter(a => !listed.has(a.id.toLowerCase()));
  document.getElementById('unlistedCount').textContent = String(unlisted.length);

  for (const item of unlisted) {
    const isHiddenByPattern = matchesAnyGlob(item.id, item.display_name, state.config.hide) || state.config.hide_unlisted;
    const tr = document.createElement('tr');
    if (isHiddenByPattern) tr.className = 'hidden-row';
    const badges = (item.providers || []).map(p => '<span class="badge">' + escHTML(p) + '</span>').join('');
    tr.innerHTML =
      '<td class="mono">' + escHTML(item.id) + '</td>' +
      '<td>' + escHTML(item.display_name || item.id) + (isHiddenByPattern ? ' <span class="badge">hidden</span>' : '') + '</td>' +
      '<td>' + badges + '</td>' +
      '<td><div class="row-actions">' +
        '<button class="sm" data-id="' + escAttr(item.id) + '" data-dn="' + escAttr(item.display_name || '') + '" onclick="pinAvailableModel(this.dataset.id, this.dataset.dn, false)">+ Pin</button>' +
        '<button class="sm" data-id="' + escAttr(item.id) + '" data-dn="' + escAttr(item.display_name || '') + '" onclick="pinAvailableModel(this.dataset.id, this.dataset.dn, true)">Hide</button>' +
      '</div></td>';
    tbody.appendChild(tr);
  }
}

function cloakClaudeID(id) {
  if (!id || id.startsWith('claude-fable-5-dd-')) return id;
  return 'claude-fable-5-dd-' + Array.from(id).reverse().join('');
}

function globToRegExp(pat) {
  const escaped = pat.replace(/[.+^${}()|[\]\\]/g, '\\$&').replace(/\*/g, '.*').replace(/\?/g, '.');
  return new RegExp('^' + escaped + '$', 'i');
}

function matchesAnyGlob(id, dn, patterns) {
  for (const p of (patterns || [])) {
    if (!p) continue;
    const rx = globToRegExp(p);
    if (rx.test(id) || (dn && rx.test(dn))) return true;
  }
  return false;
}

function renderPreview() {
  const list = document.getElementById('previewList');
  const cloak = document.getElementById('previewClaudeCloak').checked;
  list.innerHTML = '';

  const availByID = new Map();
  for (const a of state.available) availByID.set(a.id.toLowerCase(), a);

  const used = new Set();
  const finalItems = [];

  for (const r of state.config.models) {
    const id = (r.id || '').trim();
    if (!id) continue;
    used.add(id.toLowerCase());
    if (r.hidden) continue;
    const base = availByID.get(id.toLowerCase()) || { id: id, display_name: id };
    const effectiveID = (r.new_id || '').trim() || base.id;
    const effectiveName = (r.display_name || '').trim() || (r.new_id ? r.new_id : (base.display_name || base.id));
    finalItems.push({
      id: cloak ? cloakClaudeID(effectiveID) : effectiveID,
      rawId: base.id,
      displayName: effectiveName,
      pinned: true
    });
  }

  if (!state.config.hide_unlisted) {
    for (const a of state.available) {
      if (used.has(a.id.toLowerCase())) continue;
      if (matchesAnyGlob(a.id, a.display_name, state.config.hide)) continue;
      finalItems.push({
        id: cloak ? cloakClaudeID(a.id) : a.id,
        rawId: a.id,
        displayName: a.display_name || a.id,
        pinned: false
      });
    }
  }

  for (const item of finalItems) {
    const li = document.createElement('li');
    li.innerHTML = '<strong>' + escHTML(item.displayName) + '</strong> ' +
      '<span class="mono muted">(' + escHTML(item.id) + ')</span>' +
      (item.pinned ? ' <span class="badge">pinned</span>' : '');
    list.appendChild(li);
  }
}

function buildConfigObject() {
  const models = state.config.models
    .filter(r => (r.id || '').trim() !== '')
    .map(r => {
      const out = { id: r.id.trim() };
      if (r.new_id && r.new_id.trim()) out.new_id = r.new_id.trim();
      if (r.display_name && r.display_name.trim()) out.display_name = r.display_name.trim();
      if (r.provider && r.provider.trim()) out.provider = r.provider.trim();
      if (r.hidden) out.hidden = true;
      return out;
    });
  return {
    enabled: true,
    priority: state.config.priority || 100,
    hide_unlisted: !!state.config.hide_unlisted,
    hide: (state.config.hide || []).filter(Boolean),
    models: models
  };
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
    '      hide_unlisted: ' + (cfg.hide_unlisted ? 'true' : 'false')
  ];
  if (cfg.hide.length > 0) {
    lines.push('      hide:');
    for (const h of cfg.hide) lines.push('        - ' + JSON.stringify(h));
  }
  if (cfg.models.length > 0) {
    lines.push('      models:');
    for (const m of cfg.models) {
      lines.push('        - id: ' + JSON.stringify(m.id));
      if (m.new_id) lines.push('          new_id: ' + JSON.stringify(m.new_id));
      if (m.display_name) lines.push('          display_name: ' + JSON.stringify(m.display_name));
      if (m.provider) lines.push('          provider: ' + JSON.stringify(m.provider));
      if (m.hidden) lines.push('          hidden: true');
    }
  }
  document.getElementById('yamlPreview').value = lines.join('\n');
}

async function copyYAML() {
  const el = document.getElementById('yamlPreview');
  el.select();
  await navigator.clipboard.writeText(el.value);
  showStatus('Copied YAML snippet to clipboard.', true);
}

async function refreshFromServer() {
  try {
    const resp = await fetch('?op=state', { cache: 'no-store' });
    if (!resp.ok) throw new Error('HTTP ' + resp.status);
    state = await resp.json();
    initUI();
    showStatus('Refreshed model catalog from server.', true);
  } catch (err) {
    showStatus('Failed to refresh state: ' + err.message, false);
  }
}

async function saveConfig() {
  const cfg = buildConfigObject();
  const key = document.getElementById('mgmtKey').value.trim();
  const headers = { 'Content-Type': 'application/json' };
  if (key) {
    headers['Authorization'] = 'Bearer ' + key;
    headers['X-Management-Key'] = key;
  }

  try {
    const runtimeResp = await fetch('?op=apply&config=' + encodeURIComponent(JSON.stringify(cfg)), { cache: 'no-store' });
    if (!runtimeResp.ok) throw new Error('Runtime apply failed: HTTP ' + runtimeResp.status);

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
      showStatus('Applied to active runtime! Enter your Management Key at the top right to also persist to config.yaml on disk.', true);
    }
  } catch (err) {
    showStatus('Save error: ' + err.message, false);
  }
}

function showStatus(msg, ok) {
  const bar = document.getElementById('statusBar');
  bar.className = 'status-bar ' + (ok ? 'ok' : 'err');
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
