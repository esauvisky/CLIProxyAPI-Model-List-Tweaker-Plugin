package main

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestTransformCatalogBody_ClaudeCloakedReorderRenameAndHide(t *testing.T) {
	cfgYAML := []byte(`
enabled: true
priority: 100
hide:
  - "gpt-4o-mini*"
models:
  - id: "gpt-5.4"
    new_id: "codex-5.4"
    display_name: "1. Codex 5.4"
  - id: "claude-opus-4-7"
    display_name: "2. Claude Opus 4.7"
  - id: "claude-haiku-4-5"
    hidden: true
`)
	cfg, errDecode := decodePluginConfigYAML(cfgYAML)
	if errDecode != nil {
		t.Fatalf("decodePluginConfigYAML: %v", errDecode)
	}

	input := `{
		"data": [
			{"id": "claude-haiku-4-5", "display_name": "Claude Haiku 4.5", "object": "model"},
			{"id": "claude-opus-4-7", "display_name": "Claude Opus 4.7", "object": "model"},
			{"id": "claude-fable-5-dd-inim-o4-tpg", "display_name": "GPT-4o Mini", "object": "model"},
			{"id": "claude-fable-5-dd-4.5-tpg", "display_name": "GPT 5.4", "object": "model"},
			{"id": "claude-sonnet-4-6", "display_name": "Claude Sonnet 4.6", "object": "model"}
		],
		"first_id": "claude-haiku-4-5",
		"has_more": false,
		"last_id": "claude-sonnet-4-6"
	}`

	out, changed := transformCatalogBody("claude", []byte(input), cfg)
	if !changed {
		t.Fatal("expected catalog to be transformed")
	}

	var parsed struct {
		Data []struct {
			ID          string `json:"id"`
			DisplayName string `json:"display_name"`
		} `json:"data"`
		FirstID string `json:"first_id"`
		LastID  string `json:"last_id"`
	}
	if err := json.Unmarshal(out, &parsed); err != nil {
		t.Fatalf("unmarshal output: %v", err)
	}

	if len(parsed.Data) != 3 {
		t.Fatalf("len(data) = %d, want 3; body = %s", len(parsed.Data), string(out))
	}

	// First model: gpt-5.4 renamed to codex-5.4, cloaked as claude-fable-5-dd-4.5-xedoc
	wantFirstID := ensureClaudeModelIDPrefix("codex-5.4")
	if parsed.Data[0].ID != wantFirstID || parsed.Data[0].DisplayName != "1. Codex 5.4" {
		t.Fatalf("data[0] = %+v, want id=%q display_name=%q", parsed.Data[0], wantFirstID, "1. Codex 5.4")
	}
	// Second model: claude-opus-4-7 (cloaked so Claude Code does not split it below Sonnet/Haiku)
	wantSecondID := ensureClaudeModelIDPrefix("claude-opus-4-7")
	if parsed.Data[1].ID != wantSecondID || parsed.Data[1].DisplayName != "2. Claude Opus 4.7" {
		t.Fatalf("data[1] = %+v, want id=%q", parsed.Data[1], wantSecondID)
	}
	// Third model: unlisted claude-sonnet-4-6 preserved at the end (also cloaked)
	wantThirdID := ensureClaudeModelIDPrefix("claude-sonnet-4-6")
	if parsed.Data[2].ID != wantThirdID {
		t.Fatalf("data[2] = %+v, want %q", parsed.Data[2], wantThirdID)
	}
	if parsed.FirstID != wantFirstID || parsed.LastID != wantThirdID {
		t.Fatalf("pagination IDs = (%q, %q), want (%q, %q)", parsed.FirstID, parsed.LastID, wantFirstID, wantThirdID)
	}
}

func TestTransformCatalogBody_CodexPriorityAndGeminiPrefix(t *testing.T) {
	cfg := pluginConfig{
		Enabled: true,
		Models: []modelRule{
			{ID: "gpt-5.4", NewID: "codex-prime", DisplayName: "Codex Prime"},
			{ID: "gemini-2.5-pro", NewID: "gemini-pro-custom", DisplayName: "Gemini Custom"},
		},
	}

	codexIn := `{"models":[{"slug":"gpt-5.3","display_name":"GPT 5.3","priority":0},{"slug":"gpt-5.4","display_name":"GPT 5.4","priority":1}]}`
	codexOut, changed := transformCatalogBody("openai", []byte(codexIn), cfg)
	if !changed {
		t.Fatal("expected codex catalog change")
	}
	var codexParsed struct {
		Models []struct {
			Slug        string `json:"slug"`
			DisplayName string `json:"display_name"`
			Priority    int    `json:"priority"`
		} `json:"models"`
	}
	if err := json.Unmarshal(codexOut, &codexParsed); err != nil {
		t.Fatalf("unmarshal codex: %v", err)
	}
	if codexParsed.Models[0].Slug != "codex-prime" || codexParsed.Models[0].Priority != 0 || codexParsed.Models[1].Priority != 1 {
		t.Fatalf("unexpected codex models: %+v", codexParsed.Models)
	}

	geminiIn := `{"models":[{"name":"models/gemini-2.5-flash","displayName":"Flash"},{"name":"models/gemini-2.5-pro","displayName":"Pro"}]}`
	geminiOut, changed := transformCatalogBody("gemini", []byte(geminiIn), cfg)
	if !changed {
		t.Fatal("expected gemini catalog change")
	}
	var geminiParsed struct {
		Models []struct {
			Name        string `json:"name"`
			DisplayName string `json:"displayName"`
		} `json:"models"`
	}
	if err := json.Unmarshal(geminiOut, &geminiParsed); err != nil {
		t.Fatalf("unmarshal gemini: %v", err)
	}
	if geminiParsed.Models[0].Name != "models/gemini-pro-custom" || geminiParsed.Models[0].DisplayName != "Gemini Custom" {
		t.Fatalf("unexpected gemini models: %+v", geminiParsed.Models)
	}
}

func TestRouteAndRewriteRenamedModelWithThinkingSuffix(t *testing.T) {
	prevResolver := hostModelsResolver
	hostModelsResolver = func(_ string) ([]hostAuthModelsEntry, error) {
		return []hostAuthModelsEntry{
			{ID: "auth-1", Provider: "codex", Models: []string{"gpt-5.4"}},
		}, nil
	}
	t.Cleanup(func() {
		hostModelsResolver = prevResolver
	})

	setPluginConfig(pluginConfig{
		Enabled: true,
		Models: []modelRule{
			{ID: "gpt-5.4", NewID: "codex-fast", DisplayName: "Codex Fast"},
		},
	})

	routeReq, _ := json.Marshal(modelRouteRequest{
		SourceFormat:       "claude",
		RequestedModel:     "codex-fast(xhigh)",
		AvailableProviders: []string{"claude", "codex"},
	})
	rawRouteResp, errRoute := handleModelRoute(routeReq)
	if errRoute != nil {
		t.Fatalf("handleModelRoute: %v", errRoute)
	}
	var routeEnv struct {
		OK     bool               `json:"ok"`
		Result modelRouteResponse `json:"result"`
	}
	if err := json.Unmarshal(rawRouteResp, &routeEnv); err != nil {
		t.Fatalf("decode route envelope: %v", err)
	}
	if !routeEnv.Result.Handled || routeEnv.Result.Target != "codex" || routeEnv.Result.TargetModel != "gpt-5.4(xhigh)" {
		t.Fatalf("unexpected route decision: %+v", routeEnv.Result)
	}

	interceptReq, _ := json.Marshal(requestInterceptRequest{
		SourceFormat:   "claude",
		Model:          "gpt-5.4(xhigh)",
		RequestedModel: "codex-fast(xhigh)",
		Body:           []byte(`{"model":"codex-fast(xhigh)","stream":true}`),
	})
	rawInterceptResp, errIntercept := handleRequestInterceptBefore(interceptReq)
	if errIntercept != nil {
		t.Fatalf("handleRequestInterceptBefore: %v", errIntercept)
	}
	var interceptEnv struct {
		OK     bool                     `json:"ok"`
		Result requestInterceptResponse `json:"result"`
	}
	if err := json.Unmarshal(rawInterceptResp, &interceptEnv); err != nil {
		t.Fatalf("decode intercept envelope: %v", err)
	}
	if !strings.Contains(string(interceptEnv.Result.Body), `"model":"gpt-5.4(xhigh)"`) {
		t.Fatalf("rewritten body = %s, want model=gpt-5.4(xhigh)", string(interceptEnv.Result.Body))
	}
}
