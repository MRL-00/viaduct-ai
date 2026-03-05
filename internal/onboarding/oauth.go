package onboarding

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
	errCh  chan error
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

	for attempt := 0; strings.TrimSpace(code) == ""; attempt++ {
		if attempt > 0 {
			fmt.Println("OAuth callback was not captured. Paste the full redirect URL from the browser address bar, or just the `code` value.")
		}
		input := strings.TrimSpace(promptWithDefault(reader, "Paste redirect URL or authorization code", ""))
		parsedCode, parsedState := parseOpenAIAuthorizationInput(input)
		if parsedState != "" && parsedState != flow.State {
			return nil, fmt.Errorf("oauth state mismatch")
		}
		code = parsedCode
		if attempt >= 2 && strings.TrimSpace(code) == "" {
			break
		}
	}
	if strings.TrimSpace(code) == "" {
		return nil, fmt.Errorf("missing authorization code: paste the full redirect URL or the `code` query parameter")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	creds, err := exchangeOpenAICodexAuthorizationCode(ctx, code, flow.Verifier)
	if err != nil {
		return nil, err
	}
	scopes := extractOpenAIScopes(creds.AccessToken)
	if creds.AccountID != "" {
		fmt.Printf("OpenAI OAuth complete for account %s\n", creds.AccountID)
	} else {
		fmt.Println("OpenAI OAuth complete.")
	}
	if len(scopes) > 0 {
		fmt.Printf("OpenAI OAuth scopes: %s\n", strings.Join(scopes, ", "))
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
	s := &openAICallbackServer{
		codeCh: make(chan string, 1),
		errCh:  make(chan error, 1),
	}

	mux := http.NewServeMux()
	mux.HandleFunc("/auth/callback", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("state") != expectedState {
			err := fmt.Errorf("oauth state mismatch")
			http.Error(w, err.Error(), http.StatusBadRequest)
			select {
			case s.errCh <- err:
			default:
			}
			return
		}
		if oauthErr := strings.TrimSpace(r.URL.Query().Get("error")); oauthErr != "" {
			description := strings.TrimSpace(r.URL.Query().Get("error_description"))
			err := fmt.Errorf("oauth callback returned error=%s description=%s", oauthErr, description)
			http.Error(w, err.Error(), http.StatusBadRequest)
			select {
			case s.errCh <- err:
			default:
			}
			return
		}
		code := strings.TrimSpace(r.URL.Query().Get("code"))
		if code == "" {
			err := fmt.Errorf("oauth callback missing authorization code")
			http.Error(w, err.Error(), http.StatusBadRequest)
			select {
			case s.errCh <- err:
			default:
			}
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

	s.srv = &http.Server{Handler: mux}

	listenAddrs := []string{"127.0.0.1:1455", "[::1]:1455"}
	started := 0
	for _, addr := range listenAddrs {
		ln, err := net.Listen("tcp", addr)
		if err != nil {
			continue
		}
		started++
		go func(listener net.Listener) {
			if err := s.srv.Serve(listener); err != nil && err != http.ErrServerClosed {
				select {
				case s.errCh <- err:
				default:
				}
			}
		}(ln)
	}
	if started == 0 {
		return nil, fmt.Errorf("could not bind callback server on 127.0.0.1:1455 or [::1]:1455")
	}
	return s, nil
}

func (s *openAICallbackServer) WaitForCode(ctx context.Context) (string, error) {
	select {
	case code := <-s.codeCh:
		return code, nil
	case err := <-s.errCh:
		return "", err
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
	input = strings.TrimSpace(input)
	if input == "" {
		return "", ""
	}
	if strings.Contains(input, "://") {
		u, err := url.Parse(input)
		if err != nil {
			return "", ""
		}
		return strings.TrimSpace(u.Query().Get("code")), strings.TrimSpace(u.Query().Get("state"))
	}
	if strings.Contains(input, "code=") || strings.Contains(input, "state=") {
		values, err := url.ParseQuery(input)
		if err == nil {
			return strings.TrimSpace(values.Get("code")), strings.TrimSpace(values.Get("state"))
		}
	}
	return input, ""
}

func exchangeOpenAICodexAuthorizationCode(ctx context.Context, code, verifier string) (*openAICodexOAuthCredentials, error) {
	form := url.Values{}
	form.Set("grant_type", "authorization_code")
	form.Set("code", code)
	form.Set("client_id", openAICodexClientID)
	form.Set("redirect_uri", openAICodexRedirectURI)
	form.Set("code_verifier", verifier)

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, openAICodexTokenURL, strings.NewReader(form.Encode()))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")

	resp, err := (&http.Client{Timeout: 20 * time.Second}).Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return nil, err
	}

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("oauth token exchange failed (%d): %s", resp.StatusCode, strings.TrimSpace(string(body)))
	}

	var payload struct {
		AccessToken  string `json:"access_token"`
		RefreshToken string `json:"refresh_token"`
		ExpiresIn    int64  `json:"expires_in"`
		IDToken      string `json:"id_token"`
	}
	if err := json.Unmarshal(body, &payload); err != nil {
		return nil, err
	}
	if strings.TrimSpace(payload.AccessToken) == "" {
		return nil, fmt.Errorf("oauth token response missing access_token")
	}

	accountID := extractOpenAIAccountID(payload.AccessToken)
	if accountID == "" {
		accountID = extractOpenAIAccountID(payload.IDToken)
	}

	creds := &openAICodexOAuthCredentials{
		AccessToken:  payload.AccessToken,
		RefreshToken: payload.RefreshToken,
		AccountID:    accountID,
	}
	if payload.ExpiresIn > 0 {
		creds.ExpiresAt = time.Now().Add(time.Duration(payload.ExpiresIn) * time.Second)
	}
	return creds, nil
}

func extractOpenAIAccountID(jwt string) string {
	claims := decodeJWTPayload(jwt)
	if len(claims) == 0 {
		return ""
	}
	if auth, ok := claims[openAICodexJWTClaimPath].(map[string]any); ok {
		if accountID, ok := auth["chatgpt_account_id"].(string); ok {
			return strings.TrimSpace(accountID)
		}
	}
	if accountID, ok := claims["chatgpt_account_id"].(string); ok {
		return strings.TrimSpace(accountID)
	}
	return ""
}

func extractOpenAIScopes(jwt string) []string {
	claims := decodeJWTPayload(jwt)
	if len(claims) == 0 {
		return nil
	}
	scopeValue, _ := claims["scope"].(string)
	if strings.TrimSpace(scopeValue) == "" {
		return nil
	}
	return strings.Fields(scopeValue)
}

func decodeJWTPayload(jwt string) map[string]any {
	parts := strings.Split(jwt, ".")
	if len(parts) < 2 {
		return nil
	}
	payload, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return nil
	}
	var claims map[string]any
	if err := json.Unmarshal(payload, &claims); err != nil {
		return nil
	}
	return claims
}
