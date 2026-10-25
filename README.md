# CLIProxyAPI Model List Tweaker Plugin

This ABI v1 plugin lets you **reorder**, **rename** (`display_name`), and **hide** models in the global model catalog across Claude Code (`/model`), OpenAI (`/v1/models`), Codex (`/v1/models?client_version=...`), Gemini (`/v1beta/models`), and Grok clients — with a built-in drag-and-drop web editor in the CLIProxyAPI Management UI.

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
      models:
        - id: "gpt-5.4"
          display_name: "Codex 5.4"
        - id: "claude-opus-4-7"
          display_name: "Claude Opus 4.7"
        - id: "gemini-2.5-pro"
          display_name: "Gemini 2.5 Pro"
        - id: "claude-haiku-4-5"
          hidden: true
```

## Remaining quota in display names

With `quota.enabled`, display names get the remaining quota appended, e.g. `Claude Opus 4.7 (63%)`. Per credential the lowest bucket (5h, weekly, ...) counts; models served by several credentials show the average. Values come from the management API (`POST /v0/management/quota/fetch`), so a management key is required. A catalog request older than `refresh_minutes` since the last refresh triggers a background refresh and is served from the previous snapshot. Models without a quota provider get no suffix.

```yaml
    model-list-tweaker:
      quota:
        enabled: true
        management_url: "http://127.0.0.1:8317"
        management_key: "your-management-key"
        refresh_minutes: 5
```
