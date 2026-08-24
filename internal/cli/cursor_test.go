package cli

import (
	"bytes"
	"context"
	"errors"
	"io"
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
	for _, value := range []string{"prism cursor install|update", "prism cursor usage", "without replacing ~/.local/bin/agent", "does not save or print the token"} {
		if !strings.Contains(output.String(), value) {
			t.Fatalf("help did not contain %q: %s", value, output.String())
		}
	}
}
