package gemini

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
	"os"
	"strings"
	"sync"
	"time"

	"github.com/circlesac/prism-cli/internal/chatgpt"
)

type Bundle struct {
	AccessToken  string `json:"access_token"`
	RefreshToken string `json:"refresh_token"`
	ProjectID    string `json:"project_id"`
	Email        string `json:"email,omitempty"`
	Alias        string `json:"alias,omitempty"`
	AccountID    string `json:"account_id,omitempty"`
	ExpiresAt    int64  `json:"expires_at"`
	AuthMethod   string `json:"auth_method,omitempty"`
	UserAgent    string `json:"antigravity_user_agent,omitempty"`
}

type tokenResponse struct {
	AccessToken  string `json:"access_token"`
	RefreshToken string `json:"refresh_token"`
	ExpiresIn    int64  `json:"expires_in"`
}

type OAuth struct {
	Client       *http.Client
	ClientID     string
	ClientSecret string
	OpenBrowser  chatgpt.BrowserOpener
	Timeout      time.Duration
	Sleep        func(time.Duration)
}

var bundledClientIDBase64 string
var bundledClientSecretBase64 string

type loginResult struct {
	tokens tokenResponse
	err    error
}

func (o OAuth) Login(ctx context.Context) (Bundle, error) {
	clientID, clientSecret, err := o.credentials()
	if err != nil {
		return Bundle{}, err
	}
	timeout := o.Timeout
	if timeout == 0 {
		timeout = 10 * time.Minute
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
	listener, err := net.Listen("tcp", "127.0.0.1:1456")
	if err != nil {
		return Bundle{}, fmt.Errorf("listen for OAuth callback on port 1456: %w", err)
	}
	defer listener.Close()

	redirectURI := "http://localhost:1456/auth/callback"
	result := make(chan loginResult, 1)
	var once sync.Once
	mux := http.NewServeMux()
	server := &http.Server{Handler: mux, ReadHeaderTimeout: 5 * time.Second}
	mux.HandleFunc("/auth/callback", func(response http.ResponseWriter, request *http.Request) {
		query := request.URL.Query()
		if providerError := query.Get("error"); providerError != "" {
			message := query.Get("error_description")
			if message == "" {
				message = providerError
			}
			once.Do(func() { result <- loginResult{err: fmt.Errorf("Google authorization failed: %s", message)} })
			writeCallbackPage(response, "Login failed", message)
			return
		}
		if query.Get("state") != state {
			once.Do(func() { result <- loginResult{err: errors.New("OAuth callback state did not match")} })
			http.Error(response, "Invalid callback state", http.StatusBadRequest)
			return
		}
		code := query.Get("code")
		if code == "" {
			once.Do(func() { result <- loginResult{err: errors.New("OAuth callback did not include a code")} })
			http.Error(response, "Missing authorization code", http.StatusBadRequest)
			return
		}
		tokens, exchangeErr := o.exchange(ctx, code, redirectURI, verifier, clientID, clientSecret)
		once.Do(func() { result <- loginResult{tokens: tokens, err: exchangeErr} })
		if exchangeErr != nil {
			writeCallbackPage(response, "Login failed", "The authorization code could not be exchanged.")
			return
		}
		writeCallbackPage(response, "Login complete", "You can close this window and return to Prism.")
	})

	serverErrors := make(chan error, 1)
	go func() {
		if serveErr := server.Serve(listener); serveErr != nil && !errors.Is(serveErr, http.ErrServerClosed) {
			serverErrors <- serveErr
		}
	}()
	opener := o.OpenBrowser
	if opener == nil {
		opener = chatgpt.OpenBrowser
	}
	if err := opener(authorizeURL(redirectURI, challenge, state, clientID)); err != nil {
		_ = server.Shutdown(context.Background())
		return Bundle{}, fmt.Errorf("open browser: %w", err)
	}

	var login loginResult
	select {
	case login = <-result:
	case err := <-serverErrors:
		login.err = fmt.Errorf("serve OAuth callback: %w", err)
	case <-ctx.Done():
		login.err = errors.New("Google OAuth login timed out")
	}
	shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer shutdownCancel()
	_ = server.Shutdown(shutdownCtx)
	if login.err != nil {
		return Bundle{}, login.err
	}
	if login.tokens.AccessToken == "" || login.tokens.RefreshToken == "" {
		return Bundle{}, errors.New("Google token response did not include both access and refresh tokens")
	}
	client := o.client()
	projectID, err := o.setupCodeAssistProject(ctx, client, login.tokens.AccessToken)
	if err != nil {
		return Bundle{}, err
	}
	email, name, err := profile(ctx, client, login.tokens.AccessToken)
	if err != nil {
		return Bundle{}, err
	}
	alias := email
	if alias == "" {
		alias = name
	}
	expiresIn := login.tokens.ExpiresIn
	if expiresIn <= 0 {
		expiresIn = 3600
	}
	return Bundle{
		AccessToken:  login.tokens.AccessToken,
		RefreshToken: login.tokens.RefreshToken,
		ProjectID:    projectID,
		Email:        email,
		Alias:        alias,
		AccountID:    projectID,
		ExpiresAt:    time.Now().Add(time.Duration(expiresIn) * time.Second).UnixMilli(),
		AuthMethod:   "code-assist",
	}, nil
}

func (o OAuth) client() *http.Client {
	if o.Client != nil {
		return o.Client
	}
	return &http.Client{Timeout: 30 * time.Second}
}

func (o OAuth) credentials() (string, string, error) {
	clientID := strings.TrimSpace(o.ClientID)
	if clientID == "" {
		clientID = strings.TrimSpace(os.Getenv("PRISM_GEMINI_OAUTH_CLIENT_ID"))
	}
	if clientID == "" && bundledClientIDBase64 != "" {
		decoded, err := base64.StdEncoding.DecodeString(bundledClientIDBase64)
		if err != nil {
			return "", "", errors.New("bundled Gemini OAuth client ID is invalid")
		}
		clientID = string(decoded)
	}
	clientSecret := strings.TrimSpace(o.ClientSecret)
	if clientSecret == "" {
		clientSecret = strings.TrimSpace(os.Getenv("PRISM_GEMINI_OAUTH_CLIENT_SECRET"))
	}
	if clientSecret == "" && bundledClientSecretBase64 != "" {
		decoded, err := base64.StdEncoding.DecodeString(bundledClientSecretBase64)
		if err != nil {
			return "", "", errors.New("bundled Gemini OAuth client secret is invalid")
		}
		clientSecret = string(decoded)
	}
	if clientID == "" || clientSecret == "" {
		return "", "", errors.New("Gemini OAuth is available in official Prism releases; source builds must set PRISM_GEMINI_OAUTH_CLIENT_ID and PRISM_GEMINI_OAUTH_CLIENT_SECRET")
	}
	return clientID, clientSecret, nil
}

func (o OAuth) exchange(ctx context.Context, code string, redirectURI string, verifier string, clientID string, clientSecret string) (tokenResponse, error) {
	body := url.Values{
		"grant_type":    {"authorization_code"},
		"code":          {code},
		"redirect_uri":  {redirectURI},
		"client_id":     {clientID},
		"client_secret": {clientSecret},
		"code_verifier": {verifier},
	}.Encode()
	request, _ := http.NewRequestWithContext(ctx, http.MethodPost, "https://oauth2.googleapis.com/token", strings.NewReader(body))
	request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	var tokens tokenResponse
	if err := doJSON(o.client(), request, &tokens); err != nil {
		return tokenResponse{}, fmt.Errorf("Google token exchange: %w", err)
	}
	return tokens, nil
}

func profile(ctx context.Context, client *http.Client, accessToken string) (string, string, error) {
	request, _ := http.NewRequestWithContext(ctx, http.MethodGet, "https://www.googleapis.com/oauth2/v3/userinfo", nil)
	request.Header.Set("Authorization", "Bearer "+accessToken)
	var result struct {
		Email string `json:"email"`
		Name  string `json:"name"`
	}
	if err := doJSON(client, request, &result); err != nil {
		return "", "", fmt.Errorf("Google profile: %w", err)
	}
	return result.Email, result.Name, nil
}

func (o OAuth) setupCodeAssistProject(ctx context.Context, client *http.Client, accessToken string) (string, error) {
	metadata := map[string]string{
		"ideType": "IDE_UNSPECIFIED", "platform": "PLATFORM_UNSPECIFIED", "pluginType": "GEMINI",
	}
	var loaded struct {
		CurrentTier             any    `json:"currentTier"`
		CloudAICompanionProject string `json:"cloudaicompanionProject"`
		AllowedTiers            []struct {
			TierID string `json:"tierId"`
		} `json:"allowedTiers"`
		IneligibleTiers []struct {
			TierID        string `json:"tierId"`
			ReasonCode    string `json:"reasonCode"`
			ValidationURL string `json:"validationUrl"`
		} `json:"ineligibleTiers"`
	}
	if err := codeAssistJSON(ctx, client, accessToken, "v1internal:loadCodeAssist", map[string]any{"metadata": metadata}, &loaded); err != nil {
		return "", fmt.Errorf("load Code Assist: %w", err)
	}
	if loaded.CurrentTier != nil && loaded.CloudAICompanionProject != "" {
		return loaded.CloudAICompanionProject, nil
	}
	for _, tier := range loaded.IneligibleTiers {
		if tier.ReasonCode == "VALIDATION_REQUIRED" && tier.ValidationURL != "" {
			return "", fmt.Errorf("Gemini requires validation at %s", tier.ValidationURL)
		}
	}
	canOnboard := false
	for _, tier := range loaded.AllowedTiers {
		if tier.TierID == "free-tier" {
			canOnboard = true
		}
	}
	if !canOnboard {
		return "", errors.New("Gemini Code Assist free tier is unavailable for this account")
	}
	var operation struct {
		Name     string `json:"name"`
		Done     bool   `json:"done"`
		Response struct {
			Project struct {
				ID string `json:"id"`
			} `json:"cloudaicompanionProject"`
		} `json:"response"`
	}
	if err := codeAssistJSON(ctx, client, accessToken, "v1internal:onboardUser", map[string]any{
		"tierId": "free-tier", "metadata": metadata,
	}, &operation); err != nil {
		return "", fmt.Errorf("onboard Code Assist: %w", err)
	}
	if operation.Done && operation.Response.Project.ID != "" {
		return operation.Response.Project.ID, nil
	}
	sleep := o.Sleep
	if sleep == nil {
		sleep = time.Sleep
	}
	for range 60 {
		sleep(5 * time.Second)
		request, _ := http.NewRequestWithContext(ctx, http.MethodGet, "https://cloudcode-pa.googleapis.com/v1internal/"+strings.TrimPrefix(operation.Name, "/"), nil)
		request.Header.Set("Authorization", "Bearer "+accessToken)
		if err := doJSON(client, request, &operation); err != nil {
			continue
		}
		if operation.Done && operation.Response.Project.ID != "" {
			return operation.Response.Project.ID, nil
		}
	}
	return "", errors.New("Gemini Code Assist onboarding timed out")
}

func codeAssistJSON(ctx context.Context, client *http.Client, token string, method string, body any, output any) error {
	encoded, _ := json.Marshal(body)
	request, _ := http.NewRequestWithContext(ctx, http.MethodPost, "https://cloudcode-pa.googleapis.com/"+method, strings.NewReader(string(encoded)))
	request.Header.Set("Authorization", "Bearer "+token)
	request.Header.Set("Content-Type", "application/json")
	return doJSON(client, request, output)
}

func doJSON(client *http.Client, request *http.Request, output any) error {
	response, err := client.Do(request)
	if err != nil {
		return errors.New("request could not be reached")
	}
	defer response.Body.Close()
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		_, _ = io.Copy(io.Discard, response.Body)
		return fmt.Errorf("HTTP %d", response.StatusCode)
	}
	if err := json.NewDecoder(io.LimitReader(response.Body, 2<<20)).Decode(output); err != nil {
		return errors.New("response contained invalid JSON")
	}
	return nil
}

func authorizeURL(redirectURI string, challenge string, state string, clientID string) string {
	query := url.Values{
		"response_type":         {"code"},
		"client_id":             {clientID},
		"redirect_uri":          {redirectURI},
		"scope":                 {"https://www.googleapis.com/auth/cloud-platform https://www.googleapis.com/auth/userinfo.email https://www.googleapis.com/auth/userinfo.profile"},
		"code_challenge":        {challenge},
		"code_challenge_method": {"S256"},
		"access_type":           {"offline"},
		"prompt":                {"consent"},
		"state":                 {state},
	}
	return "https://accounts.google.com/o/oauth2/v2/auth?" + query.Encode()
}

func generatePKCE() (string, string, error) {
	verifier, err := randomBase64URL(48)
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

func writeCallbackPage(response http.ResponseWriter, title string, message string) {
	response.Header().Set("Content-Type", "text/html; charset=utf-8")
	_, _ = fmt.Fprintf(
		response,
		"<!doctype html><meta charset=utf-8><title>%s</title><h1>%s</h1><p>%s</p><script>setTimeout(()=>window.close(),2000)</script>",
		html.EscapeString(title), html.EscapeString(title), html.EscapeString(message),
	)
}
