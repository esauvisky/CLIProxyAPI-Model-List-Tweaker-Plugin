# CLIProxyAPI Model List Tweaker Plugin

This ABI v1 plugin lets you **reorder**, **rename** (both `id` and `display_name`), and **hide** models in the final global model catalog across Claude Code (`/model`), OpenAI (`/v1/models`), Codex (`/v1/models?client_version=...`), Gemini (`/v1beta/models`), and Grok clients — with a built-in web editor in the CLIProxyAPI Management UI.

## Features

- **Global Reordering**: Runs after CLIProxyAPI's built-in alphabetical / priority sorting (`response.intercept_after`), placing your pinned models in the exact order you specify (and re-indexing Codex `priority` fields automatically).
- **Rename Both `id` and `display_name`**:
  - Updates `display_name` (`displayName` on Gemini) and `id` (`slug` on Codex, `name` on Gemini).
  - Automatically preserves Claude Code's `claude-fable-5-dd-<reversed>` model list cloaking when active so custom non-`claude-` IDs still appear in Claude Code's `/model` menu.
  - Routes requests made to renamed model IDs back to the underlying model and provider (`model.route` + `request.intercept_before`), preserving thinking suffixes like `my-alias(high)`.
- **Hide Models Globally**: Hide individual models or wildcard patterns (`gpt-4o-mini*`, `*-preview`) in one place without configuring exclusions per provider/credential, or set `hide_unlisted: true` to expose only your curated list.
- **Web Interface Editor**:
  - **Visual Editor**: Open **Plugins > Model List Tweaker** (`/v0/resource/plugins/model-list-tweaker/ui`) for drag-and-drop reordering, inline ID/Display Name editing, live `/model` preview, and one-click save to `config.yaml`.
  - **Built-in Config Modal**: Exposes `models`, `hide`, and `hide_unlisted` via `ConfigFields` for direct editing in the CLIProxyAPI Plugins configuration dialog.

## Build

```bash
make test
```

Produces `build/model-list-tweaker.so`.

## Install from the management interface

Add this registry URL to `plugins.store-sources`:

```yaml
plugins:
  enabled: true
  store-sources:
    - "https://raw.githubusercontent.com/esauvisky/CLIProxyAPI-Model-List-Tweaker-Plugin/main/registry.json"
```

## Example `config.yaml`

```yaml
plugins:
  enabled: true
  dir: "/CLIProxyAPI/plugins"
  configs:
    model-list-tweaker:
      enabled: true
      priority: 100
      hide_unlisted: false
      hide:
        - "gpt-4o-mini*"
        - "*-legacy"
      models:
        - id: "gpt-5.4"
          new_id: "codex-5.4"
          display_name: "1. Codex 5.4"
        - id: "claude-opus-4-7"
          display_name: "2. Claude Opus 4.7"
        - id: "gemini-2.5-pro"
          new_id: "gemini-pro"
          display_name: "3. Gemini 2.5 Pro"
        - id: "claude-haiku-4-5"
          hidden: true
```
