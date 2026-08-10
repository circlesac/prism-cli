//go:build darwin

package opencodego

import (
	"crypto/aes"
	"crypto/pbkdf2"
	"crypto/sha1"
	"encoding/hex"
	"os"
	"path/filepath"
	"strconv"
	"testing"
	"time"
)

func TestChromiumProfilesFindsDefaultAndEveryNamedProfile(t *testing.T) {
	root := t.TempDir()
	for _, name := range []string{"Default", "Profile 2", "Profile 1", "System Profile", "Guest Profile"} {
		if err := os.Mkdir(filepath.Join(root, name), 0o700); err != nil {
			t.Fatal(err)
		}
	}
	got := chromiumProfiles(root)
	want := []string{"Default", "Profile 1", "Profile 2"}
	if len(got) != len(want) {
		t.Fatalf("profiles = %#v", got)
	}
	for index := range want {
		if got[index] != want[index] {
			t.Fatalf("profiles = %#v", got)
		}
	}
}

func TestChromeCookieExpiredUsesTheChromeEpoch(t *testing.T) {
	now := time.Date(2026, 8, 10, 0, 0, 0, 0, time.UTC)
	toChrome := func(value time.Time) string {
		return time.Duration(value.Unix() + 11_644_473_600).String()
	}
	// time.Duration.String is intentionally not a valid Chrome timestamp.
	if chromeCookieExpired(toChrome(now.Add(-time.Hour)), now) {
		t.Fatal("invalid timestamp should not be treated as a proven expiration")
	}
	past := (now.Add(-time.Hour).Unix() + 11_644_473_600) * 1_000_000
	future := (now.Add(time.Hour).Unix() + 11_644_473_600) * 1_000_000
	if !chromeCookieExpired(formatInt(past), now) || chromeCookieExpired(formatInt(future), now) || chromeCookieExpired("0", now) {
		t.Fatal("Chrome expiration conversion is incorrect")
	}
}

func TestSnapshotSQLiteCopiesWALAndSharedMemoryBesideDatabase(t *testing.T) {
	directory := t.TempDir()
	source := filepath.Join(directory, "Cookies")
	for suffix, value := range map[string]string{"": "main", "-wal": "wal", "-shm": "shm"} {
		if err := os.WriteFile(source+suffix, []byte(value), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	snapshot, cleanup, err := snapshotSQLite(source)
	if err != nil {
		t.Fatal(err)
	}
	defer cleanup()
	for suffix, want := range map[string]string{"": "main", "-wal": "wal", "-shm": "shm"} {
		got, err := os.ReadFile(snapshot + suffix)
		if err != nil || string(got) != want {
			t.Fatalf("snapshot %q = %q, %v", suffix, got, err)
		}
	}
}

func TestDecryptChromiumCookieFindsTheAuthenticatedValueAfterHostDigest(t *testing.T) {
	want := "Fe26.2*example-session"
	plaintext := append(make([]byte, 32), []byte(want)...)
	padding := aes.BlockSize - len(plaintext)%aes.BlockSize
	for index := 0; index < padding; index++ {
		plaintext = append(plaintext, byte(padding))
	}
	key, err := pbkdf2.Key(sha1.New, "example-password", []byte("saltysalt"), 1003, 16)
	if err != nil {
		t.Fatal(err)
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		t.Fatal(err)
	}
	ciphertext := make([]byte, len(plaintext))
	previous := []byte("                ")
	for offset := 0; offset < len(plaintext); offset += aes.BlockSize {
		for index := 0; index < aes.BlockSize; index++ {
			plaintext[offset+index] ^= previous[index]
		}
		block.Encrypt(ciphertext[offset:offset+aes.BlockSize], plaintext[offset:offset+aes.BlockSize])
		previous = ciphertext[offset : offset+aes.BlockSize]
	}
	got, err := decryptChromiumCookie(hex.EncodeToString(append([]byte("v10"), ciphertext...)), "example-password")
	if err != nil || got != want {
		t.Fatalf("cookie = %q, %v", got, err)
	}
}

func TestFirefoxProfileDiscoverySupportsRelativeAndAbsolutePaths(t *testing.T) {
	root := "/tmp/example-firefox"
	got := firefoxProfilePaths(`[Profile0]
Name=example-user
IsRelative=1
Path=Profiles/example.default
[Profile1]
Name=work-admin
IsRelative=0
Path=/tmp/work-admin.default
`, root)
	if len(got) != 2 || got[0] != (firefoxProfile{name: "example-user", path: filepath.Join(root, "Profiles/example.default")}) ||
		got[1] != (firefoxProfile{name: "work-admin", path: "/tmp/work-admin.default"}) {
		t.Fatalf("profiles = %#v", got)
	}
}

func formatInt(value int64) string {
	return strconv.FormatInt(value, 10)
}
