//go:build darwin

package cursor

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

var readKeychainAccessToken = keychainAccessToken

func readAccessToken(ctx context.Context) (string, error) {
	token, keychainErr := readKeychainAccessToken(ctx)
	if keychainErr == nil {
		return token, nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", keychainErr
	}
	token, fileErr := readAccessTokenFile(filepath.Join(home, ".cursor", "auth.json"))
	if fileErr == nil {
		return token, nil
	}
	if errors.Is(fileErr, errCursorLoginNotFound) {
		return "", keychainErr
	}
	return "", fileErr
}

func keychainAccessToken(ctx context.Context) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, time.Minute)
	defer cancel()
	output, err := exec.CommandContext(
		ctx,
		"/usr/bin/security",
		"find-generic-password",
		"-a", "cursor-user",
		"-s", "cursor-access-token",
		"-w",
	).Output()
	if err != nil {
		var exitError *exec.ExitError
		if errors.As(err, &exitError) && exitError.ExitCode() == 44 {
			return "", errCursorLoginNotFound
		}
		if ctx.Err() != nil {
			return "", errors.New("Cursor Agent login could not be read from macOS Keychain in time")
		}
		return "", errors.New("Cursor Agent login could not be read from macOS Keychain")
	}
	token := strings.TrimSpace(string(output))
	if token == "" {
		return "", errors.New("Cursor Agent login was empty; run 'prism cursor login' again")
	}
	return token, nil
}
