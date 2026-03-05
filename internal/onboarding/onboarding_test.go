package onboarding

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

func TestLLMOnboardingPromptState(t *testing.T) {
	t.Run("missing llm prompts", func(t *testing.T) {
		dir := t.TempDir()
		cfgPath := filepath.Join(dir, "viaduct.yaml")
		if err := os.WriteFile(cfgPath, []byte("connectors: {}\n"), 0o600); err != nil {
			t.Fatalf("write config: %v", err)
		}
		needs, msg, err := llmOnboardingPromptState(cfgPath)
		if err != nil {
			t.Fatalf("llmOnboardingPromptState() error = %v", err)
		}
		if !needs {
			t.Fatalf("expected prompt for missing llm config")
		}
		if !strings.Contains(strings.ToLower(msg), "llm") {
			t.Fatalf("expected llm message, got %q", msg)
		}
	})

	t.Run("missing refresh token prompts", func(t *testing.T) {
		dir := t.TempDir()
		cfgPath := filepath.Join(dir, "viaduct.yaml")
		content := `llm:
  default_provider: openai
  providers:
    openai:
      auth_type: oauth
      base_url: "https://api.openai.com/v1"
      default_model: "gpt-5.3-codex"
      oauth:
        mode: authorization_code
        token_url: "https://auth.openai.com/oauth/token"
        client_id: "app-test"
`
		if err := os.WriteFile(cfgPath, []byte(content), 0o600); err != nil {
			t.Fatalf("write config: %v", err)
		}
		needs, msg, err := llmOnboardingPromptState(cfgPath)
		if err != nil {
			t.Fatalf("llmOnboardingPromptState() error = %v", err)
		}
		if !needs {
			t.Fatalf("expected prompt for missing oauth refresh_token")
		}
		if !strings.Contains(strings.ToLower(msg), "refresh_token") {
			t.Fatalf("expected refresh_token message, got %q", msg)
		}
	})

	t.Run("complete llm setup does not prompt", func(t *testing.T) {
		dir := t.TempDir()
		cfgPath := filepath.Join(dir, "viaduct.yaml")
		content := `llm:
  default_provider: openai
  providers:
    openai:
      auth_type: oauth
      base_url: "https://api.openai.com/v1"
      default_model: "gpt-5.3-codex"
      oauth:
        mode: authorization_code
        token_url: "https://auth.openai.com/oauth/token"
        client_id: "app-test"
        refresh_token: "rt_123"
  routing:
    default: openai/gpt-5.3-codex
    analysis: openai/gpt-5.3-codex
    generation: openai/gpt-5.3-codex
    classification: openai/gpt-5.3-codex
`
		if err := os.WriteFile(cfgPath, []byte(content), 0o600); err != nil {
			t.Fatalf("write config: %v", err)
		}
		needs, _, err := llmOnboardingPromptState(cfgPath)
		if err != nil {
			t.Fatalf("llmOnboardingPromptState() error = %v", err)
		}
		if needs {
			t.Fatalf("expected no prompt for complete llm setup")
		}
	})
}

func TestSlackSetupInstructions(t *testing.T) {
	out := SlackSetupInstructions()
	for _, want := range []string{
		"xoxb-",
		"OAuth & Permissions",
		"xapp-",
		"connections:write",
		"Socket Mode",
		"app_mention",
		"commands",
		"Request URL",
		"invite it to the default channel",
	} {
		if !strings.Contains(out, want) {
			t.Fatalf("expected setup instructions to mention %q, got:\n%s", want, out)
		}
	}
}
