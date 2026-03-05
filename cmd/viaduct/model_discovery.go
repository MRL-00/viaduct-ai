package main

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"sort"
	"strings"
	"time"

	openai "github.com/sashabaranov/go-openai"
)

func apiKeyForDiscovery(reader *bufio.Reader, envVar string) string {
	apiKey := strings.TrimSpace(os.Getenv(envVar))
	if apiKey != "" {
		return apiKey
	}
	return strings.TrimSpace(promptWithDefault(reader, fmt.Sprintf("%s value for model discovery (optional)", envVar), ""))
}

func chooseModel(reader *bufio.Reader, label, defaultModel string, options []string) string {
	if len(options) == 0 {
		return promptWithDefault(reader, label, defaultModel)
	}

	sorted := uniqueSorted(options)
	defaultIndex := 0
	for i, option := range sorted {
		if option == defaultModel {
			defaultIndex = i
			break
		}
	}

	menu := make([]menuOption, 0, len(sorted)+1)
	for _, option := range sorted {
		menu = append(menu, menuOption{Key: option, Label: option})
	}
	menu = append(menu, menuOption{
		Key:         "__custom__",
		Label:       "Custom model...",
		Description: "Type a model ID not shown in the list",
	})
	if idx, ok := promptSelect(label, menu, defaultIndex); ok {
		if menu[idx].Key == "__custom__" {
			custom := strings.TrimSpace(promptWithDefault(reader, "Custom model ID", defaultModel))
			if custom == "" {
				return defaultModel
			}
			return custom
		}
		return menu[idx].Key
	}

	fmt.Printf("%s options:\n", label)
	for _, option := range sorted {
		fmt.Printf("  - %s\n", option)
	}
	input := strings.TrimSpace(promptWithDefault(reader, fmt.Sprintf("%s (name or custom)", label), defaultModel))
	if input == "" {
		return defaultModel
	}
	for _, option := range sorted {
		if input == option {
			return input
		}
	}
	fmt.Printf("Model %q is not in discovered list, using %s\n", input, defaultModel)
	return defaultModel
}

func defaultOpenAIModels() []string {
	return []string{
		"gpt-5.3-codex",
		"gpt-5.2",
		"gpt-5.2-codex",
		"gpt-5.1",
		"gpt-5.1-mini",
		"gpt-5.1-nano",
		"gpt-5-mini",
		"gpt-4o",
		"gpt-4o-mini",
		"gpt-4.1",
		"o4-mini",
		"o3",
		"o3-mini",
	}
}

func defaultOpenAIModel() string {
	return "gpt-5.3-codex"
}

func pickPreferredOpenAIModel(options []string, fallback string) string {
	if len(options) == 0 {
		return fallback
	}
	candidates := []string{
		"gpt-5.3-codex",
		"gpt-5.2-codex",
		"gpt-5.2",
		"gpt-5.1",
		"gpt-5-mini",
		"gpt-4o",
	}
	seen := make(map[string]struct{}, len(options))
	for _, opt := range options {
		seen[opt] = struct{}{}
	}
	for _, candidate := range candidates {
		if _, ok := seen[candidate]; ok {
			return candidate
		}
	}
	if _, ok := seen[fallback]; ok {
		return fallback
	}
	sorted := uniqueSorted(options)
	return sorted[0]
}

func discoverOpenAIModels(ctx context.Context, apiKey string) ([]string, error) {
	if strings.TrimSpace(apiKey) == "" {
		return nil, fmt.Errorf("OpenAI API key not set")
	}

	client := openai.NewClient(apiKey)
	list, err := client.ListModels(ctx)
	if err != nil {
		return nil, err
	}

	models := make([]string, 0, len(list.Models))
	for _, model := range list.Models {
		id := strings.TrimSpace(model.ID)
		if id == "" {
			continue
		}
		if strings.HasPrefix(id, "gpt-") || strings.HasPrefix(id, "o") || strings.Contains(id, "chatgpt") {
			models = append(models, id)
		}
	}
	models = uniqueSorted(models)
	if len(models) == 0 {
		return nil, fmt.Errorf("no chat-capable models discovered")
	}
	return models, nil
}

func discoverAnthropicModels(ctx context.Context, apiKey string) ([]string, error) {
	if strings.TrimSpace(apiKey) == "" {
		return nil, fmt.Errorf("Anthropic API key not set")
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, "https://api.anthropic.com/v1/models", nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("x-api-key", apiKey)
	req.Header.Set("anthropic-version", "2023-06-01")

	client := &http.Client{Timeout: 20 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 1024))
		return nil, fmt.Errorf("anthropic models request failed (%d): %s", resp.StatusCode, string(body))
	}

	var payload struct {
		Data []struct {
			ID string `json:"id"`
		} `json:"data"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&payload); err != nil {
		return nil, err
	}

	models := make([]string, 0, len(payload.Data))
	for _, model := range payload.Data {
		id := strings.TrimSpace(model.ID)
		if id != "" {
			models = append(models, id)
		}
	}
	models = uniqueSorted(models)
	if len(models) == 0 {
		return nil, fmt.Errorf("no models returned")
	}
	return models, nil
}

func discoverOpenAIModelsOAuth(ctx context.Context, baseURL, tokenURL, clientID, clientSecret string, scopes []string) ([]string, error) {
	if strings.TrimSpace(baseURL) == "" {
		return nil, fmt.Errorf("base URL is required")
	}
	token, err := fetchOAuthToken(ctx, tokenURL, clientID, clientSecret, scopes)
	if err != nil {
		return nil, err
	}

	endpoint := strings.TrimRight(baseURL, "/") + "/models"
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+token)

	client := &http.Client{Timeout: 20 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 1024))
		return nil, fmt.Errorf("models request failed (%d): %s", resp.StatusCode, string(body))
	}

	var payload struct {
		Data []struct {
			ID string `json:"id"`
		} `json:"data"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&payload); err != nil {
		return nil, err
	}

	models := make([]string, 0, len(payload.Data))
	for _, model := range payload.Data {
		id := strings.TrimSpace(model.ID)
		if id != "" {
			models = append(models, id)
		}
	}
	models = uniqueSorted(models)
	if len(models) == 0 {
		return nil, fmt.Errorf("no models returned")
	}
	return models, nil
}

func fetchOAuthToken(ctx context.Context, tokenURL, clientID, clientSecret string, scopes []string) (string, error) {
	if strings.TrimSpace(tokenURL) == "" {
		return "", fmt.Errorf("oauth token URL is required")
	}
	if strings.TrimSpace(clientID) == "" || strings.TrimSpace(clientSecret) == "" {
		return "", fmt.Errorf("oauth client ID and secret are required")
	}

	form := url.Values{}
	form.Set("grant_type", "client_credentials")
	form.Set("client_id", clientID)
	form.Set("client_secret", clientSecret)
	if len(scopes) > 0 {
		form.Set("scope", strings.Join(scopes, " "))
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, tokenURL, strings.NewReader(form.Encode()))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")

	client := &http.Client{Timeout: 20 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()

	var payload struct {
		AccessToken string `json:"access_token"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&payload); err != nil {
		return "", err
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return "", fmt.Errorf("oauth token request failed (%d)", resp.StatusCode)
	}
	if payload.AccessToken == "" {
		return "", fmt.Errorf("oauth token response missing access_token")
	}
	return payload.AccessToken, nil
}

func uniqueSorted(values []string) []string {
	seen := make(map[string]struct{}, len(values))
	out := make([]string, 0, len(values))
	for _, value := range values {
		if _, ok := seen[value]; ok {
			continue
		}
		seen[value] = struct{}{}
		out = append(out, value)
	}
	sort.Strings(out)
	return out
}
