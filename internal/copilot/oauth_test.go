package copilot

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"
)

type roundTripFunc func(*http.Request) (*http.Response, error)

func (function roundTripFunc) RoundTrip(request *http.Request) (*http.Response, error) {
	return function(request)
}

func TestDeviceLoginDerivesAccountIdentityFromGitHub(t *testing.T) {
	client := &http.Client{Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
		body := ""
		switch request.URL.String() {
		case "https://github.com/login/device/code":
			body = `{"device_code":"device","user_code":"ABCD-EFGH","verification_uri":"https://github.com/login/device","expires_in":60,"interval":0}`
		case "https://github.com/login/oauth/access_token":
			body = `{"access_token":"github-secret"}`
		case "https://api.github.com/copilot_internal/v2/token":
			if request.Header.Get("Authorization") != "token github-secret" {
				t.Fatalf("authorization = %q", request.Header.Get("Authorization"))
			}
			body = `{"token":"short-lived"}`
		case "https://api.github.com/user":
			body = `{"login":"octocat"}`
		default:
			t.Fatalf("unexpected request: %s", request.URL)
		}
		return &http.Response{
			StatusCode: http.StatusOK,
			Header:     http.Header{"Content-Type": []string{"application/json"}},
			Body:       io.NopCloser(strings.NewReader(body)),
		}, nil
	})}
	var opened string
	bundle, err := (OAuth{
		Client: client,
		OpenBrowser: func(target string) error {
			opened = target
			return nil
		},
		Sleep: func(time.Duration) {},
	}).Login(context.Background(), &bytes.Buffer{})
	if err != nil {
		t.Fatal(err)
	}
	if opened != "https://github.com/login/device" || bundle.Username != "octocat" || bundle.Alias != "octocat" || bundle.GitHubToken != "github-secret" {
		t.Fatalf("opened = %q, bundle = %#v", opened, bundle)
	}
}
