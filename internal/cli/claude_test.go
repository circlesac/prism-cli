package cli

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
)

func TestClaudeHelpDoesNotResolveCredentials(t *testing.T) {
	var stdout bytes.Buffer
	if err := Run(context.Background(), []string{"claude", "--help"}, &stdout, &bytes.Buffer{}, "test"); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(stdout.String(), "prism claude") ||
		!strings.Contains(stdout.String(), "crcl use <profile>") ||
		!strings.Contains(stdout.String(), "claude --help") ||
		strings.Contains(stdout.String(), "prism claude [--profile") {
		t.Fatalf("help = %q", stdout.String())
	}
}

func TestClaudeEnvironmentReplacesExistingAnthropicCredentials(t *testing.T) {
	environment := claudeEnvironment([]string{
		"PATH=/usr/bin",
		"ANTHROPIC_BASE_URL=https://api.anthropic.com",
		"ANTHROPIC_AUTH_TOKEN=old-token",
		"ANTHROPIC_API_KEY=old-key",
		"CLAUDE_CODE_USE_BEDROCK=1",
		"CLAUDE_CODE_USE_VERTEX=1",
		"ANTHROPIC_BEDROCK_BASE_URL=https://bedrock.example.com",
		"ANTHROPIC_VERTEX_BASE_URL=https://vertex.example.com",
		"ANTHROPIC_VERTEX_PROJECT_ID=example-project",
		"CLOUD_ML_REGION=us-east5",
	}, "http://127.0.0.1:12345", "local-token")
	joined := strings.Join(environment, "\n")
	for _, unwanted := range []string{
		"api.anthropic.com",
		"old-token",
		"old-key",
		"CLAUDE_CODE_USE_BEDROCK",
		"CLAUDE_CODE_USE_VERTEX",
		"bedrock.example.com",
		"vertex.example.com",
		"example-project",
		"us-east5",
	} {
		if strings.Contains(joined, unwanted) {
			t.Fatalf("environment retained %q: %s", unwanted, joined)
		}
	}
	for _, wanted := range []string{
		"PATH=/usr/bin",
		"ANTHROPIC_BASE_URL=http://127.0.0.1:12345",
		"ANTHROPIC_AUTH_TOKEN=local-token",
	} {
		if !strings.Contains(joined, wanted) {
			t.Fatalf("environment omitted %q: %s", wanted, joined)
		}
	}
}

func TestClaudeBridgeAuthenticatesLocallyAndForwardsTheCirclesCredential(t *testing.T) {
	var requests atomic.Int32
	upstream := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		requests.Add(1)
		if request.URL.Path != "/v1/messages" || request.URL.RawQuery != "beta=true" {
			t.Errorf("request URL = %s", request.URL.String())
		}
		if request.Header.Get("Authorization") != "Bearer circles-secret" {
			t.Errorf("authorization = %q", request.Header.Get("Authorization"))
		}
		if request.Header.Get("X-Api-Key") != "" {
			t.Errorf("x-api-key = %q", request.Header.Get("X-Api-Key"))
		}
		response.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(response, "event: message_stop\ndata: {\"type\":\"message_stop\"}\n\n")
	}))
	defer upstream.Close()

	bridge, err := startClaudeBridge(upstream.URL, "circles-secret", io.Discard)
	if err != nil {
		t.Fatal(err)
	}
	defer bridge.close()

	unauthorizedRequest, err := http.NewRequestWithContext(
		t.Context(), http.MethodPost, bridge.url+"/v1/messages?beta=true", strings.NewReader("{}"),
	)
	if err != nil {
		t.Fatal(err)
	}
	unauthorizedRequest.Header.Set("Content-Type", "application/json")
	unauthorized, err := http.DefaultClient.Do(unauthorizedRequest)
	if err != nil {
		t.Fatal(err)
	}
	if err := unauthorized.Body.Close(); err != nil {
		t.Fatal(err)
	}
	if unauthorized.StatusCode != http.StatusUnauthorized || requests.Load() != 0 {
		t.Fatalf("unauthorized status/requests = %d/%d", unauthorized.StatusCode, requests.Load())
	}

	request, err := http.NewRequestWithContext(
		t.Context(), http.MethodPost, bridge.url+"/v1/messages?beta=true", strings.NewReader("{}"),
	)
	if err != nil {
		t.Fatal(err)
	}
	request.Header.Set("Authorization", "Bearer "+bridge.credential)
	request.Header.Set("X-Api-Key", "remove-me")
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	body, err := io.ReadAll(response.Body)
	response.Body.Close()
	if err != nil {
		t.Fatal(err)
	}
	if response.StatusCode != http.StatusOK || !strings.Contains(string(body), "message_stop") {
		t.Fatalf("response = %d %q", response.StatusCode, body)
	}
	if requests.Load() != 1 {
		t.Fatalf("upstream requests = %d", requests.Load())
	}
}
