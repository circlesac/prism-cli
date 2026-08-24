//go:build darwin

package cursor

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestReadAccessTokenFallsBackToTheCursorAuthFile(t *testing.T) {
	original := readKeychainAccessToken
	defer func() { readKeychainAccessToken = original }()
	home := t.TempDir()
	t.Setenv("HOME", home)
	if err := os.MkdirAll(filepath.Join(home, ".cursor"), 0o700); err != nil {
		t.Fatal(err)
	}
	authFile := filepath.Join(home, ".cursor", "auth.json")

	readKeychainAccessToken = func(context.Context) (string, error) { return "", errCursorLoginNotFound }
	if _, err := readAccessToken(context.Background()); !errors.Is(err, errCursorLoginNotFound) {
		t.Fatalf("missing login error = %v", err)
	}

	if err := os.WriteFile(authFile, []byte(`{"accessToken":"file-token"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	token, err := readAccessToken(context.Background())
	if err != nil || token != "file-token" {
		t.Fatalf("token = %q, error = %v", token, err)
	}

	readKeychainAccessToken = func(context.Context) (string, error) { return "keychain-token", nil }
	token, err = readAccessToken(context.Background())
	if err != nil || token != "keychain-token" {
		t.Fatalf("token = %q, error = %v", token, err)
	}

	readKeychainAccessToken = func(context.Context) (string, error) {
		return "", errors.New("Cursor Agent login could not be read from macOS Keychain")
	}
	if err := os.Remove(authFile); err != nil {
		t.Fatal(err)
	}
	_, err = readAccessToken(context.Background())
	if err == nil || !strings.Contains(err.Error(), "macOS Keychain") {
		t.Fatalf("error = %v", err)
	}
}
