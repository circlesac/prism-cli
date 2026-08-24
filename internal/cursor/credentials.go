package cursor

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
)

var errCursorLoginNotFound = errors.New("Cursor Agent login was not found; run 'prism cursor login'")

func configDirectory() (string, error) {
	if directory := strings.TrimSpace(os.Getenv("CURSOR_CONFIG_DIR")); directory != "" {
		return directory, nil
	}
	if directory := strings.TrimSpace(os.Getenv("XDG_CONFIG_HOME")); directory != "" {
		return filepath.Join(directory, "cursor"), nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", errors.New("could not find the Cursor Agent configuration directory")
	}
	return filepath.Join(home, ".cursor"), nil
}

func readAccessTokenFile(path string) (string, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return "", errCursorLoginNotFound
		}
		return "", errors.New("Cursor Agent login could not be read")
	}
	var credential struct {
		AccessToken string `json:"accessToken"`
	}
	if json.Unmarshal(data, &credential) != nil || strings.TrimSpace(credential.AccessToken) == "" {
		return "", errors.New("Cursor Agent login was unreadable; run 'prism cursor login' again")
	}
	return strings.TrimSpace(credential.AccessToken), nil
}
