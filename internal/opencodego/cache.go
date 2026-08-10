package opencodego

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"time"
)

const sessionCacheVersion = 1

type cachedSessionFile struct {
	Version  int             `json:"version"`
	Sessions []cachedSession `json:"sessions"`
}

type cachedSession struct {
	Label   string         `json:"label"`
	Cookies []cachedCookie `json:"cookies"`
}

type cachedCookie struct {
	Name      string    `json:"name"`
	Value     string    `json:"value"`
	ExpiresAt time.Time `json:"expires_at"`
}

func loadSessionCache(now time.Time) ([]browserSession, error) {
	path, err := sessionCachePath()
	if err != nil {
		return nil, err
	}
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var cache cachedSessionFile
	if err := json.Unmarshal(data, &cache); err != nil {
		return nil, err
	}
	if cache.Version != sessionCacheVersion {
		return nil, errors.New("unsupported OpenCode session cache version")
	}

	var sessions []browserSession
	for _, cached := range cache.Sessions {
		var cookies []browserCookie
		for _, cookie := range cached.Cookies {
			if cookie.Value == "" || cookie.ExpiresAt.IsZero() || !cookie.ExpiresAt.After(now) {
				continue
			}
			if cookie.Name != "auth" && cookie.Name != "__Host-auth" {
				continue
			}
			cookies = append(cookies, browserCookie{
				name:      cookie.Name,
				value:     cookie.Value,
				expiresAt: cookie.ExpiresAt,
			})
		}
		if len(cookies) > 0 {
			sessions = append(sessions, browserSession{label: cached.Label, cookies: cookies})
		}
	}
	return sessions, nil
}

func saveSessionCache(sessions []browserSession) error {
	cache := cachedSessionFile{Version: sessionCacheVersion}
	for _, session := range sessions {
		var cookies []cachedCookie
		for _, cookie := range session.cookies {
			if cookie.value == "" || cookie.expiresAt.IsZero() {
				continue
			}
			cookies = append(cookies, cachedCookie{
				Name:      cookie.name,
				Value:     cookie.value,
				ExpiresAt: cookie.expiresAt,
			})
		}
		if len(cookies) > 0 {
			cache.Sessions = append(cache.Sessions, cachedSession{Label: session.label, Cookies: cookies})
		}
	}
	if len(cache.Sessions) == 0 {
		return nil
	}

	path, err := sessionCachePath()
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	if err := os.Chmod(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	temporary, err := os.CreateTemp(filepath.Dir(path), ".sessions-*.json")
	if err != nil {
		return err
	}
	temporaryPath := temporary.Name()
	defer func() { _ = os.Remove(temporaryPath) }()
	if err := temporary.Chmod(0o600); err != nil {
		_ = temporary.Close()
		return err
	}
	if err := json.NewEncoder(temporary).Encode(cache); err != nil {
		_ = temporary.Close()
		return err
	}
	if err := temporary.Sync(); err != nil {
		_ = temporary.Close()
		return err
	}
	if err := temporary.Close(); err != nil {
		return err
	}
	return os.Rename(temporaryPath, path)
}

func sessionCachePath() (string, error) {
	root := os.Getenv("XDG_CACHE_HOME")
	if root == "" || !filepath.IsAbs(root) {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", err
		}
		root = filepath.Join(home, ".cache")
	}
	return filepath.Join(root, "prism", "opencode-go", "sessions.json"), nil
}
