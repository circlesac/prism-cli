package copilot

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"time"

	"github.com/circlesac/prism-cli/internal/chatgpt"
)

type Bundle struct {
	GitHubToken string `json:"github_token"`
	Username    string `json:"username,omitempty"`
	Alias       string `json:"alias,omitempty"`
}

type deviceCode struct {
	DeviceCode      string `json:"device_code"`
	UserCode        string `json:"user_code"`
	VerificationURI string `json:"verification_uri"`
	ExpiresIn       int    `json:"expires_in"`
	Interval        int    `json:"interval"`
}

type OAuth struct {
	Client      *http.Client
	OpenBrowser chatgpt.BrowserOpener
	Sleep       func(time.Duration)
}

func (o OAuth) Login(ctx context.Context, output io.Writer) (Bundle, error) {
	client := o.Client
	if client == nil {
		client = &http.Client{Timeout: 30 * time.Second}
	}
	device, err := requestDeviceCode(ctx, client)
	if err != nil {
		return Bundle{}, err
	}
	_, _ = fmt.Fprintf(output, "Open %s and enter code %s.\n", device.VerificationURI, device.UserCode)
	opener := o.OpenBrowser
	if opener == nil {
		opener = chatgpt.OpenBrowser
	}
	if err := opener(device.VerificationURI); err != nil {
		return Bundle{}, fmt.Errorf("open browser: %w", err)
	}
	token, err := o.poll(ctx, client, device)
	if err != nil {
		return Bundle{}, err
	}
	if err := verifyCopilot(ctx, client, token); err != nil {
		return Bundle{}, err
	}
	username, err := githubUsername(ctx, client, token)
	if err != nil {
		return Bundle{}, err
	}
	return Bundle{GitHubToken: token, Username: username, Alias: username}, nil
}

func requestDeviceCode(ctx context.Context, client *http.Client) (deviceCode, error) {
	body, _ := json.Marshal(map[string]string{
		"client_id": "Iv1.b507a08c87ecfe98",
		"scope":     "read:user",
	})
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, "https://github.com/login/device/code", bytes.NewReader(body))
	if err != nil {
		return deviceCode{}, errors.New("could not create the GitHub device request")
	}
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Accept", "application/json")
	var result deviceCode
	if err := doJSON(client, request, &result); err != nil {
		return deviceCode{}, fmt.Errorf("GitHub device login: %w", err)
	}
	if result.DeviceCode == "" || result.UserCode == "" || result.VerificationURI == "" {
		return deviceCode{}, errors.New("GitHub device login returned an incomplete response")
	}
	return result, nil
}

func (o OAuth) poll(ctx context.Context, client *http.Client, device deviceCode) (string, error) {
	deadline := time.Now().Add(time.Duration(device.ExpiresIn) * time.Second)
	interval := time.Duration(device.Interval+1) * time.Second
	if interval <= 0 {
		interval = 6 * time.Second
	}
	sleep := o.Sleep
	if sleep == nil {
		sleep = time.Sleep
	}
	for time.Now().Before(deadline) {
		sleep(interval)
		body, _ := json.Marshal(map[string]string{
			"client_id":   "Iv1.b507a08c87ecfe98",
			"device_code": device.DeviceCode,
			"grant_type":  "urn:ietf:params:oauth:grant-type:device_code",
		})
		request, err := http.NewRequestWithContext(ctx, http.MethodPost, "https://github.com/login/oauth/access_token", bytes.NewReader(body))
		if err != nil {
			return "", errors.New("could not create the GitHub token request")
		}
		request.Header.Set("Content-Type", "application/json")
		request.Header.Set("Accept", "application/json")
		var result struct {
			AccessToken string `json:"access_token"`
			Error       string `json:"error"`
		}
		if err := doJSON(client, request, &result); err != nil {
			return "", fmt.Errorf("GitHub device token: %w", err)
		}
		if result.AccessToken != "" {
			return result.AccessToken, nil
		}
		switch result.Error {
		case "authorization_pending":
			continue
		case "slow_down":
			interval += 5 * time.Second
			continue
		default:
			return "", fmt.Errorf("GitHub device login failed: %s", result.Error)
		}
	}
	return "", errors.New("GitHub device login timed out")
}

func verifyCopilot(ctx context.Context, client *http.Client, token string) error {
	request, _ := http.NewRequestWithContext(ctx, http.MethodGet, "https://api.github.com/copilot_internal/v2/token", nil)
	request.Header.Set("Authorization", "token "+token)
	request.Header.Set("Editor-Version", "vscode/1.96.2")
	request.Header.Set("Editor-Plugin-Version", "copilot-chat/0.26.7")
	request.Header.Set("User-Agent", "GitHubCopilotChat/0.26.7")
	request.Header.Set("Accept", "application/json")
	response, err := client.Do(request)
	if err != nil {
		return errors.New("GitHub Copilot verification could not be reached")
	}
	defer response.Body.Close()
	_, _ = io.Copy(io.Discard, response.Body)
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return fmt.Errorf("GitHub Copilot access was denied (HTTP %d)", response.StatusCode)
	}
	return nil
}

func githubUsername(ctx context.Context, client *http.Client, token string) (string, error) {
	request, _ := http.NewRequestWithContext(ctx, http.MethodGet, "https://api.github.com/user", nil)
	request.Header.Set("Authorization", "token "+token)
	request.Header.Set("Accept", "application/json")
	request.Header.Set("User-Agent", "prism")
	var profile struct {
		Login string `json:"login"`
	}
	if err := doJSON(client, request, &profile); err != nil {
		return "", fmt.Errorf("GitHub profile: %w", err)
	}
	if profile.Login == "" {
		return "", errors.New("GitHub profile did not include a username")
	}
	return profile.Login, nil
}

func doJSON(client *http.Client, request *http.Request, output any) error {
	response, err := client.Do(request)
	if err != nil {
		return errors.New("request could not be reached")
	}
	defer response.Body.Close()
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		_, _ = io.Copy(io.Discard, response.Body)
		return fmt.Errorf("HTTP %d", response.StatusCode)
	}
	if err := json.NewDecoder(io.LimitReader(response.Body, 1<<20)).Decode(output); err != nil {
		return errors.New("response contained invalid JSON")
	}
	return nil
}
