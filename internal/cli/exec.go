package cli

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"time"

	credentials "github.com/circlesac/credentials/go"
	"github.com/circlesac/prism-cli/internal/api"
)

const (
	exitUsage          = 2
	exitAuthentication = 3
	exitAuthorization  = 4
	exitTransport      = 5
	exitHTTP4xx        = 6
	exitRateLimit      = 7
	exitHTTP5xx        = 8
	exitMalformed      = 9
	exitInterrupted    = 130
)

type commandError struct {
	code int
	err  error
}

func (e *commandError) Error() string { return e.err.Error() }
func (e *commandError) Unwrap() error { return e.err }

// ExitCode returns the documented process exit code for a CLI error.
func ExitCode(err error) int {
	var coded *commandError
	if errors.As(err, &coded) {
		return coded.code
	}
	return 1
}

func usageError(format string, args ...any) error {
	return &commandError{code: exitUsage, err: fmt.Errorf(format, args...)}
}

func authError(err error) error {
	return &commandError{code: exitAuthentication, err: err}
}

func transportError(err error) error {
	return &commandError{code: exitTransport, err: err}
}

func malformedError(err error) error {
	return &commandError{code: exitMalformed, err: err}
}

type execOptions struct {
	apiName      string
	bodyPath     string
	bodySet      bool
	model        string
	provider     string
	outputFormat string
	outputSet    bool
	jsonSet      bool
}

var resolveInferenceClient = prismInferenceClient

func hasOption(args []string, option string) bool {
	for _, argument := range args {
		if argument == option {
			return true
		}
	}
	return false
}

func printExecHelp(output io.Writer) {
	fmt.Fprintln(output, `Usage:
  prism exec --api chat|completions|responses|messages [options]

Options:
  --api <name>                         Prism API wire format (required)
  --body <path>                        JSON request body; use - for stdin
  --model <name>                       Override the request model
  --provider <name>                    Send an X-Prism-Provider routing hint
  --output-format text|json|stream-json  Select stdout format (default: json)
  --json                               Shorthand for --output-format json

Without --body, the request body is read from stdin. Credentials are read from
the existing Circles profile or CIRCLES_AUTH_TOKEN/CRCL_AUTH_TOKEN.`)
}

func runExecCommand(ctx context.Context, args []string, stdout io.Writer, stderr io.Writer) error {
	return runExecCommandWithIO(ctx, args, os.Stdin, stdout, stderr)
}

func runExecCommandWithIO(ctx context.Context, args []string, stdin io.Reader, stdout io.Writer, stderr io.Writer) error {
	options, err := parseExecOptions(args)
	if err != nil {
		return err
	}
	body, err := readExecBody(options, stdin)
	if err != nil {
		return err
	}
	body, err = prepareExecBody(body, options)
	if err != nil {
		return err
	}
	client, err := resolveInferenceClient(ctx)
	if err != nil {
		return err
	}
	response, err := client.Inference(ctx, api.InferenceRequest{
		API:      options.apiName,
		Provider: options.provider,
		Body:     body,
	})
	if err != nil {
		if errors.Is(err, context.Canceled) {
			return &commandError{code: exitInterrupted, err: errors.New("request interrupted")}
		}
		return transportError(errors.New("Prism could not be reached"))
	}
	defer api.CloseResponse(response)
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return inferenceHTTPError(response)
	}

	payload, err := readResponseIdle(ctx, response.Body, api.InferenceIdleTimeout)
	if err != nil {
		if errors.Is(err, context.Canceled) {
			return &commandError{code: exitInterrupted, err: errors.New("request interrupted")}
		}
		return transportError(err)
	}
	stream := isEventStream(response.Header.Get("Content-Type"), payload)
	switch options.outputFormat {
	case "json":
		if stream {
			return malformedError(errors.New("JSON output cannot represent an SSE response; use --output-format stream-json or text"))
		}
		if !json.Valid(payload) {
			return malformedError(errors.New("Prism returned invalid JSON"))
		}
		_, err = stdout.Write(payload)
		return err
	case "text":
		if stream {
			return writeStreamText(stdout, options.apiName, payload)
		}
		return writeJSONText(stdout, options.apiName, payload)
	case "stream-json":
		if !stream {
			return malformedError(errors.New("stream-json output requires an SSE response"))
		}
		return writeStreamJSON(stdout, options.apiName, payload)
	default:
		return usageError("unsupported output format %q", options.outputFormat)
	}
}

func parseExecOptions(args []string) (execOptions, error) {
	options := execOptions{outputFormat: "json"}
	for index := 0; index < len(args); index++ {
		argument := args[index]
		switch {
		case argument == "--api":
			if index+1 >= len(args) || args[index+1] == "" {
				return execOptions{}, usageError("--api requires a value")
			}
			options.apiName = strings.ToLower(args[index+1])
			index++
		case strings.HasPrefix(argument, "--api="):
			options.apiName = strings.ToLower(strings.TrimPrefix(argument, "--api="))
		case argument == "--body":
			if options.bodySet || index+1 >= len(args) || args[index+1] == "" {
				return execOptions{}, usageError("--body requires one path (or - for stdin)")
			}
			options.bodyPath = args[index+1]
			options.bodySet = true
			index++
		case strings.HasPrefix(argument, "--body="):
			if options.bodySet {
				return execOptions{}, usageError("--body may be supplied only once")
			}
			options.bodyPath = strings.TrimPrefix(argument, "--body=")
			if options.bodyPath == "" {
				return execOptions{}, usageError("--body requires one path (or - for stdin)")
			}
			options.bodySet = true
		case argument == "--model":
			if index+1 >= len(args) || args[index+1] == "" {
				return execOptions{}, usageError("--model requires a value")
			}
			options.model = args[index+1]
			index++
		case strings.HasPrefix(argument, "--model="):
			options.model = strings.TrimPrefix(argument, "--model=")
		case argument == "--provider":
			if index+1 >= len(args) || args[index+1] == "" {
				return execOptions{}, usageError("--provider requires a value")
			}
			options.provider = args[index+1]
			index++
		case strings.HasPrefix(argument, "--provider="):
			options.provider = strings.TrimPrefix(argument, "--provider=")
		case argument == "--output-format":
			if index+1 >= len(args) || args[index+1] == "" {
				return execOptions{}, usageError("--output-format requires text, json, or stream-json")
			}
			options.outputFormat = args[index+1]
			options.outputSet = true
			index++
		case strings.HasPrefix(argument, "--output-format="):
			options.outputFormat = strings.TrimPrefix(argument, "--output-format=")
			options.outputSet = true
		case argument == "--json":
			if options.jsonSet || options.outputSet {
				return execOptions{}, usageError("--json conflicts with another output format")
			}
			options.jsonSet = true
			options.outputFormat = "json"
		case argument == "--help" || argument == "-h":
			return execOptions{}, usageError("Usage: prism exec --api chat|completions|responses|messages [--body <path>|-] [--model <model>] [--provider <provider>] [--output-format text|json|stream-json]")
		case strings.HasPrefix(argument, "-"):
			return execOptions{}, usageError("unknown option %q", argument)
		default:
			return execOptions{}, usageError("unexpected argument %q", argument)
		}
	}
	if options.apiName == "" {
		return execOptions{}, usageError("--api is required")
	}
	if _, ok := api.InferencePath(options.apiName); !ok {
		return execOptions{}, usageError("unsupported API %q; use chat, completions, responses, or messages", options.apiName)
	}
	if options.outputFormat != "text" && options.outputFormat != "json" && options.outputFormat != "stream-json" {
		return execOptions{}, usageError("unsupported output format %q", options.outputFormat)
	}
	return options, nil
}

func readExecBody(options execOptions, stdin io.Reader) ([]byte, error) {
	if options.bodySet && options.bodyPath != "-" {
		if file, ok := stdin.(*os.File); ok {
			if info, err := file.Stat(); err == nil && info.Mode()&os.ModeCharDevice == 0 {
				return nil, usageError("request body was supplied by both --body and stdin")
			}
		}
		body, err := os.ReadFile(options.bodyPath)
		if err != nil {
			return nil, usageError("could not read request body: %v", err)
		}
		return body, nil
	}
	body, err := io.ReadAll(stdin)
	if err != nil {
		return nil, usageError("could not read request body: %v", err)
	}
	if len(bytes.TrimSpace(body)) == 0 {
		return nil, usageError("request body is empty")
	}
	return body, nil
}

func prepareExecBody(body []byte, options execOptions) ([]byte, error) {
	var payload map[string]any
	if err := json.Unmarshal(body, &payload); err != nil || payload == nil {
		return nil, malformedError(errors.New("request body must be a JSON object"))
	}
	if options.model != "" {
		payload["model"] = options.model
	}
	if options.outputFormat == "stream-json" {
		payload["stream"] = true
	}
	return json.Marshal(payload)
}

func prismInferenceClient(ctx context.Context) (api.Client, error) {
	provider, err := credentials.New()
	if err != nil {
		return api.Client{}, authError(err)
	}
	credential, err := provider.Resolve(ctx)
	if err != nil {
		return api.Client{}, authError(err)
	}
	baseURL := strings.TrimSpace(os.Getenv("PRISM_BASE_URL"))
	if baseURL == "" {
		var profile *credentials.StoredProfile
		if credential.Source.Type == credentials.SourceProfile {
			profile, err = provider.GetProfile(ctx)
			if err != nil {
				return api.Client{}, authError(err)
			}
		}
		baseURL, err = prismURLForProfile(profile)
		if err != nil {
			return api.Client{}, usageError("could not determine Prism endpoint: %v", err)
		}
	} else {
		baseURL = strings.TrimSuffix(baseURL, "/")
		if baseURL != "https://prism.circles.ac" && baseURL != "https://prism-dev.circles.ac" {
			return api.Client{}, usageError("PRISM_BASE_URL must be https://prism.circles.ac or https://prism-dev.circles.ac")
		}
	}
	return api.Client{BaseURL: strings.TrimSuffix(baseURL, "/"), Token: credential.Value}, nil
}

func inferenceHTTPError(response *http.Response) error {
	body, _ := io.ReadAll(io.LimitReader(response.Body, 64<<10))
	message := strings.TrimSpace(string(body))
	if message == "" {
		message = http.StatusText(response.StatusCode)
	}
	var code int
	switch {
	case response.StatusCode == http.StatusUnauthorized:
		code = exitAuthentication
	case response.StatusCode == http.StatusForbidden:
		code = exitAuthorization
	case response.StatusCode == http.StatusTooManyRequests:
		code = exitRateLimit
	case response.StatusCode >= 400 && response.StatusCode < 500:
		code = exitHTTP4xx
	case response.StatusCode >= 500:
		code = exitHTTP5xx
	default:
		code = exitTransport
	}
	return &commandError{code: code, err: fmt.Errorf("Prism returned HTTP %d: %s", response.StatusCode, compactResponseError(message))}
}

func compactResponseError(message string) string {
	var structured struct {
		Error any `json:"error"`
	}
	if json.Unmarshal([]byte(message), &structured) == nil && structured.Error != nil {
		if text, ok := structured.Error.(string); ok && text != "" {
			return text
		}
		encoded, _ := json.Marshal(structured.Error)
		return string(encoded)
	}
	return strings.Join(strings.Fields(message), " ")
}

func readResponseIdle(ctx context.Context, reader io.Reader, idle time.Duration) ([]byte, error) {
	type result struct {
		data []byte
		err  error
	}
	results := make(chan result, 1)
	go func() {
		buffer := make([]byte, 32*1024)
		for {
			count, err := reader.Read(buffer)
			if count > 0 {
				chunk := append([]byte(nil), buffer[:count]...)
				results <- result{data: chunk}
			}
			if err != nil {
				results <- result{err: err}
				return
			}
		}
	}()
	var output bytes.Buffer
	timer := time.NewTimer(idle)
	defer timer.Stop()
	for {
		select {
		case <-ctx.Done():
			if closer, ok := reader.(io.Closer); ok {
				_ = closer.Close()
			}
			return nil, ctx.Err()
		case <-timer.C:
			if closer, ok := reader.(io.Closer); ok {
				_ = closer.Close()
			}
			return nil, errors.New("Prism stream idle timeout")
		case result := <-results:
			if len(result.data) > 0 {
				_, _ = output.Write(result.data)
				if !timer.Stop() {
					select {
					case <-timer.C:
					default:
					}
				}
				timer.Reset(idle)
			}
			if result.err != nil {
				if errors.Is(result.err, io.EOF) {
					return output.Bytes(), nil
				}
				return nil, result.err
			}
		}
	}
}

func isEventStream(contentType string, payload []byte) bool {
	trimmed := bytes.TrimSpace(payload)
	return strings.Contains(strings.ToLower(contentType), "text/event-stream") || bytes.HasPrefix(trimmed, []byte("data:")) || bytes.HasPrefix(trimmed, []byte("event:"))
}

type sseEvent struct {
	Event string
	Data  string
}

func parseSSE(payload []byte) ([]sseEvent, error) {
	if !bytes.HasSuffix(payload, []byte("\n\n")) && !bytes.HasSuffix(payload, []byte("\r\n\r\n")) {
		return nil, malformedError(errors.New("SSE response ended with a truncated event"))
	}
	scanner := bufio.NewScanner(bytes.NewReader(payload))
	scanner.Buffer(make([]byte, 4096), 4<<20)
	var events []sseEvent
	var eventName string
	var data []string
	flush := func() {
		if len(data) == 0 && eventName == "" {
			return
		}
		events = append(events, sseEvent{Event: eventName, Data: strings.Join(data, "\n")})
		eventName = ""
		data = nil
	}
	for scanner.Scan() {
		line := strings.TrimSuffix(scanner.Text(), "\r")
		if line == "" {
			flush()
			continue
		}
		if strings.HasPrefix(line, ":") {
			continue
		}
		switch {
		case strings.HasPrefix(line, "event:"):
			eventName = strings.TrimSpace(strings.TrimPrefix(line, "event:"))
		case strings.HasPrefix(line, "data:"):
			data = append(data, strings.TrimPrefix(strings.TrimPrefix(line, "data:"), " "))
		case strings.HasPrefix(line, "id:") || strings.HasPrefix(line, "retry:"):
			continue
		default:
			return nil, malformedError(fmt.Errorf("malformed SSE line %q", line))
		}
	}
	if err := scanner.Err(); err != nil {
		return nil, malformedError(errors.New("could not parse SSE response"))
	}
	flush()
	if len(events) == 0 {
		return nil, malformedError(errors.New("Prism returned an empty SSE response"))
	}
	return events, nil
}

func decodeEventData(data string) (any, error) {
	if data == "[DONE]" {
		return "[DONE]", nil
	}
	var value any
	if json.Unmarshal([]byte(data), &value) == nil {
		return value, nil
	}
	return nil, malformedError(errors.New("SSE event data is not valid JSON"))
}

func eventIsTerminal(event sseEvent) bool {
	if event.Data == "[DONE]" {
		return true
	}
	name := strings.ToLower(event.Event)
	return strings.Contains(name, "done") || strings.Contains(name, "completed") || strings.Contains(name, "message_stop")
}

func writeStreamJSON(stdout io.Writer, apiName string, payload []byte) error {
	events, err := parseSSE(payload)
	if err != nil {
		return err
	}
	terminal := false
	for _, event := range events {
		name := event.Event
		if name == "" {
			name = "message"
		}
		data, decodeErr := decodeEventData(event.Data)
		if decodeErr != nil {
			return decodeErr
		}
		line, marshalErr := json.Marshal(map[string]any{
			"api":   apiName,
			"event": name,
			"data":  data,
		})
		if marshalErr != nil {
			return malformedError(errors.New("could not encode stream event"))
		}
		if _, err := fmt.Fprintln(stdout, string(line)); err != nil {
			return err
		}
		terminal = terminal || eventIsTerminal(event)
	}
	if !terminal {
		return malformedError(errors.New("SSE response ended without a terminal event"))
	}
	return nil
}

func writeJSONText(stdout io.Writer, apiName string, payload []byte) error {
	var value any
	if err := json.Unmarshal(payload, &value); err != nil {
		return malformedError(errors.New("Prism returned invalid JSON"))
	}
	text := extractText(apiName, value)
	_, err := io.WriteString(stdout, text)
	return err
}

func writeStreamText(stdout io.Writer, apiName string, payload []byte) error {
	events, err := parseSSE(payload)
	if err != nil {
		return err
	}
	terminal := false
	for _, event := range events {
		terminal = terminal || eventIsTerminal(event)
		if event.Data == "[DONE]" {
			continue
		}
		var value any
		if json.Unmarshal([]byte(event.Data), &value) != nil {
			return malformedError(errors.New("SSE event data is not valid JSON"))
		}
		if _, err := io.WriteString(stdout, extractStreamText(apiName, value)); err != nil {
			return err
		}
	}
	if !terminal {
		return malformedError(errors.New("SSE response ended without a terminal event"))
	}
	return nil
}

func extractText(apiName string, value any) string {
	root, ok := value.(map[string]any)
	if !ok {
		return ""
	}
	switch apiName {
	case "chat":
		return firstChoiceText(root, "message", "content")
	case "completions":
		return firstChoiceText(root, "", "text")
	case "responses":
		if text, ok := root["output_text"].(string); ok {
			return text
		}
		return recursiveText(root["output"])
	case "messages":
		return recursiveText(root["content"])
	default:
		return ""
	}
}

func firstChoiceText(root map[string]any, parentKey string, textKey string) string {
	choices, _ := root["choices"].([]any)
	var output strings.Builder
	for _, choice := range choices {
		item, _ := choice.(map[string]any)
		if parentKey != "" {
			item, _ = item[parentKey].(map[string]any)
		}
		if item == nil {
			continue
		}
		if text, ok := item[textKey].(string); ok {
			output.WriteString(text)
		} else {
			output.WriteString(recursiveText(item[textKey]))
		}
	}
	return output.String()
}

func recursiveText(value any) string {
	var output strings.Builder
	var walk func(any)
	walk = func(current any) {
		switch item := current.(type) {
		case string:
			output.WriteString(item)
		case []any:
			for _, child := range item {
				walk(child)
			}
		case map[string]any:
			if text, ok := item["text"].(string); ok {
				output.WriteString(text)
				return
			}
			if text, ok := item["output_text"].(string); ok {
				output.WriteString(text)
				return
			}
			if content, ok := item["content"]; ok {
				walk(content)
			}
		}
	}
	walk(value)
	return output.String()
}

func extractStreamText(apiName string, value any) string {
	root, _ := value.(map[string]any)
	if root == nil {
		return ""
	}
	switch apiName {
	case "chat":
		return firstChoiceText(root, "delta", "content")
	case "completions":
		return firstChoiceText(root, "", "text")
	case "responses":
		if text, ok := root["delta"].(string); ok {
			return text
		}
		if text, ok := root["output_text"]; ok {
			return recursiveText(text)
		}
		if response, ok := root["response"]; ok {
			return extractStreamText(apiName, response)
		}
	case "messages":
		if delta, ok := root["delta"]; ok {
			return recursiveText(delta)
		}
		if block, ok := root["content_block"]; ok {
			return recursiveText(block)
		}
	}
	return ""
}
