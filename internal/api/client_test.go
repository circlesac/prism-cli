package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestCredentialLifecycleUsesPrismAPIWithoutLeakingSecretsInURL(t *testing.T) {
	var savedRequest map[string]any
	removed := false
	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		if request.Header.Get("Authorization") != "Bearer circles-key" {
			t.Errorf("authorization = %q", request.Header.Get("Authorization"))
		}
		response.Header().Set("Content-Type", "application/json")
		switch {
		case request.Method == http.MethodPost && request.URL.Path == "/credentials/chatgpt":
			if request.URL.RawQuery != "" {
				t.Fatalf("credential leaked into query: %s", request.URL.RawQuery)
			}
			if err := json.NewDecoder(request.Body).Decode(&savedRequest); err != nil {
				t.Fatal(err)
			}
			response.WriteHeader(http.StatusCreated)
			_, _ = response.Write([]byte(`{"id":"01j00000000000000000000002","provider":"chatgpt","name":"person@example.com"}`))
		case request.Method == http.MethodGet && request.URL.Path == "/credentials/chatgpt":
			_, _ = response.Write([]byte(`{"data":[{"id":"01j00000000000000000000002","provider":"chatgpt","name":"person@example.com"}]}`))
		case request.Method == http.MethodGet && request.URL.Path == "/usage/chatgpt":
			_, _ = response.Write([]byte(`{"provider":"chatgpt","accounts":[{"id":"01j00000000000000000000002","name":"person@example.com","plan":"pro","observed_at":"2026-08-06T07:25:00Z","limits":[{"name":"default","window":"primary","used_percent":88,"remaining_percent":12,"limit_reached":false,"reset_at":"2026-08-11T00:24:55Z"}]}]}`))
		case request.Method == http.MethodDelete && request.URL.Path == "/credentials/01j00000000000000000000002":
			removed = true
			response.WriteHeader(http.StatusNoContent)
		default:
			http.Error(response, "unexpected", http.StatusNotFound)
		}
	}))
	defer server.Close()

	client := Client{BaseURL: server.URL, Token: "circles-key", HTTPClient: server.Client()}
	saved, err := client.Save(context.Background(), "chatgpt", "", map[string]string{
		"access_token": "access-secret", "refresh_token": "refresh-secret",
	})
	if err != nil {
		t.Fatal(err)
	}
	if saved.Name != "person@example.com" {
		t.Fatalf("saved = %#v", saved)
	}
	credential := savedRequest["credential"].(map[string]any)
	if credential["refresh_token"] != "refresh-secret" {
		t.Fatalf("credential = %#v", credential)
	}
	listed, err := client.List(context.Background(), "chatgpt")
	if err != nil || len(listed) != 1 || listed[0].ID != saved.ID {
		t.Fatalf("listed = %#v, err = %v", listed, err)
	}
	if err := client.Remove(context.Background(), saved.ID); err != nil {
		t.Fatal(err)
	}
	if !removed {
		t.Fatal("credential was not removed")
	}
	usage, err := client.Usage(context.Background(), "chatgpt")
	if err != nil || usage.Provider != "chatgpt" || len(usage.Accounts) != 1 || usage.Accounts[0].Limits[0].RemainingPercent != 12 {
		t.Fatalf("usage = %#v, err = %v", usage, err)
	}
}
