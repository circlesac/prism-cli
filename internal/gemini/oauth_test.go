package gemini

import (
	"context"
	"io"
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"
)

type roundTripFunc func(*http.Request) (*http.Response, error)

func (function roundTripFunc) RoundTrip(request *http.Request) (*http.Response, error) {
	return function(request)
}

func TestLoginDerivesProjectAndAliasFromGoogleCallback(t *testing.T) {
	client := &http.Client{Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
		body := ""
		switch request.URL.String() {
		case "https://oauth2.googleapis.com/token":
			body = `{"access_token":"google-access","refresh_token":"google-refresh","expires_in":3600}`
		case "https://cloudcode-pa.googleapis.com/v1internal:loadCodeAssist":
			body = `{"currentTier":{"tierId":"free-tier"},"cloudaicompanionProject":"project-123"}`
		case "https://www.googleapis.com/oauth2/v3/userinfo":
			body = `{"email":"person@example.com","name":"Person"}`
		default:
			t.Fatalf("unexpected request: %s", request.URL)
		}
		return &http.Response{
			StatusCode: http.StatusOK,
			Header:     http.Header{"Content-Type": []string{"application/json"}},
			Body:       io.NopCloser(strings.NewReader(body)),
		}, nil
	})}
	callbackDone := make(chan error, 1)
	bundle, err := (OAuth{
		Client:       client,
		ClientID:     "test-client-id",
		ClientSecret: "test-client-secret",
		Timeout:      5 * time.Second,
		OpenBrowser: func(target string) error {
			authorization, err := url.Parse(target)
			if err != nil {
				return err
			}
			if authorization.Host != "accounts.google.com" || authorization.Query().Get("code_challenge_method") != "S256" || authorization.Query().Get("client_id") != "test-client-id" {
				t.Fatalf("authorization URL = %s", target)
			}
			go func() {
				callback := "http://127.0.0.1:1456/auth/callback?code=test-code&state=" +
					url.QueryEscape(authorization.Query().Get("state"))
				response, callbackErr := http.Get(callback)
				if callbackErr == nil {
					_, _ = io.Copy(io.Discard, response.Body)
					callbackErr = response.Body.Close()
				}
				callbackDone <- callbackErr
			}()
			return nil
		},
	}).Login(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if err := <-callbackDone; err != nil {
		t.Fatal(err)
	}
	if bundle.ProjectID != "project-123" || bundle.AccountID != "project-123" || bundle.Alias != "person@example.com" || bundle.RefreshToken != "google-refresh" {
		t.Fatalf("bundle = %#v", bundle)
	}
}
