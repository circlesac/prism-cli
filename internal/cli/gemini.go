package cli

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

const defaultGeminiModel = "gemini-3.8-flash-high"

type geminiCLIExecutable struct {
	path   string
	prefix []string
}

var findGeminiCLIExecutable = findGeminiCLI

func isGeminiCLIInvocation([]string) bool {
	return true
}

func runGeminiCommand(ctx context.Context, args []string, stdout io.Writer, stderr io.Writer) error {
	if len(args) > 0 && args[0] == "usage" {
		if len(args) != 1 {
			return errors.New("usage: prism gemini usage")
		}
		return runGeminiUsage(ctx, stdout, stderr)
	}
	if len(args) > 0 && args[0] == "auth" {
		return errors.New("Antigravity CLI signs in automatically on the first 'prism gemini' run; it has no auth subcommand")
	}
	if len(args) == 1 && (args[0] == "--help" || args[0] == "-h" || args[0] == "help") {
		printGeminiHelp(stdout)
		return nil
	}
	executable, err := findGeminiCLIExecutable()
	if err != nil {
		return err
	}
	return runAntigravity(ctx, executable, withDefaultGeminiModel(args), os.Stdin, stdout, stderr)
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

var antigravityConfigPath = defaultAntigravityConfigPath

func defaultAntigravityConfigPath() string {
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	return filepath.Join(home, ".gemini", "config", "config.json")
}

func disableAntigravityCreditOverages() error {
	path := antigravityConfigPath()
	if path == "" {
		return errors.New("could not locate Antigravity shared settings")
	}
	settings := map[string]any{}
	contents, err := os.ReadFile(path)
	if err == nil {
		if len(strings.TrimSpace(string(contents))) != 0 {
			if err := json.Unmarshal(contents, &settings); err != nil {
				return errors.New("Antigravity shared settings are invalid; fix config.json before using Prism Gemini")
			}
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return errors.New("could not read Antigravity shared settings")
	}
	userSettings, ok := settings["userSettings"].(map[string]any)
	if !ok {
		if settings["userSettings"] != nil {
			return errors.New("Antigravity shared userSettings are invalid; fix config.json before using Prism Gemini")
		}
		userSettings = map[string]any{}
		settings["userSettings"] = userSettings
	}
	if value, ok := userSettings["useG1Credits"].(bool); ok && !value {
		return nil
	}
	userSettings["useG1Credits"] = false
	encoded, err := json.MarshalIndent(settings, "", "  ")
	if err != nil {
		return errors.New("could not encode Antigravity CLI settings")
	}
	encoded = append(encoded, '\n')
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return errors.New("could not create Antigravity shared settings directory")
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
		return errors.New("could not activate Antigravity shared settings")
	}
	return nil
}

func findGeminiCLI() (geminiCLIExecutable, error) {
	path, err := exec.LookPath("agy")
	if err != nil {
		return geminiCLIExecutable{}, errors.New("Antigravity CLI (agy) is not installed or is not on PATH")
	}
	return geminiCLIExecutable{path: path}, nil
}

func subscriptionGeminiEnvironment(environment []string) []string {
	filtered := make([]string, 0, len(environment))
	for _, entry := range environment {
		name, _, _ := strings.Cut(entry, "=")
		switch strings.ToUpper(name) {
		case "GEMINI_API_KEY", "GOOGLE_API_KEY", "GOOGLE_GEMINI_BASE_URL", "GOOGLE_GENAI_USE_GCA", "GOOGLE_GENAI_USE_VERTEXAI", "GOOGLE_VERTEX_BASE_URL":
			continue
		}
		filtered = append(filtered, entry)
	}
	return filtered
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

func printGeminiHelp(output io.Writer) {
	_, _ = fmt.Fprintln(output, `Usage:
  prism gemini [Antigravity CLI arguments...]
  prism gemini usage

Runs the official Antigravity CLI (agy) inside the Prism command surface.
The signed-in Google subscription remains managed by agy; Prism never copies
or proxies its OAuth tokens. Common API-billing environment variables are
removed before every run, and useG1Credits=false is persisted before launch
to disable AI Credit fallback. If no session exists, agy starts its official
Google sign-in flow automatically.
The default model is gemini-3.8-flash-high. Other Antigravity CLI
arguments are passed through the Prism surface.`)
}
