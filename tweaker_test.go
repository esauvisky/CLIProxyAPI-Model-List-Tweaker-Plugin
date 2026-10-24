package main

import (
	"encoding/json"
	"testing"
)

func TestTransformCatalogBody_ClaudeCloakedReorderRenameAndHide(t *testing.T) {
	cfgYAML := []byte(`
enabled: true
priority: 100
models:
  - id: "gpt-5.4"
    display_name: "1. Codex 5.4"
  - id: "claude-opus-4-7"
    display_name: "2. Claude Opus 4.7"
  - id: "claude-haiku-4-5"
    hidden: true
  - id: "gpt-4o-mini"
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

	wantFirstID := ensureClaudeModelIDPrefix("gpt-5.4")
	if parsed.Data[0].ID != wantFirstID || parsed.Data[0].DisplayName != "1. Codex 5.4" {
		t.Fatalf("data[0] = %+v, want id=%q display_name=%q", parsed.Data[0], wantFirstID, "1. Codex 5.4")
	}
	wantSecondID := ensureClaudeModelIDPrefix("claude-opus-4-7")
	if parsed.Data[1].ID != wantSecondID || parsed.Data[1].DisplayName != "2. Claude Opus 4.7" {
		t.Fatalf("data[1] = %+v, want id=%q", parsed.Data[1], wantSecondID)
	}
	wantThirdID := ensureClaudeModelIDPrefix("claude-sonnet-4-6")
	if parsed.Data[2].ID != wantThirdID {
		t.Fatalf("data[2] = %+v, want %q", parsed.Data[2], wantThirdID)
	}
	if parsed.FirstID != wantFirstID || parsed.LastID != wantThirdID {
		t.Fatalf("pagination IDs = (%q, %q), want (%q, %q)", parsed.FirstID, parsed.LastID, wantFirstID, wantThirdID)
	}
}

func TestTransformCatalogBody_FollowsDisabledCloaking(t *testing.T) {
	cfg := pluginConfig{
		Enabled: true,
		Models: []modelRule{
			{ID: "gpt-5.4", DisplayName: "GPT 5.4"},
			{ID: "claude-opus-4-7", DisplayName: "Opus 4.7"},
		},
	}

	input := `{
		"data": [
			{"id": "claude-opus-4-7", "display_name": "Claude Opus 4.7", "object": "model"},
			{"id": "gpt-5.4", "display_name": "GPT 5.4", "object": "model"}
		],
		"first_id": "claude-opus-4-7",
		"has_more": false,
		"last_id": "gpt-5.4"
	}`

	out, changed := transformCatalogBody("claude", []byte(input), cfg)
	if !changed {
		t.Fatal("expected catalog to be transformed")
	}

	var parsed struct {
		Data []struct {
			ID string `json:"id"`
		} `json:"data"`
	}
	if err := json.Unmarshal(out, &parsed); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if parsed.Data[0].ID != "gpt-5.4" || parsed.Data[1].ID != "claude-opus-4-7" {
		t.Fatalf("expected uncloaked IDs when global cloaking is disabled, got %+v", parsed.Data)
	}
}

func TestTransformCatalogBody_CodexPriorityAndGeminiDisplayName(t *testing.T) {
	cfg := pluginConfig{
		Enabled: true,
		Models: []modelRule{
			{ID: "gpt-5.4", DisplayName: "Codex Prime"},
			{ID: "gemini-2.5-pro", DisplayName: "Gemini Custom"},
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
	if codexParsed.Models[0].Slug != "gpt-5.4" || codexParsed.Models[0].DisplayName != "Codex Prime" || codexParsed.Models[0].Priority != 0 || codexParsed.Models[1].Priority != 1 {
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
	if geminiParsed.Models[0].Name != "models/gemini-2.5-pro" || geminiParsed.Models[0].DisplayName != "Gemini Custom" {
		t.Fatalf("unexpected gemini models: %+v", geminiParsed.Models)
	}
}
