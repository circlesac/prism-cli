package cli

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"runtime"
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
		!strings.Contains(stdout.String(), "prism claude login") ||
		!strings.Contains(stdout.String(), "crcl use <profile>") ||
		!strings.Contains(stdout.String(), "claude --help") ||
		strings.Contains(stdout.String(), "prism claude [--profile") {
		t.Fatalf("help = %q", stdout.String())
	}
}

func TestParseClaudeOptionsStripsAccountAndPreservesOrder(t *testing.T) {
	account, remainingArgs, err := parseClaudeOptions([]string{
		"--model", "gpt-5.6-sol",
		"--account", "acct-01",
		"--print",
		"--",
		"say hi",
	})
	if err != nil {
		t.Fatal(err)
	}
	if account != "acct-01" {
		t.Fatalf("account = %q", account)
	}
	if !reflect.DeepEqual(remainingArgs, []string{"--model", "gpt-5.6-sol", "--print", "--", "say hi"}) {
		t.Fatalf("remainingArgs = %#v", remainingArgs)
	}
}

func TestParseClaudeOptionsRequiresValue(t *testing.T) {
	for _, args := range [][]string{{"--account"}, {"--account", ""}, {"--account", "--"}, {"--account="}} {
		_, _, err := parseClaudeOptions(args)
		if err == nil || err.Error() != "--account requires a value" {
			t.Fatalf("args/error = %#v/%v", args, err)
		}
	}
}

func TestParseClaudeOptionsStopsAtClaudeArgumentSeparator(t *testing.T) {
	account, remainingArgs, err := parseClaudeOptions([]string{"--account=work-admin", "--", "--account", "prompt-value"})
	if err != nil {
		t.Fatal(err)
	}
	if account != "work-admin" || !reflect.DeepEqual(remainingArgs, []string{"--", "--account", "prompt-value"}) {
		t.Fatalf("account/remainingArgs = %q/%#v", account, remainingArgs)
	}
}

func TestParseClaudeOptionsRejectsDuplicateAccount(t *testing.T) {
	_, _, err := parseClaudeOptions([]string{"--account", "Personal", "--account=Team"})
	if err == nil || err.Error() != "--account may be specified only once" {
		t.Fatalf("error = %v", err)
	}
}

func TestRunClaudeCommandPreservesClaudeArgumentsWithoutAccount(t *testing.T) {
	commandDir := t.TempDir()
	command := filepath.Join(commandDir, "claude")
	if runtime.GOOS == "windows" {
		command += ".exe"
	}
	source := filepath.Join(commandDir, "main.go")
	if err := os.WriteFile(source, []byte(`package main
import (
	"fmt"
	"os"
)

func main() {
	for _, arg := range os.Args[1:] {
		fmt.Println(arg)
	}
	}
`), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := exec.Command("go", "build", "-o", command, source).Run(); err != nil {
		t.Fatalf("build fake claude command = %v", err)
	}

	t.Setenv("PATH", commandDir+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("CIRCLES_AUTH_TOKEN", "circles-secret")

	var stdout bytes.Buffer
	err := runClaudeCommand(context.Background(), []string{"--account", "acct-01", "--model", "gpt-5.6-sol", "--print", "--", "say hi"}, &stdout, &bytes.Buffer{})
	if err != nil {
		t.Fatal(err)
	}
	if got := stdout.String(); got != "--model\ngpt-5.6-sol\n--print\n--\nsay hi\n" {
		t.Fatalf("stdout = %q", got)
	}
}

func TestClaudeEnvironmentPreservesClaudeLoginModeWithoutTokenInjection(t *testing.T) {
	environment := claudeEnvironment([]string{
		"PATH=/usr/bin",
		"ANTHROPIC_BASE_URL=https://api.anthropic.com",
		"ANTHROPIC_AUTH_TOKEN=old-token",
		"ANTHROPIC_API_KEY=old-key",
		"ANTHROPIC_CUSTOM_HEADERS=Authorization: Bearer old-bridge-token",
		"_CLAUDE_CODE_ASSUME_FIRST_PARTY_BASE_URL=0",
		"CLAUDE_CODE_USE_BEDROCK=1",
		"CLAUDE_CODE_USE_VERTEX=1",
		"ANTHROPIC_BEDROCK_BASE_URL=https://bedrock.example.com",
		"ANTHROPIC_VERTEX_BASE_URL=https://vertex.example.com",
		"ANTHROPIC_VERTEX_PROJECT_ID=example-project",
		"CLOUD_ML_REGION=us-east5",
		"CLAUDE_CODE_OAUTH_TOKEN=existing-login-token",
	}, "http://127.0.0.1:12345", "X-Prism-Claude-Bridge-abc: 123456")
	joined := strings.Join(environment, "\n")
	for _, unwanted := range []string{
		"api.anthropic.com",
		"old-token",
		"old-key",
		"Authorization: Bearer old-bridge-token",
		"CLAUDE_CODE_USE_BEDROCK",
		"CLAUDE_CODE_USE_VERTEX",
		"bedrock.example.com",
		"vertex.example.com",
		"example-project",
		"us-east5",
		"_CLAUDE_CODE_ASSUME_FIRST_PARTY_BASE_URL=0",
	} {
		if strings.Contains(joined, unwanted) {
			t.Fatalf("environment retained %q: %s", unwanted, joined)
		}
	}
	for _, wanted := range []string{
		"PATH=/usr/bin",
		"CLAUDE_CODE_OAUTH_TOKEN=existing-login-token",
		"ANTHROPIC_BASE_URL=http://127.0.0.1:12345",
		"ANTHROPIC_CUSTOM_HEADERS=X-Prism-Claude-Bridge-abc: 123456",
		"_CLAUDE_CODE_ASSUME_FIRST_PARTY_BASE_URL=1",
	} {
		if !strings.Contains(joined, wanted) {
			t.Fatalf("environment omitted %q: %s", wanted, joined)
		}
	}
	if strings.Contains(joined, "ANTHROPIC_AUTH_TOKEN=") {
		t.Fatalf("environment still contains ANTHROPIC_AUTH_TOKEN: %s", joined)
	}
	if strings.Contains(joined, "ANTHROPIC_API_KEY=") {
		t.Fatalf("environment still contains ANTHROPIC_API_KEY: %s", joined)
	}
}

func TestClaudeBridgeAuthenticatesLocallyAndForwardsTheCirclesCredential(t *testing.T) {
	var requests atomic.Int32
	var seenIncomingOAuth atomic.Bool
	var seenBridgeHeader atomic.Bool
	var observedCustomHeader atomic.Value
	upstream := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		requests.Add(1)
		if request.URL.Path != "/v1/messages" || request.URL.RawQuery != "beta=true" {
			t.Errorf("request URL = %s", request.URL.String())
		}
		if request.Header.Get("Authorization") != "Bearer circles-secret" {
			t.Errorf("authorization = %q", request.Header.Get("Authorization"))
		}
		if request.Header.Get("Authorization") == "Bearer incoming-claude-oauth-token" {
			seenIncomingOAuth.Store(true)
		}
		if observed := observedCustomHeader.Load(); observed != nil {
			if request.Header.Get(observed.(string)) != "" {
				seenBridgeHeader.Store(true)
			}
		}
		if request.Header.Get("X-Api-Key") != "" {
			t.Errorf("x-api-key = %q", request.Header.Get("X-Api-Key"))
		}
		response.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(response, "event: message_stop\ndata: {\"type\":\"message_stop\"}\n\n")
	}))
	defer upstream.Close()

	bridge, err := startClaudeBridge(upstream.URL, "circles-secret", "", io.Discard)
	if err != nil {
		t.Fatal(err)
	}
	defer bridge.close()
	observedCustomHeader.Store(bridge.headerName)
	if bridge.headerName == "" || bridge.headerValue == "" {
		t.Fatal("missing bridge auth header")
	}

	if bridge.url == "" || strings.HasPrefix(bridge.url, "https://") {
		t.Fatal("bridge URL is invalid")
	}

	accountRequest, err := http.NewRequestWithContext(
		t.Context(), http.MethodPost, bridge.url+"/v1/messages?beta=true", strings.NewReader("{}"),
	)
	if err != nil {
		t.Fatal(err)
	}
	accountRequest.Header.Set("Authorization", "Bearer incoming-claude-oauth-token")
	accountRequest.Header.Set(bridge.headerName, "wrong-token")
	wrong, err := http.DefaultClient.Do(accountRequest)
	if err != nil {
		t.Fatal(err)
	}
	if err := wrong.Body.Close(); err != nil {
		t.Fatal(err)
	}
	if wrong.StatusCode != http.StatusUnauthorized || requests.Load() != 0 {
		t.Fatalf("wrong header status/requests = %d/%d", wrong.StatusCode, requests.Load())
	}

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
	request.Header.Set(bridge.headerName, bridge.headerValue)
	request.Header.Set("Authorization", "Bearer incoming-claude-oauth-token")
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
	if seenIncomingOAuth.Load() {
		t.Fatal("incoming OAuth authorization leaked upstream")
	}
	if seenBridgeHeader.Load() {
		t.Fatalf("bridge header %q leaked upstream", bridge.headerName)
	}
}

func TestClaudeBridgeProxyErrorsDoNotLogCredentials(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	target := upstream.URL
	upstream.Close()
	var stderr bytes.Buffer
	bridge, err := startClaudeBridge(target, "circles-secret", "", &stderr)
	if err != nil {
		t.Fatal(err)
	}
	defer bridge.close()

	request, err := http.NewRequestWithContext(
		t.Context(), http.MethodPost, bridge.url+"/v1/messages", strings.NewReader("{}"),
	)
	if err != nil {
		t.Fatal(err)
	}
	request.Header.Set(bridge.headerName, bridge.headerValue)
	request.Header.Set("Authorization", "Bearer incoming-claude-oauth-token")
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	body, err := io.ReadAll(response.Body)
	response.Body.Close()
	if err != nil {
		t.Fatal(err)
	}
	if response.StatusCode != http.StatusBadGateway {
		t.Fatalf("status/body = %d/%q", response.StatusCode, body)
	}
	combined := stderr.String() + string(body)
	for _, secret := range []string{
		"incoming-claude-oauth-token",
		"circles-secret",
		bridge.headerValue,
	} {
		if strings.Contains(combined, secret) {
			t.Fatalf("proxy error leaked a credential: %q", combined)
		}
	}
}

func TestClaudeBridgeInjectsAnthropicAccountHeader(t *testing.T) {
	var requests atomic.Int32
	upstream := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		requests.Add(1)
		if request.Header.Get("X-Prism-Anthropic-Account") != "b64:YWNjdC0wMQ" {
			t.Errorf("account header = %q", request.Header.Get("X-Prism-Anthropic-Account"))
		}
		if request.Header.Get("X-Custom") != "keep-me" {
			t.Errorf("custom header = %q", request.Header.Get("X-Custom"))
		}
		response.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(response, "event: message_stop\ndata: {\"type\":\"message_stop\"}\n\n")
	}))
	defer upstream.Close()

	bridge, err := startClaudeBridge(upstream.URL, "circles-secret", "acct-01", io.Discard)
	if err != nil {
		t.Fatal(err)
	}
	defer bridge.close()

	request, err := http.NewRequestWithContext(
		t.Context(), http.MethodPost, bridge.url+"/v1/messages?beta=true", strings.NewReader("{}"),
	)
	if err != nil {
		t.Fatal(err)
	}
	request.Header.Set(bridge.headerName, bridge.headerValue)
	request.Header.Set("Authorization", "Bearer incoming-claude-oauth-token")
	request.Header.Set("X-Custom", "keep-me")
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

func TestClaudeBridgeEncodesUnicodeAnthropicAccountHeader(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		if request.Header.Get("X-Prism-Anthropic-Account") != "b64:7YyAIOqzhOyglSDigJQg7JiI7Iuc" {
			t.Errorf("account header = %q", request.Header.Get("X-Prism-Anthropic-Account"))
		}
		response.WriteHeader(http.StatusNoContent)
	}))
	defer upstream.Close()

	bridge, err := startClaudeBridge(upstream.URL, "circles-secret", "팀 계정 — 예시", io.Discard)
	if err != nil {
		t.Fatal(err)
	}
	defer bridge.close()

	request, err := http.NewRequestWithContext(t.Context(), http.MethodPost, bridge.url+"/v1/messages", strings.NewReader("{}"))
	if err != nil {
		t.Fatal(err)
	}
	request.Header.Set("Authorization", "Bearer incoming-claude-oauth-token")
	request.Header.Set(bridge.headerName, bridge.headerValue)
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	response.Body.Close()
	if response.StatusCode != http.StatusNoContent {
		t.Fatalf("status = %d", response.StatusCode)
	}
}
