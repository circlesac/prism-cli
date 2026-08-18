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
	"strings"
	"time"
)

type claudeBridge struct {
	server      *http.Server
	url         string
	headerName  string
	headerValue string
}

func runClaudeCommand(ctx context.Context, args []string, stdout io.Writer, stderr io.Writer) error {
	if len(args) == 1 && (args[0] == "--help" || args[0] == "-h") {
		printClaudeHelp(stdout)
		return nil
	}
	if len(args) > 0 && args[0] == "login" {
		options, positionals, err := parseCommonOptions(args[1:])
		if err != nil {
			return err
		}
		if len(positionals) != 0 || options.name != "" || options.providerAccountID != "" || options.ownerID != "" {
			return errors.New("usage: prism claude login [--profile <name>]")
		}
		client, err := prismClient(ctx, options)
		if err != nil {
			return err
		}
		return loginProvider(ctx, "anthropic", client, stdout)
	}
	account, remainingArgs, err := parseClaudeOptions(args)
	if err != nil {
		return err
	}
	client, err := prismClient(ctx, commonOptions{})
	if err != nil {
		return err
	}
	return runClaude(ctx, client.BaseURL, client.Token, account, remainingArgs, os.Stdin, stdout, stderr)
}

func runClaude(
	ctx context.Context,
	prismURL string,
	prismCredential string,
	prismAccount string,
	args []string,
	stdin io.Reader,
	stdout io.Writer,
	stderr io.Writer,
) error {
	claudePath, err := exec.LookPath("claude")
	if err != nil {
		return errors.New("Claude Code is not installed or is not on PATH")
	}
	bridge, err := startClaudeBridge(prismURL, prismCredential, prismAccount, stderr)
	if err != nil {
		return err
	}
	defer bridge.close()

	command := exec.CommandContext(ctx, claudePath, args...)
	command.Stdin = stdin
	command.Stdout = stdout
	command.Stderr = stderr
	command.Env = claudeEnvironment(
		os.Environ(),
		bridge.url,
		bridge.headerName+": "+bridge.headerValue,
	)
	if err := command.Run(); err != nil {
		var exitError *exec.ExitError
		if errors.As(err, &exitError) {
			return fmt.Errorf("Claude Code exited with status %d", exitError.ExitCode())
		}
		return fmt.Errorf("could not run Claude Code: %w", err)
	}
	return nil
}

func startClaudeBridge(prismURL string, prismCredential string, prismAnthropicAccount string, stderr io.Writer) (*claudeBridge, error) {
	target, err := url.Parse(prismURL)
	if err != nil || (target.Scheme != "https" && target.Scheme != "http") || target.Host == "" {
		return nil, errors.New("Prism URL is invalid")
	}
	if strings.TrimSpace(prismCredential) == "" || strings.ContainsAny(prismCredential, " \t\r\n") {
		return nil, errors.New("Circles credential is invalid")
	}
	if strings.ContainsAny(prismAnthropicAccount, "\r\n") {
		return nil, errors.New("Anthropic account selector is invalid")
	}
	credentialBytes := make([]byte, 32)
	if _, err := rand.Read(credentialBytes); err != nil {
		return nil, errors.New("could not create a local Claude credential")
	}
	localHeaderName := "X-Prism-Claude-Bridge"
	localHeaderValue := hex.EncodeToString(credentialBytes)

	proxy := httputil.NewSingleHostReverseProxy(target)
	director := proxy.Director
	proxy.Director = func(request *http.Request) {
		director(request)
		request.Host = target.Host
		request.Header.Del("X-Api-Key")
		request.Header.Del(localHeaderName)
		request.Header.Set("Authorization", "Bearer "+prismCredential)
		if prismAnthropicAccount != "" {
			request.Header.Set("X-Prism-Anthropic-Account", "b64:"+base64.RawURLEncoding.EncodeToString([]byte(prismAnthropicAccount)))
		}
	}
	proxy.ErrorLog = log.New(stderr, "prism: ", 0)
	proxy.ErrorHandler = func(response http.ResponseWriter, _ *http.Request, _ error) {
		http.Error(response, "Prism could not be reached", http.StatusBadGateway)
	}

	handler := http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		if subtle.ConstantTimeCompare(
			[]byte(request.Header.Get(localHeaderName)),
			[]byte(localHeaderValue),
		) != 1 {
			http.Error(response, "Unauthorized", http.StatusUnauthorized)
			return
		}
		proxy.ServeHTTP(response, request)
	})
	var listenConfig net.ListenConfig
	listener, err := listenConfig.Listen(context.Background(), "tcp", "127.0.0.1:0")
	if err != nil {
		return nil, errors.New("could not start the local Claude bridge")
	}
	server := &http.Server{
		Handler:           handler,
		ReadHeaderTimeout: 5 * time.Second,
		IdleTimeout:       120 * time.Second,
	}
	go func() {
		_ = server.Serve(listener)
	}()
	return &claudeBridge{
		server:      server,
		url:         "http://" + listener.Addr().String(),
		headerName:  localHeaderName,
		headerValue: localHeaderValue,
	}, nil
}

func (bridge *claudeBridge) close() {
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	_ = bridge.server.Shutdown(ctx)
}

func claudeEnvironment(environment []string, baseURL string, customHeaders string) []string {
	filtered := make([]string, 0, len(environment)+2)
	for _, entry := range environment {
		name, _, _ := strings.Cut(entry, "=")
		switch strings.ToUpper(name) {
		case "ANTHROPIC_BASE_URL",
			"ANTHROPIC_CUSTOM_HEADERS",
			"ANTHROPIC_AUTH_TOKEN",
			"ANTHROPIC_API_KEY",
			"CLAUDE_CODE_USE_BEDROCK",
			"CLAUDE_CODE_USE_VERTEX",
			"ANTHROPIC_BEDROCK_BASE_URL",
			"ANTHROPIC_VERTEX_BASE_URL",
			"ANTHROPIC_VERTEX_PROJECT_ID",
			"CLOUD_ML_REGION",
			"_CLAUDE_CODE_ASSUME_FIRST_PARTY_BASE_URL":
			continue
		}
		filtered = append(filtered, entry)
	}
	return append(filtered,
		"ANTHROPIC_BASE_URL="+baseURL,
		"ANTHROPIC_CUSTOM_HEADERS="+customHeaders,
		"_CLAUDE_CODE_ASSUME_FIRST_PARTY_BASE_URL=1",
	)
}

func printClaudeHelp(output io.Writer) {
	_, _ = fmt.Fprintln(output, `Usage:
  prism claude login [--profile <name>]
  prism claude [--account <alias-or-id>] [claude arguments...]

Pass --model with any model supported by Prism.
Use --account to target a specific Claude account on Prism.
Uses the current Circles profile. Run 'crcl auth status' to list profiles and
'crcl use <profile>' to switch before launching Claude Code.
Run 'claude --help' for Claude Code options.`)
}

func parseClaudeOptions(args []string) (account string, passthroughArgs []string, err error) {
	for index := 0; index < len(args); index++ {
		argument := args[index]
		switch {
		case argument == "--":
			return account, append(passthroughArgs, args[index:]...), nil
		case argument == "--account":
			if account != "" {
				return "", nil, errors.New("--account may be specified only once")
			}
			index++
			if index >= len(args) || strings.TrimSpace(args[index]) == "" || args[index] == "--" {
				return "", nil, errors.New("--account requires a value")
			}
			account = strings.TrimSpace(args[index])
		case strings.HasPrefix(argument, "--account="):
			if account != "" {
				return "", nil, errors.New("--account may be specified only once")
			}
			account = strings.TrimSpace(strings.TrimPrefix(argument, "--account="))
			if account == "" {
				return "", nil, errors.New("--account requires a value")
			}
		default:
			passthroughArgs = append(passthroughArgs, argument)
		}
	}
	return account, passthroughArgs, nil
}
