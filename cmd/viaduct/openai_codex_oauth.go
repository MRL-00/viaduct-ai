package main

import (
	"bufio"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"
)

const (
	openAICodexClientID     = "app_EMoamEEZ73f0CkXaXp7hrann"
	openAICodexAuthorizeURL = "https://auth.openai.com/oauth/authorize"
	openAICodexTokenURL     = "https://auth.openai.com/oauth/token"
	openAICodexRedirectURI  = "http://localhost:1455/auth/callback"
	openAICodexScopes       = "openid profile email offline_access"
	openAICodexJWTClaimPath = "https://api.openai.com/auth"
)

const openAICallbackSuccessHTML = `<!doctype html>
<html lang="en">
<head>
  <meta charset="utf-8" />
  <meta name="viewport" content="width=device-width, initial-scale=1" />
  <title>Authentication successful</title>
</head>
<body>
  <p>Authentication successful. Return to your terminal to continue.</p>
</body>
</html>`

type openAICodexAuthFlow struct {
	Verifier string
	State    string
	URL      string
}

type openAICodexOAuthCredentials struct {
	AccessToken  string
	RefreshToken string
	ExpiresAt    time.Time
	AccountID    string
}

type openAICallbackServer struct {
	srv    *http.Server
	codeCh chan string
}

func runOpenAICodexOAuthLogin(reader *bufio.Reader) (*openAICodexOAuthCredentials, error) {
	flow, err := createOpenAICodexAuthorizationFlow("pi")
	if err != nil {
		return nil, err
	}

	server, err := startOpenAICallbackServer(flow.State)
	if err != nil {
		fmt.Printf("Could not bind callback at http://127.0.0.1:1455/auth/callback: %v\n", err)
		fmt.Println("Falling back to manual paste flow.")
	} else {
		defer func() {
			closeCtx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
			defer cancel()
			_ = server.Close(closeCtx)
		}()
	}

	fmt.Printf("Open this URL to login:\n%s\n", flow.URL)
	if err := openURLInBrowser(flow.URL); err != nil {
		fmt.Printf("Could not open browser automatically: %v\n", err)
	}

	code := ""
	if server != nil {
		fmt.Println("Waiting for OAuth callback on http://127.0.0.1:1455/auth/callback ...")
		waitCtx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
		defer cancel()
		callbackCode, waitErr := server.WaitForCode(waitCtx)
		if waitErr != nil {
			fmt.Printf("Could not capture callback automatically: %v\n", waitErr)
		} else {
			code = callbackCode
		}
	}

	if strings.TrimSpace(code) == "" {
		input := strings.TrimSpace(promptWithDefault(reader, "Paste redirect URL or authorization code", ""))
		parsedCode, parsedState := parseOpenAIAuthorizationInput(input)
		if parsedState != "" && parsedState != flow.State {
			return nil, fmt.Errorf("oauth state mismatch")
		}
		code = parsedCode
	}
	if strings.TrimSpace(code) == "" {
		return nil, fmt.Errorf("missing authorization code")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	creds, err := exchangeOpenAICodexAuthorizationCode(ctx, code, flow.Verifier)
	if err != nil {
		return nil, err
	}
	if creds.AccountID != "" {
		fmt.Printf("OpenAI OAuth complete for account %s\n", creds.AccountID)
	} else {
		fmt.Println("OpenAI OAuth complete.")
	}
	return creds, nil
}

func createOpenAICodexAuthorizationFlow(originator string) (openAICodexAuthFlow, error) {
	verifier, challenge, err := generatePKCE()
	if err != nil {
		return openAICodexAuthFlow{}, err
	}
	state, err := randomHex(16)
	if err != nil {
		return openAICodexAuthFlow{}, err
	}
	if strings.TrimSpace(originator) == "" {
		originator = "pi"
	}

	u, err := url.Parse(openAICodexAuthorizeURL)
	if err != nil {
		return openAICodexAuthFlow{}, err
	}
	q := u.Query()
	q.Set("response_type", "code")
	q.Set("client_id", openAICodexClientID)
	q.Set("redirect_uri", openAICodexRedirectURI)
	q.Set("scope", openAICodexScopes)
	q.Set("code_challenge", challenge)
	q.Set("code_challenge_method", "S256")
	q.Set("state", state)
	q.Set("id_token_add_organizations", "true")
	q.Set("codex_cli_simplified_flow", "true")
	q.Set("originator", originator)
	u.RawQuery = q.Encode()

	return openAICodexAuthFlow{
		Verifier: verifier,
		State:    state,
		URL:      u.String(),
	}, nil
}

func generatePKCE() (verifier string, challenge string, err error) {
	seed := make([]byte, 32)
	if _, err = rand.Read(seed); err != nil {
		return "", "", fmt.Errorf("generate oauth verifier: %w", err)
	}
	verifier = base64.RawURLEncoding.EncodeToString(seed)
	sum := sha256.Sum256([]byte(verifier))
	challenge = base64.RawURLEncoding.EncodeToString(sum[:])
	return verifier, challenge, nil
}

func randomHex(size int) (string, error) {
	buf := make([]byte, size)
	if _, err := rand.Read(buf); err != nil {
		return "", fmt.Errorf("generate random state: %w", err)
	}
	return hex.EncodeToString(buf), nil
}

func startOpenAICallbackServer(expectedState string) (*openAICallbackServer, error) {
	s := &openAICallbackServer{codeCh: make(chan string, 1)}

	mux := http.NewServeMux()
	mux.HandleFunc("/auth/callback", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("state") != expectedState {
			http.Error(w, "State mismatch", http.StatusBadRequest)
			return
		}
		code := strings.TrimSpace(r.URL.Query().Get("code"))
		if code == "" {
			http.Error(w, "Missing authorization code", http.StatusBadRequest)
			return
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(openAICallbackSuccessHTML))
		select {
		case s.codeCh <- code:
		default:
		}
	})

	ln, err := net.Listen("tcp", "127.0.0.1:1455")
	if err != nil {
		return nil, err
	}

	s.srv = &http.Server{Handler: mux}
	go func() {
		_ = s.srv.Serve(ln)
	}()
	return s, nil
}

func (s *openAICallbackServer) WaitForCode(ctx context.Context) (string, error) {
	select {
	case code := <-s.codeCh:
		return code, nil
	case <-ctx.Done():
		return "", ctx.Err()
	}
}

func (s *openAICallbackServer) Close(ctx context.Context) error {
	if s == nil || s.srv == nil {
		return nil
	}
	return s.srv.Shutdown(ctx)
}

func parseOpenAIAuthorizationInput(input string) (code string, state string) {
	value := strings.TrimSpace(input)
	if value == "" {
		return "", ""
	}

	if parsedURL, err := url.Parse(value); err == nil && parsedURL.Scheme != "" && parsedURL.Host != "" {
		return parsedURL.Query().Get("code"), parsedURL.Query().Get("state")
	}

	if strings.Contains(value, "#") {
		parts := strings.SplitN(value, "#", 2)
		code = strings.TrimSpace(parts[0])
		if len(parts) > 1 {
			state = strings.TrimSpace(parts[1])
		}
		return code, state
	}

	if strings.Contains(value, "code=") {
		params, err := url.ParseQuery(value)
		if err == nil {
			return params.Get("code"), params.Get("state")
		}
	}

	return value, ""
}

func exchangeOpenAICodexAuthorizationCode(ctx context.Context, code, verifier string) (*openAICodexOAuthCredentials, error) {
	form := url.Values{}
	form.Set("grant_type", "authorization_code")
	form.Set("client_id", openAICodexClientID)
	form.Set("code", code)
	form.Set("code_verifier", verifier)
	form.Set("redirect_uri", openAICodexRedirectURI)

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, openAICodexTokenURL, strings.NewReader(form.Encode()))
	if err != nil {
		return nil, fmt.Errorf("build oauth token request: %w", err)
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")

	client := &http.Client{Timeout: 30 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("request oauth token: %w", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return nil, fmt.Errorf("read oauth token response: %w", err)
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("oauth token request failed (%d): %s", resp.StatusCode, strings.TrimSpace(string(body)))
	}

	var payload struct {
		AccessToken  string `json:"access_token"`
		RefreshToken string `json:"refresh_token"`
		ExpiresIn    int64  `json:"expires_in"`
	}
	if err := json.Unmarshal(body, &payload); err != nil {
		return nil, fmt.Errorf("decode oauth token response: %w", err)
	}
	if payload.AccessToken == "" || payload.RefreshToken == "" || payload.ExpiresIn <= 0 {
		return nil, fmt.Errorf("oauth token response missing required fields")
	}

	creds := &openAICodexOAuthCredentials{
		AccessToken:  payload.AccessToken,
		RefreshToken: payload.RefreshToken,
		ExpiresAt:    time.Now().Add(time.Duration(payload.ExpiresIn) * time.Second),
		AccountID:    extractOpenAIAccountID(payload.AccessToken),
	}
	return creds, nil
}

func extractOpenAIAccountID(accessToken string) string {
	parts := strings.Split(accessToken, ".")
	if len(parts) != 3 {
		return ""
	}
	decoded, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return ""
	}
	var payload map[string]any
	if err := json.Unmarshal(decoded, &payload); err != nil {
		return ""
	}
	authClaim, ok := payload[openAICodexJWTClaimPath]
	if !ok {
		return ""
	}
	authMap, ok := authClaim.(map[string]any)
	if !ok {
		return ""
	}
	accountID, _ := authMap["chatgpt_account_id"].(string)
	return strings.TrimSpace(accountID)
}
