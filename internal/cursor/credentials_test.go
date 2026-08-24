package cursor

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestReadAccessTokenFileReportsExplicitErrors(t *testing.T) {
	path := filepath.Join(t.TempDir(), "auth.json")
	if _, err := readAccessTokenFile(path); !errors.Is(err, errCursorLoginNotFound) {
		t.Fatalf("missing login error = %v", err)
	}
	for _, content := range []string{"{", `{"accessToken":"   "}`, `{"refreshToken":"only-refresh"}`} {
		if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
		_, err := readAccessTokenFile(path)
		if err == nil || !strings.Contains(err.Error(), "unreadable") {
			t.Fatalf("content %s error = %v", content, err)
		}
	}
	if err := os.WriteFile(path, []byte(`{"accessToken":" header.payload.signature "}`), 0o600); err != nil {
		t.Fatal(err)
	}
	token, err := readAccessTokenFile(path)
	if err != nil || token != "header.payload.signature" {
		t.Fatalf("token = %q, error = %v", token, err)
	}
}
