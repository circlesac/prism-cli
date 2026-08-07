package cli

import (
	"context"
	"crypto/rand"
	"crypto/subtle"
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
	server     *http.Server
	url        string
	credential string
}

func runClaudeCommand(ctx context.Context, args []string, stdout io.Writer, stderr io.Writer) error {
	options, claudeArgs, err := parseClaudeOptions(args)
	if err != nil {
		return err
	}
	if options.help {
		printClaudeHelp(stdout)
		return nil
	}
	client, err := prismClient(ctx, options)
	if err != nil {
		return err
	}
	return runClaude(ctx, client.BaseURL, client.Token, claudeArgs, os.Stdin, stdout, stderr)
}

func runClaude(
	ctx context.Context,
	prismURL string,
	prismCredential string,
	args []string,
	stdin io.Reader,
	stdout io.Writer,
	stderr io.Writer,
) error {
	claudePath, err := exec.LookPath("claude")
	if err != nil {
		return errors.New("Claude Code is not installed or is not on PATH")
	}
	bridge, err := startClaudeBridge(prismURL, prismCredential, stderr)
	if err != nil {
		return err
	}
	defer bridge.close()

	command := exec.CommandContext(ctx, claudePath, args...)
	command.Stdin = stdin
	command.Stdout = stdout
	command.Stderr = stderr
	command.Env = claudeEnvironment(os.Environ(), bridge.url, bridge.credential)
	if err := command.Run(); err != nil {
		var exitError *exec.ExitError
		if errors.As(err, &exitError) {
			return fmt.Errorf("Claude Code exited with status %d", exitError.ExitCode())
		}
		return fmt.Errorf("could not run Claude Code: %w", err)
	}
	return nil
}

func startClaudeBridge(prismURL string, prismCredential string, stderr io.Writer) (*claudeBridge, error) {
	target, err := url.Parse(prismURL)
	if err != nil || target.Scheme != "https" && target.Scheme != "http" || target.Host == "" {
		return nil, errors.New("Prism URL is invalid")
	}
	if strings.TrimSpace(prismCredential) == "" || strings.ContainsAny(prismCredential, " \t\r\n") {
		return nil, errors.New("Circles credential is invalid")
	}
	credentialBytes := make([]byte, 32)
	if _, err := rand.Read(credentialBytes); err != nil {
		return nil, errors.New("could not create a local Claude credential")
	}
	localCredential := hex.EncodeToString(credentialBytes)

	proxy := httputil.NewSingleHostReverseProxy(target)
	director := proxy.Director
	proxy.Director = func(request *http.Request) {
		director(request)
		request.Host = target.Host
		request.Header.Del("X-Api-Key")
		request.Header.Set("Authorization", "Bearer "+prismCredential)
	}
	proxy.ErrorLog = log.New(stderr, "prism: ", 0)
	proxy.ErrorHandler = func(response http.ResponseWriter, _ *http.Request, _ error) {
		http.Error(response, "Prism could not be reached", http.StatusBadGateway)
	}

	handler := http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		if subtle.ConstantTimeCompare(
			[]byte(request.Header.Get("Authorization")),
			[]byte("Bearer "+localCredential),
		) != 1 {
			http.Error(response, "Unauthorized", http.StatusUnauthorized)
			return
		}
		proxy.ServeHTTP(response, request)
	})
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return nil, errors.New("could not start the local Claude bridge")
	}
	server := &http.Server{Handler: handler, ReadHeaderTimeout: 5 * time.Second}
	go func() {
		_ = server.Serve(listener)
	}()
	return &claudeBridge{
		server:     server,
		url:        "http://" + listener.Addr().String(),
		credential: localCredential,
	}, nil
}

func (bridge *claudeBridge) close() {
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	_ = bridge.server.Shutdown(ctx)
}

func claudeEnvironment(environment []string, baseURL string, credential string) []string {
	filtered := make([]string, 0, len(environment)+2)
	for _, entry := range environment {
		name, _, _ := strings.Cut(entry, "=")
		switch strings.ToUpper(name) {
		case "ANTHROPIC_BASE_URL", "ANTHROPIC_AUTH_TOKEN", "ANTHROPIC_API_KEY":
			continue
		}
		filtered = append(filtered, entry)
	}
	return append(filtered,
		"ANTHROPIC_BASE_URL="+baseURL,
		"ANTHROPIC_AUTH_TOKEN="+credential,
	)
}

func parseClaudeOptions(args []string) (commonOptions, []string, error) {
	if len(args) == 1 && (args[0] == "--help" || args[0] == "-h") {
		return commonOptions{help: true}, nil, nil
	}
	var options commonOptions
	claudeArgs := make([]string, 0, len(args))
	for index := 0; index < len(args); index++ {
		argument := args[index]
		switch {
		case argument == "--":
			claudeArgs = append(claudeArgs, args[index:]...)
			return options, claudeArgs, nil
		case argument == "--profile":
			index++
			if index >= len(args) || args[index] == "" {
				return commonOptions{}, nil, errors.New("--profile requires a value")
			}
			options.profile = args[index]
			options.profileSet = true
		case strings.HasPrefix(argument, "--profile="):
			options.profile = strings.TrimPrefix(argument, "--profile=")
			if options.profile == "" {
				return commonOptions{}, nil, errors.New("--profile requires a value")
			}
			options.profileSet = true
		default:
			claudeArgs = append(claudeArgs, argument)
		}
	}
	return options, claudeArgs, nil
}

func printClaudeHelp(output io.Writer) {
	fmt.Fprintln(output, `Usage:
  prism claude [--profile <name>] [claude arguments...]

Pass --model with any model supported by Prism.
Run 'claude --help' for Claude Code options.`)
}
