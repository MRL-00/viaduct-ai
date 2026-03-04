package config

import (
	"fmt"
	"os"
	"regexp"
	"strings"
	"time"

	"github.com/mitchellh/mapstructure"
	"github.com/robfig/cron/v3"
	"github.com/spf13/viper"
)

var envPattern = regexp.MustCompile(`\$\{([A-Za-z_][A-Za-z0-9_]*)\}`)

type Config struct {
	Server     ServerConfig               `mapstructure:"server"`
	Storage    StorageConfig              `mapstructure:"storage"`
	LLM        LLMConfig                  `mapstructure:"llm"`
	Connectors map[string]ConnectorConfig `mapstructure:"connectors"`
	Scheduler  SchedulerConfig            `mapstructure:"scheduler"`
	Jobs       []JobConfig                `mapstructure:"jobs"`
	Security   SecurityConfig             `mapstructure:"security"`
}

type ServerConfig struct {
	Host string `mapstructure:"host"`
	Port int    `mapstructure:"port"`
}

type StorageConfig struct {
	Path string `mapstructure:"path"`
}

type LLMConfig struct {
	DefaultProvider string                     `mapstructure:"default_provider"`
	Providers       map[string]LLMProvider     `mapstructure:"providers"`
	Routing         LLMRoutingConfig           `mapstructure:"routing"`
	Pricing         map[string]PricingConfig   `mapstructure:"pricing"`
	RateLimits      map[string]RateLimitConfig `mapstructure:"rate_limits"`
}

type LLMProvider struct {
	APIKey       string      `mapstructure:"api_key"`
	DefaultModel string      `mapstructure:"default_model"`
	BaseURL      string      `mapstructure:"base_url"`
	AuthType     string      `mapstructure:"auth_type"`
	OAuth        OAuthConfig `mapstructure:"oauth"`
}

type OAuthConfig struct {
	TokenURL     string   `mapstructure:"token_url"`
	ClientID     string   `mapstructure:"client_id"`
	ClientSecret string   `mapstructure:"client_secret"`
	Scopes       []string `mapstructure:"scopes"`
}

type LLMRoutingConfig struct {
	Strategy        string            `mapstructure:"strategy"`
	Classification  string            `mapstructure:"classification"`
	Summarisation   string            `mapstructure:"summarisation"`
	Analysis        string            `mapstructure:"analysis"`
	Generation      string            `mapstructure:"generation"`
	Code            string            `mapstructure:"code"`
	Default         string            `mapstructure:"default"`
	Chain           []string          `mapstructure:"chain"`
	EscalationChain []string          `mapstructure:"escalation_chain"`
	Retry           RetryConfig       `mapstructure:"retry"`
	Tasks           map[string]string `mapstructure:"tasks"`
}

type RetryConfig struct {
	MaxAttempts int    `mapstructure:"max_attempts"`
	Backoff     string `mapstructure:"backoff"`
}

type PricingConfig struct {
	InputPerMillion  float64 `mapstructure:"input_per_million"`
	OutputPerMillion float64 `mapstructure:"output_per_million"`
}

type RateLimitConfig struct {
	RequestsPerMinute int `mapstructure:"requests_per_minute"`
	TokensPerMinute   int `mapstructure:"tokens_per_minute"`
}

type ConnectorConfig struct {
	Permissions []string       `mapstructure:"permissions"`
	Config      map[string]any `mapstructure:",remain"`
}

type SchedulerConfig struct {
	Timezone string `mapstructure:"timezone"`
}

type JobConfig struct {
	Name                 string            `mapstructure:"name"`
	Cron                 string            `mapstructure:"cron"`
	Timezone             string            `mapstructure:"timezone"`
	Task                 string            `mapstructure:"task"`
	Connectors           []string          `mapstructure:"connectors"`
	Permissions          map[string]string `mapstructure:"permissions"`
	CostLimitUSD         float64           `mapstructure:"cost_limit_usd"`
	OnFailure            string            `mapstructure:"on_failure"`
	Enabled              *bool             `mapstructure:"enabled"`
	Timeout              string            `mapstructure:"timeout"`
	AllowOverlap         bool              `mapstructure:"allow_overlap"`
	RunOnStartupIfMissed bool              `mapstructure:"run_on_startup_if_missed"`
}

type SecurityConfig struct {
	Audit         AuditConfig         `mapstructure:"audit"`
	LLMDataPolicy LLMDataPolicyConfig `mapstructure:"llm_data_policy"`
}

type AuditConfig struct {
	RetentionDays int               `mapstructure:"retention_days"`
	Export        AuditExportConfig `mapstructure:"export"`
}

type AuditExportConfig struct {
	Enabled     bool   `mapstructure:"enabled"`
	Destination string `mapstructure:"destination"`
}

type LLMDataPolicyConfig struct {
	RestrictedConnectors []string `mapstructure:"restricted_connectors"`
	RestrictedMode       string   `mapstructure:"restricted_mode"`
}

func Load(path string) (Config, error) {
	v := viper.New()
	v.SetConfigFile(path)
	v.SetConfigType("yaml")
	v.SetEnvPrefix("VIADUCT")
	v.SetEnvKeyReplacer(strings.NewReplacer(".", "_"))
	v.AutomaticEnv()

	v.SetDefault("server.host", "0.0.0.0")
	v.SetDefault("server.port", 8080)
	v.SetDefault("storage.path", "./viaduct.db")
	v.SetDefault("scheduler.timezone", "UTC")

	if err := v.ReadInConfig(); err != nil {
		return Config{}, fmt.Errorf("read config: %w", err)
	}

	settings := v.AllSettings()
	interpolated, err := interpolateMap(settings)
	if err != nil {
		return Config{}, err
	}

	var cfg Config
	decoder, err := mapstructure.NewDecoder(&mapstructure.DecoderConfig{
		TagName:          "mapstructure",
		Result:           &cfg,
		WeaklyTypedInput: true,
	})
	if err != nil {
		return Config{}, fmt.Errorf("create decoder: %w", err)
	}
	if err := decoder.Decode(interpolated); err != nil {
		return Config{}, fmt.Errorf("decode config: %w", err)
	}

	if err := cfg.Validate(); err != nil {
		return Config{}, err
	}

	return cfg, nil
}

func (c Config) Validate() error {
	if c.Storage.Path == "" {
		return fmt.Errorf("storage.path is required")
	}
	if c.LLM.DefaultProvider == "" {
		return fmt.Errorf("llm.default_provider is required")
	}
	if len(c.LLM.Providers) == 0 {
		return fmt.Errorf("llm.providers must not be empty")
	}

	knownProviders := map[string]struct{}{
		"anthropic": {},
		"openai":    {},
		"gemini":    {},
		"custom":    {},
	}

	for name, provider := range c.LLM.Providers {
		if _, ok := knownProviders[name]; !ok {
			return fmt.Errorf("llm.providers contains unsupported provider %q", name)
		}
		if provider.DefaultModel == "" {
			return fmt.Errorf("llm.providers.%s.default_model is required", name)
		}
		switch name {
		case "custom":
			if provider.BaseURL == "" {
				return fmt.Errorf("llm.providers.%s.base_url is required", name)
			}
			if strings.ToLower(provider.AuthType) != "oauth" {
				return fmt.Errorf("llm.providers.%s.auth_type must be oauth", name)
			}
			if provider.APIKey != "" {
				return fmt.Errorf("llm.providers.%s.api_key is not supported; use oauth only", name)
			}
			if provider.OAuth.TokenURL == "" {
				return fmt.Errorf("llm.providers.%s.oauth.token_url is required", name)
			}
			if provider.OAuth.ClientID == "" {
				return fmt.Errorf("llm.providers.%s.oauth.client_id is required", name)
			}
			if provider.OAuth.ClientSecret == "" {
				return fmt.Errorf("llm.providers.%s.oauth.client_secret is required", name)
			}
		default:
			if provider.APIKey == "" {
				return fmt.Errorf("llm.providers.%s.api_key is required", name)
			}
		}
	}

	if _, ok := c.LLM.Providers[c.LLM.DefaultProvider]; !ok {
		return fmt.Errorf("llm.default_provider %q is not defined in llm.providers", c.LLM.DefaultProvider)
	}

	for _, route := range []string{
		c.LLM.Routing.Classification,
		c.LLM.Routing.Summarisation,
		c.LLM.Routing.Analysis,
		c.LLM.Routing.Generation,
		c.LLM.Routing.Code,
		c.LLM.Routing.Default,
	} {
		if route == "" {
			continue
		}
		provider, _, ok := strings.Cut(route, "/")
		if !ok {
			return fmt.Errorf("route %q must be in provider/model format", route)
		}
		if _, exists := c.LLM.Providers[provider]; !exists {
			return fmt.Errorf("route provider %q is not configured", provider)
		}
	}

	for name, connector := range c.Connectors {
		if len(connector.Permissions) == 0 {
			connector.Permissions = []string{"read"}
			c.Connectors[name] = connector
		}
		for _, permission := range connector.Permissions {
			switch permission {
			case "read", "write", "admin":
			default:
				return fmt.Errorf("connector %q has invalid permission %q", name, permission)
			}
		}
	}

	if c.Scheduler.Timezone != "" {
		if _, err := time.LoadLocation(c.Scheduler.Timezone); err != nil {
			return fmt.Errorf("scheduler.timezone is invalid: %w", err)
		}
	}

	parser := cron.NewParser(cron.Minute | cron.Hour | cron.Dom | cron.Month | cron.Dow)
	for i, job := range c.Jobs {
		if job.Name == "" {
			return fmt.Errorf("jobs[%d].name is required", i)
		}
		if job.Cron == "" {
			return fmt.Errorf("jobs[%d].cron is required", i)
		}
		if _, err := parser.Parse(job.Cron); err != nil {
			return fmt.Errorf("jobs[%d].cron invalid: %w", i, err)
		}
		if job.Task == "" {
			return fmt.Errorf("jobs[%d].task is required", i)
		}
		if job.Timezone != "" {
			if _, err := time.LoadLocation(job.Timezone); err != nil {
				return fmt.Errorf("jobs[%d].timezone invalid: %w", i, err)
			}
		}
		if job.Timeout != "" {
			if _, err := time.ParseDuration(job.Timeout); err != nil {
				return fmt.Errorf("jobs[%d].timeout invalid: %w", i, err)
			}
		}
		for connectorName, perm := range job.Permissions {
			switch perm {
			case "read", "write", "admin":
			default:
				return fmt.Errorf("jobs[%d].permissions[%s] invalid: %s", i, connectorName, perm)
			}
		}
	}

	return nil
}

func interpolateMap(in map[string]any) (map[string]any, error) {
	out := make(map[string]any, len(in))
	for k, v := range in {
		nv, err := interpolateValue(v)
		if err != nil {
			return nil, err
		}
		out[k] = nv
	}
	return out, nil
}

func interpolateValue(v any) (any, error) {
	switch val := v.(type) {
	case map[string]any:
		return interpolateMap(val)
	case []any:
		items := make([]any, len(val))
		for i := range val {
			nv, err := interpolateValue(val[i])
			if err != nil {
				return nil, err
			}
			items[i] = nv
		}
		return items, nil
	case string:
		matches := envPattern.FindAllStringSubmatch(val, -1)
		if len(matches) == 0 {
			return val, nil
		}
		result := val
		for _, match := range matches {
			key := match[1]
			envVal, ok := os.LookupEnv(key)
			if !ok {
				return nil, fmt.Errorf("environment variable %s is not set", key)
			}
			result = strings.ReplaceAll(result, match[0], envVal)
		}
		return result, nil
	default:
		return v, nil
	}
}
