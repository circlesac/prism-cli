package cli

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"

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
	if len(args) > 0 && args[0] == "usage" {
		if len(args) != 1 {
			return errors.New("usage: prism cursor usage")
		}
		usage, err := fetchCursorUsage(ctx, prismcursor.UsageOptions{})
		if err != nil {
			return err
		}
		printUsage(stdout, usage)
		return nil
	}
	return runCursorAgent(ctx, args, os.Stdin, stdout, stderr)
}

func runCursorAgent(ctx context.Context, args []string, stdin io.Reader, stdout io.Writer, stderr io.Writer) error {
	path, err := cursorAgentExecutable()
	if err != nil {
		return err
	}
	commandArgs := append([]string{"--disable-auto-update"}, args...)
	command := exec.CommandContext(ctx, path, commandArgs...)
	command.Stdin = stdin
	command.Stdout = stdout
	command.Stderr = stderr
	if err := command.Run(); err != nil {
		var exitError *exec.ExitError
		if errors.As(err, &exitError) {
			return fmt.Errorf("Cursor Agent exited with status %d", exitError.ExitCode())
		}
		return fmt.Errorf("could not run Cursor Agent: %w", err)
	}
	return nil
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
  prism cursor login
  prism cursor status
  prism cursor models
  prism cursor usage
  prism cursor [Cursor Agent arguments...]

Install and run the official Cursor Agent without replacing ~/.local/bin/agent.
Cursor login stays in Cursor Agent's supported credential store. Prism reads it
only when showing usage and does not save or print the token.
Run 'cursor-agent --help' for Cursor Agent options.`)
}
