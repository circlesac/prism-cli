package cli

import (
	"bytes"
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
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

func TestClaudeEnvironmentUsesPrismBearerTokenWithoutLocalLogin(t *testing.T) {
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
	}, "http://127.0.0.1:12345", "X-Prism-Claude-Bridge-abc: 123456", "prism-secret")
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
		"existing-login-token",
	} {
		if strings.Contains(joined, unwanted) {
			t.Fatalf("environment retained %q: %s", unwanted, joined)
		}
	}
	for _, wanted := range []string{
		"PATH=/usr/bin",
		"ANTHROPIC_BASE_URL=http://127.0.0.1:12345",
		"ANTHROPIC_CUSTOM_HEADERS=X-Prism-Claude-Bridge-abc: 123456",
		"ANTHROPIC_AUTH_TOKEN=prism-secret",
	} {
		if !strings.Contains(joined, wanted) {
			t.Fatalf("environment omitted %q: %s", wanted, joined)
		}
	}
	if strings.Contains(joined, "ANTHROPIC_API_KEY=") {
		t.Fatalf("environment still contains ANTHROPIC_API_KEY: %s", joined)
	}
}

func TestClaudeAccountHeaders(t *testing.T) {
	if got := claudeAccountHeaders(""); got != "" {
		t.Fatalf("empty account header = %q", got)
	}
	if got := claudeAccountHeaders("acct-01"); got != "X-Prism-Anthropic-Account: b64:YWNjdC0wMQ" {
		t.Fatalf("account header = %q", got)
	}
	if got := claudeAccountHeaders("팀 계정 — 예시"); got != "X-Prism-Anthropic-Account: b64:7YyAIOqzhOyglSDigJQg7JiI7Iuc" {
		t.Fatalf("unicode account header = %q", got)
	}
}
