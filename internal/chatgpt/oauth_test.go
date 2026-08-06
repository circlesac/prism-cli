package chatgpt

import (
	"context"
	"encoding/base64"
	"encoding/json"
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

func TestLoginCompletesLoopbackPKCEFlow(t *testing.T) {
	accountID := "account-123"
	idToken := testJWT(map[string]any{
		"chatgpt_account_id":                   accountID,
		"https://api.openai.com/profile.email": "person@example.com",
	})
	client := &http.Client{
		Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
			if request.URL.String() != "https://auth.openai.com/oauth/token" {
				t.Fatalf("unexpected token URL: %s", request.URL)
			}
			body, err := io.ReadAll(request.Body)
			if err != nil {
				t.Fatal(err)
			}
			values, err := url.ParseQuery(string(body))
			if err != nil {
				t.Fatal(err)
			}
			if values.Get("code") != "test-code" {
				t.Fatalf("unexpected code: %q", values.Get("code"))
			}
			if len(values.Get("code_verifier")) < 43 {
				t.Fatal("PKCE verifier was too short")
			}
			response, err := json.Marshal(tokenResponse{
				IDToken:      idToken,
				AccessToken:  testJWT(map[string]any{"sub": "access"}),
				RefreshToken: "refresh-token",
				ExpiresIn:    3600,
			})
			if err != nil {
				t.Fatal(err)
			}
			return &http.Response{
				StatusCode: http.StatusOK,
				Header:     http.Header{"Content-Type": []string{"application/json"}},
				Body:       io.NopCloser(strings.NewReader(string(response))),
			}, nil
		}),
	}

	callbackDone := make(chan error, 1)
	login := OAuth{
		Client:  client,
		Timeout: 5 * time.Second,
		OpenBrowser: func(target string) error {
			authorization, err := url.Parse(target)
			if err != nil {
				return err
			}
			if authorization.Host != "auth.openai.com" {
				t.Fatalf("unexpected authorization host: %s", authorization.Host)
			}
			if authorization.Query().Get("code_challenge_method") != "S256" {
				t.Fatal("authorization did not request S256 PKCE")
			}
			go func() {
				callback := "http://127.0.0.1:1455/auth/callback?code=test-code&state=" +
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
	}

	bundle, err := login.Login(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if err := <-callbackDone; err != nil {
		t.Fatal(err)
	}
	if bundle.AccountID != accountID {
		t.Fatalf("account ID = %q, want %q", bundle.AccountID, accountID)
	}
	if bundle.Alias != "person@example.com" {
		t.Fatalf("alias = %q", bundle.Alias)
	}
	if bundle.RefreshToken != "refresh-token" {
		t.Fatal("refresh token was not returned")
	}
	if bundle.IDToken != idToken {
		t.Fatal("ID token was not preserved for server-side account verification")
	}
}

func TestBundleUsesNestedAccountClaimAndNameAlias(t *testing.T) {
	tokens := tokenResponse{
		IDToken: testJWT(map[string]any{
			"https://api.openai.com/auth": map[string]any{
				"chatgpt_account_id": "12345678-abcdefgh",
			},
			"name": "Example Person",
		}),
		AccessToken:  testJWT(map[string]any{"sub": "access"}),
		RefreshToken: "refresh-token",
		ExpiresIn:    60,
	}
	bundle, err := bundleFromTokens(tokens, time.Unix(100, 0))
	if err != nil {
		t.Fatal(err)
	}
	if bundle.AccountID != "12345678-abcdefgh" {
		t.Fatalf("account ID = %q", bundle.AccountID)
	}
	if bundle.Alias != "Example Person (12345678)" {
		t.Fatalf("alias = %q", bundle.Alias)
	}
	if bundle.ExpiresAt != time.Unix(160, 0).UnixMilli() {
		t.Fatalf("expires_at = %d", bundle.ExpiresAt)
	}
}

func testJWT(claims map[string]any) string {
	header, _ := json.Marshal(map[string]string{"alg": "none"})
	payload, _ := json.Marshal(claims)
	return base64.RawURLEncoding.EncodeToString(header) + "." +
		base64.RawURLEncoding.EncodeToString(payload) + ".signature"
}
