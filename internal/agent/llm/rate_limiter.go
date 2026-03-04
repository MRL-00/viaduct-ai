package llm

import (
	"context"
	"fmt"
	"time"

	"golang.org/x/time/rate"
)

type ProviderLimiter struct {
	requests *rate.Limiter
	tokens   *rate.Limiter
}

func NewProviderLimiter(requestsPerMinute, tokensPerMinute int) *ProviderLimiter {
	if requestsPerMinute <= 0 {
		requestsPerMinute = 60
	}
	if tokensPerMinute <= 0 {
		tokensPerMinute = 120000
	}
	return &ProviderLimiter{
		requests: rate.NewLimiter(rate.Limit(float64(requestsPerMinute)/60.0), requestsPerMinute),
		tokens:   rate.NewLimiter(rate.Limit(float64(tokensPerMinute)/60.0), tokensPerMinute),
	}
}

func (l *ProviderLimiter) Wait(ctx context.Context, estimatedTokens int) error {
	if estimatedTokens < 1 {
		estimatedTokens = 1
	}
	if err := l.requests.Wait(ctx); err != nil {
		return fmt.Errorf("request limiter: %w", err)
	}
	reservation := l.tokens.ReserveN(time.Now(), estimatedTokens)
	if !reservation.OK() {
		return fmt.Errorf("token reservation denied")
	}
	delay := reservation.Delay()
	if delay <= 0 {
		return nil
	}
	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		reservation.CancelAt(time.Now())
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

func estimateTokens(req CompletionRequest) int {
	tokens := 0
	for _, msg := range req.Messages {
		tokens += len(msg.Content) / 4
	}
	tokens += len(req.SystemPrompt) / 4
	if req.MaxTokens > 0 {
		tokens += req.MaxTokens
	}
	if tokens < 1 {
		return 1
	}
	return tokens
}
