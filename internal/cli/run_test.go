package cli

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	credentials "github.com/circlesac/credentials-go"
)

func TestHelpDocumentsOnlySupportedChatGPTCommands(t *testing.T) {
	var stdout bytes.Buffer
	if err := Run(context.Background(), []string{"help"}, &stdout, &bytes.Buffer{}, "test"); err != nil {
		t.Fatal(err)
	}
	output := stdout.String()
	for _, command := range []string{"auth login", "auth list", "auth remove"} {
		if !strings.Contains(output, command) {
			t.Fatalf("help did not contain %q", command)
		}
	}
	if strings.Contains(output, "set-default") {
		t.Fatal("help exposed the unsupported ChatGPT set-default command")
	}
}

func TestCommonOptionsMayAppearBeforeOrAfterAccountID(t *testing.T) {
	options, positionals, err := parseCommonOptions([]string{
		"--org", "circlesac",
		"account-123",
		"--profile=dev",
	})
	if err != nil {
		t.Fatal(err)
	}
	if options.org != "circlesac" ||
		options.profile != "dev" ||
		!options.profileSet ||
		len(positionals) != 1 ||
		positionals[0] != "account-123" {
		t.Fatalf("options = %#v, positionals = %#v", options, positionals)
	}
}

func TestPublicCredentialProviderSupportsHeadlessUseWithoutDiskWrites(t *testing.T) {
	home := t.TempDir()
	secret := "headless-circles-secret"
	provider, err := credentials.New(
		credentials.WithHomeDir(home),
		credentials.WithEnvironment(map[string]string{"CIRCLES_AUTH_TOKEN": secret}),
	)
	if err != nil {
		t.Fatal(err)
	}
	credential, err := provider.Resolve(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if credential.Value != secret ||
		credential.Kind != credentials.KindAPIKey ||
		credential.Source != (credentials.Source{Type: credentials.SourceEnvironment}) {
		t.Fatalf("credential kind/source = %s/%#v", credential.Kind, credential.Source)
	}
	if _, err := os.Stat(filepath.Join(home, ".crcl")); !os.IsNotExist(err) {
		t.Fatalf("environment credential wrote to disk: %v", err)
	}
}

func TestCredentialValueIsNotAcceptedAsCommandLineOption(t *testing.T) {
	secret := "command-line-secret"
	err := Run(
		context.Background(),
		[]string{"chatgpt", "auth", "list", "--token", secret},
		&bytes.Buffer{},
		&bytes.Buffer{},
		"test",
	)
	if err == nil {
		t.Fatal("credential command-line option was accepted")
	}
	if strings.Contains(err.Error(), secret) {
		t.Fatal("rejected credential value was echoed in the error")
	}
}
