package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestSupportedProvidersExcludeUsageBilledGeminiAIStudio(t *testing.T) {
	if SupportedProvider("gemini-ai") {
		t.Fatal("gemini-ai must remain disabled")
	}
	if !SupportedProvider("gemini") {
		t.Fatal("Gemini subscription OAuth must remain supported")
	}
}

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
			_, _ = response.Write([]byte(`{"provider":"chatgpt","accounts":[{"id":"01j00000000000000000000002","name":"person@example.com","plan":"pro","observed_at":"2026-08-06T07:25:00Z","limits":[{"name":"default","window":"primary","used_percent":88,"remaining_percent":12,"limit_reached":false,"reset_at":"2026-08-11T00:24:55Z","window_seconds":604800}]}]}`))
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
	if err != nil || usage.Provider != "chatgpt" || len(usage.Accounts) != 1 ||
		usage.Accounts[0].Limits[0].RemainingPercent != 12 ||
		usage.Accounts[0].Limits[0].WindowSeconds == nil ||
		*usage.Accounts[0].Limits[0].WindowSeconds != 604800 {
		t.Fatalf("usage = %#v, err = %v", usage, err)
	}
}

func TestConsumeChatGPTResetUsesExplicitAccountAndIdempotencyKey(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		if request.Method != http.MethodPost || request.URL.Path != "/usage/chatgpt/reset" {
			t.Fatalf("request = %s %s", request.Method, request.URL.Path)
		}
		if request.Header.Get("Authorization") != "Bearer circles-key" {
			t.Fatalf("authorization = %q", request.Header.Get("Authorization"))
		}
		var body ChatGPTResetRequest
		if err := json.NewDecoder(request.Body).Decode(&body); err != nil {
			t.Fatal(err)
		}
		if body.Account != "person@example.com" || body.CreditID != "credit-1" || body.IdempotencyKey != "request-1" {
			t.Fatalf("body = %#v", body)
		}
		response.Header().Set("Content-Type", "application/json")
		_, _ = response.Write([]byte(`{"provider":"chatgpt","account":{"id":"credential-1","name":"person@example.com"},"outcome":"reset","windows_reset":2,"usage_refreshed":true}`))
	}))
	defer server.Close()

	client := Client{BaseURL: server.URL, Token: "circles-key", HTTPClient: server.Client()}
	result, err := client.ConsumeChatGPTReset(context.Background(), ChatGPTResetRequest{
		Account: "person@example.com", CreditID: "credit-1", IdempotencyKey: "request-1",
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.Outcome != "reset" || result.WindowsReset == nil || *result.WindowsReset != 2 || !result.UsageRefreshed {
		t.Fatalf("result = %#v", result)
	}
}

func TestInferenceMapsAPIPathAndProviderHeader(t *testing.T) {
	server := httptest.NewTLSServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		if request.URL.Path != "/v1/messages" {
			t.Errorf("path = %q", request.URL.Path)
		}
		if request.Header.Get("Authorization") != "Bearer test-token" {
			t.Errorf("authorization = %q", request.Header.Get("Authorization"))
		}
		if request.Header.Get("X-Prism-Provider") != "anthropic" {
			t.Errorf("provider = %q", request.Header.Get("X-Prism-Provider"))
		}
		response.Header().Set("Content-Type", "application/json")
		_, _ = response.Write([]byte(`{"content":[{"type":"text","text":"ok"}]}`))
	}))
	defer server.Close()

	client := Client{BaseURL: server.URL, Token: "test-token", HTTPClient: server.Client()}
	response, err := client.Inference(context.Background(), InferenceRequest{
		API: "messages", Provider: "anthropic", Body: []byte(`{"model":"claude-test"}`),
	})
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		t.Fatalf("status = %d", response.StatusCode)
	}
}

func TestInferencePathMapsEverySupportedAPI(t *testing.T) {
	for apiName, want := range map[string]string{
		"chat":        "/v1/chat/completions",
		"completions": "/v1/completions",
		"responses":   "/v1/responses",
		"messages":    "/v1/messages",
	} {
		if got, ok := InferencePath(apiName); !ok || got != want {
			t.Fatalf("InferencePath(%q) = %q, %v; want %q", apiName, got, ok, want)
		}
	}
	if _, ok := InferencePath("unknown"); ok {
		t.Fatal("unknown API was accepted")
	}
}

func TestInferenceRejectsNonHTTPSAndClearsLegacyTotalTimeout(t *testing.T) {
	client := Client{BaseURL: "http://example.com", Token: "test-token"}
	if _, err := client.Inference(context.Background(), InferenceRequest{API: "chat", Body: []byte(`{}`)}); err == nil {
		t.Fatal("HTTP endpoint was accepted")
	}

	server := httptest.NewTLSServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		response.Header().Set("Content-Type", "application/json")
		_, _ = response.Write([]byte(`{}`))
	}))
	defer server.Close()
	legacy := *server.Client()
	legacy.Timeout = 1
	client = Client{BaseURL: server.URL, Token: "test-token", HTTPClient: &legacy}
	response, err := client.Inference(context.Background(), InferenceRequest{API: "chat", Body: []byte(`{}`)})
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
}
