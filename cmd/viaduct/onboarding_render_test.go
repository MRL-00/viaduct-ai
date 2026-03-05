package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestRenderConnectorSection(t *testing.T) {
	t.Run("disabled", func(t *testing.T) {
		out := renderConnectorSection(onboardingConnectorOptions{})
		if !strings.Contains(out, "connectors: {}") {
			t.Fatalf("expected empty connectors block, got:\n%s", out)
		}
	})

	t.Run("slack enabled with default channel", func(t *testing.T) {
		out := renderConnectorSection(onboardingConnectorOptions{
			EnableSlack:   true,
			SlackChannel:  "#alerts",
			SlackBotToken: "xoxb-abc",
			SlackAppToken: "xapp-xyz",
		})
		if !strings.Contains(out, "slack:") {
			t.Fatalf("expected slack connector block, got:\n%s", out)
		}
		if !strings.Contains(out, `default_channel: "#alerts"`) {
			t.Fatalf("expected default_channel entry, got:\n%s", out)
		}
		if !strings.Contains(out, `bot_token: "xoxb-abc"`) {
			t.Fatalf("expected bot_token entry, got:\n%s", out)
		}
		if !strings.Contains(out, `app_token: "xapp-xyz"`) {
			t.Fatalf("expected app_token entry, got:\n%s", out)
		}
	})
}

func TestRenderOpenAIConfigIncludesConnectorSection(t *testing.T) {
	out := renderOpenAIConfig(
		"0.0.0.0",
		"8080",
		"./viaduct.db",
		"Pacific/Auckland",
		"gpt-4o",
		"OPENAI_API_KEY",
		onboardingConnectorOptions{
			EnableSlack:   true,
			SlackChannel:  "#ops",
			SlackBotToken: "xoxb-123",
		},
	)

	if !strings.Contains(out, "connectors:") {
		t.Fatalf("expected connectors section, got:\n%s", out)
	}
	if !strings.Contains(out, `default_channel: "#ops"`) {
		t.Fatalf("expected default channel to be rendered, got:\n%s", out)
	}
}

func TestUpsertSlackConnectorConfig(t *testing.T) {
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "viaduct.yaml")
	initial := `server:
  host: 0.0.0.0
  port: 8080
connectors: {}
`
	if err := os.WriteFile(cfgPath, []byte(initial), 0o600); err != nil {
		t.Fatalf("write config: %v", err)
	}
	if err := upsertSlackConnectorConfig(cfgPath, "#alerts", "xoxb-abc", "xapp-xyz"); err != nil {
		t.Fatalf("upsertSlackConnectorConfig() error = %v", err)
	}

	out, err := os.ReadFile(cfgPath)
	if err != nil {
		t.Fatalf("read config: %v", err)
	}
	rendered := string(out)
	if !strings.Contains(rendered, "slack:") {
		t.Fatalf("expected slack section, got:\n%s", rendered)
	}
	if !strings.Contains(rendered, `default_channel: '#alerts'`) &&
		!strings.Contains(rendered, `default_channel: "#alerts"`) {
		t.Fatalf("expected default channel in rendered config, got:\n%s", rendered)
	}
	if !strings.Contains(rendered, "permissions:") {
		t.Fatalf("expected permissions in rendered config, got:\n%s", rendered)
	}
	if !strings.Contains(rendered, "bot_token: xoxb-abc") {
		t.Fatalf("expected bot_token in rendered config, got:\n%s", rendered)
	}
	if !strings.Contains(rendered, "app_token: xapp-xyz") {
		t.Fatalf("expected app_token in rendered config, got:\n%s", rendered)
	}
}

func TestConnectorConfigReadySlack(t *testing.T) {
	if ok, _ := connectorConfigReady("slack", map[string]any{}); ok {
		t.Fatalf("expected slack connector to be not ready without bot_token")
	}
	if ok, _ := connectorConfigReady("slack", map[string]any{"bot_token": "xoxb-123"}); !ok {
		t.Fatalf("expected slack connector to be ready with bot_token")
	}
}

func TestSlackOnboardingPromptState(t *testing.T) {
	t.Run("missing slack connector prompts", func(t *testing.T) {
		dir := t.TempDir()
		cfgPath := filepath.Join(dir, "viaduct.yaml")
		if err := os.WriteFile(cfgPath, []byte("connectors: {}\n"), 0o600); err != nil {
			t.Fatalf("write config: %v", err)
		}
		needs, msg, err := slackOnboardingPromptState(cfgPath)
		if err != nil {
			t.Fatalf("slackOnboardingPromptState() error = %v", err)
		}
		if !needs {
			t.Fatalf("expected prompt for missing slack connector")
		}
		if !strings.Contains(strings.ToLower(msg), "missing") {
			t.Fatalf("expected missing message, got %q", msg)
		}
	})

	t.Run("missing bot token prompts", func(t *testing.T) {
		dir := t.TempDir()
		cfgPath := filepath.Join(dir, "viaduct.yaml")
		content := `connectors:
  slack:
    default_channel: "#ops"
`
		if err := os.WriteFile(cfgPath, []byte(content), 0o600); err != nil {
			t.Fatalf("write config: %v", err)
		}
		needs, msg, err := slackOnboardingPromptState(cfgPath)
		if err != nil {
			t.Fatalf("slackOnboardingPromptState() error = %v", err)
		}
		if !needs {
			t.Fatalf("expected prompt for missing bot token")
		}
		if !strings.Contains(strings.ToLower(msg), "bot token") {
			t.Fatalf("expected bot token message, got %q", msg)
		}
	})

	t.Run("complete setup does not prompt", func(t *testing.T) {
		dir := t.TempDir()
		cfgPath := filepath.Join(dir, "viaduct.yaml")
		content := `connectors:
  slack:
    default_channel: "#ops"
    bot_token: "xoxb-123"
`
		if err := os.WriteFile(cfgPath, []byte(content), 0o600); err != nil {
			t.Fatalf("write config: %v", err)
		}
		needs, _, err := slackOnboardingPromptState(cfgPath)
		if err != nil {
			t.Fatalf("slackOnboardingPromptState() error = %v", err)
		}
		if needs {
			t.Fatalf("expected no prompt for complete setup")
		}
	})
}
