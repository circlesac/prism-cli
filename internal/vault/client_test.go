package vault

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/circlesac/prism-cli/internal/chatgpt"
)

func TestChatGPTLifecycleUsesPersonalVaultWithoutLeakingTokensIntoMetadata(t *testing.T) {
	var mutex sync.Mutex
	vaultCreated := false
	itemCreated := false
	itemDeleted := false
	var storedBody itemBody

	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		mutex.Lock()
		defer mutex.Unlock()
		if request.Header.Get("Authorization") != "Bearer circles-key" {
			t.Errorf("authorization = %q", request.Header.Get("Authorization"))
		}
		response.Header().Set("Content-Type", "application/json")

		switch {
		case request.Method == http.MethodGet && request.URL.Path == "/v1/vaults":
			if !vaultCreated {
				_, _ = response.Write([]byte("[]"))
				return
			}
			_, _ = response.Write([]byte(`[{"id":"vault-1","name":"Prism"}]`))
		case request.Method == http.MethodPost && request.URL.Path == "/v1/vaults":
			vaultCreated = true
			_, _ = response.Write([]byte(`{"id":"vault-1","name":"Prism"}`))
		case request.Method == http.MethodGet && request.URL.Path == "/v1/vaults/vault-1/items":
			if !itemCreated || itemDeleted {
				_, _ = response.Write([]byte("[]"))
				return
			}
			_ = json.NewEncoder(response).Encode([]itemRecord{{
				ID:      "item-1",
				Title:   storedBody.Title,
				Version: 1,
				Tags:    storedBody.Tags,
			}})
		case request.Method == http.MethodPost && request.URL.Path == "/v1/vaults/vault-1/items":
			if err := json.NewDecoder(request.Body).Decode(&storedBody); err != nil {
				t.Error(err)
			}
			itemCreated = true
			_, _ = response.Write([]byte(`{"id":"item-1","title":"created","version":1}`))
		case request.Method == http.MethodDelete && request.URL.Path == "/v1/vaults/vault-1/items/item-1":
			itemDeleted = true
			response.WriteHeader(http.StatusNoContent)
		default:
			t.Errorf("unexpected request: %s %s", request.Method, request.URL.String())
			http.Error(response, "unexpected", http.StatusNotFound)
		}
	}))
	defer server.Close()

	client := Client{
		BaseURL:    server.URL,
		Token:      "circles-key",
		HTTPClient: server.Client(),
	}
	bundle := chatgpt.Bundle{
		AccessToken:  "access-secret",
		RefreshToken: "refresh-secret",
		AccountID:    "account-123",
		Alias:        "person@example.com",
		ExpiresAt:    123456,
	}
	if err := client.UpsertChatGPT(context.Background(), bundle); err != nil {
		t.Fatal(err)
	}
	metadata, err := json.Marshal(struct {
		Title string
		Tags  []string
	}{storedBody.Title, storedBody.Tags})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(metadata), "access-secret") ||
		strings.Contains(string(metadata), "refresh-secret") {
		t.Fatal("provider token leaked into Vault item metadata")
	}
	credentialField := ""
	for _, field := range storedBody.Fields {
		if field.ID == "credential" {
			credentialField = field.Value
		}
	}
	var storedBundle chatgpt.Bundle
	if err := json.Unmarshal([]byte(credentialField), &storedBundle); err != nil {
		t.Fatal(err)
	}
	if storedBundle.RefreshToken != "refresh-secret" {
		t.Fatal("credential bundle did not contain the refresh token")
	}

	accounts, err := client.ListChatGPT(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(accounts) != 1 ||
		accounts[0].ID != "account-123" ||
		accounts[0].Alias != "person@example.com" {
		t.Fatalf("accounts = %#v", accounts)
	}
	removed, err := client.RemoveChatGPT(context.Background(), "account-123")
	if err != nil {
		t.Fatal(err)
	}
	if !removed || !itemDeleted {
		t.Fatal("account was not deleted")
	}
}

func TestExistingOrgAccountUsesOrgNamespaceAndVersionPrecondition(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		response.Header().Set("Content-Type", "application/json")
		switch {
		case request.Method == http.MethodGet && request.URL.Path == "/circlesac/v1/vaults":
			_, _ = response.Write([]byte(`[{"id":"vault-1","name":"Prism"}]`))
		case request.Method == http.MethodGet && request.URL.Path == "/circlesac/v1/vaults/vault-1/items":
			_, _ = response.Write([]byte(
				`[{"id":"item-1","title":"ChatGPT — old","version":7,"tags":["prism","provider:chatgpt","account:account-123"]}]`,
			))
		case request.Method == http.MethodPut && request.URL.Path == "/circlesac/v1/vaults/vault-1/items/item-1":
			if request.Header.Get("If-Match") != "7" {
				t.Errorf("If-Match = %q", request.Header.Get("If-Match"))
			}
			_, _ = response.Write([]byte(`{"id":"item-1","version":8}`))
		default:
			t.Errorf("unexpected request: %s %s", request.Method, request.URL.String())
			http.Error(response, "unexpected", http.StatusNotFound)
		}
	}))
	defer server.Close()

	client := Client{
		BaseURL:    server.URL,
		Token:      "circles-key",
		Org:        "circlesac",
		HTTPClient: server.Client(),
	}
	err := client.UpsertChatGPT(context.Background(), chatgpt.Bundle{
		AccessToken:  "access-secret",
		RefreshToken: "refresh-secret",
		AccountID:    "account-123",
		Alias:        "new@example.com",
		ExpiresAt:    123456,
	})
	if err != nil {
		t.Fatal(err)
	}
}
