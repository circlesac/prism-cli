package anthropic

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"fmt"
	"html"
	"net"
	"net/http"
	"net/url"
	"os/exec"
	"runtime"
	"strings"
	"sync"
	"time"
)

const clientID = "9d1c250a-e61b-44d9-88ed-5944d1962f5e"
const scope = "user:profile user:inference user:sessions:claude_code user:mcp_servers user:file_upload"

type AuthorizationGrant struct {
	AuthorizationCode string `json:"authorization_code"`
	CodeVerifier      string `json:"code_verifier"`
	RedirectURI       string `json:"redirect_uri"`
	State             string `json:"state"`
}

type BrowserOpener func(string) error

type OAuth struct {
	OpenBrowser BrowserOpener
	Timeout     time.Duration
}

type loginResult struct {
	grant AuthorizationGrant
	err   error
}

func (o OAuth) Login(ctx context.Context) (AuthorizationGrant, error) {
	timeout := o.Timeout
	if timeout == 0 {
		timeout = 5 * time.Minute
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	verifier, challenge, err := generatePKCE()
	if err != nil {
		return AuthorizationGrant{}, fmt.Errorf("generate PKCE: %w", err)
	}
	state, err := randomBase64URL(32)
	if err != nil {
		return AuthorizationGrant{}, fmt.Errorf("generate OAuth state: %w", err)
	}
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return AuthorizationGrant{}, errors.New("could not start the Anthropic OAuth callback")
	}
	defer listener.Close()

	port := listener.Addr().(*net.TCPAddr).Port
	redirectURI := fmt.Sprintf("http://localhost:%d/callback", port)
	expectedHost := fmt.Sprintf("localhost:%d", port)
	result := make(chan loginResult, 1)
	var once sync.Once
	mux := http.NewServeMux()
	server := &http.Server{Handler: mux, ReadHeaderTimeout: 5 * time.Second}
	mux.HandleFunc("/callback", func(response http.ResponseWriter, request *http.Request) {
		if request.Method != http.MethodGet || !strings.EqualFold(request.Host, expectedHost) {
			http.Error(response, "Invalid callback", http.StatusBadRequest)
			return
		}
		query := request.URL.Query()
		if providerError := query.Get("error"); providerError != "" {
			message := query.Get("error_description")
			if message == "" {
				message = providerError
			}
			once.Do(func() { result <- loginResult{err: fmt.Errorf("Anthropic authorization failed: %s", message)} })
			writeCallbackPage(response, "Login failed", "Anthropic authorization did not complete.")
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
		once.Do(func() {
			result <- loginResult{grant: AuthorizationGrant{
				AuthorizationCode: code,
				CodeVerifier:      verifier,
				RedirectURI:       redirectURI,
				State:             state,
			}}
		})
		writeCallbackPage(response, "Login complete", "Return to Prism to finish saving this account.")
	})
	go func() { _ = server.Serve(listener) }()
	defer func() {
		shutdown, stop := context.WithTimeout(context.Background(), time.Second)
		defer stop()
		_ = server.Shutdown(shutdown)
	}()

	opener := o.OpenBrowser
	if opener == nil {
		opener = openBrowser
	}
	if err := opener(authorizeURL(redirectURI, challenge, state)); err != nil {
		return AuthorizationGrant{}, fmt.Errorf("open Anthropic login: %w", err)
	}

	select {
	case outcome := <-result:
		return outcome.grant, outcome.err
	case <-ctx.Done():
		return AuthorizationGrant{}, errors.New("Anthropic login timed out or was cancelled")
	}
}

func authorizeURL(redirectURI string, challenge string, state string) string {
	query := url.Values{
		"code":                  {"true"},
		"client_id":             {clientID},
		"response_type":         {"code"},
		"redirect_uri":          {redirectURI},
		"scope":                 {scope},
		"code_challenge":        {challenge},
		"code_challenge_method": {"S256"},
		"state":                 {state},
	}
	return "https://claude.com/cai/oauth/authorize?" + query.Encode()
}

func generatePKCE() (string, string, error) {
	verifier, err := randomBase64URL(48)
	if err != nil {
		return "", "", err
	}
	sum := sha256.Sum256([]byte(verifier))
	return verifier, base64.RawURLEncoding.EncodeToString(sum[:]), nil
}

func randomBase64URL(size int) (string, error) {
	value := make([]byte, size)
	if _, err := rand.Read(value); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(value), nil
}

func openBrowser(target string) error {
	var command *exec.Cmd
	switch runtime.GOOS {
	case "darwin":
		command = exec.Command("open", target)
	case "windows":
		command = exec.Command("rundll32", "url.dll,FileProtocolHandler", target)
	default:
		command = exec.Command("xdg-open", target)
	}
	return command.Run()
}

func writeCallbackPage(response http.ResponseWriter, title string, message string) {
	response.Header().Set("Content-Type", "text/html; charset=utf-8")
	_, _ = fmt.Fprintf(
		response,
		"<!doctype html><meta charset=utf-8><title>%s</title><h1>%s</h1><p>%s</p><script>setTimeout(()=>window.close(),2000)</script>",
		html.EscapeString(title), html.EscapeString(title), html.EscapeString(message),
	)
}
