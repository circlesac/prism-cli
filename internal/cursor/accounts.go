package cursor

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
)

var accountNamePattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._@+\-]{0,127}$`)

type Account struct {
	Name      string
	Email     string
	Directory string
}

func AccountsDirectory() (string, error) {
	root := strings.TrimSpace(os.Getenv("XDG_CONFIG_HOME"))
	if root == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", errors.New("could not find the Prism configuration directory")
		}
		root = filepath.Join(home, ".config")
	}
	return filepath.Join(root, "prism", "cursor", "accounts"), nil
}

func ClientVersion() string {
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	target, err := filepath.EvalSymlinks(filepath.Join(home, ".local", "bin", "cursor-agent"))
	if err != nil {
		return ""
	}
	version := filepath.Base(filepath.Dir(target))
	if !accountNamePattern.MatchString(version) {
		return ""
	}
	return "cli-" + version
}

func ValidateAccountName(name string) error {
	if !accountNamePattern.MatchString(name) {
		return errors.New("Cursor account name must use only letters, numbers, '.', '_', '@', '+', or '-'")
	}
	return nil
}

func ListAccounts() ([]Account, error) {
	root, err := AccountsDirectory()
	if err != nil {
		return nil, err
	}
	entries, err := os.ReadDir(root)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, errors.New("Cursor accounts could not be read")
	}
	accounts := make([]Account, 0, len(entries))
	for _, entry := range entries {
		if !entry.IsDir() || !accountNamePattern.MatchString(entry.Name()) {
			continue
		}
		directory := filepath.Join(root, entry.Name())
		if _, err := TokenFromDirectory(directory); err != nil {
			continue
		}
		accounts = append(accounts, Account{Name: entry.Name(), Email: accountEmail(directory), Directory: directory})
	}
	sort.Slice(accounts, func(i, j int) bool { return accounts[i].Name < accounts[j].Name })
	return accounts, nil
}

func ResolveAccount(selector string) (Account, error) {
	accounts, err := ListAccounts()
	if err != nil {
		return Account{}, err
	}
	for _, account := range accounts {
		if account.Name == selector || account.Email == selector {
			return account, nil
		}
	}
	return Account{}, fmt.Errorf("Cursor account %q is not registered", selector)
}

func ImportCurrent(ctx context.Context, requestedName string) (Account, error) {
	token, err := readAccessToken(ctx)
	if err != nil {
		return Account{}, err
	}
	subject, _, err := accessTokenIdentity(token)
	if err != nil {
		return Account{}, errors.New("Cursor Agent login is unreadable; run 'prism cursor login' again")
	}
	identity := readLocalIdentity(subject)
	name := strings.TrimSpace(requestedName)
	if name == "" {
		name = strings.TrimSpace(identity.Email)
	}
	if name == "" {
		name = "current"
	}
	if err := ValidateAccountName(name); err != nil {
		return Account{}, err
	}
	root, err := AccountsDirectory()
	if err != nil {
		return Account{}, err
	}
	directory := filepath.Join(root, name)
	if err := os.MkdirAll(directory, 0o700); err != nil {
		return Account{}, errors.New("could not create the Cursor account directory")
	}
	auth, err := json.Marshal(map[string]string{"accessToken": token})
	if err != nil {
		return Account{}, errors.New("could not encode the Cursor account")
	}
	if err := os.WriteFile(filepath.Join(directory, "auth.json"), auth, 0o600); err != nil {
		return Account{}, errors.New("could not save the Cursor account")
	}
	config, err := json.Marshal(map[string]any{"authInfo": map[string]string{"authId": subject, "email": identity.Email}})
	if err != nil {
		return Account{}, errors.New("could not encode the Cursor account identity")
	}
	if err := os.WriteFile(filepath.Join(directory, "cli-config.json"), config, 0o600); err != nil {
		return Account{}, errors.New("could not save the Cursor account identity")
	}
	return Account{Name: name, Email: identity.Email, Directory: directory}, nil
}

func RemoveAccount(selector string) error {
	account, err := ResolveAccount(selector)
	if err != nil {
		return err
	}
	root, err := AccountsDirectory()
	if err != nil {
		return err
	}
	if filepath.Dir(account.Directory) != root {
		return errors.New("Cursor account path is invalid")
	}
	if err := os.RemoveAll(account.Directory); err != nil {
		return errors.New("Cursor account could not be removed")
	}
	return nil
}

func AccountFromDirectory(directory string, requestedName string) (Account, error) {
	token, err := TokenFromDirectory(directory)
	if err != nil {
		return Account{}, errors.New("Cursor login did not save an account")
	}
	subject, _, err := accessTokenIdentity(token)
	if err != nil {
		return Account{}, errors.New("Cursor login saved an unreadable account")
	}
	identity := readLocalIdentityFromDirectory(subject, directory)
	name := strings.TrimSpace(requestedName)
	if name == "" {
		name = strings.TrimSpace(identity.Email)
	}
	if name == "" {
		return Account{}, errors.New("Cursor login did not identify the account; use --name")
	}
	if err := ValidateAccountName(name); err != nil {
		return Account{}, err
	}
	root, err := AccountsDirectory()
	if err != nil {
		return Account{}, err
	}
	final := filepath.Join(root, name)
	if _, err := os.Stat(final); err == nil {
		return Account{}, fmt.Errorf("Cursor account %q is already registered", name)
	} else if !errors.Is(err, os.ErrNotExist) {
		return Account{}, errors.New("Cursor account destination could not be inspected")
	}
	if err := os.Rename(directory, final); err != nil {
		return Account{}, errors.New("Cursor account could not be finalized")
	}
	return Account{Name: name, Email: identity.Email, Directory: final}, nil
}

func accountEmail(directory string) string {
	data, err := os.ReadFile(filepath.Join(directory, "cli-config.json"))
	if err != nil {
		return ""
	}
	var config struct {
		AuthInfo struct {
			Email string `json:"email"`
		} `json:"authInfo"`
	}
	if json.Unmarshal(data, &config) != nil {
		return ""
	}
	return strings.TrimSpace(config.AuthInfo.Email)
}
