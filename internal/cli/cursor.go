package cli

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/circlesac/prism-cli/internal/api"
	prismcursor "github.com/circlesac/prism-cli/internal/cursor"
)

var installCursorAgent = prismcursor.Install
var fetchCursorUsage = prismcursor.FetchUsage
var cursorAgentExecutable = findCursorAgent

func runCursorCommand(ctx context.Context, args []string, stdout io.Writer, stderr io.Writer) error {
	if len(args) == 1 && (args[0] == "help" || args[0] == "--help" || args[0] == "-h") {
		printCursorHelp(stdout)
		return nil
	}
	if len(args) > 0 && (args[0] == "install" || args[0] == "update" || args[0] == "upgrade") {
		if len(args) != 1 {
			return fmt.Errorf("usage: prism cursor %s", args[0])
		}
		return installCursorAgent(ctx, prismcursor.InstallOptions{}, stdout)
	}
	if len(args) > 0 && args[0] == "login" {
		name, err := parseCursorName(args[1:])
		if err != nil {
			return err
		}
		return loginCursorAccount(ctx, name, os.Stdin, stdout, stderr)
	}
	if len(args) > 0 && args[0] == "auth" {
		return runCursorAuthCommand(ctx, args[1:], stdout)
	}
	if len(args) > 0 && args[0] == "usage" {
		account, remaining, err := parseClaudeOptions(args[1:])
		if err != nil {
			return err
		}
		if len(remaining) != 0 {
			return errors.New("usage: prism cursor usage [--account <name-or-email>]")
		}
		usage, err := fetchAllCursorUsage(ctx, account)
		if err != nil {
			return err
		}
		printUsage(stdout, usage)
		return nil
	}
	account, passthrough, err := parseClaudeOptions(args)
	if err != nil {
		return err
	}
	registered, err := prismcursor.ListAccounts()
	if err != nil {
		return err
	}
	if len(registered) == 0 && account == "" {
		return runCursorAgent(ctx, passthrough, os.Stdin, stdout, stderr)
	}
	selected, err := selectCursorAccount(account, registered)
	if err != nil {
		return err
	}
	return runCursorAgentForAccount(ctx, passthrough, selected.Directory, os.Stdin, stdout, stderr)
}

func runCursorAgent(ctx context.Context, args []string, stdin io.Reader, stdout io.Writer, stderr io.Writer) error {
	return runCursorAgentWithEnvironment(ctx, args, "", stdin, stdout, stderr)
}

func runCursorAgentForAccount(ctx context.Context, args []string, directory string, stdin io.Reader, stdout io.Writer, stderr io.Writer) error {
	token, err := prismcursor.TokenFromDirectory(directory)
	if err != nil {
		return err
	}
	return runCursorAgentConfigured(ctx, args, directory, token, "", stdin, stdout, stderr)
}

func runCursorAgentWithEnvironment(ctx context.Context, args []string, directory string, stdin io.Reader, stdout io.Writer, stderr io.Writer) error {
	return runCursorAgentConfigured(ctx, args, directory, "", "", stdin, stdout, stderr)
}

func runCursorAgentConfigured(ctx context.Context, args []string, directory string, token string, home string, stdin io.Reader, stdout io.Writer, stderr io.Writer) error {
	path, err := cursorAgentExecutable()
	if err != nil {
		return err
	}
	commandArgs := append([]string{"--disable-auto-update"}, args...)
	command := exec.CommandContext(ctx, path, commandArgs...)
	command.Stdin = stdin
	command.Stdout = stdout
	command.Stderr = stderr
	if directory != "" {
		command.Env = cursorEnvironment(os.Environ(), directory, token, home)
	}
	if err := command.Run(); err != nil {
		var exitError *exec.ExitError
		if errors.As(err, &exitError) {
			return fmt.Errorf("Cursor Agent exited with status %d", exitError.ExitCode())
		}
		return fmt.Errorf("could not run Cursor Agent: %w", err)
	}
	return nil
}

func loginCursorAccount(ctx context.Context, name string, stdin io.Reader, stdout io.Writer, stderr io.Writer) error {
	root, err := prismcursor.AccountsDirectory()
	if err != nil {
		return err
	}
	if err := os.MkdirAll(root, 0o700); err != nil {
		return errors.New("could not create the Cursor accounts directory")
	}
	home, err := os.MkdirTemp(root, ".login-")
	if err != nil {
		return errors.New("could not create a temporary Cursor account directory")
	}
	defer os.RemoveAll(home)
	directory := filepath.Join(home, ".cursor")
	if err := os.MkdirAll(directory, 0o700); err != nil {
		return errors.New("could not create a temporary Cursor account profile")
	}
	if err := runCursorAgentConfigured(ctx, []string{"login"}, directory, "", home, stdin, stdout, stderr); err != nil {
		return err
	}
	account, err := prismcursor.AccountFromDirectory(directory, name)
	if err != nil {
		return err
	}
	fmt.Fprintf(stdout, "Saved Cursor account %s", account.Name)
	if account.Email != "" && account.Email != account.Name {
		fmt.Fprintf(stdout, " (%s)", account.Email)
	}
	fmt.Fprintln(stdout, ".")
	return nil
}

func runCursorAuthCommand(ctx context.Context, args []string, stdout io.Writer) error {
	if len(args) == 0 {
		return errors.New("usage: prism cursor auth list|import|remove")
	}
	switch args[0] {
	case "list":
		if len(args) != 1 {
			return errors.New("usage: prism cursor auth list")
		}
		accounts, err := prismcursor.ListAccounts()
		if err != nil {
			return err
		}
		if len(accounts) == 0 {
			fmt.Fprintln(stdout, "No Cursor accounts are registered.")
			return nil
		}
		fmt.Fprintln(stdout, "NAME\tEMAIL")
		for _, account := range accounts {
			fmt.Fprintf(stdout, "%s\t%s\n", account.Name, account.Email)
		}
		return nil
	case "import":
		name, err := parseCursorName(args[1:])
		if err != nil {
			return err
		}
		account, err := prismcursor.ImportCurrent(ctx, name)
		if err != nil {
			return err
		}
		fmt.Fprintf(stdout, "Imported Cursor account %s.\n", account.Name)
		return nil
	case "remove":
		if len(args) != 2 {
			return errors.New("usage: prism cursor auth remove <name-or-email>")
		}
		if err := prismcursor.RemoveAccount(args[1]); err != nil {
			return err
		}
		fmt.Fprintf(stdout, "Removed Cursor account %s.\n", args[1])
		return nil
	default:
		return errors.New("usage: prism cursor auth list|import|remove")
	}
}

func parseCursorName(args []string) (string, error) {
	name := ""
	for index := 0; index < len(args); index++ {
		switch argument := args[index]; {
		case argument == "--name":
			index++
			if index >= len(args) || strings.TrimSpace(args[index]) == "" {
				return "", errors.New("--name requires a value")
			}
			name = strings.TrimSpace(args[index])
		case strings.HasPrefix(argument, "--name="):
			name = strings.TrimSpace(strings.TrimPrefix(argument, "--name="))
			if name == "" {
				return "", errors.New("--name requires a value")
			}
		default:
			return "", fmt.Errorf("unexpected argument %q", argument)
		}
	}
	if name != "" {
		if err := prismcursor.ValidateAccountName(name); err != nil {
			return "", err
		}
	}
	return name, nil
}

func selectCursorAccount(selector string, accounts []prismcursor.Account) (prismcursor.Account, error) {
	if selector != "" {
		for _, account := range accounts {
			if account.Name == selector || account.Email == selector {
				return account, nil
			}
		}
		return prismcursor.Account{}, fmt.Errorf("Cursor account %q is not registered", selector)
	}
	credentials := make([]api.Credential, len(accounts))
	for index, account := range accounts {
		credentials[index] = api.Credential{ID: account.Name, Name: account.Name}
	}
	name, err := rotateProviderAccount("cursor", credentials)
	if err != nil {
		return prismcursor.Account{}, err
	}
	for _, account := range accounts {
		if account.Name == name {
			return account, nil
		}
	}
	return prismcursor.Account{}, errors.New("Cursor account rotation selected an unknown account")
}

func fetchAllCursorUsage(ctx context.Context, selector string) (api.ProviderUsage, error) {
	accounts, err := prismcursor.ListAccounts()
	if err != nil {
		return api.ProviderUsage{}, err
	}
	if len(accounts) == 0 {
		return fetchCursorUsage(ctx, prismcursor.UsageOptions{})
	}
	if selector != "" {
		selected, err := selectCursorAccount(selector, accounts)
		if err != nil {
			return api.ProviderUsage{}, err
		}
		accounts = []prismcursor.Account{selected}
	}
	usage := api.ProviderUsage{Provider: "cursor"}
	for _, account := range accounts {
		token, tokenErr := prismcursor.TokenFromDirectory(account.Directory)
		if tokenErr != nil {
			usage.Accounts = append(usage.Accounts, api.UsageAccount{Name: account.Name, Error: &api.UsageError{Code: "usage_unavailable", Message: tokenErr.Error()}})
			continue
		}
		result, fetchErr := fetchCursorUsage(ctx, prismcursor.UsageOptions{Token: token, ConfigDirectory: account.Directory})
		if fetchErr != nil {
			usage.Accounts = append(usage.Accounts, api.UsageAccount{Name: account.Name, Error: &api.UsageError{Code: "usage_unavailable", Message: fetchErr.Error()}})
			continue
		}
		usage.Accounts = append(usage.Accounts, result.Accounts...)
	}
	return usage, nil
}

func cursorEnvironment(environment []string, directory string, token string, home string) []string {
	filtered := make([]string, 0, len(environment)+4)
	for _, entry := range environment {
		name, _, _ := strings.Cut(entry, "=")
		if strings.EqualFold(name, "CURSOR_CONFIG_DIR") || strings.EqualFold(name, "AGENT_CLI_CREDENTIAL_STORE") ||
			strings.EqualFold(name, "CURSOR_AUTH_TOKEN") || home != "" && strings.EqualFold(name, "HOME") {
			continue
		}
		filtered = append(filtered, entry)
	}
	store := "memory"
	if token == "" {
		store = "file"
	}
	filtered = append(filtered, "CURSOR_CONFIG_DIR="+directory, "AGENT_CLI_CREDENTIAL_STORE="+store)
	if token != "" {
		filtered = append(filtered, "CURSOR_AUTH_TOKEN="+token)
	}
	if home != "" {
		filtered = append(filtered, "HOME="+home)
	}
	return filtered
}

func findCursorAgent() (string, error) {
	home, err := os.UserHomeDir()
	if err == nil {
		managed := filepath.Join(home, ".local", "bin", "cursor-agent")
		if info, statErr := os.Stat(managed); statErr == nil && !info.IsDir() {
			return managed, nil
		}
	}
	path, err := exec.LookPath("cursor-agent")
	if err != nil {
		return "", errors.New("Cursor Agent is not installed; run 'prism cursor install'")
	}
	return path, nil
}

func printCursorHelp(output io.Writer) {
	_, _ = fmt.Fprintln(output, `Usage:
  prism cursor install|update|upgrade
  prism cursor login [--name <alias>]
  prism cursor auth import [--name <alias>]
  prism cursor auth list|remove <name-or-email>
  prism cursor status
  prism cursor models
  prism cursor usage [--account <name-or-email>]
  prism cursor [--account <name-or-email>] [Cursor Agent arguments...]

Install and run the official Cursor Agent without replacing ~/.local/bin/agent.
Each login stays in an isolated official Cursor file credential store. Without
--account, registered accounts are selected in balanced rotation. Use auth
import once to migrate the currently active singleton Cursor login.
Run 'cursor-agent --help' for Cursor Agent options.`)
}
