# Viaduct

Enterprise-ready, self-hosted AI operations agent in Go.

## Current Phase

This repository implements a Phase 1 foundation:
- Config + env loading (`viper`)
- Embedded SQLite storage + migrations (`modernc.org/sqlite`)
- Connector contracts + registry
- Connectors: Microsoft 365 (read), Azure (read), Slack (read/write/listen)
- LLM router + Anthropic/OpenAI providers
- Agent orchestration loop with tool calls
- CRON scheduler (`robfig/cron/v3`)
- Audit + LLM usage logging
- Cobra CLI

## Prerequisites

- Go 1.22+
- A config file (`viaduct.yaml`)

## Quick Start

1. Copy the example config:

```bash
cp configs/viaduct.example.yaml viaduct.yaml
```

2. Set required environment variables (minimum depends on providers/connectors you enable):

```bash
export ANTHROPIC_API_KEY=...
export OPENAI_API_KEY=...
export AZURE_TENANT_ID=...
export AZURE_CLIENT_ID=...
export AZURE_CLIENT_SECRET=...
export AZURE_SUBSCRIPTION_ID=...
export SLACK_BOT_TOKEN=...
export SLACK_APP_TOKEN=...
```

3. Run locally:

```bash
make run
```

Or directly:

```bash
go run ./cmd/viaduct --config ./viaduct.yaml
```

4. Health check:

```bash
curl -s http://127.0.0.1:8080/healthz
```

## Local Testing

```bash
make test
make lint
make build
```

CLI examples:

```bash
# Jobs
viaduct jobs list --config ./viaduct.yaml
viaduct jobs run morning-briefing --config ./viaduct.yaml
viaduct jobs history morning-briefing --last 10 --config ./viaduct.yaml

# Ad-hoc task
viaduct task run "Summarise active Azure alerts and draft a Slack update" --config ./viaduct.yaml
```

## Model Setup

Viaduct currently supports:
- `anthropic` (API key)
- `openai` (API key)
- `custom` (OpenAI-compatible endpoint with OAuth client-credentials only)

### OAuth-only custom model provider

For your own models, configure provider `custom` with OAuth settings:

```yaml
llm:
  default_provider: custom
  providers:
    custom:
      auth_type: oauth
      base_url: https://my-model-gateway.example.com/v1
      default_model: my-org-model
      oauth:
        token_url: https://login.example.com/oauth2/token
        client_id: ${MODEL_OAUTH_CLIENT_ID}
        client_secret: ${MODEL_OAUTH_CLIENT_SECRET}
        scopes:
          - ai.inference
```

Notes:
- `custom` provider rejects API key auth by design.
- OAuth flow is client credentials (`grant_type=client_credentials`).

## Model CLI

```bash
# List configured model providers
viaduct models list --config ./viaduct.yaml

# Send a small test prompt to one provider
viaduct models test custom --config ./viaduct.yaml
```
