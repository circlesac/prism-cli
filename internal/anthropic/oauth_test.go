package anthropic

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"io"
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"
)

func TestLoginCapturesAValidatedLoopbackAuthorizationCode(t *testing.T) {
	var authorize *url.URL
	oauth := OAuth{
		Timeout: time.Second,
		OpenBrowser: func(target string) error {
			var err error
			authorize, err = url.Parse(target)
			if err != nil {
				return err
			}
			redirect := authorize.Query().Get("redirect_uri")
			state := authorize.Query().Get("state")
			go func() {
				response, requestErr := http.Get(redirect + "?code=one-time-code&state=" + url.QueryEscape(state))
				if requestErr == nil {
					_, _ = io.Copy(io.Discard, response.Body)
					_ = response.Body.Close()
				}
			}()
			return nil
		},
	}

	grant, err := oauth.Login(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if authorize.Host != "claude.com" || authorize.Path != "/cai/oauth/authorize" {
		t.Fatalf("authorize URL = %s", authorize)
	}
	query := authorize.Query()
	for key, want := range map[string]string{
		"code":                  "true",
		"client_id":             clientID,
		"response_type":         "code",
		"code_challenge_method": "S256",
		"scope":                 scope,
	} {
		if query.Get(key) != want {
			t.Fatalf("%s = %q, want %q", key, query.Get(key), want)
		}
	}
	if grant.AuthorizationCode != "one-time-code" || grant.State != query.Get("state") || grant.RedirectURI != query.Get("redirect_uri") {
		t.Fatalf("grant = %#v", grant)
	}
	if !strings.HasPrefix(grant.RedirectURI, "http://localhost:") || !strings.HasSuffix(grant.RedirectURI, "/callback") {
		t.Fatalf("redirect URI = %q", grant.RedirectURI)
	}
	sum := sha256.Sum256([]byte(grant.CodeVerifier))
	if query.Get("code_challenge") != base64.RawURLEncoding.EncodeToString(sum[:]) {
		t.Fatal("PKCE challenge does not match the verifier")
	}
}

func TestLoginRejectsACallbackWithTheWrongState(t *testing.T) {
	oauth := OAuth{
		Timeout: 100 * time.Millisecond,
		OpenBrowser: func(target string) error {
			authorize, _ := url.Parse(target)
			go func() {
				response, err := http.Get(authorize.Query().Get("redirect_uri") + "?code=one-time-code&state=wrong")
				if err == nil {
					_ = response.Body.Close()
				}
			}()
			return nil
		},
	}
	_, err := oauth.Login(context.Background())
	if err == nil || !strings.Contains(err.Error(), "state did not match") {
		t.Fatalf("error = %v", err)
	}
}

func TestLoginRejectsTheWrongCallbackHostWithoutConsumingTheGrant(t *testing.T) {
	oauth := OAuth{
		Timeout: time.Second,
		OpenBrowser: func(target string) error {
			authorize, _ := url.Parse(target)
			redirect := authorize.Query().Get("redirect_uri")
			state := authorize.Query().Get("state")
			go func() {
				request, _ := http.NewRequest(http.MethodGet, redirect+"?code=wrong-host&state="+url.QueryEscape(state), nil)
				request.Host = "attacker.example"
				response, err := http.DefaultClient.Do(request)
				if err == nil {
					_, _ = io.Copy(io.Discard, response.Body)
					_ = response.Body.Close()
				}
				response, err = http.Get(redirect + "?code=valid-code&state=" + url.QueryEscape(state))
				if err == nil {
					_, _ = io.Copy(io.Discard, response.Body)
					_ = response.Body.Close()
				}
			}()
			return nil
		},
	}
	grant, err := oauth.Login(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if grant.AuthorizationCode != "valid-code" {
		t.Fatalf("authorization code = %q", grant.AuthorizationCode)
	}
}

func TestLoginConsumesOnlyTheFirstValidCallback(t *testing.T) {
	oauth := OAuth{
		Timeout: time.Second,
		OpenBrowser: func(target string) error {
			authorize, _ := url.Parse(target)
			redirect := authorize.Query().Get("redirect_uri")
			state := url.QueryEscape(authorize.Query().Get("state"))
			for _, code := range []string{"first-code", "second-code"} {
				response, err := http.Get(redirect + "?code=" + code + "&state=" + state)
				if err != nil {
					return err
				}
				_, _ = io.Copy(io.Discard, response.Body)
				_ = response.Body.Close()
			}
			return nil
		},
	}
	grant, err := oauth.Login(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if grant.AuthorizationCode != "first-code" {
		t.Fatalf("authorization code = %q", grant.AuthorizationCode)
	}
}

func TestLoginTimesOutAndHonorsCancellation(t *testing.T) {
	for _, test := range []struct {
		name string
		ctx  func() context.Context
	}{
		{name: "timeout", ctx: func() context.Context { return context.Background() }},
		{name: "cancelled", ctx: func() context.Context {
			ctx, cancel := context.WithCancel(context.Background())
			cancel()
			return ctx
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			oauth := OAuth{Timeout: 20 * time.Millisecond, OpenBrowser: func(string) error { return nil }}
			_, err := oauth.Login(test.ctx())
			if err == nil || !strings.Contains(err.Error(), "timed out or was cancelled") {
				t.Fatalf("error = %v", err)
			}
		})
	}
}
