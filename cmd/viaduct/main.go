package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
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
	"github.com/MRL-00/viaduct-ai/internal/onboarding"
	"github.com/MRL-00/viaduct-ai/internal/scheduler"
	"github.com/MRL-00/viaduct-ai/internal/security"
	"github.com/MRL-00/viaduct-ai/internal/storage"
	"github.com/spf13/cobra"
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
	if err := onboarding.EnsureConfigExistsForServe(cfgPath); err != nil {
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
				oauthCfg := llm.OAuthClientCredentialsConfig{
					Mode:         providerCfg.OAuth.Mode,
					TokenURL:     providerCfg.OAuth.TokenURL,
					ClientID:     providerCfg.OAuth.ClientID,
					ClientSecret: providerCfg.OAuth.ClientSecret,
					Scopes:       providerCfg.OAuth.Scopes,
					AccessToken:  providerCfg.OAuth.AccessToken,
					RefreshToken: providerCfg.OAuth.RefreshToken,
					ExpiresAt:    providerCfg.OAuth.ExpiresAt,
				}
				mode := strings.ToLower(strings.TrimSpace(providerCfg.OAuth.Mode))
				if mode == "" && (providerCfg.OAuth.RefreshToken != "" || providerCfg.OAuth.AccessToken != "") {
					mode = "authorization_code"
				}
				if mode == "authorization_code" {
					providers[name] = llm.NewOpenAICodexOAuthProvider(
						name,
						providerCfg.DefaultModel,
						providerCfg.BaseURL,
						oauthCfg,
					)
				} else {
					providers[name] = llm.NewOpenAICompatibleOAuthProvider(
						name,
						providerCfg.DefaultModel,
						providerCfg.BaseURL,
						oauthCfg,
					)
				}
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
			return onboarding.RunSetupInit(*cfgPath, force, advanced)
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
			return onboarding.RunSetupSlack(*cfgPath)
		},
	}
}
