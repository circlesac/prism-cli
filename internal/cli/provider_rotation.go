package cli

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"github.com/circlesac/prism-cli/internal/api"
)

func rotateProviderAccount(provider string, accounts []api.Credential) (string, error) {
	if len(accounts) == 0 {
		return "", fmt.Errorf("no %s accounts are registered", provider)
	}
	directory := os.Getenv("XDG_CONFIG_HOME")
	if directory == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", errors.New("could not find the Prism configuration directory")
		}
		directory = filepath.Join(home, ".config")
	}
	path := filepath.Join(directory, "prism", "account-rotation.json")
	state := map[string]int{}
	if data, readErr := os.ReadFile(path); readErr == nil {
		_ = json.Unmarshal(data, &state)
	}
	index := state[provider] % len(accounts)
	if index < 0 {
		index = 0
	}
	state[provider] = (index + 1) % len(accounts)
	data, err := json.Marshal(state)
	if err != nil {
		return "", errors.New("could not encode provider account rotation")
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return "", errors.New("could not create the Prism configuration directory")
	}
	temporary, err := os.CreateTemp(filepath.Dir(path), ".account-rotation-")
	if err != nil {
		return "", errors.New("could not save provider account rotation")
	}
	temporaryPath := temporary.Name()
	defer os.Remove(temporaryPath)
	if err := temporary.Chmod(0o600); err != nil {
		temporary.Close()
		return "", errors.New("could not protect provider account rotation")
	}
	if _, err := temporary.Write(data); err != nil {
		temporary.Close()
		return "", errors.New("could not save provider account rotation")
	}
	if err := temporary.Close(); err != nil {
		return "", errors.New("could not save provider account rotation")
	}
	if err := os.Rename(temporaryPath, path); err != nil {
		return "", errors.New("could not save provider account rotation")
	}
	return accounts[index].ID, nil
}
