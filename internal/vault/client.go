package vault

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/circlesac/prism-cli/internal/chatgpt"
)

type Client struct {
	BaseURL    string
	Token      string
	Org        string
	HTTPClient *http.Client
}

type Account struct {
	ID    string
	Alias string
}

type vaultRecord struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

type itemRecord struct {
	ID      string   `json:"id"`
	Title   string   `json:"title"`
	Version int      `json:"version"`
	Tags    []string `json:"tags"`
}

type field struct {
	ID      string `json:"id"`
	Type    string `json:"type"`
	Label   string `json:"label"`
	Value   string `json:"value"`
	Purpose string `json:"purpose,omitempty"`
}

type itemBody struct {
	Title    string   `json:"title"`
	Category string   `json:"category"`
	Tags     []string `json:"tags"`
	Fields   []field  `json:"fields"`
}

func (c Client) UpsertChatGPT(ctx context.Context, bundle chatgpt.Bundle) error {
	if err := c.validate(); err != nil {
		return err
	}
	if err := validateAccountID(bundle.AccountID); err != nil {
		return err
	}
	vault, err := c.ensurePrismVault(ctx)
	if err != nil {
		return err
	}
	items, err := c.listChatGPTItems(ctx, vault.ID, bundle.AccountID)
	if err != nil {
		return err
	}
	if len(items) > 1 {
		return fmt.Errorf("Vault contains more than one ChatGPT record for account %q", bundle.AccountID)
	}
	credential, err := json.Marshal(bundle)
	if err != nil {
		return errors.New("could not encode the ChatGPT credential bundle")
	}
	title := "ChatGPT — " + bundle.AccountID
	if bundle.Alias != "" {
		title = "ChatGPT — " + bundle.Alias
	}
	body := itemBody{
		Title:    title,
		Category: "API_CREDENTIAL",
		Tags:     chatGPTTags(bundle.AccountID),
		Fields: []field{
			{
				ID:    "credential",
				Type:  "CONCEALED",
				Label: "credential",
				Value: string(credential),
			},
			{
				ID:    "provider",
				Type:  "STRING",
				Label: "provider",
				Value: "chatgpt",
			},
			{
				ID:    "account_id",
				Type:  "STRING",
				Label: "account_id",
				Value: bundle.AccountID,
			},
			{
				ID:    "alias",
				Type:  "STRING",
				Label: "alias",
				Value: bundle.Alias,
			},
			{
				ID:    "schema_version",
				Type:  "STRING",
				Label: "schema_version",
				Value: "1",
			},
		},
	}

	if len(items) == 0 {
		return c.request(ctx, http.MethodPost, c.namespacePath()+"/vaults/"+url.PathEscape(vault.ID)+"/items", body, nil, nil)
	}
	headers := http.Header{}
	headers.Set("If-Match", strconv.Itoa(items[0].Version))
	return c.request(
		ctx,
		http.MethodPut,
		c.namespacePath()+"/vaults/"+url.PathEscape(vault.ID)+"/items/"+url.PathEscape(items[0].ID),
		body,
		nil,
		headers,
	)
}

func (c Client) ListChatGPT(ctx context.Context) ([]Account, error) {
	if err := c.validate(); err != nil {
		return nil, err
	}
	vaults, err := c.listVaults(ctx)
	if err != nil {
		return nil, err
	}
	var prismVault *vaultRecord
	for index := range vaults {
		if vaults[index].Name == "Prism" {
			if prismVault != nil {
				return nil, errors.New("Vault contains more than one vault named Prism")
			}
			prismVault = &vaults[index]
		}
	}
	if prismVault == nil {
		return []Account{}, nil
	}
	items, err := c.listChatGPTItems(ctx, prismVault.ID, "")
	if err != nil {
		return nil, err
	}
	accounts := make([]Account, 0, len(items))
	for _, item := range items {
		accountID := accountIDFromTags(item.Tags)
		if accountID == "" {
			continue
		}
		alias := strings.TrimPrefix(item.Title, "ChatGPT — ")
		if alias == item.Title {
			alias = ""
		}
		accounts = append(accounts, Account{ID: accountID, Alias: alias})
	}
	return accounts, nil
}

func (c Client) RemoveChatGPT(ctx context.Context, accountID string) (bool, error) {
	if err := c.validate(); err != nil {
		return false, err
	}
	if err := validateAccountID(accountID); err != nil {
		return false, err
	}
	vaults, err := c.listVaults(ctx)
	if err != nil {
		return false, err
	}
	for _, vault := range vaults {
		if vault.Name != "Prism" {
			continue
		}
		items, err := c.listChatGPTItems(ctx, vault.ID, accountID)
		if err != nil {
			return false, err
		}
		if len(items) > 1 {
			return false, fmt.Errorf("Vault contains more than one ChatGPT record for account %q", accountID)
		}
		if len(items) == 0 {
			return false, nil
		}
		err = c.request(
			ctx,
			http.MethodDelete,
			c.namespacePath()+"/vaults/"+url.PathEscape(vault.ID)+"/items/"+url.PathEscape(items[0].ID),
			nil,
			nil,
			nil,
		)
		return err == nil, err
	}
	return false, nil
}

func (c Client) ensurePrismVault(ctx context.Context) (vaultRecord, error) {
	vaults, err := c.listVaults(ctx)
	if err != nil {
		return vaultRecord{}, err
	}
	var match *vaultRecord
	for index := range vaults {
		if vaults[index].Name != "Prism" {
			continue
		}
		if match != nil {
			return vaultRecord{}, errors.New("Vault contains more than one vault named Prism")
		}
		match = &vaults[index]
	}
	if match != nil {
		return *match, nil
	}

	var created vaultRecord
	err = c.request(
		ctx,
		http.MethodPost,
		c.namespacePath()+"/vaults",
		map[string]string{
			"name":        "Prism",
			"description": "Provider credentials managed by Prism",
		},
		&created,
		nil,
	)
	return created, err
}

func (c Client) listVaults(ctx context.Context) ([]vaultRecord, error) {
	query := url.Values{}
	query.Set("filter", `name co "Prism"`)
	var result []vaultRecord
	err := c.request(
		ctx,
		http.MethodGet,
		c.namespacePath()+"/vaults?"+query.Encode(),
		nil,
		&result,
		nil,
	)
	return result, err
}

func (c Client) listChatGPTItems(
	ctx context.Context,
	vaultID string,
	accountID string,
) ([]itemRecord, error) {
	tags := []string{"prism", "provider:chatgpt"}
	if accountID != "" {
		tags = append(tags, "account:"+accountID)
	}
	query := url.Values{}
	query.Set("tags", strings.Join(tags, ","))
	var result []itemRecord
	err := c.request(
		ctx,
		http.MethodGet,
		c.namespacePath()+"/vaults/"+url.PathEscape(vaultID)+"/items?"+query.Encode(),
		nil,
		&result,
		nil,
	)
	return result, err
}

func (c Client) request(
	ctx context.Context,
	method string,
	path string,
	body any,
	output any,
	headers http.Header,
) error {
	var requestBody io.Reader
	if body != nil {
		encoded, err := json.Marshal(body)
		if err != nil {
			return errors.New("could not encode the Vault request")
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
		return errors.New("could not create the Vault request")
	}
	request.Header.Set("Authorization", "Bearer "+c.Token)
	if body != nil {
		request.Header.Set("Content-Type", "application/json")
	}
	for key, values := range headers {
		for _, value := range values {
			request.Header.Add(key, value)
		}
	}

	client := c.HTTPClient
	if client == nil {
		client = &http.Client{Timeout: 30 * time.Second}
	}
	response, err := client.Do(request)
	if err != nil {
		return errors.New("Circles Vault could not be reached")
	}
	defer response.Body.Close()
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		var errorResponse struct {
			Message string `json:"message"`
		}
		_ = json.NewDecoder(io.LimitReader(response.Body, 64<<10)).Decode(&errorResponse)
		if errorResponse.Message == "" {
			errorResponse.Message = http.StatusText(response.StatusCode)
		}
		return fmt.Errorf("Circles Vault returned HTTP %d: %s", response.StatusCode, errorResponse.Message)
	}
	if output == nil || response.StatusCode == http.StatusNoContent {
		_, _ = io.Copy(io.Discard, response.Body)
		return nil
	}
	if err := json.NewDecoder(io.LimitReader(response.Body, 2<<20)).Decode(output); err != nil {
		return errors.New("Circles Vault returned invalid JSON")
	}
	return nil
}

func (c Client) validate() error {
	endpoint, err := url.Parse(c.BaseURL)
	if err != nil || endpoint.Scheme != "https" && endpoint.Scheme != "http" || endpoint.Host == "" {
		return errors.New("Circles Vault URL is invalid")
	}
	if strings.TrimSpace(c.Token) == "" || strings.ContainsAny(c.Token, " \t\r\n") {
		return errors.New("Circles credential is invalid")
	}
	if c.Org != "" && !regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]*$`).MatchString(c.Org) {
		return errors.New("organization slug is invalid")
	}
	return nil
}

func (c Client) namespacePath() string {
	if c.Org == "" {
		return "/v1"
	}
	return "/" + url.PathEscape(c.Org) + "/v1"
}

func chatGPTTags(accountID string) []string {
	return []string{"prism", "provider:chatgpt", "account:" + accountID}
}

func accountIDFromTags(tags []string) string {
	for _, tag := range tags {
		if strings.HasPrefix(tag, "account:") {
			return strings.TrimPrefix(tag, "account:")
		}
	}
	return ""
}

func validateAccountID(accountID string) error {
	if accountID == "" ||
		len(accountID) > 200 ||
		strings.ContainsAny(accountID, ",\r\n\t") {
		return errors.New("ChatGPT account ID is invalid")
	}
	return nil
}
