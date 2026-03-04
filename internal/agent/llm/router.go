package llm

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/MRL-00/viaduct-ai/internal/config"
)

type Router struct {
	defaultProvider string
	providers       map[string]Provider
	routing         config.LLMRoutingConfig
	limiters        map[string]*ProviderLimiter
}

func NewRouter(cfg config.LLMConfig, providers map[string]Provider) *Router {
	limiters := make(map[string]*ProviderLimiter)
	for providerName, limits := range cfg.RateLimits {
		limiters[providerName] = NewProviderLimiter(limits.RequestsPerMinute, limits.TokensPerMinute)
	}
	return &Router{
		defaultProvider: cfg.DefaultProvider,
		providers:       providers,
		routing:         cfg.Routing,
		limiters:        limiters,
	}
}

func (r *Router) Complete(ctx context.Context, req CompletionRequest) (CompletionResponse, error) {
	chain := r.resolveChain(req.TaskType)
	if len(chain) == 0 {
		chain = []string{r.defaultProvider}
	}

	var lastErr error
	for _, route := range chain {
		providerName, model := parseRoute(route)
		provider, ok := r.providers[providerName]
		if !ok {
			lastErr = fmt.Errorf("provider %q is not registered", providerName)
			continue
		}

		request := req
		if request.Model == "" {
			request.Model = model
		}

		if limiter, ok := r.limiters[providerName]; ok {
			if err := limiter.Wait(ctx, estimateTokens(request)); err != nil {
				lastErr = err
				continue
			}
		}

		resp, err := provider.Complete(ctx, request)
		if err == nil {
			if resp.Provider == "" {
				resp.Provider = provider.Name()
			}
			if resp.Model == "" {
				resp.Model = request.Model
			}
			return resp, nil
		}

		lastErr = err
		if !isRetryable(err) {
			break
		}
	}

	if lastErr == nil {
		lastErr = errors.New("no provider available")
	}
	return CompletionResponse{}, lastErr
}

func (r *Router) resolveChain(taskType string) []string {
	if taskType == "" {
		taskType = "default"
	}

	order := make([]string, 0, 4)
	routeByTask := map[string]string{
		"classification": r.routing.Classification,
		"summarisation":  r.routing.Summarisation,
		"analysis":       r.routing.Analysis,
		"generation":     r.routing.Generation,
		"code":           r.routing.Code,
		"default":        r.routing.Default,
	}
	if taskType != "" {
		if route, ok := r.routing.Tasks[taskType]; ok && route != "" {
			order = append(order, route)
		}
	}
	if route := routeByTask[taskType]; route != "" {
		order = append(order, route)
	}
	if route := r.routing.Default; route != "" {
		order = append(order, route)
	}
	order = append(order, r.routing.Chain...)
	if len(order) == 0 {
		order = append(order, r.defaultProvider)
	}

	seen := map[string]struct{}{}
	out := make([]string, 0, len(order))
	for _, route := range order {
		if route == "" {
			continue
		}
		if !strings.Contains(route, "/") {
			route = route + "/"
		}
		if _, ok := seen[route]; ok {
			continue
		}
		seen[route] = struct{}{}
		out = append(out, route)
	}
	return out
}

func parseRoute(route string) (provider string, model string) {
	provider, model, _ = strings.Cut(route, "/")
	if provider == "" {
		provider = route
	}
	return provider, model
}

func isRetryable(err error) bool {
	if err == nil {
		return false
	}
	if errors.Is(err, context.DeadlineExceeded) || errors.Is(err, context.Canceled) {
		return true
	}
	s := strings.ToLower(err.Error())
	return strings.Contains(s, "timeout") || strings.Contains(s, " 5") ||
		strings.Contains(s, "(5") || strings.Contains(s, "tempor")
}
