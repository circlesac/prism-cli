//go:build !darwin

package cursor

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"runtime"
)

func readAccessToken(_ context.Context) (string, error) {
	path, err := authFilePath()
	if err != nil {
		return "", err
	}
	return readAccessTokenFile(path)
}

func authFilePath() (string, error) {
	if runtime.GOOS == "windows" {
		root := os.Getenv("APPDATA")
		if root == "" {
			home, err := os.UserHomeDir()
			if err != nil {
				return "", errors.New("Cursor Agent login directory was not found")
			}
			root = filepath.Join(home, "AppData", "Roaming")
		}
		return filepath.Join(root, "Cursor", "auth.json"), nil
	}
	root := os.Getenv("XDG_CONFIG_HOME")
	if root == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", errors.New("Cursor Agent login directory was not found")
		}
		root = filepath.Join(home, ".config")
	}
	return filepath.Join(root, "cursor", "auth.json"), nil
}
