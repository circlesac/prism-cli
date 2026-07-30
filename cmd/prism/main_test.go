package main

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func cleanCredentialEnvironment(t *testing.T, home string) {
	t.Helper()
	for _, name := range []string{
		"CIRCLES_AUTH_TOKEN",
		"CRCL_AUTH_TOKEN",
		"CIRCLES_PROFILE",
		"CRCL_PROFILE",
		"CIRCLES_CONFIG_FILE",
		"CIRCLES_SHARED_CREDENTIALS_FILE",
	} {
		value, exists := os.LookupEnv(name)
		if err := os.Unsetenv(name); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() {
			if exists {
				_ = os.Setenv(name, value)
			} else {
				_ = os.Unsetenv(name)
			}
		})
	}
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
}

func writeCredentials(t *testing.T, home, contents string) {
	t.Helper()
	directory := filepath.Join(home, ".crcl")
	if err := os.MkdirAll(directory, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(directory, "credentials"), []byte(contents), 0o600); err != nil {
		t.Fatal(err)
	}
}

func TestEnvironmentCredentialStatusIsSecretSafe(t *testing.T) {
	home := t.TempDir()
	cleanCredentialEnvironment(t, home)
	secret := "environment-secret-value"
	t.Setenv("CIRCLES_AUTH_TOKEN", secret)
	var stdout bytes.Buffer
	var stderr bytes.Buffer

	if code := run(context.Background(), []string{"auth", "status"}, &stdout, &stderr); code != 0 {
		t.Fatalf("exit %d: %s", code, stderr.String())
	}
	if !strings.Contains(stdout.String(), "api_key from the environment") {
		t.Fatalf("unexpected status: %s", stdout.String())
	}
	if strings.Contains(stdout.String(), secret) || strings.Contains(stderr.String(), secret) {
		t.Fatal("credential value was printed")
	}
	if _, err := os.Stat(filepath.Join(home, ".crcl")); !os.IsNotExist(err) {
		t.Fatalf("environment credential wrote to disk: %v", err)
	}
}

func TestSelectedProfileUsesSharedGoProvider(t *testing.T) {
	home := t.TempDir()
	cleanCredentialEnvironment(t, home)
	secret := "profile-secret-value"
	writeCredentials(t, home, "[dev]\napi_key = "+secret+"\n")
	var stdout bytes.Buffer
	var stderr bytes.Buffer

	if code := run(context.Background(), []string{"auth", "status", "--profile", "dev"}, &stdout, &stderr); code != 0 {
		t.Fatalf("exit %d: %s", code, stderr.String())
	}
	if !strings.Contains(stdout.String(), `api_key from profile "dev"`) {
		t.Fatalf("unexpected status: %s", stdout.String())
	}
	if strings.Contains(stdout.String(), secret) || strings.Contains(stderr.String(), secret) {
		t.Fatal("credential value was printed")
	}
}

func TestMissingCredentialUsesStableActionableError(t *testing.T) {
	home := t.TempDir()
	cleanCredentialEnvironment(t, home)
	var stdout bytes.Buffer
	var stderr bytes.Buffer

	if code := run(context.Background(), []string{"auth", "status"}, &stdout, &stderr); code != 1 {
		t.Fatalf("exit %d, want 1", code)
	}
	for _, expected := range []string{"CREDENTIAL_NOT_FOUND", "CIRCLES_AUTH_TOKEN", "crcl login"} {
		if !strings.Contains(stderr.String(), expected) {
			t.Fatalf("error %q does not contain %q", stderr.String(), expected)
		}
	}
}

func TestSecretCommandLineOptionIsNotAccepted(t *testing.T) {
	home := t.TempDir()
	cleanCredentialEnvironment(t, home)
	secret := "command-line-secret"
	var stdout bytes.Buffer
	var stderr bytes.Buffer

	if code := run(context.Background(), []string{"auth", "status", "--token", secret}, &stdout, &stderr); code != 2 {
		t.Fatalf("exit %d, want 2", code)
	}
	if strings.Contains(stderr.String(), secret) {
		t.Fatal("rejected command-line secret was printed")
	}

	stderr.Reset()
	if code := run(context.Background(), []string{"auth", "status", secret}, &stdout, &stderr); code != 2 {
		t.Fatalf("exit %d, want 2", code)
	}
	if strings.Contains(stderr.String(), secret) {
		t.Fatal("rejected positional secret was printed")
	}
}
