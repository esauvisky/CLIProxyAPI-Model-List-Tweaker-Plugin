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
