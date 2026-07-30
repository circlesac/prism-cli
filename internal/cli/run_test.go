package cli

import (
	"bytes"
	"context"
	"strings"
	"testing"
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
