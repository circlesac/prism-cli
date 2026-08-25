package cli

import (
	"context"
	"crypto/rand"
	"crypto/subtle"
	"encoding/base64"
	"encoding/hex"
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
	direct bool
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
	case "auth", "login", "add", "list", "remove":
		return false
	default:
		return true
	}
}

func runGeminiCommand(ctx context.Context, args []string, stdout io.Writer, stderr io.Writer) error {
	if len(args) > 0 && args[0] == "usage" {
		if len(args) != 1 {
			return errors.New("usage: prism gemini usage")
		}
		return runGeminiUsage(ctx, stdout, stderr)
	}
	if len(args) == 1 && (args[0] == "--help" || args[0] == "-h" || args[0] == "help") {
		printGeminiHelp(stdout)
		return nil
	}
	if executable, executableErr := findGeminiCLIExecutable(); executableErr == nil && executable.direct {
		return runAntigravity(ctx, executable, withDefaultGeminiModel(args), os.Stdin, stdout, stderr)
	}
	account, passthrough, err := parseGeminiOptions(args)
	if err != nil {
		return err
	}
	client, err := prismClient(ctx, commonOptions{})
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
		return "", errors.New("no Gemini subscription accounts are registered; run 'prism gemini auth login'")
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
	if executable.direct {
		return runAntigravity(ctx, executable, args, stdin, stdout, stderr)
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

func runAntigravity(ctx context.Context, executable geminiCLIExecutable, args []string, stdin io.Reader, stdout io.Writer, stderr io.Writer) error {
	if err := disableAntigravityCreditOverages(); err != nil {
		return err
	}
	commandArgs := append(append([]string{}, executable.prefix...), args...)
	command := exec.CommandContext(ctx, executable.path, commandArgs...)
	command.Stdin = stdin
	command.Stdout = stdout
	command.Stderr = stderr
	command.Env = subscriptionGeminiEnvironment(os.Environ())
	if err := command.Run(); err != nil {
		var exitError *exec.ExitError
		if errors.As(err, &exitError) {
			return fmt.Errorf("Antigravity CLI exited with status %d", exitError.ExitCode())
		}
		return fmt.Errorf("could not run Antigravity CLI: %w", err)
	}
	return nil
}

var antigravitySettingsPath = defaultAntigravitySettingsPath

func defaultAntigravitySettingsPath() string {
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	return filepath.Join(home, ".gemini", "antigravity-cli", "settings.json")
}

func disableAntigravityCreditOverages() error {
	path := antigravitySettingsPath()
	if path == "" {
		return errors.New("could not locate Antigravity CLI settings")
	}
	settings := map[string]any{}
	contents, err := os.ReadFile(path)
	if err == nil {
		if len(strings.TrimSpace(string(contents))) != 0 {
			if err := json.Unmarshal(contents, &settings); err != nil {
				return errors.New("Antigravity CLI settings are invalid; fix settings.json before using Prism Gemini")
			}
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return errors.New("could not read Antigravity CLI settings")
	}
	if value, ok := settings["useG1Credits"].(bool); ok && !value {
		return nil
	}
	settings["useG1Credits"] = false
	encoded, err := json.MarshalIndent(settings, "", "  ")
	if err != nil {
		return errors.New("could not encode Antigravity CLI settings")
	}
	encoded = append(encoded, '\n')
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return errors.New("could not create Antigravity CLI settings directory")
	}
	temporary, err := os.CreateTemp(filepath.Dir(path), ".settings-*")
	if err != nil {
		return errors.New("could not write Antigravity CLI settings")
	}
	temporaryPath := temporary.Name()
	defer os.Remove(temporaryPath)
	if err := temporary.Chmod(0o600); err != nil {
		temporary.Close()
		return errors.New("could not protect Antigravity CLI settings")
	}
	if _, err := temporary.Write(encoded); err != nil {
		temporary.Close()
		return errors.New("could not write Antigravity CLI settings")
	}
	if err := temporary.Close(); err != nil {
		return errors.New("could not close Antigravity CLI settings")
	}
	if err := os.Rename(temporaryPath, path); err != nil {
		return errors.New("could not activate Antigravity CLI settings")
	}
	return nil
}

func findGeminiCLI() (geminiCLIExecutable, error) {
	if path, err := exec.LookPath("agy"); err == nil {
		return geminiCLIExecutable{path: path, direct: true}, nil
	}
	if path, err := exec.LookPath("gemini"); err == nil {
		return geminiCLIExecutable{path: path}, nil
	}
	if path, err := exec.LookPath("npx"); err == nil {
		return geminiCLIExecutable{path: path, prefix: []string{"--yes", "@google/gemini-cli"}}, nil
	}
	return geminiCLIExecutable{}, errors.New("Gemini CLI is not installed and npx is not on PATH")
}

func subscriptionGeminiEnvironment(environment []string) []string {
	filtered := make([]string, 0, len(environment))
	for _, entry := range environment {
		name, _, _ := strings.Cut(entry, "=")
		switch strings.ToUpper(name) {
		case "GEMINI_API_KEY", "GOOGLE_API_KEY", "GOOGLE_GEMINI_BASE_URL", "GOOGLE_GENAI_USE_VERTEXAI", "GOOGLE_VERTEX_BASE_URL":
			continue
		}
		filtered = append(filtered, entry)
	}
	return filtered
}

func parseGeminiOptions(args []string) (string, []string, error) {
	account, passthrough, err := parseClaudeOptions(args)
	if err != nil {
		return "", nil, err
	}
	return account, passthrough, nil
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
  prism gemini auth login|list|remove
  prism gemini [--account <alias-or-id>] [Gemini CLI arguments...]

	Runs Antigravity CLI (agy) with the signed-in Google Gemini subscription.
	AI Studio API keys are intentionally unsupported to prevent usage-based charges.
	Prism forces useG1Credits=false before every run to prevent paid overages.
	Use 'prism gemini usage' or 'prism usage' to show the subscription quota.
	The default model is gemini-3.7-flash-low; use --model gemini-3.1-pro-high for
hard software-engineering and multi-step tool-use work.
Run 'gemini --help' for Gemini CLI options.`)
}
