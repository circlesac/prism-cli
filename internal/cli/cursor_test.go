package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/circlesac/prism-cli/internal/api"
	prismcursor "github.com/circlesac/prism-cli/internal/cursor"
)

func TestCursorAgentAlwaysDisablesAutoUpdate(t *testing.T) {
	original := cursorAgentExecutable
	defer func() { cursorAgentExecutable = original }()
	directory := t.TempDir()
	executable := filepath.Join(directory, "cursor-agent")
	if err := os.WriteFile(executable, []byte("#!/bin/sh\nprintf '%s\\n' \"$@\"\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	cursorAgentExecutable = func() (string, error) { return executable, nil }

	var output bytes.Buffer
	if err := runCursorAgent(context.Background(), []string{"status", "--format", "json"}, strings.NewReader(""), &output, &bytes.Buffer{}); err != nil {
		t.Fatal(err)
	}
	if output.String() != "--disable-auto-update\nstatus\n--format\njson\n" {
		t.Fatalf("arguments = %q", output.String())
	}
}

func TestCursorAgentUsesIsolatedOfficialCredentialStoreForAccount(t *testing.T) {
	original := cursorAgentExecutable
	defer func() { cursorAgentExecutable = original }()
	directory := t.TempDir()
	if err := os.WriteFile(filepath.Join(directory, "auth.json"), []byte(`{"accessToken":"account-token"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	executable := filepath.Join(directory, "cursor-agent")
	if err := os.WriteFile(executable, []byte("#!/bin/sh\nprintf 'config=%s\\nstore=%s\\ntoken=%s\\n' \"$CURSOR_CONFIG_DIR\" \"$AGENT_CLI_CREDENTIAL_STORE\" \"$CURSOR_AUTH_TOKEN\"\nprintf '%s\\n' \"$@\"\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	cursorAgentExecutable = func() (string, error) { return executable, nil }

	var output bytes.Buffer
	if err := runCursorAgentForAccount(context.Background(), []string{"status"}, directory, strings.NewReader(""), &output, io.Discard); err != nil {
		t.Fatal(err)
	}
	for _, value := range []string{"config=" + directory, "store=memory", "token=account-token", "--disable-auto-update\nstatus"} {
		if !strings.Contains(output.String(), value) {
			t.Fatalf("output omitted %q: %s", value, output.String())
		}
	}
}

func TestCursorInstallUpdateAndUpgradeUseTheManagedInstaller(t *testing.T) {
	originalInstall := installCursorAgent
	originalExecutable := cursorAgentExecutable
	defer func() {
		installCursorAgent = originalInstall
		cursorAgentExecutable = originalExecutable
	}()
	cursorAgentExecutable = func() (string, error) { return "", errors.New("Cursor Agent must not be launched") }
	installed := 0
	installCursorAgent = func(context.Context, prismcursor.InstallOptions, io.Writer) error {
		installed++
		return nil
	}
	for _, command := range []string{"install", "update", "upgrade"} {
		if err := runCursorCommand(context.Background(), []string{command}, &bytes.Buffer{}, &bytes.Buffer{}); err != nil {
			t.Fatalf("%s: %v", command, err)
		}
		err := runCursorCommand(context.Background(), []string{command, "extra"}, &bytes.Buffer{}, &bytes.Buffer{})
		if err == nil || !strings.Contains(err.Error(), "usage: prism cursor "+command) {
			t.Fatalf("%s extra error = %v", command, err)
		}
	}
	if installed != 3 {
		t.Fatalf("installed = %d", installed)
	}
}

func TestCursorUsageShowsBothMonthlyPools(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	originalUsage := fetchCursorUsage
	originalExecutable := cursorAgentExecutable
	defer func() {
		fetchCursorUsage = originalUsage
		cursorAgentExecutable = originalExecutable
	}()
	cursorAgentExecutable = func() (string, error) { return "", errors.New("Cursor Agent must not be launched") }
	fetchCursorUsage = func(context.Context, prismcursor.UsageOptions) (api.ProviderUsage, error) {
		plan := "pro_plus"
		return api.ProviderUsage{Provider: "cursor", Accounts: []api.UsageAccount{{
			Name: "person@example.com", Plan: &plan, Status: "active", Limits: []api.UsageLimit{
				{Name: "Cursor Models", Window: "monthly", UsedPercent: 12.5, RemainingPercent: 87.5},
				{Name: "Other Models", Window: "monthly", UsedPercent: 34, RemainingPercent: 66},
			},
		}}}, nil
	}
	var output bytes.Buffer
	if err := runCursorCommand(context.Background(), []string{"usage"}, &output, &bytes.Buffer{}); err != nil {
		t.Fatal(err)
	}
	for _, value := range []string{"Cursor Models", "Other Models", "monthly", "12.5%", "87.5%", "34%", "66%", "person@example.com"} {
		if !strings.Contains(output.String(), value) {
			t.Fatalf("usage output did not contain %q: %s", value, output.String())
		}
	}
	if err := runCursorCommand(context.Background(), []string{"usage", "extra"}, &bytes.Buffer{}, &bytes.Buffer{}); err == nil {
		t.Fatal("usage accepted an extra argument")
	}
}

func TestCursorUsageReportsAuthenticationErrorWithoutZeroUsage(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	originalUsage := fetchCursorUsage
	defer func() { fetchCursorUsage = originalUsage }()
	fetchCursorUsage = func(context.Context, prismcursor.UsageOptions) (api.ProviderUsage, error) {
		return api.ProviderUsage{}, errors.New("Cursor Agent login was not found; run 'prism cursor login'")
	}
	var output bytes.Buffer
	err := runCursorCommand(context.Background(), []string{"usage"}, &output, &bytes.Buffer{})
	if err == nil || !strings.Contains(err.Error(), "login was not found") {
		t.Fatalf("error = %v", err)
	}
	if strings.Contains(output.String(), "0%") {
		t.Fatalf("output = %q", output.String())
	}
}

func TestFindCursorAgentPrefersTheManagedInstall(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("PATH", filepath.Join(home, "empty"))
	if _, err := findCursorAgent(); err == nil {
		t.Fatal("missing Cursor Agent was reported as installed")
	}
	bin := filepath.Join(home, ".local", "bin")
	if err := os.MkdirAll(bin, 0o755); err != nil {
		t.Fatal(err)
	}
	managed := filepath.Join(bin, "cursor-agent")
	if err := os.WriteFile(managed, []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(bin, "agent"), []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	path, err := findCursorAgent()
	if err != nil || path != managed {
		t.Fatalf("path = %q, error = %v", path, err)
	}
}

func TestCursorHelpDocumentsIsolatedInstallAndUsage(t *testing.T) {
	var output bytes.Buffer
	if err := runCursorCommand(context.Background(), []string{"help"}, &output, &bytes.Buffer{}); err != nil {
		t.Fatal(err)
	}
	for _, value := range []string{"prism cursor install|update", "prism cursor usage", "without replacing ~/.local/bin/agent", "balanced rotation", "auth import", "auth sync"} {
		if !strings.Contains(output.String(), value) {
			t.Fatalf("help did not contain %q: %s", value, output.String())
		}
	}
}

func TestCursorAuthSyncUploadsTheSelectedSubscriptionWithoutLaunchingAgent(t *testing.T) {
	root := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", root)
	directory := filepath.Join(root, "prism", "cursor", "accounts", "person@example.com")
	if err := os.MkdirAll(directory, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(directory, "auth.json"), []byte(`{"accessToken":"cursor-subscription-token"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(directory, "cli-config.json"), []byte(`{"authInfo":{"email":"person@example.com"}}`), 0o600); err != nil {
		t.Fatal(err)
	}

	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		if request.Method != http.MethodPost || request.URL.Path != "/credentials/cursor" {
			http.NotFound(response, request)
			return
		}
		var body struct {
			Name       string         `json:"name"`
			Credential map[string]any `json:"credential"`
		}
		if err := json.NewDecoder(request.Body).Decode(&body); err != nil {
			t.Fatal(err)
		}
		if body.Name != "person@example.com" || body.Credential["access_token"] != "cursor-subscription-token" || body.Credential["client_version"] != "cli-test" {
			t.Fatalf("body = %#v", body)
		}
		response.Header().Set("Content-Type", "application/json")
		_, _ = response.Write([]byte(`{"id":"credential-id","provider":"cursor","name":"person@example.com"}`))
	}))
	defer server.Close()

	originalClient := cursorPrismClient
	originalVersion := cursorClientVersion
	originalExecutable := cursorAgentExecutable
	defer func() {
		cursorPrismClient = originalClient
		cursorClientVersion = originalVersion
		cursorAgentExecutable = originalExecutable
	}()
	cursorPrismClient = func(context.Context, commonOptions) (api.Client, error) {
		return api.Client{BaseURL: server.URL, Token: "circles-token", HTTPClient: server.Client()}, nil
	}
	cursorClientVersion = func() string { return "cli-test" }
	cursorAgentExecutable = func() (string, error) { return "", errors.New("Cursor Agent must not be launched") }

	var output bytes.Buffer
	if err := runCursorCommand(context.Background(), []string{"auth", "sync", "--account", "person@example.com", "--profile", "dev"}, &output, io.Discard); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(output.String(), "Synced Cursor subscription person@example.com (credential-id).") {
		t.Fatalf("output = %q", output.String())
	}
}
