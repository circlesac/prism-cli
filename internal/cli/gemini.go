package cli

import (
	"context"
	"crypto/rand"
	"crypto/subtle"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"net/http/httputil"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/circlesac/prism-cli/internal/api"
)

const defaultGeminiModel = "gemini-3.7-flash-low"

type geminiCLIExecutable struct {
	path   string
	prefix []string
}

type geminiBridge struct {
	server      *http.Server
	url         string
	headerName  string
	headerValue string
}

var findGeminiCLIExecutable = findGeminiCLI

func isGeminiCLIInvocation(args []string) bool {
	if len(args) == 0 {
		return true
	}
	switch args[0] {
	case "auth", "login", "add", "list", "remove", "usage":
		return false
	default:
		return true
	}
}

func runGeminiCommand(ctx context.Context, args []string, stdout io.Writer, stderr io.Writer) error {
	if len(args) == 1 && (args[0] == "--help" || args[0] == "-h" || args[0] == "help") {
		printGeminiHelp(stdout)
		return nil
	}
	options, account, passthrough, err := parseGeminiOptions(args)
	if err != nil {
		return err
	}
	client, err := prismClient(ctx, options)
	if err != nil {
		return err
	}
	accounts, err := client.List(ctx, "gemini")
	if err != nil {
		return err
	}
	selectedAccount, err := selectGeminiAccount(account, accounts)
	if err != nil {
		return err
	}
	return runGemini(ctx, client.BaseURL, client.Token, selectedAccount, withDefaultGeminiModel(passthrough), os.Stdin, stdout, stderr)
}

func selectGeminiAccount(selector string, accounts []api.Credential) (string, error) {
	if selector != "" {
		matches := make([]string, 0, 1)
		for _, account := range accounts {
			if account.ID == selector || account.Name == selector {
				matches = append(matches, account.ID)
			}
		}
		if len(matches) == 0 {
			return "", fmt.Errorf("Gemini subscription account %q is not registered", selector)
		}
		if len(matches) > 1 {
			return "", fmt.Errorf("Gemini subscription account %q is ambiguous; use its credential ID", selector)
		}
		return matches[0], nil
	}
	if len(accounts) == 0 {
		return "", errors.New("no Gemini subscription accounts are registered; run 'prism gemini auth import'")
	}
	return rotateProviderAccount("gemini", accounts)
}

func runGemini(
	ctx context.Context,
	prismURL string,
	prismCredential string,
	account string,
	args []string,
	stdin io.Reader,
	stdout io.Writer,
	stderr io.Writer,
) error {
	executable, err := findGeminiCLIExecutable()
	if err != nil {
		return err
	}

	bridge, err := startGeminiBridge(prismURL, prismCredential, account, stderr)
	if err != nil {
		return err
	}
	defer bridge.close()

	settingsDirectory, err := os.MkdirTemp("", "prism-gemini-")
	if err != nil {
		return errors.New("could not create Gemini CLI gateway settings")
	}
	defer os.RemoveAll(settingsDirectory)
	settingsPath := filepath.Join(settingsDirectory, "settings.json")
	if err := os.WriteFile(settingsPath, []byte(`{"security":{"auth":{"selectedType":"gateway","useExternal":true}}}`), 0o600); err != nil {
		return errors.New("could not create Gemini CLI gateway settings")
	}

	commandArgs := append(append([]string{}, executable.prefix...), args...)
	command := exec.CommandContext(ctx, executable.path, commandArgs...)
	command.Stdin = stdin
	command.Stdout = stdout
	command.Stderr = stderr
	command.Env = geminiEnvironment(os.Environ(), bridge.url, bridge.headerName+": "+bridge.headerValue, settingsPath, isAutomatedGeminiPrompt(args))
	if err := command.Run(); err != nil {
		var exitError *exec.ExitError
		if errors.As(err, &exitError) {
			return fmt.Errorf("Gemini CLI exited with status %d", exitError.ExitCode())
		}
		return fmt.Errorf("could not run Gemini CLI: %w", err)
	}
	return nil
}
func findGeminiCLI() (geminiCLIExecutable, error) {
	if path, err := exec.LookPath("gemini"); err == nil {
		return geminiCLIExecutable{path: path}, nil
	}
	if path, err := exec.LookPath("npx"); err == nil {
		return geminiCLIExecutable{path: path, prefix: []string{"--yes", "@google/gemini-cli"}}, nil
	}
	return geminiCLIExecutable{}, errors.New("Gemini CLI is not installed and npx is not on PATH")
}
func parseGeminiOptions(args []string) (commonOptions, string, []string, error) {
	var options commonOptions
	var account string
	var passthrough []string
	for index := 0; index < len(args); index++ {
		argument := args[index]
		switch {
		case argument == "--":
			return options, account, append(passthrough, args[index:]...), nil
		case argument == "--profile":
			if options.profileSet {
				return commonOptions{}, "", nil, errors.New("--profile may be specified only once")
			}
			index++
			if index >= len(args) || strings.TrimSpace(args[index]) == "" || args[index] == "--" {
				return commonOptions{}, "", nil, errors.New("--profile requires a value")
			}
			options.profile = strings.TrimSpace(args[index])
			options.profileSet = true
		case strings.HasPrefix(argument, "--profile="):
			if options.profileSet {
				return commonOptions{}, "", nil, errors.New("--profile may be specified only once")
			}
			options.profile = strings.TrimSpace(strings.TrimPrefix(argument, "--profile="))
			if options.profile == "" {
				return commonOptions{}, "", nil, errors.New("--profile requires a value")
			}
			options.profileSet = true
		case argument == "--account":
			if account != "" {
				return commonOptions{}, "", nil, errors.New("--account may be specified only once")
			}
			index++
			if index >= len(args) || strings.TrimSpace(args[index]) == "" || args[index] == "--" {
				return commonOptions{}, "", nil, errors.New("--account requires a value")
			}
			account = strings.TrimSpace(args[index])
		case strings.HasPrefix(argument, "--account="):
			if account != "" {
				return commonOptions{}, "", nil, errors.New("--account may be specified only once")
			}
			account = strings.TrimSpace(strings.TrimPrefix(argument, "--account="))
			if account == "" {
				return commonOptions{}, "", nil, errors.New("--account requires a value")
			}
		default:
			passthrough = append(passthrough, argument)
		}
	}
	return options, account, passthrough, nil
}

func withDefaultGeminiModel(args []string) []string {
	for index, argument := range args {
		if argument == "--" {
			break
		}
		if argument == "-m" || argument == "--model" || strings.HasPrefix(argument, "--model=") {
			return args
		}
		if index > 0 && (args[index-1] == "-m" || args[index-1] == "--model") {
			return args
		}
	}
	return append([]string{"--model", defaultGeminiModel}, args...)
}

func startGeminiBridge(prismURL string, prismCredential string, account string, stderr io.Writer) (*geminiBridge, error) {
	target, err := url.Parse(prismURL)
	if err != nil || (target.Scheme != "https" && target.Scheme != "http") || target.Host == "" {
		return nil, errors.New("Prism URL is invalid")
	}
	if strings.TrimSpace(prismCredential) == "" || strings.ContainsAny(prismCredential, " \t\r\n") {
		return nil, errors.New("Circles credential is invalid")
	}
	if strings.TrimSpace(account) == "" || strings.ContainsAny(account, "\r\n") {
		return nil, errors.New("Gemini account selector is invalid")
	}
	credentialBytes := make([]byte, 32)
	if _, err := rand.Read(credentialBytes); err != nil {
		return nil, errors.New("could not create a local Gemini credential")
	}
	localHeaderName := "X-Prism-Gemini-Bridge"
	localHeaderValue := hex.EncodeToString(credentialBytes)

	proxy := httputil.NewSingleHostReverseProxy(target)
	director := proxy.Director
	proxy.Director = func(request *http.Request) {
		director(request)
		request.Host = target.Host
		request.Header.Del("Authorization")
		request.Header.Del("X-Goog-Api-Key")
		request.Header.Del(localHeaderName)
		request.Header.Del("X-Prism-Gemini-Provider")
		request.Header.Set("Authorization", "Bearer "+prismCredential)
		request.Header.Set("X-Prism-Gemini-Account", "b64:"+base64.RawURLEncoding.EncodeToString([]byte(account)))
	}
	proxy.ErrorLog = log.New(stderr, "prism: ", 0)
	proxy.ErrorHandler = func(response http.ResponseWriter, _ *http.Request, _ error) {
		http.Error(response, "Prism could not be reached", http.StatusBadGateway)
	}

	handler := http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		if subtle.ConstantTimeCompare([]byte(request.Header.Get(localHeaderName)), []byte(localHeaderValue)) != 1 {
			http.Error(response, "Unauthorized", http.StatusUnauthorized)
			return
		}
		proxy.ServeHTTP(response, request)
	})
	var listenConfig net.ListenConfig
	listener, err := listenConfig.Listen(context.Background(), "tcp", "127.0.0.1:0")
	if err != nil {
		return nil, errors.New("could not start the local Gemini bridge")
	}
	server := &http.Server{Handler: handler, ReadHeaderTimeout: 5 * time.Second, IdleTimeout: 120 * time.Second}
	go func() { _ = server.Serve(listener) }()
	return &geminiBridge{
		server: server, url: "http://" + listener.Addr().String(),
		headerName: localHeaderName, headerValue: localHeaderValue,
	}, nil
}

func (bridge *geminiBridge) close() {
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	_ = bridge.server.Shutdown(ctx)
}

func isAutomatedGeminiPrompt(args []string) bool {
	for _, argument := range args {
		if argument == "-p" || argument == "--prompt" || strings.HasPrefix(argument, "--prompt=") {
			return true
		}
	}
	return false
}

func geminiEnvironment(environment []string, baseURL string, customHeaders string, settingsPath string, trustWorkspace bool) []string {
	filtered := make([]string, 0, len(environment)+4)
	for _, entry := range environment {
		name, _, _ := strings.Cut(entry, "=")
		switch strings.ToUpper(name) {
		case "GOOGLE_GEMINI_BASE_URL", "GEMINI_API_KEY", "GOOGLE_API_KEY", "GOOGLE_GENAI_USE_GCA",
			"GOOGLE_GENAI_USE_VERTEXAI", "GOOGLE_VERTEX_BASE_URL", "GEMINI_CLI_CUSTOM_HEADERS",
			"GEMINI_CLI_SYSTEM_SETTINGS_PATH":
			continue
		}
		if trustWorkspace && strings.EqualFold(name, "GEMINI_CLI_TRUST_WORKSPACE") {
			continue
		}
		filtered = append(filtered, entry)
	}
	filtered = append(filtered,
		"GOOGLE_GEMINI_BASE_URL="+baseURL,
		"GEMINI_CLI_CUSTOM_HEADERS="+customHeaders,
		"GEMINI_CLI_SYSTEM_SETTINGS_PATH="+settingsPath,
	)
	if trustWorkspace {
		filtered = append(filtered, "GEMINI_CLI_TRUST_WORKSPACE=true")
	}
	return filtered
}

func printGeminiHelp(output io.Writer) {
	_, _ = fmt.Fprintln(output, `Usage:
  prism gemini auth import|login|list|remove
  prism gemini [--profile <name>] [--account <alias-or-id>] [Gemini CLI arguments...]

Runs the official Gemini CLI through Prism's registered subscription accounts.
Accounts rotate automatically unless --account selects one. AI Studio API keys
are intentionally unsupported to prevent usage-based charges. Use
'prism gemini usage' or 'prism usage' to show every registered account.
The default model is gemini-3.7-flash-low; use --model gemini-3.1-pro-high for
hard software-engineering and multi-step tool-use work.
Run 'gemini --help' for Gemini CLI options.`)
}
