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
)

func TestGeminiDefaultsTo37FlashAndPreservesExplicitModel(t *testing.T) {
	if got := withDefaultGeminiModel([]string{"-p", "hello"}); !reflect.DeepEqual(got, []string{"--model", "gemini-3.7-flash", "-p", "hello"}) {
		t.Fatalf("default args = %#v", got)
	}
	for _, args := range [][]string{{"--model", "gemini-3.1-pro-preview", "-p", "hard"}, {"-m", "gemini-3.1-pro-preview"}, {"--model=gemini-3.1-pro-preview"}} {
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
	for _, value := range []string{"official Gemini CLI", "--account", "balanced rotation", "gemini-3.7-flash", "gemini-3.1-pro-preview"} {
		if !strings.Contains(output.String(), value) {
			t.Fatalf("help omitted %q: %s", value, output.String())
		}
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
	if err := os.WriteFile(executable, []byte("#!/bin/sh\nprintf '%s\\n' \"$@\"\nprintf 'base=%s\\nheaders=%s\\nsettings=%s\\n' \"$GOOGLE_GEMINI_BASE_URL\" \"$GEMINI_CLI_CUSTOM_HEADERS\" \"$GEMINI_CLI_SYSTEM_SETTINGS_PATH\"\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	findGeminiCLIExecutable = func() (geminiCLIExecutable, error) { return geminiCLIExecutable{path: executable}, nil }
	upstream := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	defer upstream.Close()
	var output bytes.Buffer
	if err := runGemini(context.Background(), upstream.URL, "circles-secret", "person@example.com", withDefaultGeminiModel([]string{"-p", "hello"}), strings.NewReader(""), &output, io.Discard); err != nil {
		t.Fatal(err)
	}
	for _, value := range []string{"--model\ngemini-3.7-flash\n-p\nhello", "base=http://127.0.0.1:", "headers=X-Prism-Gemini-Bridge:", "settings=/"} {
		if !strings.Contains(output.String(), value) {
			t.Fatalf("output omitted %q: %s", value, output.String())
		}
	}
}
