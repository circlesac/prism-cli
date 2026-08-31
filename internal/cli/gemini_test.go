package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func TestGeminiDefaultsTo37FlashLowAndPreservesExplicitModel(t *testing.T) {
	if got := withDefaultGeminiModel([]string{"-p", "hello"}); !reflect.DeepEqual(got, []string{"--model", "gemini-3.7-flash-low", "-p", "hello"}) {
		t.Fatalf("default args = %#v", got)
	}
	for _, args := range [][]string{{"--model", "gemini-3.1-pro-high", "-p", "hard"}, {"-m", "gemini-3.1-pro-high"}, {"--model=gemini-3.1-pro-high"}} {
		if got := withDefaultGeminiModel(args); !reflect.DeepEqual(got, args) {
			t.Fatalf("explicit model args = %#v", got)
		}
	}
}

func TestGeminiHelpDocumentsAntigravityContract(t *testing.T) {
	var output bytes.Buffer
	if err := runGeminiCommand(context.Background(), []string{"--help"}, &output, io.Discard); err != nil {
		t.Fatal(err)
	}
	for _, value := range []string{"official Antigravity CLI", "never copies", "API-billing environment", "useG1Credits=false", "sign-in flow automatically", "gemini-3.7-flash-low", "gemini-3.1-pro-high"} {
		if !strings.Contains(output.String(), value) {
			t.Fatalf("help omitted %q: %s", value, output.String())
		}
	}
}

func TestGeminiRejectsInventedAuthSubcommand(t *testing.T) {
	err := runGeminiCommand(context.Background(), []string{"auth", "login"}, io.Discard, io.Discard)
	if err == nil || !strings.Contains(err.Error(), "has no auth subcommand") {
		t.Fatalf("error = %v", err)
	}
}

func TestFindGeminiCLIRequiresAntigravity(t *testing.T) {
	directory := t.TempDir()
	if err := os.WriteFile(filepath.Join(directory, "agy"), []byte("#!/bin/sh\nexit 0\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(directory, "gemini"), []byte("#!/bin/sh\nexit 9\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", directory)

	executable, err := findGeminiCLI()
	if err != nil {
		t.Fatal(err)
	}
	if executable.path != filepath.Join(directory, "agy") || len(executable.prefix) != 0 {
		t.Fatalf("executable = %#v", executable)
	}
}

func TestRunAntigravityPassesStreamingAndTimeoutArgumentsAndScrubsAPIKeys(t *testing.T) {
	originalConfig := antigravityConfigPath
	defer func() { antigravityConfigPath = originalConfig }()
	directory := t.TempDir()
	antigravityConfigPath = func() string { return filepath.Join(directory, "config.json") }
	executable := filepath.Join(directory, "agy")
	if err := os.WriteFile(executable, []byte("#!/bin/sh\nif [ -n \"$GEMINI_API_KEY$GOOGLE_API_KEY$GOOGLE_GEMINI_BASE_URL$GOOGLE_GENAI_USE_GCA$GOOGLE_GENAI_USE_VERTEXAI$GOOGLE_VERTEX_BASE_URL\" ]; then exit 3; fi\nprintf '%s\\n' \"$@\"\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("GEMINI_API_KEY", "must-not-pass")
	t.Setenv("GOOGLE_API_KEY", "must-not-pass")
	t.Setenv("GOOGLE_GEMINI_BASE_URL", "must-not-pass")
	t.Setenv("GOOGLE_GENAI_USE_GCA", "must-not-pass")
	t.Setenv("GOOGLE_GENAI_USE_VERTEXAI", "must-not-pass")
	t.Setenv("GOOGLE_VERTEX_BASE_URL", "must-not-pass")
	var output bytes.Buffer
	args := withDefaultGeminiModel([]string{"-p", "hello", "--output-format", "stream-json", "--print-timeout", "30m"})
	if err := runAntigravity(context.Background(), geminiCLIExecutable{path: executable}, args, strings.NewReader(""), &output, io.Discard); err != nil {
		t.Fatal(err)
	}
	want := "--model\ngemini-3.7-flash-low\n-p\nhello\n--output-format\nstream-json\n--print-timeout\n30m\n"
	if output.String() != want {
		t.Fatalf("output = %q, want %q", output.String(), want)
	}
}

func TestAntigravityUsageParsesSubscriptionWindows(t *testing.T) {
	originalExecutable := findGeminiCLIExecutable
	originalConfig := antigravityConfigPath
	defer func() {
		findGeminiCLIExecutable = originalExecutable
		antigravityConfigPath = originalConfig
	}()
	directory := t.TempDir()
	antigravityConfigPath = func() string { return filepath.Join(directory, "config.json") }
	executable := filepath.Join(directory, "agy")
	if err := os.WriteFile(executable, []byte("#!/bin/sh\nprintf '%s' '{\"status\":\"SUCCESS\",\"command\":{\"data\":{\"groups\":[{\"name\":\"Gemini Models\",\"buckets\":[{\"name\":\"Five Hour Limit Remaining\",\"window\":\"5h\",\"remaining_fraction\":0.75,\"reset_time\":\"2026-08-25T13:00:00Z\"}]}]}}}'\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	findGeminiCLIExecutable = func() (geminiCLIExecutable, error) { return geminiCLIExecutable{path: executable}, nil }
	usage, err := fetchAntigravityUsage(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if usage.Provider != "gemini" || len(usage.Accounts) != 1 || len(usage.Accounts[0].Limits) != 1 {
		t.Fatalf("usage = %#v", usage)
	}
	limit := usage.Accounts[0].Limits[0]
	if limit.RemainingPercent != 75 || limit.UsedPercent != 25 || limit.Window != "5h" || limit.WindowSeconds == nil || *limit.WindowSeconds != 18000 {
		t.Fatalf("limit = %#v", limit)
	}
}

func TestRunAntigravityDisablesCreditOveragesBeforeStartingCLI(t *testing.T) {
	original := antigravityConfigPath
	defer func() { antigravityConfigPath = original }()
	directory := t.TempDir()
	path := filepath.Join(directory, "config.json")
	antigravityConfigPath = func() string { return path }
	if err := os.WriteFile(path, []byte("{\"userSettings\":{\"remoteControlHostname\":\"example-host\",\"useG1Credits\":true}}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	executable := filepath.Join(directory, "agy")
	if err := os.WriteFile(executable, []byte("#!/bin/sh\nprintf CREDIT_GUARD_OK\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	var output bytes.Buffer
	if err := runAntigravity(context.Background(), geminiCLIExecutable{path: executable}, nil, strings.NewReader(""), &output, io.Discard); err != nil {
		t.Fatal(err)
	}
	if output.String() != "CREDIT_GUARD_OK" {
		t.Fatalf("output = %q", output.String())
	}
	var settings map[string]any
	contents, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(contents, &settings); err != nil {
		t.Fatal(err)
	}
	userSettings, ok := settings["userSettings"].(map[string]any)
	if !ok || userSettings["useG1Credits"] != false || userSettings["remoteControlHostname"] != "example-host" {
		t.Fatalf("settings = %#v", settings)
	}
}
