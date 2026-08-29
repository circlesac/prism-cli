package gemini

import (
	"context"
	"encoding/base64"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"
)

func TestImportAntigravityLogin(t *testing.T) {
	expires := time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC)
	secret := antigravityKeyringPrefix + base64.StdEncoding.EncodeToString([]byte(`{
		"auth_method":"consumer",
		"token":{"access_token":"provider-access","refresh_token":"provider-refresh","expiry":"2026-09-01T00:00:00Z"}
	}`))
	client := &http.Client{Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
		var body string
		switch request.URL.String() {
		case "https://daily-cloudcode-pa.googleapis.com/v1internal:loadCodeAssist":
			if request.Header.Get("Authorization") != "Bearer provider-access" || request.Header.Get("User-Agent") != "antigravity/cli/prism" {
				t.Fatalf("subscription headers = %#v", request.Header)
			}
			body = `{"cloudaicompanionProject":"project-123"}`
		case "https://www.googleapis.com/oauth2/v3/userinfo":
			body = `{"sub":"subject-123","email":"person@example.com","name":"Person"}`
		default:
			t.Fatalf("unexpected request: %s", request.URL)
		}
		return &http.Response{StatusCode: http.StatusOK, Header: http.Header{"Content-Type": []string{"application/json"}}, Body: io.NopCloser(strings.NewReader(body))}, nil
	})}

	bundle, err := (AntigravityImport{
		Client:      client,
		ReadSecret:  func() (string, error) { return secret, nil },
		ReadVersion: func(context.Context) (string, error) { return "1.1.22", nil },
		Now:         func() time.Time { return expires.Add(-time.Hour) },
	}).Import(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if bundle.ProjectID != "project-123" || bundle.Alias != "person@example.com" || bundle.AuthMethod != "antigravity" || bundle.UserAgent != "antigravity/cli/1.1.22" || bundle.RefreshToken != "provider-refresh" {
		t.Fatalf("bundle = %#v", bundle)
	}
}

func TestDecodeAntigravityLoginAcceptsKeyringDecodedJSON(t *testing.T) {
	login, err := decodeAntigravityLogin(`{
		"auth_method":"consumer",
		"token":{"access_token":"provider-access","refresh_token":"provider-refresh","expiry":"2026-09-01T00:00:00Z"}
	}`)
	if err != nil {
		t.Fatal(err)
	}
	if login.Token.AccessToken != "provider-access" {
		t.Fatalf("access token = %q", login.Token.AccessToken)
	}
}

func TestImportAntigravityRejectsStaleLogin(t *testing.T) {
	secret := antigravityKeyringPrefix + base64.StdEncoding.EncodeToString([]byte(`{
		"auth_method":"consumer",
		"token":{"access_token":"provider-access","refresh_token":"provider-refresh","expiry":"2026-08-01T00:00:00Z"}
	}`))
	_, err := (AntigravityImport{
		ReadSecret:  func() (string, error) { return secret, nil },
		ReadVersion: func(context.Context) (string, error) { return "1.1.22", nil },
		Now:         func() time.Time { return time.Date(2026, 8, 29, 0, 0, 0, 0, time.UTC) },
	}).Import(context.Background())
	if err == nil || !strings.Contains(err.Error(), "stale") {
		t.Fatalf("error = %v", err)
	}
}
