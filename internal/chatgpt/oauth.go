package chatgpt

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"html"
	"io"
	"net"
	"net/http"
	"net/url"
	"os/exec"
	"runtime"
	"strings"
	"sync"
	"time"
)

type Bundle struct {
	AccessToken  string `json:"access_token"`
	RefreshToken string `json:"refresh_token"`
	AccountID    string `json:"account_id"`
	Alias        string `json:"alias,omitempty"`
	ExpiresAt    int64  `json:"expires_at"`
}

type tokenResponse struct {
	IDToken      string `json:"id_token"`
	AccessToken  string `json:"access_token"`
	RefreshToken string `json:"refresh_token"`
	ExpiresIn    int64  `json:"expires_in"`
}

type BrowserOpener func(string) error

type OAuth struct {
	Client      *http.Client
	OpenBrowser BrowserOpener
	Timeout     time.Duration
}

func (o OAuth) Login(ctx context.Context) (Bundle, error) {
	timeout := o.Timeout
	if timeout == 0 {
		timeout = 5 * time.Minute
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	verifier, challenge, err := generatePKCE()
	if err != nil {
		return Bundle{}, fmt.Errorf("generate PKCE: %w", err)
	}
	state, err := randomBase64URL(32)
	if err != nil {
		return Bundle{}, fmt.Errorf("generate OAuth state: %w", err)
	}

	listener, err := net.Listen("tcp", "127.0.0.1:1455")
	if err != nil {
		return Bundle{}, fmt.Errorf("listen for OAuth callback on port 1455: %w", err)
	}
	defer listener.Close()

	redirectURI := "http://localhost:1455/auth/callback"
	result := make(chan loginResult, 1)
	var once sync.Once
	mux := http.NewServeMux()
	server := &http.Server{
		Handler:           mux,
		ReadHeaderTimeout: 5 * time.Second,
	}

	mux.HandleFunc("/auth/callback", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
			return
		}
		query := r.URL.Query()
		if providerError := query.Get("error"); providerError != "" {
			message := query.Get("error_description")
			if message == "" {
				message = providerError
			}
			once.Do(func() { result <- loginResult{err: fmt.Errorf("OpenAI authorization failed: %s", message)} })
			writeCallbackPage(w, "Login failed", message)
			return
		}
		if query.Get("state") != state {
			once.Do(func() { result <- loginResult{err: errors.New("OAuth callback state did not match")} })
			http.Error(w, "Invalid callback state", http.StatusBadRequest)
			return
		}
		code := query.Get("code")
		if code == "" {
			once.Do(func() { result <- loginResult{err: errors.New("OAuth callback did not include a code")} })
			http.Error(w, "Missing authorization code", http.StatusBadRequest)
			return
		}

		tokens, exchangeErr := o.exchange(ctx, code, redirectURI, verifier)
		if exchangeErr != nil {
			once.Do(func() { result <- loginResult{err: exchangeErr} })
			writeCallbackPage(w, "Login failed", "The authorization code could not be exchanged.")
			return
		}
		bundle, bundleErr := bundleFromTokens(tokens, time.Now())
		once.Do(func() { result <- loginResult{bundle: bundle, err: bundleErr} })
		if bundleErr != nil {
			writeCallbackPage(w, "Login failed", bundleErr.Error())
			return
		}
		writeCallbackPage(w, "Login complete", "You can close this window and return to Prism.")
	})

	serverErrors := make(chan error, 1)
	go func() {
		if serveErr := server.Serve(listener); serveErr != nil && !errors.Is(serveErr, http.ErrServerClosed) {
			serverErrors <- serveErr
		}
	}()

	opener := o.OpenBrowser
	if opener == nil {
		opener = OpenBrowser
	}
	if err := opener(authorizeURL(redirectURI, challenge, state)); err != nil {
		_ = server.Shutdown(context.Background())
		return Bundle{}, fmt.Errorf("open browser: %w", err)
	}

	var login loginResult
	select {
	case login = <-result:
	case err := <-serverErrors:
		login.err = fmt.Errorf("serve OAuth callback: %w", err)
	case <-ctx.Done():
		login.err = errors.New("OAuth login timed out after 5 minutes")
	}

	shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer shutdownCancel()
	_ = server.Shutdown(shutdownCtx)
	if login.err != nil {
		return Bundle{}, login.err
	}
	return login.bundle, nil
}

type loginResult struct {
	bundle Bundle
	err    error
}

func (o OAuth) exchange(
	ctx context.Context,
	code string,
	redirectURI string,
	verifier string,
) (tokenResponse, error) {
	body := url.Values{
		"grant_type":    {"authorization_code"},
		"code":          {code},
		"redirect_uri":  {redirectURI},
		"client_id":     {"app_EMoamEEZ73f0CkXaXp7hrann"},
		"code_verifier": {verifier},
	}.Encode()
	request, err := http.NewRequestWithContext(
		ctx,
		http.MethodPost,
		"https://auth.openai.com/oauth/token",
		strings.NewReader(body),
	)
	if err != nil {
		return tokenResponse{}, fmt.Errorf("create token request: %w", err)
	}
	request.Header.Set("Content-Type", "application/x-www-form-urlencoded")

	client := o.Client
	if client == nil {
		client = &http.Client{Timeout: 30 * time.Second}
	}
	response, err := client.Do(request)
	if err != nil {
		return tokenResponse{}, errors.New("OpenAI token exchange could not be reached")
	}
	defer response.Body.Close()
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		_, _ = io.Copy(io.Discard, response.Body)
		return tokenResponse{}, fmt.Errorf("OpenAI token exchange returned HTTP %d", response.StatusCode)
	}

	var tokens tokenResponse
	if err := json.NewDecoder(io.LimitReader(response.Body, 1<<20)).Decode(&tokens); err != nil {
		return tokenResponse{}, errors.New("OpenAI token exchange returned invalid JSON")
	}
	return tokens, nil
}

func authorizeURL(redirectURI string, challenge string, state string) string {
	query := url.Values{
		"response_type":              {"code"},
		"client_id":                  {"app_EMoamEEZ73f0CkXaXp7hrann"},
		"redirect_uri":               {redirectURI},
		"scope":                      {"openid profile email offline_access"},
		"code_challenge":             {challenge},
		"code_challenge_method":      {"S256"},
		"id_token_add_organizations": {"true"},
		"codex_cli_simplified_flow":  {"true"},
		"state":                      {state},
		"originator":                 {"opencode"},
	}
	return "https://auth.openai.com/oauth/authorize?" + query.Encode()
}

func generatePKCE() (string, string, error) {
	verifier, err := randomBase64URL(64)
	if err != nil {
		return "", "", err
	}
	sum := sha256.Sum256([]byte(verifier))
	return verifier, base64.RawURLEncoding.EncodeToString(sum[:]), nil
}

func randomBase64URL(bytes int) (string, error) {
	value := make([]byte, bytes)
	if _, err := rand.Read(value); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(value), nil
}

func bundleFromTokens(tokens tokenResponse, now time.Time) (Bundle, error) {
	if strings.TrimSpace(tokens.AccessToken) == "" || strings.TrimSpace(tokens.RefreshToken) == "" {
		return Bundle{}, errors.New("OpenAI token response did not include both access and refresh tokens")
	}
	accountID := extractAccountID(tokens)
	if accountID == "" {
		return Bundle{}, errors.New("could not determine the ChatGPT account ID from the OAuth response")
	}
	expiresIn := tokens.ExpiresIn
	if expiresIn <= 0 {
		expiresIn = 3600
	}
	return Bundle{
		AccessToken:  tokens.AccessToken,
		RefreshToken: tokens.RefreshToken,
		AccountID:    accountID,
		Alias:        extractAlias(tokens, accountID),
		ExpiresAt:    now.Add(time.Duration(expiresIn) * time.Second).UnixMilli(),
	}, nil
}

func extractAccountID(tokens tokenResponse) string {
	for _, token := range []string{tokens.IDToken, tokens.AccessToken} {
		claims := jwtClaims(token)
		if claims == nil {
			continue
		}
		if id, ok := claims["chatgpt_account_id"].(string); ok && id != "" {
			return id
		}
		if auth, ok := claims["https://api.openai.com/auth"].(map[string]any); ok {
			if id, ok := auth["chatgpt_account_id"].(string); ok && id != "" {
				return id
			}
		}
		if organizations, ok := claims["organizations"].([]any); ok && len(organizations) > 0 {
			if organization, ok := organizations[0].(map[string]any); ok {
				if id, ok := organization["id"].(string); ok && id != "" {
					return id
				}
			}
		}
	}
	return ""
}

func extractAlias(tokens tokenResponse, accountID string) string {
	for _, token := range []string{tokens.IDToken, tokens.AccessToken} {
		claims := jwtClaims(token)
		if claims == nil {
			continue
		}
		profile, _ := claims["https://api.openai.com/profile"].(map[string]any)
		for _, value := range []any{
			claims["https://api.openai.com/profile.email"],
			profile["email"],
			claims["email"],
		} {
			if alias := cleanAlias(value); alias != "" {
				return alias
			}
		}
		for _, value := range []any{
			claims["https://api.openai.com/profile.name"],
			profile["name"],
			claims["name"],
		} {
			if alias := cleanAlias(value); alias != "" {
				if len(accountID) > 8 {
					accountID = accountID[:8]
				}
				return alias + " (" + accountID + ")"
			}
		}
	}
	return ""
}

func cleanAlias(value any) string {
	text, ok := value.(string)
	if !ok {
		return ""
	}
	text = strings.TrimSpace(text)
	if len(text) > 160 {
		text = text[:160]
	}
	return text
}

func jwtClaims(token string) map[string]any {
	parts := strings.Split(token, ".")
	if len(parts) != 3 {
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

func writeCallbackPage(w http.ResponseWriter, title string, message string) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_, _ = fmt.Fprintf(
		w,
		"<!doctype html><meta charset=utf-8><title>%s</title><h1>%s</h1><p>%s</p><script>setTimeout(()=>window.close(),2000)</script>",
		html.EscapeString(title),
		html.EscapeString(title),
		html.EscapeString(message),
	)
}

func OpenBrowser(target string) error {
	var command *exec.Cmd
	switch runtime.GOOS {
	case "darwin":
		command = exec.Command("open", target)
	case "windows":
		command = exec.Command("rundll32", "url.dll,FileProtocolHandler", target)
	default:
		command = exec.Command("xdg-open", target)
	}
	if err := command.Start(); err != nil {
		return err
	}
	return command.Process.Release()
}
