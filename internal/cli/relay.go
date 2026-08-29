package cli

import (
	"context"
	"crypto/subtle"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"net/http/httputil"
	"net/url"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"syscall"
	"time"
)

const prismRelayTokenEnv = "PRISM_RELAY_TOKEN"

type providerRelay struct {
	server   *http.Server
	listener net.Listener
	provider string
}

func runRelayCommand(ctx context.Context, args []string, stdout io.Writer, stderr io.Writer) error {
	provider, port, options, help, err := parseRelayOptions(args)
	if err != nil {
		return err
	}
	if help {
		printRelayHelp(stdout)
		return nil
	}
	token := strings.TrimSpace(os.Getenv(prismRelayTokenEnv))
	if len(token) < 32 || strings.ContainsAny(token, " \t\r\n") {
		return errors.New("PRISM_RELAY_TOKEN must contain at least 32 non-whitespace characters")
	}
	client, err := prismClient(ctx, options)
	if err != nil {
		return err
	}
	account := ""
	if provider == "gemini" {
		accounts, listErr := client.List(ctx, "gemini")
		if listErr != nil {
			return listErr
		}
		account, err = selectGeminiAccount("", accounts)
		if err != nil {
			return err
		}
	}
	relay, err := startProviderRelay(client.BaseURL, client.Token, provider, account, token, port, stderr)
	if err != nil {
		return err
	}
	defer relay.close()

	actualPort := relay.listener.Addr().(*net.TCPAddr).Port
	fmt.Fprintf(stderr, "[prism-relay] listening on tcp://0.0.0.0:%d provider=%s\n", actualPort, provider)

	signalContext, stop := signal.NotifyContext(ctx, os.Interrupt, syscall.SIGTERM)
	defer stop()
	done := make(chan error, 1)
	go func() { done <- relay.server.Serve(relay.listener) }()
	select {
	case <-signalContext.Done():
		return nil
	case serveErr := <-done:
		if errors.Is(serveErr, http.ErrServerClosed) {
			return nil
		}
		return fmt.Errorf("Prism relay stopped: %w", serveErr)
	}
}

func parseRelayOptions(args []string) (string, int, commonOptions, bool, error) {
	var options commonOptions
	provider := ""
	port := -1
	for index := 0; index < len(args); index++ {
		argument := args[index]
		switch {
		case argument == "--help" || argument == "-h":
			return provider, port, options, true, nil
		case argument == "--profile":
			if options.profileSet {
				return "", 0, commonOptions{}, false, errors.New("--profile may be specified only once")
			}
			index++
			if index >= len(args) || strings.TrimSpace(args[index]) == "" {
				return "", 0, commonOptions{}, false, errors.New("--profile requires a value")
			}
			options.profile = strings.TrimSpace(args[index])
			options.profileSet = true
		case strings.HasPrefix(argument, "--profile="):
			if options.profileSet {
				return "", 0, commonOptions{}, false, errors.New("--profile may be specified only once")
			}
			options.profile = strings.TrimSpace(strings.TrimPrefix(argument, "--profile="))
			if options.profile == "" {
				return "", 0, commonOptions{}, false, errors.New("--profile requires a value")
			}
			options.profileSet = true
		case argument == "--port":
			index++
			if index >= len(args) {
				return "", 0, commonOptions{}, false, errors.New("--port requires a value")
			}
			parsed, parseErr := strconv.Atoi(args[index])
			if parseErr != nil || parsed < 0 || parsed > 65535 {
				return "", 0, commonOptions{}, false, fmt.Errorf("invalid --port value %q", args[index])
			}
			port = parsed
		case strings.HasPrefix(argument, "--port="):
			parsed, parseErr := strconv.Atoi(strings.TrimPrefix(argument, "--port="))
			if parseErr != nil || parsed < 0 || parsed > 65535 {
				return "", 0, commonOptions{}, false, fmt.Errorf("invalid --port value %q", strings.TrimPrefix(argument, "--port="))
			}
			port = parsed
		case strings.HasPrefix(argument, "-"):
			return "", 0, commonOptions{}, false, fmt.Errorf("unknown relay option %q", argument)
		case provider == "":
			provider = strings.ToLower(strings.TrimSpace(argument))
		default:
			return "", 0, commonOptions{}, false, fmt.Errorf("unexpected relay argument %q", argument)
		}
	}
	if provider != "claude" && provider != "codex" && provider != "gemini" {
		return "", 0, commonOptions{}, false, errors.New("relay provider must be claude, codex, or gemini")
	}
	if port < 0 {
		return "", 0, commonOptions{}, false, errors.New("relay requires --port")
	}
	return provider, port, options, false, nil
}

func startProviderRelay(prismURL string, prismCredential string, provider string, account string, localToken string, port int, stderr io.Writer) (*providerRelay, error) {
	target, err := url.Parse(prismURL)
	if err != nil || (target.Scheme != "https" && target.Scheme != "http") || target.Host == "" {
		return nil, errors.New("Prism URL is invalid")
	}
	if strings.TrimSpace(prismCredential) == "" || strings.ContainsAny(prismCredential, " \t\r\n") {
		return nil, errors.New("Circles credential is invalid")
	}
	if len(localToken) < 32 || strings.ContainsAny(localToken, " \t\r\n") {
		return nil, errors.New("Prism relay credential is invalid")
	}
	if provider != "claude" && provider != "codex" && provider != "gemini" {
		return nil, errors.New("Prism relay provider is invalid")
	}
	if provider == "gemini" && strings.TrimSpace(account) == "" {
		return nil, errors.New("Prism Gemini relay requires an account")
	}

	proxy := httputil.NewSingleHostReverseProxy(target)
	director := proxy.Director
	proxy.Director = func(request *http.Request) {
		director(request)
		request.Host = target.Host
		request.Header.Del("Authorization")
		request.Header.Del("X-Api-Key")
		request.Header.Del("X-Goog-Api-Key")
		request.Header.Del("X-Prism-Relay")
		request.Header.Del("X-Prism-Anthropic-Account")
		request.Header.Del("X-Prism-Gemini-Account")
		request.Header.Set("Authorization", "Bearer "+prismCredential)
		if provider == "gemini" {
			request.Header.Set("X-Prism-Gemini-Account", "b64:"+base64.RawURLEncoding.EncodeToString([]byte(account)))
		}
	}
	proxy.ErrorLog = log.New(stderr, "prism relay: ", 0)
	proxy.ErrorHandler = func(response http.ResponseWriter, _ *http.Request, _ error) {
		http.Error(response, "Prism could not be reached", http.StatusBadGateway)
	}

	handler := http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		if request.Method == http.MethodGet && request.URL.Path == "/__prism_relay/health" {
			response.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(response).Encode(map[string]any{"ok": true, "provider": provider})
			return
		}
		customAuthorized := subtle.ConstantTimeCompare([]byte(request.Header.Get("X-Prism-Relay")), []byte(localToken)) == 1
		bearerAuthorized := subtle.ConstantTimeCompare([]byte(strings.TrimPrefix(request.Header.Get("Authorization"), "Bearer ")), []byte(localToken)) == 1
		if !customAuthorized && !bearerAuthorized {
			http.Error(response, "Unauthorized", http.StatusUnauthorized)
			return
		}
		if !relayPathAllowed(provider, request.URL.Path) {
			http.Error(response, "Provider route is not available through this relay", http.StatusForbidden)
			return
		}
		proxy.ServeHTTP(response, request)
	})
	listener, err := net.Listen("tcp", fmt.Sprintf("0.0.0.0:%d", port))
	if err != nil {
		return nil, fmt.Errorf("could not start Prism relay: %w", err)
	}
	return &providerRelay{
		provider: provider,
		listener: listener,
		server: &http.Server{
			Handler:           handler,
			ReadHeaderTimeout: 5 * time.Second,
			IdleTimeout:       120 * time.Second,
		},
	}, nil
}

func relayPathAllowed(provider string, path string) bool {
	switch provider {
	case "claude":
		return path == "/v1/messages" || strings.HasPrefix(path, "/v1/messages/")
	case "codex":
		return path == "/v1/responses" || strings.HasPrefix(path, "/v1/responses/")
	case "gemini":
		return strings.HasPrefix(path, "/v1beta/")
	default:
		return false
	}
}

func (relay *providerRelay) close() {
	if relay == nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	_ = relay.server.Shutdown(ctx)
	_ = relay.listener.Close()
}

func printRelayHelp(output io.Writer) {
	fmt.Fprintln(output, `Usage:
  prism relay claude|codex|gemini --port <port> [--profile <name>]

Starts a short-lived authenticated provider relay for a local runtime adapter.
The caller must supply a random PRISM_RELAY_TOKEN of at least 32 characters.
The relay keeps the Circles credential and provider account selection outside
the provider process. Port 0 selects an available port.`)
}
