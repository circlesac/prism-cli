package cli

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestProviderRelayAuthenticatesLocallyAndInjectsPrismCredential(t *testing.T) {
	var authorization string
	var localHeader string
	upstream := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		authorization = request.Header.Get("Authorization")
		localHeader = request.Header.Get("X-Prism-Relay")
		response.WriteHeader(http.StatusNoContent)
	}))
	defer upstream.Close()

	relay, err := startProviderRelay(upstream.URL, "circles-secret", "claude", "", strings.Repeat("a", 32), 0, io.Discard)
	if err != nil {
		t.Fatal(err)
	}
	defer relay.close()
	go relay.server.Serve(relay.listener)

	baseURL := "http://" + relay.listener.Addr().String()
	unauthorized, err := http.Post(baseURL+"/v1/messages", "application/json", strings.NewReader("{}"))
	if err != nil {
		t.Fatal(err)
	}
	unauthorized.Body.Close()
	if unauthorized.StatusCode != http.StatusUnauthorized {
		t.Fatalf("unauthorized status = %d", unauthorized.StatusCode)
	}

	request, _ := http.NewRequestWithContext(context.Background(), http.MethodPost, baseURL+"/v1/messages", strings.NewReader("{}"))
	request.Header.Set("X-Prism-Relay", strings.Repeat("a", 32))
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	response.Body.Close()
	if response.StatusCode != http.StatusNoContent {
		t.Fatalf("authorized status = %d", response.StatusCode)
	}
	if authorization != "Bearer circles-secret" || localHeader != "" {
		t.Fatalf("upstream auth=%q local-header=%q", authorization, localHeader)
	}
}

func TestParseRelayOptions(t *testing.T) {
	provider, port, options, help, err := parseRelayOptions([]string{"claude", "--port", "0", "--profile", "work"})
	if err != nil || help || provider != "claude" || port != 0 || options.profile != "work" || !options.profileSet {
		t.Fatalf("parsed = provider=%q port=%d options=%+v help=%t err=%v", provider, port, options, help, err)
	}
	for _, args := range [][]string{{"claude"}, {"gemini", "--port", "0"}, {"unknown", "--port", "0"}, {"codex", "--port", "-1"}} {
		if _, _, _, _, err := parseRelayOptions(args); err == nil {
			t.Fatalf("expected error for %#v", args)
		}
	}
}

func TestProviderRelayRejectsSiblingProviderRoutes(t *testing.T) {
	upstreamCalls := 0
	upstream := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		upstreamCalls++
		response.WriteHeader(http.StatusNoContent)
	}))
	defer upstream.Close()

	relay, err := startProviderRelay(upstream.URL, "circles-secret", "codex", "", strings.Repeat("c", 32), 0, io.Discard)
	if err != nil {
		t.Fatal(err)
	}
	defer relay.close()
	go relay.server.Serve(relay.listener)

	request, _ := http.NewRequest(http.MethodPost, "http://"+relay.listener.Addr().String()+"/v1/messages", strings.NewReader("{}"))
	request.Header.Set("Authorization", "Bearer "+strings.Repeat("c", 32))
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	response.Body.Close()
	if response.StatusCode != http.StatusForbidden || upstreamCalls != 0 {
		t.Fatalf("status=%d upstream calls=%d", response.StatusCode, upstreamCalls)
	}
}

func TestRelayPathAllowed(t *testing.T) {
	tests := []struct {
		provider string
		path     string
		want     bool
	}{
		{provider: "claude", path: "/v1/messages", want: true},
		{provider: "claude", path: "/v1/messages/count_tokens", want: true},
		{provider: "claude", path: "/v1/responses", want: false},
		{provider: "codex", path: "/v1/responses", want: true},
		{provider: "codex", path: "/v1/responses/compact", want: true},
	}
	for _, test := range tests {
		if got := relayPathAllowed(test.provider, test.path); got != test.want {
			t.Errorf("relayPathAllowed(%q, %q) = %t, want %t", test.provider, test.path, got, test.want)
		}
	}
}
