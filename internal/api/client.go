package api

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

type Client struct {
	BaseURL    string
	Token      string
	HTTPClient *http.Client
}

type Credential struct {
	ID       string `json:"id"`
	Provider string `json:"provider"`
	Name     string `json:"name"`
}

type UsageLimit struct {
	Name             string  `json:"name"`
	Window           string  `json:"window"`
	UsedPercent      float64 `json:"used_percent"`
	RemainingPercent float64 `json:"remaining_percent"`
	LimitReached     bool    `json:"limit_reached"`
	ResetAt          *string `json:"reset_at"`
	WindowSeconds    *int    `json:"window_seconds"`
}

type UsageResetCredit struct {
	GrantedAt   *string `json:"granted_at"`
	ExpiresAt   *string `json:"expires_at"`
	Title       *string `json:"title"`
	Description *string `json:"description"`
}

type UsageResetCredits struct {
	AvailableCount int                `json:"available_count"`
	Credits        []UsageResetCredit `json:"credits"`
}

type UsageError struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

type UsageAccount struct {
	ID           string             `json:"id"`
	Name         string             `json:"name"`
	Plan         *string            `json:"plan"`
	ObservedAt   string             `json:"observed_at"`
	Status       string             `json:"status"`
	Limits       []UsageLimit       `json:"limits"`
	ResetCredits *UsageResetCredits `json:"reset_credits,omitempty"`
	Error        *UsageError        `json:"error"`
}

type ChatGPTResetRequest struct {
	Account        string `json:"account"`
	CreditID       string `json:"credit_id,omitempty"`
	IdempotencyKey string `json:"idempotency_key,omitempty"`
}

type ChatGPTResetResponse struct {
	Provider       string             `json:"provider"`
	Account        Credential         `json:"account"`
	Outcome        string             `json:"outcome"`
	WindowsReset   *int               `json:"windows_reset,omitempty"`
	UsageRefreshed bool               `json:"usage_refreshed"`
	ResetCredits   *UsageResetCredits `json:"reset_credits,omitempty"`
}

type ProviderUsage struct {
	Provider string         `json:"provider"`
	Accounts []UsageAccount `json:"accounts"`
}

var providers = map[string]struct{}{
	"chatgpt": {}, "anthropic": {}, "copilot": {}, "gemini": {}, "cursor": {},
	"groq": {}, "mistral": {}, "deepseek": {}, "opencode-go": {},
	"cloudflare": {}, "vercel": {}, "gemini-app": {},
}

func SupportedProvider(provider string) bool {
	_, ok := providers[provider]
	return ok
}

func (c Client) Save(
	ctx context.Context,
	provider string,
	name string,
	credential any,
) (Credential, error) {
	var saved Credential
	err := c.request(ctx, http.MethodPost, "/credentials/"+url.PathEscape(provider), map[string]any{
		"name":       name,
		"credential": credential,
	}, &saved)
	return saved, err
}

func (c Client) List(ctx context.Context, provider string) ([]Credential, error) {
	path := "/credentials"
	if provider != "" {
		path += "/" + url.PathEscape(provider)
	}
	var result struct {
		Data []Credential `json:"data"`
	}
	err := c.request(ctx, http.MethodGet, path, nil, &result)
	return result.Data, err
}

func (c Client) Remove(ctx context.Context, id string) error {
	return c.request(ctx, http.MethodDelete, "/credentials/"+url.PathEscape(id), nil, nil)
}

func (c Client) Usage(ctx context.Context, provider string) (ProviderUsage, error) {
	var usage ProviderUsage
	err := c.request(ctx, http.MethodGet, "/usage/"+url.PathEscape(provider), nil, &usage)
	return usage, err
}

func (c Client) ConsumeChatGPTReset(ctx context.Context, request ChatGPTResetRequest) (ChatGPTResetResponse, error) {
	var response ChatGPTResetResponse
	err := c.request(ctx, http.MethodPost, "/usage/chatgpt/reset", request, &response)
	return response, err
}

func (c Client) request(
	ctx context.Context,
	method string,
	path string,
	body any,
	output any,
) error {
	endpoint, err := url.Parse(c.BaseURL)
	if err != nil || endpoint.Scheme != "https" && endpoint.Scheme != "http" || endpoint.Host == "" {
		return errors.New("Prism URL is invalid")
	}
	if strings.TrimSpace(c.Token) == "" || strings.ContainsAny(c.Token, " \t\r\n") {
		return errors.New("Circles credential is invalid")
	}

	var requestBody io.Reader
	if body != nil {
		encoded, encodeErr := json.Marshal(body)
		if encodeErr != nil {
			return errors.New("could not encode the Prism request")
		}
		requestBody = bytes.NewReader(encoded)
	}
	request, err := http.NewRequestWithContext(
		ctx,
		method,
		strings.TrimSuffix(c.BaseURL, "/")+path,
		requestBody,
	)
	if err != nil {
		return errors.New("could not create the Prism request")
	}
	request.Header.Set("Authorization", "Bearer "+c.Token)
	if body != nil {
		request.Header.Set("Content-Type", "application/json")
	}

	client := c.HTTPClient
	if client == nil {
		client = &http.Client{Timeout: 30 * time.Second}
	}
	response, err := client.Do(request)
	if err != nil {
		return errors.New("Prism could not be reached")
	}
	defer response.Body.Close()
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		var errorResponse struct {
			Error string `json:"error"`
		}
		_ = json.NewDecoder(io.LimitReader(response.Body, 64<<10)).Decode(&errorResponse)
		if errorResponse.Error == "" {
			errorResponse.Error = http.StatusText(response.StatusCode)
		}
		return fmt.Errorf("Prism returned HTTP %d: %s", response.StatusCode, errorResponse.Error)
	}
	if output == nil || response.StatusCode == http.StatusNoContent {
		_, _ = io.Copy(io.Discard, response.Body)
		return nil
	}
	if err := json.NewDecoder(io.LimitReader(response.Body, 2<<20)).Decode(output); err != nil {
		return errors.New("Prism returned invalid JSON")
	}
	return nil
}
