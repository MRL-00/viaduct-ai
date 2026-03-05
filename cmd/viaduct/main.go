package main

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/MRL-00/viaduct-ai/internal/agent"
	"github.com/MRL-00/viaduct-ai/internal/agent/llm"
	"github.com/MRL-00/viaduct-ai/internal/config"
	"github.com/MRL-00/viaduct-ai/internal/connector"
	azureconnector "github.com/MRL-00/viaduct-ai/internal/connectors/azure"
	microsoftconnector "github.com/MRL-00/viaduct-ai/internal/connectors/microsoft365"
	slackconnector "github.com/MRL-00/viaduct-ai/internal/connectors/slack"
	"github.com/MRL-00/viaduct-ai/internal/scheduler"
	"github.com/MRL-00/viaduct-ai/internal/security"
	"github.com/MRL-00/viaduct-ai/internal/storage"
	"github.com/spf13/cobra"
	"go.yaml.in/yaml/v3"
)

type app struct {
	logger    *slog.Logger
	cfg       config.Config
	store     *storage.Store
	registry  *connector.Registry
	scheduler *scheduler.Scheduler
	agent     *agent.Agent
}

func main() {
	root := newRootCommand()
	if err := root.Execute(); err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(1)
	}
}

func newRootCommand() *cobra.Command {
	var cfgPath string

	cmd := &cobra.Command{
		Use:   "viaduct",
		Short: "Viaduct enterprise AI operations agent",
		RunE: func(cmd *cobra.Command, _ []string) error {
			return runServe(cfgPath)
		},
	}
	cmd.PersistentFlags().StringVar(&cfgPath, "config", "viaduct.yaml", "path to config file")

	serveCmd := &cobra.Command{
		Use:   "serve",
		Short: "Run Viaduct daemon",
		RunE: func(cmd *cobra.Command, _ []string) error {
			return runServe(cfgPath)
		},
	}
	cmd.AddCommand(serveCmd)

	jobsCmd := &cobra.Command{Use: "jobs", Short: "Manage scheduled jobs"}
	jobsCmd.AddCommand(newJobsListCommand(&cfgPath))
	jobsCmd.AddCommand(newJobsRunCommand(&cfgPath))
	jobsCmd.AddCommand(newJobsHistoryCommand(&cfgPath))
	jobsCmd.AddCommand(newJobsEnableCommand(&cfgPath, true))
	jobsCmd.AddCommand(newJobsEnableCommand(&cfgPath, false))
	cmd.AddCommand(jobsCmd)

	taskCmd := &cobra.Command{
		Use:   "task",
		Short: "Run ad-hoc tasks",
	}
	taskCmd.AddCommand(newTaskRunCommand(&cfgPath))
	cmd.AddCommand(taskCmd)

	modelsCmd := &cobra.Command{
		Use:   "models",
		Short: "Inspect and test LLM provider configuration",
	}
	modelsCmd.AddCommand(newModelsListCommand(&cfgPath))
	modelsCmd.AddCommand(newModelsTestCommand(&cfgPath))
	cmd.AddCommand(modelsCmd)

	setupCmd := &cobra.Command{
		Use:   "setup",
		Short: "First-run configuration setup",
	}
	setupCmd.AddCommand(newSetupInitCommand(&cfgPath))
	setupCmd.AddCommand(newSetupSlackCommand(&cfgPath))
	cmd.AddCommand(setupCmd)

	return cmd
}

func newJobsListCommand(cfgPath *string) *cobra.Command {
	return &cobra.Command{
		Use:   "list",
		Short: "List jobs",
		RunE: func(cmd *cobra.Command, _ []string) error {
			ctx := context.Background()
			app, err := newApp(ctx, *cfgPath)
			if err != nil {
				return err
			}
			defer app.close()
			if err := app.scheduler.Start(ctx); err != nil {
				return err
			}
			jobs := app.scheduler.ListJobs()
			sort.Slice(jobs, func(i, j int) bool { return jobs[i].Name < jobs[j].Name })
			for _, job := range jobs {
				status := "disabled"
				if job.Enabled {
					status = "enabled"
				}
				fmt.Printf("%s\t%s\t%s\n", job.Name, status, job.CronExpr)
			}
			app.scheduler.Stop(ctx)
			return nil
		},
	}
}

func newJobsRunCommand(cfgPath *string) *cobra.Command {
	return &cobra.Command{
		Use:   "run <name>",
		Short: "Run a job immediately",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := context.Background()
			app, err := newApp(ctx, *cfgPath)
			if err != nil {
				return err
			}
			defer app.close()
			if err := app.scheduler.Start(ctx); err != nil {
				return err
			}
			defer app.scheduler.Stop(ctx)
			if err := app.scheduler.RunNow(ctx, args[0]); err != nil {
				return err
			}
			fmt.Printf("job %s triggered\n", args[0])
			return nil
		},
	}
}

func newJobsHistoryCommand(cfgPath *string) *cobra.Command {
	var last int
	cmd := &cobra.Command{
		Use:   "history <name>",
		Short: "Show job history",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := context.Background()
			app, err := newApp(ctx, *cfgPath)
			if err != nil {
				return err
			}
			defer app.close()
			runs, err := app.scheduler.History(ctx, args[0], last)
			if err != nil {
				return err
			}
			for _, run := range runs {
				fmt.Printf("%d\t%s\t%s\t%dms\t$%.6f\t%s\n", run.ID, run.Status,
					run.StartedAt.Format(time.RFC3339), run.DurationMS, run.CostUSD, truncate(run.Error, 80))
			}
			return nil
		},
	}
	cmd.Flags().IntVar(&last, "last", 20, "number of runs to show")
	return cmd
}

func newJobsEnableCommand(cfgPath *string, enabled bool) *cobra.Command {
	use := "disable"
	short := "Disable a job"
	if enabled {
		use = "enable"
		short = "Enable a job"
	}
	return &cobra.Command{
		Use:   use + " <name>",
		Short: short,
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := context.Background()
			app, err := newApp(ctx, *cfgPath)
			if err != nil {
				return err
			}
			defer app.close()
			if err := app.scheduler.Start(ctx); err != nil {
				return err
			}
			defer app.scheduler.Stop(ctx)
			if err := app.scheduler.SetEnabled(ctx, args[0], enabled); err != nil {
				return err
			}
			fmt.Printf("job %s %s\n", args[0], map[bool]string{true: "enabled", false: "disabled"}[enabled])
			return nil
		},
	}
}

func newTaskRunCommand(cfgPath *string) *cobra.Command {
	return &cobra.Command{
		Use:   "run <goal>",
		Short: "Run an ad-hoc goal through the agent",
		Args:  cobra.MinimumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := context.Background()
			app, err := newApp(ctx, *cfgPath)
			if err != nil {
				return err
			}
			defer app.close()
			goal := strings.Join(args, " ")
			result, err := app.agent.Execute(ctx, agent.TaskRequest{
				Goal:          goal,
				TaskType:      "analysis",
				TriggerSource: "manual",
				TriggerRef:    strconv.FormatInt(time.Now().Unix(), 10),
			})
			if err != nil {
				return err
			}
			fmt.Println(result.Response)
			fmt.Printf("\niterations=%d tool_calls=%d cost_usd=%.6f\n", result.Iterations,
				result.ToolInvocations, result.TotalCostUSD)
			return nil
		},
	}
}

func newModelsListCommand(cfgPath *string) *cobra.Command {
	return &cobra.Command{
		Use:   "list",
		Short: "List configured LLM providers",
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, err := config.Load(*cfgPath)
			if err != nil {
				return err
			}
			names := make([]string, 0, len(cfg.LLM.Providers))
			for name := range cfg.LLM.Providers {
				names = append(names, name)
			}
			sort.Strings(names)
			for _, name := range names {
				p := cfg.LLM.Providers[name]
				authType := p.AuthType
				if authType == "" {
					authType = "api_key"
				}
				fmt.Printf("%s\tmodel=%s\tauth=%s\tbase_url=%s\n", name, p.DefaultModel, authType, p.BaseURL)
			}
			return nil
		},
	}
}

func newModelsTestCommand(cfgPath *string) *cobra.Command {
	var prompt string
	cmd := &cobra.Command{
		Use:   "test <provider>",
		Short: "Send a small test prompt to one provider",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, err := config.Load(*cfgPath)
			if err != nil {
				return err
			}
			providers, err := buildProviders(cfg)
			if err != nil {
				return err
			}
			providerName := args[0]
			provider, ok := providers[providerName]
			if !ok {
				return fmt.Errorf("provider %q is not configured", providerName)
			}
			ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
			defer cancel()
			resp, err := provider.Complete(ctx, llm.CompletionRequest{
				SystemPrompt: "You are a concise system check assistant.",
				Messages:     []llm.Message{{Role: "user", Content: prompt}},
				MaxTokens:    128,
				Temperature:  0,
			})
			if err != nil {
				return err
			}
			fmt.Printf("provider=%s model=%s\n", providerName, resp.Model)
			fmt.Printf("response: %s\n", resp.Content)
			fmt.Printf("usage: input=%d output=%d\n", resp.Usage.InputTokens, resp.Usage.OutputTokens)
			return nil
		},
	}
	cmd.Flags().StringVar(&prompt, "prompt", "Reply with exactly: ok", "test prompt")
	return cmd
}

func runServe(cfgPath string) error {
	if err := ensureConfigExistsForServe(cfgPath); err != nil {
		return err
	}

	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()

	app, err := newApp(ctx, cfgPath)
	if err != nil {
		return err
	}
	defer app.close()

	if err := app.scheduler.Start(ctx); err != nil {
		return err
	}
	defer app.scheduler.Stop(context.Background())
	app.startMessageBridges(ctx)

	mux := http.NewServeMux()
	mux.HandleFunc("GET /health", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"status":"ok"}`))
	})
	mux.HandleFunc("GET /jobs", func(w http.ResponseWriter, r *http.Request) {
		jobs := app.scheduler.ListJobs()
		_ = json.NewEncoder(w).Encode(jobs)
	})

	srv := &http.Server{
		Addr:              fmt.Sprintf("%s:%d", app.cfg.Server.Host, app.cfg.Server.Port),
		Handler:           mux,
		ReadHeaderTimeout: 5 * time.Second,
	}

	go func() {
		<-ctx.Done()
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		_ = srv.Shutdown(shutdownCtx)
	}()

	app.logger.Info("viaduct started", "addr", srv.Addr)
	if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
		return err
	}
	return nil
}

func newApp(ctx context.Context, cfgPath string) (*app, error) {
	logger := slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelInfo}))

	cfg, err := config.Load(cfgPath)
	if err != nil {
		return nil, err
	}

	store, err := storage.Open(ctx, cfg.Storage.Path)
	if err != nil {
		return nil, err
	}

	registry := connector.NewRegistry()
	connectors := []connector.Connector{
		microsoftconnector.New(),
		azureconnector.New(),
		slackconnector.New(),
	}

	auditLogger := security.NewAuditLogger(store.Audit)
	for _, c := range connectors {
		configSection, ok := cfg.Connectors[c.Name()]
		if !ok {
			continue
		}
		if ready, reason := connectorConfigReady(c.Name(), configSection.Config); !ready {
			logger.Info("skipping connector; onboarding is incomplete", "connector", c.Name(), "reason", reason)
			continue
		}
		if err := c.Configure(connector.ConnectorConfig(configSection.Config)); err != nil {
			store.Close()
			return nil, fmt.Errorf("configure %s connector: %w", c.Name(), err)
		}
		wrapped := auditLogger.Wrap(c.Name(), c)
		if err := registry.Register(wrapped); err != nil {
			store.Close()
			return nil, err
		}
	}

	providers, err := buildProviders(cfg)
	if err != nil {
		store.Close()
		return nil, err
	}

	router := llm.NewRouter(cfg.LLM, providers)
	permissions := map[string]string{}
	for name, connectorCfg := range cfg.Connectors {
		level := "read"
		if len(connectorCfg.Permissions) > 0 {
			level = connectorCfg.Permissions[0]
		}
		permissions[name] = level
	}
	checker := security.NewPermissionChecker(permissions)
	ag := agent.New(logger, router, registry, checker, store.Audit, store.LLMUsage, cfg.LLM.Pricing, 10)

	sch, err := scheduler.New(logger, cfg, store, ag, registry)
	if err != nil {
		store.Close()
		return nil, err
	}

	return &app{
		logger:    logger,
		cfg:       cfg,
		store:     store,
		registry:  registry,
		scheduler: sch,
		agent:     ag,
	}, nil
}

func (a *app) close() {
	if a.store != nil {
		_ = a.store.Close()
	}
}

func connectorConfigReady(name string, cfg map[string]any) (bool, string) {
	switch name {
	case "slack":
		raw, ok := cfg["bot_token"]
		token, okString := raw.(string)
		if !ok || !okString || strings.TrimSpace(token) == "" {
			return false, "slack.bot_token is not configured yet"
		}
	}
	return true, ""
}

func truncate(value string, max int) string {
	if len(value) <= max {
		return value
	}
	return value[:max-3] + "..."
}

func buildProviders(cfg config.Config) (map[string]llm.Provider, error) {
	providers := map[string]llm.Provider{}
	for name, providerCfg := range cfg.LLM.Providers {
		switch name {
		case "anthropic":
			providers[name] = llm.NewAnthropicProvider(
				providerCfg.APIKey,
				providerCfg.DefaultModel,
				providerCfg.BaseURL,
			)
		case "openai":
			if strings.EqualFold(providerCfg.AuthType, "oauth") {
				providers[name] = llm.NewOpenAICompatibleOAuthProvider(
					name,
					providerCfg.DefaultModel,
					providerCfg.BaseURL,
					llm.OAuthClientCredentialsConfig{
						Mode:         providerCfg.OAuth.Mode,
						TokenURL:     providerCfg.OAuth.TokenURL,
						ClientID:     providerCfg.OAuth.ClientID,
						ClientSecret: providerCfg.OAuth.ClientSecret,
						Scopes:       providerCfg.OAuth.Scopes,
						AccessToken:  providerCfg.OAuth.AccessToken,
						RefreshToken: providerCfg.OAuth.RefreshToken,
						ExpiresAt:    providerCfg.OAuth.ExpiresAt,
					},
				)
			} else {
				providers[name] = llm.NewOpenAIProvider(
					providerCfg.APIKey,
					providerCfg.DefaultModel,
					providerCfg.BaseURL,
				)
			}
		case "custom":
			providers[name] = llm.NewOpenAICompatibleOAuthProvider(
				name,
				providerCfg.DefaultModel,
				providerCfg.BaseURL,
				llm.OAuthClientCredentialsConfig{
					Mode:         providerCfg.OAuth.Mode,
					TokenURL:     providerCfg.OAuth.TokenURL,
					ClientID:     providerCfg.OAuth.ClientID,
					ClientSecret: providerCfg.OAuth.ClientSecret,
					Scopes:       providerCfg.OAuth.Scopes,
					AccessToken:  providerCfg.OAuth.AccessToken,
					RefreshToken: providerCfg.OAuth.RefreshToken,
					ExpiresAt:    providerCfg.OAuth.ExpiresAt,
				},
			)
		default:
			return nil, fmt.Errorf("provider %q is configured but not supported in this build", name)
		}
	}
	return providers, nil
}

func newSetupInitCommand(cfgPath *string) *cobra.Command {
	var force bool
	var advanced bool
	cmd := &cobra.Command{
		Use:   "init",
		Short: "Run onboarding and write config",
		RunE: func(cmd *cobra.Command, args []string) error {
			return runSetupInit(*cfgPath, force, advanced)
		},
	}
	cmd.Flags().BoolVar(&force, "force", false, "overwrite existing config file")
	cmd.Flags().BoolVar(&advanced, "advanced", false, "use full questionnaire instead of quick enterprise OAuth setup")
	return cmd
}

func newSetupSlackCommand(cfgPath *string) *cobra.Command {
	return &cobra.Command{
		Use:   "slack",
		Short: "Configure or update Slack connector onboarding settings",
		RunE: func(cmd *cobra.Command, args []string) error {
			return runSetupSlack(*cfgPath)
		},
	}
}

func ensureConfigExistsForServe(cfgPath string) error {
	_, err := os.Stat(cfgPath)
	if err == nil {
		return maybeRunMissingOnboardingSteps(cfgPath)
	}
	if !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("check config: %w", err)
	}

	fmt.Printf("Config %q not found. Starting first-run setup...\n", cfgPath)
	if err := runSetupInit(cfgPath, false, false); err != nil {
		return err
	}
	fmt.Printf("Created %s. Continuing startup.\n", cfgPath)
	return nil
}

func maybeRunMissingOnboardingSteps(cfgPath string) error {
	if !isInteractiveFile(os.Stdin) || !isInteractiveFile(os.Stdout) {
		return nil
	}
	needsPrompt, message, err := slackOnboardingPromptState(cfgPath)
	if err != nil {
		return err
	}
	if !needsPrompt {
		return nil
	}
	fmt.Println(message)
	reader := bufio.NewReader(os.Stdin)
	if !confirmWithDefault(reader, "Configure Slack onboarding now?", true) {
		return nil
	}
	return runSetupSlackWithReader(cfgPath, reader)
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
	if strings.TrimSpace(fmt.Sprint(slackMap["default_channel"])) == "" {
		return true, "Slack setup is incomplete (default_channel is missing).", nil
	}
	botToken, _ := slackMap["bot_token"].(string)
	if strings.TrimSpace(botToken) == "" {
		return true, "Slack setup is incomplete (bot token is missing).", nil
	}
	return false, "", nil
}

func runSetupSlack(cfgPath string) error {
	if _, err := os.Stat(cfgPath); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return fmt.Errorf("config not found at %s; run `viaduct setup init --config %s` first", cfgPath, cfgPath)
		}
		return fmt.Errorf("check config: %w", err)
	}
	reader := bufio.NewReader(os.Stdin)
	return runSetupSlackWithReader(cfgPath, reader)
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

func runSetupInit(cfgPath string, force bool, advanced bool) error {
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

func runSetupQuickOAuth(reader *bufio.Reader) (string, error) {
	profile := chooseMenuOption(
		reader,
		"Quick setup profile",
		[]menuOption{
			{
				Key: "openai_oauth", Label: "OpenAI-compatible OAuth",
				Description: "Recommended enterprise path",
			},
			{
				Key: "anthropic", Label: "Anthropic + Claude Code",
				Description: "Runs claude setup-token",
			},
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
		return renderAnthropicConfig(host, port, storagePath, timezone,
			"claude-sonnet-4-5-20250514", "ANTHROPIC_API_KEY", connectorOptions), nil
	default:
		baseURL := strings.TrimSpace(os.Getenv("VIADUCT_OPENAI_BASE_URL"))
		if baseURL == "" {
			baseURL = "https://api.openai.com/v1"
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

		return renderOpenAICodexOAuthConfig(
			host,
			port,
			storagePath,
			timezone,
			model,
			baseURL,
			creds,
			connectorOptions,
		), nil
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
	answer := strings.ToLower(strings.TrimSpace(
		promptWithDefault(reader, label+" [y/n]", defaultValue)))
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
			fmt.Printf("  %d)%s %s — %s\n", i+1, marker, option.Label, option.Description)
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
	options.SlackChannel = promptWithDefault(reader, "Slack default channel", "#ops")
	options.SlackBotToken = promptOptional(reader, "Slack bot token (optional)")
	options.SlackAppToken = promptOptional(reader, "Slack app token (optional; needed for inbound chat)")
	return options
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
	return strings.TrimSpace(promptWithDefault(reader,
		fmt.Sprintf("%s value for model discovery (optional)", label), ""))
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
	values := make(map[string]string)
	values["response_type"] = "code"
	values["client_id"] = clientID
	values["redirect_uri"] = redirectURI
	values["scope"] = strings.Join(scopes, " ")
	values["state"] = state

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

func renderAnthropicConfig(host, port, storagePath, timezone, model, apiEnv string,
	connectors onboardingConnectorOptions,
) string {
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

func renderOpenAIConfig(host, port, storagePath, timezone, model, apiEnv string,
	connectors onboardingConnectorOptions,
) string {
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

func renderOpenAIOAuthConfig(host, port, storagePath, timezone, model, baseURL, tokenURL,
	clientIDEnv, clientSecretEnv string, scopes []string, connectors onboardingConnectorOptions,
) string {
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

`, baseURL, model, tokenURL, clientIDEnv, clientSecretEnv, strings.Join(scopeLines, "\n"), model, model, model, model) +
		renderConnectorSection(connectors) + `jobs: []
`
}

func renderOpenAICodexOAuthConfig(host, port, storagePath, timezone, model, baseURL string,
	creds *openAICodexOAuthCredentials, connectors onboardingConnectorOptions,
) string {
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
`, yamlQuote(baseURL), yamlQuote(model), yamlQuote(openAICodexTokenURL),
		yamlQuote(openAICodexClientID), yamlQuote(refreshToken), yamlQuote(accessToken), expiresAt,
		model, model, model, model, renderConnectorSection(connectors))
}

func renderCustomOAuthConfig(host, port, storagePath, timezone, model, baseURL, tokenURL,
	clientIDEnv, clientSecretEnv string, scopes []string, connectors onboardingConnectorOptions,
) string {
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

`, baseURL, model, tokenURL, clientIDEnv, clientSecretEnv, strings.Join(scopeLines, "\n"), model, model, model, model) +
		renderConnectorSection(connectors) + `jobs: []
`
}

func yamlQuote(value string) string {
	return strconv.Quote(value)
}
