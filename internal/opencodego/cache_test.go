package opencodego

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/circlesac/prism-cli/internal/api"
)

func TestSessionCacheUsesXDGPathAndRestrictivePermissions(t *testing.T) {
	root := t.TempDir()
	t.Setenv("XDG_CACHE_HOME", root)
	now := time.Date(2026, 8, 10, 0, 0, 0, 0, time.UTC)
	sessions := []browserSession{{
		label: "Chrome Default",
		cookies: []browserCookie{{
			name: "auth", value: "example-session", expiresAt: now.Add(time.Hour),
		}},
	}}
	if err := saveSessionCache(sessions); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(root, "prism", "opencode-go", "sessions.json")
	fileInfo, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	directoryInfo, err := os.Stat(filepath.Dir(path))
	if err != nil {
		t.Fatal(err)
	}
	if fileInfo.Mode().Perm() != 0o600 || directoryInfo.Mode().Perm() != 0o700 {
		t.Fatalf("file mode = %o, directory mode = %o", fileInfo.Mode().Perm(), directoryInfo.Mode().Perm())
	}
	loaded, err := loadSessionCache(now)
	if err != nil {
		t.Fatal(err)
	}
	if len(loaded) != 1 || loaded[0].label != "Chrome Default" || len(loaded[0].cookies) != 1 || loaded[0].cookies[0].value != "example-session" {
		t.Fatalf("sessions = %#v", loaded)
	}
}

func TestSessionCacheDropsExpiredAndNonPersistentCookies(t *testing.T) {
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	now := time.Date(2026, 8, 10, 0, 0, 0, 0, time.UTC)
	if err := saveSessionCache([]browserSession{{
		label: "Chrome Default",
		cookies: []browserCookie{
			{name: "auth", value: "expired", expiresAt: now.Add(-time.Minute)},
			{name: "__Host-auth", value: "session-only"},
		},
	}}); err != nil {
		t.Fatal(err)
	}
	loaded, err := loadSessionCache(now)
	if err != nil {
		t.Fatal(err)
	}
	if len(loaded) != 0 {
		t.Fatalf("sessions = %#v", loaded)
	}
}

func TestFetchUsesCacheWithoutScanningBrowsers(t *testing.T) {
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	if err := saveSessionCache([]browserSession{{
		label: "Chrome Default",
		cookies: []browserCookie{{
			name: "auth", value: "cached-session", expiresAt: time.Now().Add(time.Hour),
		}},
	}}); err != nil {
		t.Fatal(err)
	}

	originalScan := scanBrowserSessions
	originalFetch := fetchSessionUsage
	defer func() {
		scanBrowserSessions = originalScan
		fetchSessionUsage = originalFetch
	}()
	scanBrowserSessions = func(time.Time) sessionScan {
		t.Fatal("browser scan was called for a valid cached session")
		return sessionScan{}
	}
	fetchSessionUsage = func(_ context.Context, _ time.Time, sessions []browserSession) (api.ProviderUsage, error) {
		if len(sessions) != 1 || len(sessions[0].cookies) != 1 || sessions[0].cookies[0].value != "cached-session" {
			t.Fatalf("sessions = %#v", sessions)
		}
		return api.ProviderUsage{Provider: "opencode-go"}, nil
	}
	if _, err := Fetch(context.Background()); err != nil {
		t.Fatal(err)
	}
}

func TestFetchReplacesRejectedCacheFromBrowser(t *testing.T) {
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	if err := saveSessionCache([]browserSession{{
		label: "Chrome Default",
		cookies: []browserCookie{{
			name: "auth", value: "rejected-session", expiresAt: time.Now().Add(time.Hour),
		}},
	}}); err != nil {
		t.Fatal(err)
	}

	originalScan := scanBrowserSessions
	originalFetch := fetchSessionUsage
	defer func() {
		scanBrowserSessions = originalScan
		fetchSessionUsage = originalFetch
	}()
	fresh := browserSession{
		label: "Chrome Profile 1",
		cookies: []browserCookie{{
			name: "auth", value: "fresh-session", expiresAt: time.Now().Add(2 * time.Hour),
		}},
	}
	scanBrowserSessions = func(time.Time) sessionScan {
		return sessionScan{sessions: []browserSession{fresh}}
	}
	requests := 0
	fetchSessionUsage = func(_ context.Context, _ time.Time, sessions []browserSession) (api.ProviderUsage, error) {
		requests++
		if sessions[0].cookies[0].value == "rejected-session" {
			return api.ProviderUsage{}, errOpenCodeSessionInvalid
		}
		if sessions[0].cookies[0].value != "fresh-session" {
			t.Fatalf("sessions = %#v", sessions)
		}
		return api.ProviderUsage{Provider: "opencode-go"}, nil
	}
	if _, err := Fetch(context.Background()); err != nil {
		t.Fatal(err)
	}
	if requests != 2 {
		t.Fatalf("requests = %d", requests)
	}
	loaded, err := loadSessionCache(time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if len(loaded) != 1 || loaded[0].cookies[0].value != "fresh-session" {
		t.Fatalf("sessions = %#v", loaded)
	}
}

func TestFetchDoesNotScanBrowsersAfterCachedNetworkFailure(t *testing.T) {
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	if err := saveSessionCache([]browserSession{{
		label: "Chrome Default",
		cookies: []browserCookie{{
			name: "auth", value: "cached-session", expiresAt: time.Now().Add(time.Hour),
		}},
	}}); err != nil {
		t.Fatal(err)
	}

	originalScan := scanBrowserSessions
	originalFetch := fetchSessionUsage
	defer func() {
		scanBrowserSessions = originalScan
		fetchSessionUsage = originalFetch
	}()
	scanBrowserSessions = func(time.Time) sessionScan {
		t.Fatal("browser scan was called after a network failure")
		return sessionScan{}
	}
	fetchSessionUsage = func(context.Context, time.Time, []browserSession) (api.ProviderUsage, error) {
		return api.ProviderUsage{}, errors.New("network unavailable")
	}
	_, err := Fetch(context.Background())
	if err == nil || err.Error() != "network unavailable" {
		t.Fatalf("error = %v", err)
	}
}
