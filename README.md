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
- Inbound chat bridge for messenger connectors (Slack app mentions, direct messages, and slash commands)
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

If `viaduct.yaml` is missing, Viaduct automatically starts first-run onboarding and creates it.
Onboarding uses interactive selector menus (arrow keys/enter when terminal supports it) and defaults to OAuth.
Default onboarding is a quick enterprise OAuth flow with minimal prompts.
Onboarding now includes optional Slack connector setup, including default channel.
If config already exists but Slack setup is missing or incomplete (for example missing bot token), `viaduct serve` prompts to complete Slack onboarding.
Slack setup prompts for default channel plus optional bot/app tokens.

4. Health check:

```bash
curl -s http://127.0.0.1:8080/health
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

# Setup/onboarding
viaduct setup init --config ./viaduct.yaml
viaduct setup init --advanced --config ./viaduct.yaml
viaduct setup slack --config ./viaduct.yaml
```

## Slack App Setup

When onboarding asks for Slack tokens:

- Bot token: in your Slack app dashboard under `OAuth & Permissions` -> `Bot User OAuth Token`. This value starts with `xoxb-`.
- App token: in `Settings` -> `Basic Information` -> `App-Level Tokens`. Create one with the `connections:write` scope. This value starts with `xapp-`.

Recommended Slack app configuration for Viaduct:

- Under `OAuth & Permissions`, add bot scopes `chat:write`, `channels:read`, and `channels:history`.
- If you want inbound chat when someone mentions the app, also add `app_mentions:read`.
- If you want users to DM the app from App Home, enable `Messages Tab` in `App Home`, keep `im:history`, and subscribe to `message.im`.
- If you want follow-up replies in public-channel threads after the first mention, subscribe to `message.channels`.
- If you want follow-up replies in private-channel threads, add `groups:history` and subscribe to `message.groups`.
- If you want slash commands, add the `commands` scope and create a command under `Slash Commands`.
- Under `Settings` -> `Socket Mode`, enable Socket Mode if you want inbound chat. Viaduct uses Socket Mode, so you do not need a Request URL for events or slash commands.
- Under `Event Subscriptions`, enable events and subscribe to `app_mention`, plus `message.channels` and/or `message.groups` if you want thread follow-ups.
- Reinstall the app to the workspace after changing scopes, then invite it to the channel you configure as `default_channel` (for example `/invite @your-app`).

If you only want Viaduct to post messages to Slack, the bot token is enough. The app token is only needed for inbound chat features such as app mentions and slash commands.

## Model Setup

Viaduct currently supports:
- `anthropic` (API key)
- `openai` (API key or OpenAI OAuth authorization-code flow)
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
- `custom` OAuth flow is client credentials (`grant_type=client_credentials`).
- OpenAI quick onboarding now follows OpenClaw-style PKCE OAuth:
  - callback listener on `http://127.0.0.1:1455/auth/callback`
  - browser URL uses OpenAI Codex OAuth parameters (`code_challenge`, `state`, `originator`, etc.)
  - automatic browser open + code exchange at `https://auth.openai.com/oauth/token`
- Quick onboarding auto-selects a recommended OpenAI/Codex default model (no model prompt). Use `viaduct setup init --advanced` to choose manually.
- During onboarding, Anthropic setup can run `claude setup-token` if the `claude` CLI is installed.

## Model CLI

```bash
# List configured model providers
viaduct models list --config ./viaduct.yaml

# Send a small test prompt to one provider
viaduct models test custom --config ./viaduct.yaml
```
