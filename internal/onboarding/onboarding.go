package onboarding

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"time"

	"go.yaml.in/yaml/v3"
)

func EnsureConfigExistsForServe(cfgPath string) error {
	_, err := os.Stat(cfgPath)
	if err == nil {
		return maybeRunMissingOnboardingSteps(cfgPath)
	}
	if !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("check config: %w", err)
	}

	fmt.Printf("Config %q not found. Starting first-run setup...\n", cfgPath)
	if err := RunSetupInit(cfgPath, false, false); err != nil {
		return err
	}
	fmt.Printf("Created %s. Continuing startup.\n", cfgPath)
	return nil
}

func RunSetupSlack(cfgPath string) error {
	if _, err := os.Stat(cfgPath); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return fmt.Errorf("config not found at %s; run `viaduct setup init --config %s` first", cfgPath, cfgPath)
		}
		return fmt.Errorf("check config: %w", err)
	}
	reader := bufio.NewReader(os.Stdin)
	return runSetupSlackWithReader(cfgPath, reader)
}

func RunSetupInit(cfgPath string, force bool, advanced bool) error {
	if _, err := os.Stat(cfgPath); err == nil && !force {
		return fmt.Errorf("config already exists at %s (use --force to overwrite)", cfgPath)
	} else if err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("check existing config: %w", err)
	}

	reader := bufio.NewReader(os.Stdin)

	if !advanced {
		rendered, err := runSetupQuickOAuth(reader)
		if err != nil {
			return err
		}
		if dir := filepath.Dir(cfgPath); dir != "." && dir != "" {
			if err := os.MkdirAll(dir, 0o755); err != nil {
				return fmt.Errorf("create config directory: %w", err)
			}
		}
		if err := os.WriteFile(cfgPath, []byte(rendered), 0o600); err != nil {
			return fmt.Errorf("write config file: %w", err)
		}
		fmt.Printf("Config written to %s\n", cfgPath)
		return nil
	}

	host := promptWithDefault(reader, "Server host", "0.0.0.0")
	port := promptWithDefault(reader, "Server port", "8080")
	storagePath := promptWithDefault(reader, "SQLite file path", "./viaduct.db")
	timezone := promptWithDefault(reader, "Scheduler timezone", "Pacific/Auckland")
	connectorOptions := promptOnboardingConnectorOptions(reader)

	authMode := chooseMenuOption(
		reader,
		"Authentication mode",
		[]menuOption{
			{Key: "oauth", Label: "OAuth 2.0 (client credentials)", Description: "No API keys in config"},
			{Key: "api_key", Label: "API Key", Description: "Direct vendor APIs"},
		},
		"oauth",
	)

	var rendered string
	if authMode == "oauth" {
		provider := chooseMenuOption(
			reader,
			"Provider",
			[]menuOption{
				{
					Key: "openai", Label: "OpenAI-compatible gateway",
					Description: "Use provider name openai with OAuth",
				},
				{
					Key: "custom", Label: "Custom OpenAI-compatible gateway",
					Description: "Use provider name custom with OAuth",
				},
			},
			"custom",
		)

		model := defaultOpenAIModel()
		baseURL := promptWithDefault(reader, "Gateway base URL", "https://llm-gateway.example.com/v1")
		tokenURL := promptWithDefault(reader, "OAuth token URL",
			"https://auth.example.com/oauth2/token")
		clientIDEnv := promptWithDefault(reader, "OAuth client ID env var", "MODEL_OAUTH_CLIENT_ID")
		clientSecretEnv := promptWithDefault(reader, "OAuth client secret env var", "MODEL_OAUTH_CLIENT_SECRET")
		scopesRaw := promptWithDefault(reader, "OAuth scopes (comma separated)", "ai.inference")
		scopes := splitCSV(scopesRaw)

		if provider == "openai" && confirmWithDefault(reader,
			"Generate OAuth login URL now?", true) {
			authEndpoint := promptWithDefault(reader, "OAuth authorization endpoint",
				"https://auth.openai.com/oauth/authorize")
			clientIDForLogin := valueForDiscovery(reader, clientIDEnv, "OAuth client ID")
			redirectURI := promptWithDefault(reader, "OAuth redirect_uri", "http://127.0.0.1:8787/callback")
			url := buildOAuthAuthorizationURL(authEndpoint, clientIDForLogin, redirectURI, scopes, "viaduct-setup")
			if url != "" {
				fmt.Printf("Open this URL to login:\n%s\n", url)
				if confirmWithDefault(reader, "Open URL in browser now?", true) {
					if err := openURLInBrowser(url); err != nil {
						fmt.Printf("Could not open browser: %v\n", err)
					}
				}
			}
		}

		clientID := valueForDiscovery(reader, clientIDEnv, "OAuth client ID")
		clientSecret := valueForDiscovery(reader, clientSecretEnv, "OAuth client secret")
		if clientID != "" && clientSecret != "" {
			ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
			models, err := discoverOpenAIModelsOAuth(ctx, baseURL, tokenURL, clientID, clientSecret, scopes)
			cancel()
			if err != nil {
				fmt.Printf("Could not discover models from OAuth endpoint: %v\n", err)
			} else {
				model = chooseModel(reader, "Default model", model, models)
			}
		} else {
			fmt.Println("Skipping model discovery (OAuth credentials not provided for discovery).")
			model = promptWithDefault(reader, "Default model", model)
		}

		if provider == "openai" {
			rendered = renderOpenAIOAuthConfig(host, port, storagePath, timezone, model,
				baseURL, tokenURL, clientIDEnv, clientSecretEnv, scopes, connectorOptions)
		} else {
			rendered = renderCustomOAuthConfig(host, port, storagePath, timezone, model,
				baseURL, tokenURL, clientIDEnv, clientSecretEnv, scopes, connectorOptions)
		}
	} else {
		provider := chooseMenuOption(
			reader,
			"Provider",
			[]menuOption{
				{Key: "openai", Label: "OpenAI (API key)", Description: "Direct OpenAI API"},
				{Key: "anthropic", Label: "Anthropic (API key)", Description: "Direct Anthropic API"},
			},
			"openai",
		)

		switch provider {
		case "anthropic":
			if confirmWithDefault(reader, "Run `claude setup-token` now?", true) {
				if err := runClaudeSetupToken(); err != nil {
					fmt.Printf("Could not run claude setup-token: %v\n", err)
				}
			}
			apiEnv := promptWithDefault(reader, "Anthropic API key env var", "ANTHROPIC_API_KEY")
			discoveryKey := apiKeyForDiscovery(reader, apiEnv)
			model := "claude-sonnet-4-5-20250514"
			models, err := discoverAnthropicModels(context.Background(), discoveryKey)
			if err != nil {
				fmt.Printf("Could not discover Anthropic models from API: %v\n", err)
			}
			model = chooseModel(reader, "Anthropic default model", model, models)
			rendered = renderAnthropicConfig(host, port, storagePath, timezone, model, apiEnv, connectorOptions)
		default:
			apiEnv := promptWithDefault(reader, "OpenAI API key env var", "OPENAI_API_KEY")
			discoveryKey := apiKeyForDiscovery(reader, apiEnv)
			model := defaultOpenAIModel()
			if confirmWithDefault(reader, "Generate OAuth login URL for OpenAI now?", true) {
				authEndpoint := promptWithDefault(reader,
					"OAuth authorization endpoint", "https://auth.openai.com/oauth/authorize")
				clientID := promptWithDefault(reader, "OAuth client_id", "")
				redirectURI := promptWithDefault(reader, "OAuth redirect_uri", "http://127.0.0.1:8787/callback")
				scopes := splitCSV(promptWithDefault(reader,
					"OAuth scopes (comma separated)", "openid profile email"))
				url := buildOAuthAuthorizationURL(authEndpoint, clientID, redirectURI, scopes, "viaduct-setup")
				fmt.Printf("Open this URL to login:\n%s\n", url)
				if confirmWithDefault(reader, "Open URL in browser now?", true) {
					if err := openURLInBrowser(url); err != nil {
						fmt.Printf("Could not open browser: %v\n", err)
					}
				}
			}
			models, err := discoverOpenAIModels(context.Background(), discoveryKey)
			if err != nil {
				fmt.Printf("Could not discover OpenAI models from API: %v\n", err)
			}
			model = chooseModel(reader, "OpenAI default model", model, models)
			rendered = renderOpenAIConfig(host, port, storagePath, timezone, model, apiEnv, connectorOptions)
		}
	}

	if dir := filepath.Dir(cfgPath); dir != "." && dir != "" {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return fmt.Errorf("create config directory: %w", err)
		}
	}
	if err := os.WriteFile(cfgPath, []byte(rendered), 0o600); err != nil {
		return fmt.Errorf("write config file: %w", err)
	}

	fmt.Printf("Config written to %s\n", cfgPath)
	return nil
}

func maybeRunMissingOnboardingSteps(cfgPath string) error {
	if !isInteractiveFile(os.Stdin) || !isInteractiveFile(os.Stdout) {
		return nil
	}
	reader := bufio.NewReader(os.Stdin)

	needsPrompt, message, err := llmOnboardingPromptState(cfgPath)
	if err != nil {
		return err
	}
	if needsPrompt {
		fmt.Println(message)
		if confirmWithDefault(reader, "Configure LLM onboarding now?", true) {
			if err := runSetupLLMWithReader(cfgPath, reader); err != nil {
				return err
			}
		}
	}

	needsPrompt, message, err = slackOnboardingPromptState(cfgPath)
	if err != nil {
		return err
	}
	if !needsPrompt {
		return nil
	}
	fmt.Println(message)
	if !confirmWithDefault(reader, "Configure Slack onboarding now?", true) {
		return nil
	}
	return runSetupSlackWithReader(cfgPath, reader)
}

func llmOnboardingPromptState(cfgPath string) (bool, string, error) {
	root, err := readConfigYAML(cfgPath)
	if err != nil {
		return false, "", err
	}
	llmRoot := asStringAnyMap(root["llm"])
	if llmRoot == nil {
		return true, "LLM setup step is missing from your existing config.", nil
	}
	defaultProvider := scalarString(llmRoot["default_provider"])
	if defaultProvider == "" {
		return true, "LLM setup is incomplete (default_provider is missing).", nil
	}
	providers := asStringAnyMap(llmRoot["providers"])
	if providers == nil {
		return true, "LLM setup is incomplete (providers block is missing).", nil
	}
	providerCfg := asStringAnyMap(providers[defaultProvider])
	if providerCfg == nil {
		return true, fmt.Sprintf("LLM setup is incomplete (provider %q is missing).", defaultProvider), nil
	}
	if scalarString(providerCfg["default_model"]) == "" {
		return true, fmt.Sprintf("LLM setup is incomplete (%s.default_model is missing).", defaultProvider), nil
	}

	authType := strings.ToLower(scalarString(providerCfg["auth_type"]))
	if authType == "" {
		authType = "api_key"
		if defaultProvider == "openai" && asStringAnyMap(providerCfg["oauth"]) != nil {
			authType = "oauth"
		}
	}

	switch authType {
	case "oauth":
		oauthCfg := asStringAnyMap(providerCfg["oauth"])
		if oauthCfg == nil {
			return true, fmt.Sprintf("LLM setup is incomplete (%s.oauth block is missing).", defaultProvider), nil
		}
		mode := strings.ToLower(scalarString(oauthCfg["mode"]))
		if mode == "" {
			if scalarString(oauthCfg["refresh_token"]) != "" || scalarString(oauthCfg["access_token"]) != "" {
				mode = "authorization_code"
			} else {
				mode = "client_credentials"
			}
		}
		if scalarString(oauthCfg["token_url"]) == "" {
			return true, fmt.Sprintf("LLM setup is incomplete (%s.oauth.token_url is missing).", defaultProvider), nil
		}
		if scalarString(oauthCfg["client_id"]) == "" {
			return true, fmt.Sprintf("LLM setup is incomplete (%s.oauth.client_id is missing).", defaultProvider), nil
		}
		if mode == "authorization_code" && scalarString(oauthCfg["refresh_token"]) == "" {
			return true, fmt.Sprintf("LLM setup is incomplete (%s.oauth.refresh_token is missing).", defaultProvider), nil
		}
		if mode == "client_credentials" && scalarString(oauthCfg["client_secret"]) == "" {
			return true, fmt.Sprintf("LLM setup is incomplete (%s.oauth.client_secret is missing).", defaultProvider), nil
		}
	default:
		if scalarString(providerCfg["api_key"]) == "" {
			return true, fmt.Sprintf("LLM setup is incomplete (%s.api_key is missing).", defaultProvider), nil
		}
	}

	return false, "", nil
}

func slackOnboardingPromptState(cfgPath string) (bool, string, error) {
	root, err := readConfigYAML(cfgPath)
	if err != nil {
		return false, "", err
	}
	connectors := asStringAnyMap(root["connectors"])
	if connectors == nil {
		return true, "Slack setup step is missing from your existing config.", nil
	}
	slackCfg, ok := connectors["slack"]
	if !ok {
		return true, "Slack setup step is missing from your existing config.", nil
	}
	slackMap := asStringAnyMap(slackCfg)
	if slackMap == nil {
		return true, "Slack setup is incomplete in your existing config.", nil
	}
	if scalarString(slackMap["default_channel"]) == "" {
		return true, "Slack setup is incomplete (default_channel is missing).", nil
	}
	botToken, _ := slackMap["bot_token"].(string)
	if strings.TrimSpace(botToken) == "" {
		return true, "Slack setup is incomplete (bot token is missing).", nil
	}
	return false, "", nil
}

func runSetupLLMWithReader(cfgPath string, reader *bufio.Reader) error {
	root, err := readConfigYAML(cfgPath)
	if err != nil {
		return err
	}

	llmRoot := asStringAnyMap(root["llm"])
	defaultProvider := ""
	if llmRoot != nil {
		defaultProvider = strings.ToLower(strings.TrimSpace(fmt.Sprint(llmRoot["default_provider"])))
	}
	if defaultProvider != "" && defaultProvider != "openai" {
		return fmt.Errorf("automatic LLM onboarding repair currently supports the OpenAI OAuth setup path; current default_provider is %q", defaultProvider)
	}

	baseURL := strings.TrimSpace(os.Getenv("VIADUCT_OPENAI_CODEX_BASE_URL"))
	if baseURL == "" {
		baseURL = "https://chatgpt.com/backend-api"
	}
	model := defaultOpenAIModel()
	if llmRoot != nil {
		if providers := asStringAnyMap(llmRoot["providers"]); providers != nil {
			if openaiCfg := asStringAnyMap(providers["openai"]); openaiCfg != nil {
				if current, ok := openaiCfg["base_url"].(string); ok && strings.TrimSpace(current) != "" {
					baseURL = current
				}
				if current, ok := openaiCfg["default_model"].(string); ok && strings.TrimSpace(current) != "" {
					model = current
				}
			}
		}
	}

	fmt.Printf("Using gateway base URL: %s\n", baseURL)
	creds, err := runOpenAICodexOAuthLogin(reader)
	if err != nil {
		return fmt.Errorf("openai oauth login failed: %w", err)
	}

	root["llm"] = buildOpenAICodexOAuthLLMConfigMap(baseURL, model, creds)
	if err := writeConfigYAML(cfgPath, root); err != nil {
		return err
	}
	fmt.Printf("Updated LLM onboarding in %s\n", cfgPath)
	return nil
}

func runSetupSlackWithReader(cfgPath string, reader *bufio.Reader) error {
	defaultChannel := "#ops"
	root, err := readConfigYAML(cfgPath)
	if err == nil {
		connectors := asStringAnyMap(root["connectors"])
		if connectors != nil {
			if slackCfg := asStringAnyMap(connectors["slack"]); slackCfg != nil {
				if current, ok := slackCfg["default_channel"].(string); ok && strings.TrimSpace(current) != "" {
					defaultChannel = current
				}
			}
		}
	}
	printSlackSetupInstructions()
	defaultChannel = promptWithDefault(reader, "Slack default channel", defaultChannel)
	botToken := promptOptional(reader, "Slack bot token (optional)")
	appToken := promptOptional(reader, "Slack app token (optional; needed for inbound chat)")
	if err := upsertSlackConnectorConfig(cfgPath, defaultChannel, botToken, appToken); err != nil {
		return err
	}
	fmt.Printf("Updated Slack onboarding in %s\n", cfgPath)
	if strings.TrimSpace(botToken) == "" {
		fmt.Println("Slack bot token not set. Slack connector will stay disabled until token is configured.")
	}
	return nil
}

func runSetupQuickOAuth(reader *bufio.Reader) (string, error) {
	profile := chooseMenuOption(
		reader,
		"Quick setup profile",
		[]menuOption{
			{Key: "openai_oauth", Label: "OpenAI-compatible OAuth", Description: "Recommended enterprise path"},
			{Key: "anthropic", Label: "Anthropic + Claude Code", Description: "Runs claude setup-token"},
		},
		"openai_oauth",
	)

	const (
		host        = "0.0.0.0"
		port        = "8080"
		storagePath = "./viaduct.db"
		timezone    = "Pacific/Auckland"
	)
	connectorOptions := promptOnboardingConnectorOptions(reader)

	switch profile {
	case "anthropic":
		fmt.Println("Running Anthropic token setup...")
		if err := runClaudeSetupToken(); err != nil {
			fmt.Printf("Could not run claude setup-token: %v\n", err)
		}
		return renderAnthropicConfig(host, port, storagePath, timezone, "claude-sonnet-4-5-20250514", "ANTHROPIC_API_KEY", connectorOptions), nil
	default:
		baseURL := strings.TrimSpace(os.Getenv("VIADUCT_OPENAI_CODEX_BASE_URL"))
		if baseURL == "" {
			baseURL = "https://chatgpt.com/backend-api"
		}
		fmt.Printf("Using gateway base URL: %s\n", baseURL)
		creds, err := runOpenAICodexOAuthLogin(reader)
		if err != nil {
			return "", fmt.Errorf("openai oauth login failed: %w", err)
		}
		modelOptions := defaultOpenAIModels()
		if creds != nil && strings.TrimSpace(creds.AccessToken) != "" {
			discoveryCtx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
			discovered, discoverErr := discoverOpenAIModels(discoveryCtx, creds.AccessToken)
			cancel()
			if discoverErr != nil {
				if strings.Contains(strings.ToLower(discoverErr.Error()), "403") {
					fmt.Println("Model discovery skipped: OpenAI account does not have model-list scope (api.model.read).")
				} else {
					fmt.Printf("Could not discover models from OpenAI: %v\n", discoverErr)
				}
			} else {
				modelOptions = uniqueSorted(append(modelOptions, discovered...))
			}
		}
		model := pickPreferredOpenAIModel(modelOptions, defaultOpenAIModel())
		fmt.Printf("Using default model: %s\n", model)
		return renderOpenAICodexOAuthConfig(host, port, storagePath, timezone, model, baseURL, creds, connectorOptions), nil
	}
}

func promptWithDefault(reader *bufio.Reader, label, defaultValue string) string {
	fmt.Printf("%s [%s]: ", label, defaultValue)
	line, err := reader.ReadString('\n')
	if err != nil {
		return defaultValue
	}
	line = strings.TrimSpace(line)
	if line == "" {
		return defaultValue
	}
	return line
}

func promptOptional(reader *bufio.Reader, label string) string {
	fmt.Printf("%s: ", label)
	line, err := reader.ReadString('\n')
	if err != nil {
		return ""
	}
	return strings.TrimSpace(line)
}

func confirmWithDefault(reader *bufio.Reader, label string, defaultYes bool) bool {
	defaultValue := "n"
	if defaultYes {
		defaultValue = "y"
	}
	answer := strings.ToLower(strings.TrimSpace(promptWithDefault(reader, label+" [y/n]", defaultValue)))
	if answer == "" {
		return defaultYes
	}
	return answer == "y" || answer == "yes"
}

func splitCSV(raw string) []string {
	parts := strings.Split(raw, ",")
	out := make([]string, 0, len(parts))
	for _, part := range parts {
		trimmed := strings.TrimSpace(part)
		if trimmed != "" {
			out = append(out, trimmed)
		}
	}
	if len(out) == 0 {
		out = []string{"ai.inference"}
	}
	return out
}

type menuOption struct {
	Key         string
	Label       string
	Description string
}

type onboardingConnectorOptions struct {
	EnableSlack   bool
	SlackChannel  string
	SlackBotToken string
	SlackAppToken string
}

func chooseMenuOption(reader *bufio.Reader, title string, options []menuOption, defaultKey string) string {
	defaultIndex := 0
	for i, option := range options {
		if option.Key == defaultKey {
			defaultIndex = i
			break
		}
	}

	if idx, ok := promptSelect(title, options, defaultIndex); ok {
		return options[idx].Key
	}

	fmt.Printf("%s:\n", title)
	for i, option := range options {
		marker := " "
		if i == defaultIndex {
			marker = "*"
		}
		if option.Description != "" {
			fmt.Printf("  %d)%s %s - %s\n", i+1, marker, option.Label, option.Description)
		} else {
			fmt.Printf("  %d)%s %s\n", i+1, marker, option.Label)
		}
	}

	input := promptWithDefault(reader, "Choose option number or key", strconv.Itoa(defaultIndex+1))
	if n, err := strconv.Atoi(input); err == nil {
		if n >= 1 && n <= len(options) {
			return options[n-1].Key
		}
	}
	for _, option := range options {
		if strings.EqualFold(option.Key, input) {
			return option.Key
		}
	}
	return options[defaultIndex].Key
}

func promptOnboardingConnectorOptions(reader *bufio.Reader) onboardingConnectorOptions {
	options := onboardingConnectorOptions{
		EnableSlack:   false,
		SlackChannel:  "#ops",
		SlackBotToken: "",
		SlackAppToken: "",
	}
	if !confirmWithDefault(reader, "Configure Slack connector for chat?", true) {
		return options
	}
	options.EnableSlack = true
	printSlackSetupInstructions()
	options.SlackChannel = promptWithDefault(reader, "Slack default channel", "#ops")
	options.SlackBotToken = promptOptional(reader, "Slack bot token (optional)")
	options.SlackAppToken = promptOptional(reader, "Slack app token (optional; needed for inbound chat)")
	return options
}

func SlackSetupInstructions() string {
	return strings.Join([]string{
		"Slack app setup:",
		"  - Bot token (`xoxb-...`): Slack app dashboard -> OAuth & Permissions -> Bot User OAuth Token.",
		"  - Bot scopes for Viaduct: `chat:write`, `channels:read`, `channels:history`.",
		"  - For inbound chat via mentions, also add `app_mentions:read`.",
		"  - For direct messages in App Home, enable the Messages tab under App Home, turn on `Allow users to send Slash commands and messages from the messages tab`, and subscribe to `message.im`.",
		"  - For follow-up replies in public-channel threads, subscribe to `message.channels`.",
		"  - For follow-up replies in private-channel threads, add `groups:history` and subscribe to `message.groups`.",
		"  - For slash commands, add `commands` and create a command under Slash Commands.",
		"  - App token (`xapp-...`, optional unless you want inbound chat): Settings -> Basic Information -> App-Level Tokens. Generate one with `connections:write`.",
		"  - Turn on Settings -> Socket Mode for inbound chat.",
		"  - Turn on Event Subscriptions and subscribe to the `app_mention` bot event, plus `message.channels`/`message.groups` if you want thread follow-ups.",
		"  - Socket Mode does not need a Request URL.",
		"  - After changing scopes, reinstall the app to the workspace and invite it to the default channel.",
	}, "\n")
}

func printSlackSetupInstructions() {
	fmt.Printf("\n%s\n\n", SlackSetupInstructions())
}

func readConfigYAML(cfgPath string) (map[string]any, error) {
	data, err := os.ReadFile(cfgPath)
	if err != nil {
		return nil, fmt.Errorf("read config: %w", err)
	}
	root := map[string]any{}
	if err := yaml.Unmarshal(data, &root); err != nil {
		return nil, fmt.Errorf("parse yaml config: %w", err)
	}
	return root, nil
}

func asStringAnyMap(v any) map[string]any {
	if v == nil {
		return nil
	}
	if m, ok := v.(map[string]any); ok {
		return m
	}
	return nil
}

func scalarString(v any) string {
	if v == nil {
		return ""
	}
	switch s := v.(type) {
	case string:
		return strings.TrimSpace(s)
	default:
		return strings.TrimSpace(fmt.Sprint(v))
	}
}

func upsertSlackConnectorConfig(cfgPath, defaultChannel, botToken, appToken string) error {
	root, err := readConfigYAML(cfgPath)
	if err != nil {
		return err
	}
	connectors := asStringAnyMap(root["connectors"])
	if connectors == nil {
		connectors = map[string]any{}
	}
	slackCfg := asStringAnyMap(connectors["slack"])
	if slackCfg == nil {
		slackCfg = map[string]any{}
	}

	slackCfg["default_channel"] = defaultChannel
	if strings.TrimSpace(botToken) != "" {
		slackCfg["bot_token"] = botToken
	}
	if strings.TrimSpace(appToken) != "" {
		slackCfg["app_token"] = appToken
	}
	if _, ok := slackCfg["permissions"]; !ok {
		slackCfg["permissions"] = []string{"read", "write"}
	}
	connectors["slack"] = slackCfg
	root["connectors"] = connectors
	return writeConfigYAML(cfgPath, root)
}

func buildOpenAICodexOAuthLLMConfigMap(baseURL, model string, creds *openAICodexOAuthCredentials) map[string]any {
	refreshToken := ""
	accessToken := ""
	expiresAt := int64(0)
	if creds != nil {
		refreshToken = creds.RefreshToken
		accessToken = creds.AccessToken
		expiresAt = creds.ExpiresAt.Unix()
	}

	return map[string]any{
		"default_provider": "openai",
		"providers": map[string]any{
			"openai": map[string]any{
				"auth_type":     "oauth",
				"base_url":      baseURL,
				"default_model": model,
				"oauth": map[string]any{
					"mode":          "authorization_code",
					"token_url":     openAICodexTokenURL,
					"client_id":     openAICodexClientID,
					"refresh_token": refreshToken,
					"access_token":  accessToken,
					"expires_at":    expiresAt,
				},
			},
		},
		"routing": map[string]any{
			"default":        "openai/" + model,
			"analysis":       "openai/" + model,
			"generation":     "openai/" + model,
			"classification": "openai/" + model,
		},
	}
}

func writeConfigYAML(cfgPath string, root map[string]any) error {
	rendered, err := yaml.Marshal(root)
	if err != nil {
		return fmt.Errorf("render yaml config: %w", err)
	}

	mode := os.FileMode(0o600)
	if fi, err := os.Stat(cfgPath); err == nil && fi.Mode().Perm() != 0 {
		mode = fi.Mode().Perm()
	}
	if err := os.WriteFile(cfgPath, rendered, mode); err != nil {
		return fmt.Errorf("write config: %w", err)
	}
	return nil
}

func isInteractiveFile(f *os.File) bool {
	if f == nil {
		return false
	}
	info, err := f.Stat()
	if err != nil {
		return false
	}
	return (info.Mode() & os.ModeCharDevice) != 0
}

func valueForDiscovery(reader *bufio.Reader, envVar, label string) string {
	if v := strings.TrimSpace(os.Getenv(envVar)); v != "" {
		return v
	}
	return strings.TrimSpace(promptWithDefault(reader, fmt.Sprintf("%s value for model discovery (optional)", label), ""))
}

func runClaudeSetupToken() error {
	path, err := exec.LookPath("claude")
	if err != nil {
		return fmt.Errorf("claude CLI not found in PATH")
	}
	cmd := exec.Command(path, "setup-token")
	cmd.Stdin = os.Stdin
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	return cmd.Run()
}

func buildOAuthAuthorizationURL(authEndpoint, clientID, redirectURI string, scopes []string, state string) string {
	if authEndpoint == "" || clientID == "" {
		return ""
	}
	values := map[string]string{
		"response_type": "code",
		"client_id":     clientID,
		"redirect_uri":  redirectURI,
		"scope":         strings.Join(scopes, " "),
		"state":         state,
	}

	var query []string
	for key, value := range values {
		query = append(query, fmt.Sprintf("%s=%s", key, urlEncode(value)))
	}
	sort.Strings(query)
	separator := "?"
	if strings.Contains(authEndpoint, "?") {
		separator = "&"
	}
	return authEndpoint + separator + strings.Join(query, "&")
}

func urlEncode(value string) string {
	replacer := strings.NewReplacer(
		"%", "%25",
		" ", "%20",
		"\"", "%22",
		"#", "%23",
		"&", "%26",
		"+", "%2B",
		",", "%2C",
		"/", "%2F",
		":", "%3A",
		";", "%3B",
		"=", "%3D",
		"?", "%3F",
		"@", "%40",
	)
	return replacer.Replace(value)
}

func openURLInBrowser(link string) error {
	if strings.TrimSpace(link) == "" {
		return fmt.Errorf("empty URL")
	}
	var cmd *exec.Cmd
	switch runtime.GOOS {
	case "darwin":
		cmd = exec.Command("open", link)
	case "linux":
		cmd = exec.Command("xdg-open", link)
	case "windows":
		cmd = exec.Command("rundll32", "url.dll,FileProtocolHandler", link)
	default:
		return fmt.Errorf("unsupported OS for automatic browser open")
	}
	return cmd.Run()
}

func renderCommonPrefix(host, port, storagePath, timezone string) string {
	return fmt.Sprintf(`server:
  host: %s
  port: %s

storage:
  path: %s

scheduler:
  timezone: %s

`, host, port, storagePath, timezone)
}

func renderConnectorSection(opts onboardingConnectorOptions) string {
	if !opts.EnableSlack {
		return "connectors: {}\n"
	}
	lines := []string{
		"connectors:",
		"  slack:",
		fmt.Sprintf("    default_channel: %s", yamlQuote(opts.SlackChannel)),
	}
	if strings.TrimSpace(opts.SlackBotToken) != "" {
		lines = append(lines, fmt.Sprintf("    bot_token: %s", yamlQuote(opts.SlackBotToken)))
	}
	if strings.TrimSpace(opts.SlackAppToken) != "" {
		lines = append(lines, fmt.Sprintf("    app_token: %s", yamlQuote(opts.SlackAppToken)))
	}
	lines = append(lines, "    permissions: [read, write]")
	return strings.Join(lines, "\n") + "\n"
}

func renderAnthropicConfig(host, port, storagePath, timezone, model, apiEnv string, connectors onboardingConnectorOptions) string {
	return renderCommonPrefix(host, port, storagePath, timezone) + fmt.Sprintf(`llm:
  default_provider: anthropic
  providers:
    anthropic:
      api_key: ${%s}
      default_model: %s
      base_url: ""
  routing:
    default: anthropic/%s
    analysis: anthropic/%s
    generation: anthropic/%s
    classification: anthropic/%s

`, apiEnv, model, model, model, model, model) + renderConnectorSection(connectors) + `jobs: []
`
}

func renderOpenAIConfig(host, port, storagePath, timezone, model, apiEnv string, connectors onboardingConnectorOptions) string {
	return renderCommonPrefix(host, port, storagePath, timezone) + fmt.Sprintf(`llm:
  default_provider: openai
  providers:
    openai:
      api_key: ${%s}
      default_model: %s
      base_url: ""
  routing:
    default: openai/%s
    analysis: openai/%s
    generation: openai/%s
    classification: openai/%s

`, apiEnv, model, model, model, model, model) + renderConnectorSection(connectors) + `jobs: []
`
}

func renderOpenAIOAuthConfig(host, port, storagePath, timezone, model, baseURL, tokenURL, clientIDEnv, clientSecretEnv string, scopes []string, connectors onboardingConnectorOptions) string {
	scopeLines := make([]string, 0, len(scopes))
	for _, scope := range scopes {
		scopeLines = append(scopeLines, "        - "+scope)
	}
	return renderCommonPrefix(host, port, storagePath, timezone) + fmt.Sprintf(`llm:
  default_provider: openai
  providers:
    openai:
      auth_type: oauth
      base_url: %s
      default_model: %s
      oauth:
        token_url: %s
        client_id: ${%s}
        client_secret: ${%s}
        scopes:
%s
  routing:
    default: openai/%s
    analysis: openai/%s
    generation: openai/%s
    classification: openai/%s

`, baseURL, model, tokenURL, clientIDEnv, clientSecretEnv, strings.Join(scopeLines, "\n"), model, model, model, model) + renderConnectorSection(connectors) + `jobs: []
`
}

func renderOpenAICodexOAuthConfig(host, port, storagePath, timezone, model, baseURL string, creds *openAICodexOAuthCredentials, connectors onboardingConnectorOptions) string {
	refreshToken := ""
	accessToken := ""
	expiresAt := int64(0)
	if creds != nil {
		refreshToken = creds.RefreshToken
		accessToken = creds.AccessToken
		expiresAt = creds.ExpiresAt.Unix()
	}
	return renderCommonPrefix(host, port, storagePath, timezone) + fmt.Sprintf(`llm:
  default_provider: openai
  providers:
    openai:
      auth_type: oauth
      base_url: %s
      default_model: %s
      oauth:
        mode: authorization_code
        token_url: %s
        client_id: %s
        refresh_token: %s
        access_token: %s
        expires_at: %d
  routing:
    default: openai/%s
    analysis: openai/%s
    generation: openai/%s
    classification: openai/%s

%sjobs: []
`, yamlQuote(baseURL), yamlQuote(model), yamlQuote(openAICodexTokenURL), yamlQuote(openAICodexClientID), yamlQuote(refreshToken), yamlQuote(accessToken), expiresAt, model, model, model, model, renderConnectorSection(connectors))
}

func renderCustomOAuthConfig(host, port, storagePath, timezone, model, baseURL, tokenURL, clientIDEnv, clientSecretEnv string, scopes []string, connectors onboardingConnectorOptions) string {
	scopeLines := make([]string, 0, len(scopes))
	for _, scope := range scopes {
		scopeLines = append(scopeLines, "        - "+scope)
	}
	return renderCommonPrefix(host, port, storagePath, timezone) + fmt.Sprintf(`llm:
  default_provider: custom
  providers:
    custom:
      auth_type: oauth
      base_url: %s
      default_model: %s
      oauth:
        token_url: %s
        client_id: ${%s}
        client_secret: ${%s}
        scopes:
%s
  routing:
    default: custom/%s
    analysis: custom/%s
    generation: custom/%s
    classification: custom/%s

`, baseURL, model, tokenURL, clientIDEnv, clientSecretEnv, strings.Join(scopeLines, "\n"), model, model, model, model) + renderConnectorSection(connectors) + `jobs: []
`
}

func yamlQuote(value string) string {
	return strconv.Quote(value)
}
