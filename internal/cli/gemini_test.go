package cli

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/circlesac/prism-cli/internal/api"
)

func TestGeminiDefaultsTo37FlashLowAndPreservesExplicitModel(t *testing.T) {
	if got := withDefaultGeminiModel([]string{"-p", "hello"}); !reflect.DeepEqual(got, []string{"--model", "gemini-3.7-flash-low", "-p", "hello"}) {
		t.Fatalf("default args = %#v", got)
	}
	for _, args := range [][]string{{"--model", "gemini-3.1-pro-high", "-p", "hard"}, {"-m", "gemini-3.1-pro-high"}, {"--model=gemini-3.1-pro-high"}} {
		if got := withDefaultGeminiModel(args); !reflect.DeepEqual(got, args) {
			t.Fatalf("explicit model args = %#v", got)
		}
	}
}

func TestGeminiHelpDocumentsOfficialCLIAccountsAndModels(t *testing.T) {
	var output bytes.Buffer
	if err := runGeminiCommand(context.Background(), []string{"--help"}, &output, io.Discard); err != nil {
		t.Fatal(err)
	}
	for _, value := range []string{"official Gemini CLI", "auth import", "--account", "rotate automatically", "subscription", "AI Studio API keys", "gemini-3.7-flash-low", "gemini-3.1-pro-high"} {
		if !strings.Contains(output.String(), value) {
			t.Fatalf("help omitted %q: %s", value, output.String())
		}
	}
}

func TestFindGeminiCLIIgnoresAntigravityAndUsesGatewayCompatibleCLI(t *testing.T) {
	directory := t.TempDir()
	for _, name := range []string{"agy", "gemini"} {
		if err := os.WriteFile(filepath.Join(directory, name), []byte("#!/bin/sh\nexit 0\n"), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	t.Setenv("PATH", directory)

	executable, err := findGeminiCLI()
	if err != nil {
		t.Fatal(err)
	}
	if executable.path != filepath.Join(directory, "gemini") || len(executable.prefix) != 0 {
		t.Fatalf("executable = %#v", executable)
	}
}

func TestGeminiAccountSelectionUsesSubscriptionAccounts(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	accounts := []api.Credential{{ID: "oauth-1", Name: "personal"}, {ID: "oauth-2", Name: "work-admin"}}

	account, err := selectGeminiAccount("", accounts)
	if err != nil || account != "oauth-1" {
		t.Fatalf("default = %q, error = %v", account, err)
	}
	account, err = selectGeminiAccount("work-admin", accounts)
	if err != nil || account != "oauth-2" {
		t.Fatalf("explicit = %q, error = %v", account, err)
	}
}

func TestGeminiOptionsSelectProfileAndAccountWithoutPassingThemThrough(t *testing.T) {
	options, account, passthrough, err := parseGeminiOptions([]string{
		"--profile", "dev", "--account=work-admin", "-p", "hello",
	})
	if err != nil {
		t.Fatal(err)
	}
	if !options.profileSet || options.profile != "dev" || account != "work-admin" {
		t.Fatalf("options = %#v, account = %q", options, account)
	}
	if !reflect.DeepEqual(passthrough, []string{"-p", "hello"}) {
		t.Fatalf("passthrough = %#v", passthrough)
	}
}

func TestGeminiOptionsPreserveArgumentsAfterSeparator(t *testing.T) {
	options, account, passthrough, err := parseGeminiOptions([]string{
		"--profile=dev", "--account", "personal", "--", "--account", "prompt-value",
	})
	if err != nil {
		t.Fatal(err)
	}
	if options.profile != "dev" || account != "personal" {
		t.Fatalf("options = %#v, account = %q", options, account)
	}
	if !reflect.DeepEqual(passthrough, []string{"--", "--account", "prompt-value"}) {
		t.Fatalf("passthrough = %#v", passthrough)
	}
}

func TestGeminiBridgeAuthenticatesLocallyAndSelectsAccount(t *testing.T) {
	var requests atomic.Int32
	upstream := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		requests.Add(1)
		if request.URL.Path != "/v1beta/models/gemini-3.7-flash:generateContent" {
			t.Errorf("path = %q", request.URL.Path)
		}
		if request.Header.Get("Authorization") != "Bearer circles-secret" {
			t.Errorf("authorization = %q", request.Header.Get("Authorization"))
		}
		if request.Header.Get("X-Prism-Gemini-Account") != "b64:cGVyc29uQGV4YW1wbGUuY29t" {
			t.Errorf("account = %q", request.Header.Get("X-Prism-Gemini-Account"))
		}
		if request.Header.Get("X-Prism-Gemini-Provider") != "" {
			t.Errorf("provider header leaked = %q", request.Header.Get("X-Prism-Gemini-Provider"))
		}
		if request.Header.Get("X-Goog-Api-Key") != "" || request.Header.Get("X-Prism-Gemini-Bridge") != "" {
			t.Errorf("private headers leaked")
		}
		response.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(response, `{"candidates":[]}`)
	}))
	defer upstream.Close()

	bridge, err := startGeminiBridge(upstream.URL, "circles-secret", "person@example.com", io.Discard)
	if err != nil {
		t.Fatal(err)
	}
	defer bridge.close()

	unauthorized, err := http.Post(bridge.url+"/v1beta/models/gemini-3.7-flash:generateContent", "application/json", strings.NewReader("{}"))
	if err != nil {
		t.Fatal(err)
	}
	unauthorized.Body.Close()
	if unauthorized.StatusCode != http.StatusUnauthorized || requests.Load() != 0 {
		t.Fatalf("unauthorized = %d, requests = %d", unauthorized.StatusCode, requests.Load())
	}

	request, _ := http.NewRequest(http.MethodPost, bridge.url+"/v1beta/models/gemini-3.7-flash:generateContent", strings.NewReader("{}"))
	request.Header.Set(bridge.headerName, bridge.headerValue)
	request.Header.Set("X-Goog-Api-Key", "remove-me")
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	response.Body.Close()
	if response.StatusCode != http.StatusOK || requests.Load() != 1 {
		t.Fatalf("response = %d, requests = %d", response.StatusCode, requests.Load())
	}
}

func TestRunGeminiUsesOfficialCLIWithGatewayEnvironment(t *testing.T) {
	original := findGeminiCLIExecutable
	defer func() { findGeminiCLIExecutable = original }()
	directory := t.TempDir()
	executable := filepath.Join(directory, "gemini")
	if err := os.WriteFile(executable, []byte("#!/bin/sh\nprintf '%s\\n' \"$@\"\nprintf 'base=%s\\nheaders=%s\\nsettings=%s\\ntrust=%s\\n' \"$GOOGLE_GEMINI_BASE_URL\" \"$GEMINI_CLI_CUSTOM_HEADERS\" \"$GEMINI_CLI_SYSTEM_SETTINGS_PATH\" \"$GEMINI_CLI_TRUST_WORKSPACE\"\ncat \"$GEMINI_CLI_SYSTEM_SETTINGS_PATH\"\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	findGeminiCLIExecutable = func() (geminiCLIExecutable, error) { return geminiCLIExecutable{path: executable}, nil }
	upstream := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	defer upstream.Close()
	var output bytes.Buffer
	if err := runGemini(context.Background(), upstream.URL, "circles-secret", "person@example.com", withDefaultGeminiModel([]string{"-p", "hello"}), strings.NewReader(""), &output, io.Discard); err != nil {
		t.Fatal(err)
	}
	for _, value := range []string{"--model\ngemini-3.7-flash-low\n-p\nhello", "base=http://127.0.0.1:", "headers=X-Prism-Gemini-Bridge:", "settings=/", "trust=true", `"selectedType":"gateway"`, `"useExternal":true`} {
		if !strings.Contains(output.String(), value) {
			t.Fatalf("output omitted %q: %s", value, output.String())
		}
	}
}
