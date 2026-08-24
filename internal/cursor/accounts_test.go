package cursor

import (
	"encoding/base64"
	"fmt"
	"os"
	"path/filepath"
	"testing"
)

func TestListAndResolveIsolatedCursorAccounts(t *testing.T) {
	root := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", root)
	accounts, err := AccountsDirectory()
	if err != nil {
		t.Fatal(err)
	}
	for _, item := range []struct{ name, email, subject string }{
		{name: "work-admin", email: "work@example.com", subject: "auth0|work"},
		{name: "personal", email: "person@example.com", subject: "auth0|personal"},
	} {
		directory := filepath.Join(accounts, item.name)
		if err := os.MkdirAll(directory, 0o700); err != nil {
			t.Fatal(err)
		}
		payload := base64.RawURLEncoding.EncodeToString([]byte(fmt.Sprintf(`{"sub":%q}`, item.subject)))
		if err := os.WriteFile(filepath.Join(directory, "auth.json"), []byte(fmt.Sprintf(`{"accessToken":"header.%s.signature"}`, payload)), 0o600); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(directory, "cli-config.json"), []byte(fmt.Sprintf(`{"authInfo":{"authId":%q,"email":%q}}`, item.subject, item.email)), 0o600); err != nil {
			t.Fatal(err)
		}
	}

	listed, err := ListAccounts()
	if err != nil {
		t.Fatal(err)
	}
	if len(listed) != 2 || listed[0].Name != "personal" || listed[1].Name != "work-admin" {
		t.Fatalf("accounts = %#v", listed)
	}
	resolved, err := ResolveAccount("work@example.com")
	if err != nil || resolved.Name != "work-admin" {
		t.Fatalf("resolved = %#v, error = %v", resolved, err)
	}
	if err := RemoveAccount("person@example.com"); err != nil {
		t.Fatal(err)
	}
	listed, err = ListAccounts()
	if err != nil || len(listed) != 1 || listed[0].Name != "work-admin" {
		t.Fatalf("remaining = %#v, error = %v", listed, err)
	}
}

func TestValidateCursorAccountNameRejectsPaths(t *testing.T) {
	for _, value := range []string{"", "../escape", "has space", "/absolute"} {
		if ValidateAccountName(value) == nil {
			t.Fatalf("accepted %q", value)
		}
	}
	for _, value := range []string{"personal", "work-admin", "person@example.com"} {
		if err := ValidateAccountName(value); err != nil {
			t.Fatalf("rejected %q: %v", value, err)
		}
	}
}
