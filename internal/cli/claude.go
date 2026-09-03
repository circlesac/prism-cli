package cli

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"
)

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
	command := exec.CommandContext(ctx, claudePath, args...)
	command.Stdin = stdin
	command.Stdout = stdout
	command.Stderr = stderr
	command.Env = claudeEnvironment(
		os.Environ(),
		prismURL,
		claudeAccountHeaders(prismAccount),
		prismCredential,
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

func claudeAccountHeaders(account string) string {
	if account == "" {
		return ""
	}
	return "X-Prism-Anthropic-Account: b64:" + base64.RawURLEncoding.EncodeToString([]byte(account))
}

func claudeEnvironment(environment []string, baseURL string, customHeaders string, prismCredential string) []string {
	filtered := make([]string, 0, len(environment)+3)
	for _, entry := range environment {
		name, _, _ := strings.Cut(entry, "=")
		switch strings.ToUpper(name) {
		case "ANTHROPIC_BASE_URL",
			"ANTHROPIC_CUSTOM_HEADERS",
			"ANTHROPIC_AUTH_TOKEN",
			"ANTHROPIC_API_KEY",
			"CLAUDE_CODE_OAUTH_TOKEN",
			"CLAUDE_CODE_USE_BEDROCK",
			"CLAUDE_CODE_USE_VERTEX",
			"ANTHROPIC_BEDROCK_BASE_URL",
			"ANTHROPIC_VERTEX_BASE_URL",
			"ANTHROPIC_VERTEX_PROJECT_ID",
			"CLOUD_ML_REGION",
			"_CLAUDE_CODE_ASSUME_FIRST_PARTY_BASE_URL",
			// A parent Claude Code session exports these; a child launched with
			// them set reuses the parent's credential path instead of the Prism
			// bearer token and Prism rejects that request.
			"CLAUDECODE",
			"CLAUDE_CODE_ENTRYPOINT":
			continue
		}
		filtered = append(filtered, entry)
	}
	return append(filtered,
		"ANTHROPIC_BASE_URL="+baseURL,
		"ANTHROPIC_CUSTOM_HEADERS="+customHeaders,
		"ANTHROPIC_AUTH_TOKEN="+prismCredential,
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
			if strings.ContainsAny(account, "\r\n") {
				return "", nil, errors.New("Anthropic account selector is invalid")
			}
		case strings.HasPrefix(argument, "--account="):
			if account != "" {
				return "", nil, errors.New("--account may be specified only once")
			}
			account = strings.TrimSpace(strings.TrimPrefix(argument, "--account="))
			if account == "" {
				return "", nil, errors.New("--account requires a value")
			}
			if strings.ContainsAny(account, "\r\n") {
				return "", nil, errors.New("Anthropic account selector is invalid")
			}
		default:
			passthroughArgs = append(passthroughArgs, argument)
		}
	}
	return account, passthroughArgs, nil
}
