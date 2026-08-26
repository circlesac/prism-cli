package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/circlesac/prism-cli/internal/api"
)

func TestParseExecOptionsRequiresAPIAndRejectsConflictingOutput(t *testing.T) {
	if _, err := parseExecOptions(nil); err == nil || ExitCode(err) != exitUsage {
		t.Fatalf("missing api error = %v", err)
	}
	if _, err := parseExecOptions([]string{"--api", "chat", "--output-format", "text", "--json"}); err == nil || ExitCode(err) != exitUsage {
		t.Fatalf("conflicting output error = %v", err)
	}
	options, err := parseExecOptions([]string{"--api=responses", "--body=-", "--model", "gpt-test", "--provider", "chatgpt", "--output-format", "stream-json"})
	if err != nil {
		t.Fatal(err)
	}
	if options.apiName != "responses" || options.bodyPath != "-" || options.model != "gpt-test" || options.provider != "chatgpt" || options.outputFormat != "stream-json" {
		t.Fatalf("options = %#v", options)
	}
}

func TestExecSendsMappedEndpointHeadersAndModelOverride(t *testing.T) {
	var requestBody map[string]any
	var requestPath string
	var providerHeader string
	server := httptest.NewTLSServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		requestPath = request.URL.Path
		providerHeader = request.Header.Get("X-Prism-Provider")
		if err := json.NewDecoder(request.Body).Decode(&requestBody); err != nil {
			t.Fatal(err)
		}
		writer.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(writer, `{"id":"resp_1","output_text":"hello"}`)
	}))
	defer server.Close()

	original := resolveInferenceClient
	resolveInferenceClient = func(context.Context) (api.Client, error) {
		return api.Client{BaseURL: server.URL, Token: "test-token", HTTPClient: server.Client()}, nil
	}
	defer func() { resolveInferenceClient = original }()

	var output bytes.Buffer
	err := runExecCommandWithIO(context.Background(), []string{
		"--api", "responses", "--model", "gpt-override", "--provider", "chatgpt",
	}, strings.NewReader(`{"model":"gpt-original","input":"hello"}`), &output, io.Discard)
	if err != nil {
		t.Fatal(err)
	}
	if requestPath != "/v1/responses" || providerHeader != "chatgpt" || requestBody["model"] != "gpt-override" {
		t.Fatalf("path=%q provider=%q body=%#v", requestPath, providerHeader, requestBody)
	}
	if output.String() != `{"id":"resp_1","output_text":"hello"}` {
		t.Fatalf("output = %q", output.String())
	}
}

func TestExecTextAndStreamJSONOutputs(t *testing.T) {
	original := resolveInferenceClient
	defer func() { resolveInferenceClient = original }()

	server := httptest.NewTLSServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		writer.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(writer, "event: response.output_text.delta\ndata: {\"delta\":\"hel\"}\n\n")
		_, _ = io.WriteString(writer, "event: response.output_text.delta\ndata: {\"delta\":\"lo\"}\n\n")
		_, _ = io.WriteString(writer, "data: [DONE]\n\n")
	}))
	defer server.Close()
	resolveInferenceClient = func(context.Context) (api.Client, error) {
		return api.Client{BaseURL: server.URL, Token: "test-token", HTTPClient: server.Client()}, nil
	}

	var text bytes.Buffer
	if err := runExecCommandWithIO(context.Background(), []string{"--api", "responses", "--output-format", "text"}, strings.NewReader(`{"input":"hello"}`), &text, io.Discard); err != nil {
		t.Fatal(err)
	}
	if text.String() != "hello" {
		t.Fatalf("text = %q", text.String())
	}

	var stream bytes.Buffer
	if err := runExecCommandWithIO(context.Background(), []string{"--api", "responses", "--output-format", "stream-json"}, strings.NewReader(`{"input":"hello"}`), &stream, io.Discard); err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimSpace(stream.String()), "\n")
	if len(lines) != 3 || !strings.Contains(lines[0], `"api":"responses"`) || !strings.Contains(lines[0], `"event":"response.output_text.delta"`) || !strings.Contains(lines[2], `"data":"[DONE]"`) {
		t.Fatalf("stream lines = %#v", lines)
	}
}

func TestExecRejectsSSEInJSONModeAndMissingTerminalEvent(t *testing.T) {
	original := resolveInferenceClient
	defer func() { resolveInferenceClient = original }()
	server := httptest.NewTLSServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		writer.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(writer, "data: {\"delta\":\"partial\"}\n\n")
	}))
	defer server.Close()
	resolveInferenceClient = func(context.Context) (api.Client, error) {
		return api.Client{BaseURL: server.URL, Token: "test-token", HTTPClient: server.Client()}, nil
	}
	for _, format := range []string{"json", "stream-json"} {
		var output bytes.Buffer
		err := runExecCommandWithIO(context.Background(), []string{"--api", "chat", "--output-format", format}, strings.NewReader(`{"messages":[]}`), &output, io.Discard)
		if err == nil || ExitCode(err) != exitMalformed {
			t.Fatalf("format=%s err=%v", format, err)
		}
	}
}

func TestExecHTTPFailuresUseStableExitCodes(t *testing.T) {
	original := resolveInferenceClient
	defer func() { resolveInferenceClient = original }()
	for status, wantCode := range map[int]int{
		http.StatusUnauthorized:        exitAuthentication,
		http.StatusForbidden:           exitAuthorization,
		http.StatusBadRequest:          exitHTTP4xx,
		http.StatusTooManyRequests:     exitRateLimit,
		http.StatusInternalServerError: exitHTTP5xx,
	} {
		server := httptest.NewTLSServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
			writer.Header().Set("Content-Type", "application/json")
			writer.WriteHeader(status)
			_, _ = io.WriteString(writer, `{"error":"test failure"}`)
		}))
		resolveInferenceClient = func(context.Context) (api.Client, error) {
			return api.Client{BaseURL: server.URL, Token: "test-token", HTTPClient: server.Client()}, nil
		}
		var output bytes.Buffer
		err := runExecCommandWithIO(context.Background(), []string{"--api", "chat"}, strings.NewReader(`{"messages":[]}`), &output, io.Discard)
		server.Close()
		if err == nil || ExitCode(err) != wantCode {
			t.Fatalf("status=%d err=%v code=%d want=%d", status, err, ExitCode(err), wantCode)
		}
	}
}

func TestExecHelpIsPublicAndStable(t *testing.T) {
	var output bytes.Buffer
	printExecHelp(&output)
	for _, value := range []string{"prism exec", "--api", "--body", "--output-format", "CIRCLES_AUTH_TOKEN"} {
		if !strings.Contains(output.String(), value) {
			t.Fatalf("help missing %q: %s", value, output.String())
		}
	}
}
