package cli

import (
	"testing"

	"github.com/circlesac/prism-cli/internal/api"
)

func TestProviderAccountRotationPersistsBalancedSelection(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	accounts := []api.Credential{
		{ID: "account-1", Name: "personal"},
		{ID: "account-2", Name: "work-admin"},
	}

	want := []string{"account-1", "account-2", "account-1", "account-2"}
	for index, expected := range want {
		selected, err := rotateProviderAccount("gemini", accounts)
		if err != nil {
			t.Fatal(err)
		}
		if selected != expected {
			t.Fatalf("selection %d = %q, want %q", index, selected, expected)
		}
	}

	selected, err := rotateProviderAccount("cursor", accounts)
	if err != nil {
		t.Fatal(err)
	}
	if selected != "account-1" {
		t.Fatalf("independent provider selection = %q", selected)
	}
}
