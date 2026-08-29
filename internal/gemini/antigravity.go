package gemini

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os/exec"
	"strings"
	"time"

	"github.com/zalando/go-keyring"
)

const antigravityKeyringPrefix = "go-keyring-base64:"

type AntigravityImport struct {
	Client      *http.Client
	ReadSecret  func() (string, error)
	ReadVersion func(context.Context) (string, error)
	Now         func() time.Time
}

type antigravityStoredLogin struct {
	AuthMethod string `json:"auth_method"`
	Token      struct {
		AccessToken  string    `json:"access_token"`
		RefreshToken string    `json:"refresh_token"`
		Expiry       time.Time `json:"expiry"`
	} `json:"token"`
}

func (i AntigravityImport) Import(ctx context.Context) (Bundle, error) {
	readSecret := i.ReadSecret
	if readSecret == nil {
		readSecret = func() (string, error) { return keyring.Get("gemini", "antigravity") }
	}
	encoded, err := readSecret()
	if err != nil {
		return Bundle{}, errors.New("Antigravity login was not found; sign in with 'agy' first")
	}
	login, err := decodeAntigravityLogin(encoded)
	if err != nil {
		return Bundle{}, err
	}
	now := time.Now
	if i.Now != nil {
		now = i.Now
	}
	if !login.Token.Expiry.After(now().Add(5 * time.Minute)) {
		return Bundle{}, errors.New("Antigravity login is stale; run 'agy -p /usage --output-format json' and import it again")
	}
	readVersion := i.ReadVersion
	if readVersion == nil {
		readVersion = func(ctx context.Context) (string, error) {
			output, err := exec.CommandContext(ctx, "agy", "--version").Output()
			return strings.TrimSpace(string(output)), err
		}
	}
	version, err := readVersion(ctx)
	if err != nil || version == "" || strings.ContainsAny(version, " /()\t\r\n") {
		return Bundle{}, errors.New("could not read the Antigravity CLI version; run 'agy --version' and try again")
	}
	client := i.Client
	if client == nil {
		client = &http.Client{Timeout: 30 * time.Second}
	}
	projectID, err := antigravityProject(ctx, client, login.Token.AccessToken)
	if err != nil {
		return Bundle{}, err
	}
	email, name, err := profile(ctx, client, login.Token.AccessToken)
	if err != nil {
		return Bundle{}, err
	}
	alias := email
	if alias == "" {
		alias = name
	}
	return Bundle{
		AccessToken:  login.Token.AccessToken,
		RefreshToken: login.Token.RefreshToken,
		ProjectID:    projectID,
		Email:        email,
		Alias:        alias,
		ExpiresAt:    login.Token.Expiry.UnixMilli(),
		AuthMethod:   "antigravity",
		UserAgent:    "antigravity/cli/" + version,
	}, nil
}

func decodeAntigravityLogin(value string) (antigravityStoredLogin, error) {
	decoded := []byte(strings.TrimSpace(value))
	if encoded, ok := strings.CutPrefix(string(decoded), antigravityKeyringPrefix); ok {
		var err error
		decoded, err = base64.StdEncoding.DecodeString(encoded)
		if err != nil {
			return antigravityStoredLogin{}, errors.New("Antigravity login is corrupt")
		}
	}
	var login antigravityStoredLogin
	if err := json.Unmarshal(decoded, &login); err != nil {
		return antigravityStoredLogin{}, errors.New("Antigravity login is corrupt")
	}
	if login.AuthMethod != "consumer" || login.Token.AccessToken == "" || login.Token.RefreshToken == "" || login.Token.Expiry.IsZero() {
		return antigravityStoredLogin{}, errors.New("Antigravity consumer login is incomplete")
	}
	return login, nil
}

func antigravityProject(ctx context.Context, client *http.Client, accessToken string) (string, error) {
	request, _ := http.NewRequestWithContext(ctx, http.MethodPost, "https://daily-cloudcode-pa.googleapis.com/v1internal:loadCodeAssist", strings.NewReader(`{"metadata":{"ideType":"ANTIGRAVITY"}}`))
	request.Header.Set("Authorization", "Bearer "+accessToken)
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("User-Agent", "antigravity/cli/prism")
	var response struct {
		CloudAICompanionProject string `json:"cloudaicompanionProject"`
	}
	if err := doJSON(client, request, &response); err != nil {
		return "", fmt.Errorf("load Antigravity subscription: %w", err)
	}
	if response.CloudAICompanionProject == "" {
		return "", errors.New("Antigravity subscription did not return a Code Assist project")
	}
	return response.CloudAICompanionProject, nil
}
