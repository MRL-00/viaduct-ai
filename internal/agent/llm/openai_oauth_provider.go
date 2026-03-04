package llm

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	openai "github.com/sashabaranov/go-openai"
)

type OAuthClientCredentialsConfig struct {
	TokenURL     string
	ClientID     string
	ClientSecret string
	Scopes       []string
}

func NewOpenAICompatibleOAuthProvider(name, defaultModel, baseURL string, oauthCfg OAuthClientCredentialsConfig) *OpenAIProvider {
	cfg := openai.DefaultConfig("oauth-unused")
	if baseURL != "" {
		cfg.BaseURL = strings.TrimRight(baseURL, "/")
	}
	tokenSource := &oauthTokenSource{httpClient: &http.Client{Timeout: 30 * time.Second}, cfg: oauthCfg}
	cfg.HTTPClient = &http.Client{
		Timeout: 60 * time.Second,
		Transport: &oauthTransport{
			base:        http.DefaultTransport,
			tokenSource: tokenSource,
		},
	}
	return &OpenAIProvider{
		name:         name,
		client:       openai.NewClientWithConfig(cfg),
		defaultModel: defaultModel,
	}
}

type oauthTransport struct {
	base        http.RoundTripper
	tokenSource *oauthTokenSource
}

func (t *oauthTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	token, err := t.tokenSource.Token(req.Context())
	if err != nil {
		return nil, err
	}
	clone := req.Clone(req.Context())
	clone.Header = clone.Header.Clone()
	clone.Header.Set("Authorization", "Bearer "+token)
	return t.base.RoundTrip(clone)
}

type oauthTokenSource struct {
	httpClient *http.Client
	cfg        OAuthClientCredentialsConfig

	mu        sync.Mutex
	token     string
	expiresAt time.Time
}

func (s *oauthTokenSource) Token(ctx context.Context) (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.token != "" && time.Now().Before(s.expiresAt.Add(-30*time.Second)) {
		return s.token, nil
	}

	form := url.Values{}
	form.Set("grant_type", "client_credentials")
	form.Set("client_id", s.cfg.ClientID)
	form.Set("client_secret", s.cfg.ClientSecret)
	if len(s.cfg.Scopes) > 0 {
		form.Set("scope", strings.Join(s.cfg.Scopes, " "))
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, s.cfg.TokenURL, strings.NewReader(form.Encode()))
	if err != nil {
		return "", fmt.Errorf("build oauth token request: %w", err)
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")

	resp, err := s.httpClient.Do(req)
	if err != nil {
		return "", fmt.Errorf("request oauth token: %w", err)
	}
	defer resp.Body.Close()

	var payload struct {
		AccessToken string `json:"access_token"`
		TokenType   string `json:"token_type"`
		ExpiresIn   int64  `json:"expires_in"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&payload); err != nil {
		return "", fmt.Errorf("decode oauth token response: %w", err)
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return "", fmt.Errorf("oauth token request failed (%d)", resp.StatusCode)
	}
	if payload.AccessToken == "" {
		return "", fmt.Errorf("oauth token response missing access_token")
	}

	s.token = payload.AccessToken
	if payload.ExpiresIn <= 0 {
		payload.ExpiresIn = 300
	}
	s.expiresAt = time.Now().Add(time.Duration(payload.ExpiresIn) * time.Second)
	return s.token, nil
}
