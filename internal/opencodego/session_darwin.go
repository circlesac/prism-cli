//go:build darwin

package opencodego

import (
	"bytes"
	"crypto/aes"
	"crypto/pbkdf2"
	"crypto/sha1"
	"encoding/hex"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"
)

type chromiumProduct struct {
	name             string
	root             string
	keychainServices []string
	keychainAccount  string
}

func browserSessions(now time.Time) sessionScan {
	home, err := os.UserHomeDir()
	if err != nil {
		return sessionScan{unreadableStores: 1}
	}
	products := []chromiumProduct{
		{
			name: "Chrome", root: filepath.Join(home, "Library/Application Support/Google/Chrome"),
			keychainServices: []string{"Chrome Safe Storage"}, keychainAccount: "Chrome",
		},
		{
			name: "Chromium", root: filepath.Join(home, "Library/Application Support/Chromium"),
			keychainServices: []string{"Chromium Safe Storage"}, keychainAccount: "Chromium",
		},
		{
			name: "Comet", root: filepath.Join(home, "Library/Application Support/Comet"),
			keychainServices: []string{"Comet Safe Storage", "Chromium Safe Storage"}, keychainAccount: "Comet",
		},
		{
			name: "Arc", root: filepath.Join(home, "Library/Application Support/Arc/User Data"),
			keychainServices: []string{"Arc Safe Storage"}, keychainAccount: "Arc",
		},
		{
			name: "Edge", root: filepath.Join(home, "Library/Application Support/Microsoft Edge"),
			keychainServices: []string{"Microsoft Edge Safe Storage"}, keychainAccount: "Microsoft Edge",
		},
		{
			name: "Brave", root: filepath.Join(home, "Library/Application Support/BraveSoftware/Brave-Browser"),
			keychainServices: []string{"Brave Safe Storage"}, keychainAccount: "Brave",
		},
	}

	var result sessionScan
	for _, product := range products {
		for _, profile := range chromiumProfiles(product.root) {
			for _, database := range []string{
				filepath.Join(product.root, profile, "Cookies"),
				filepath.Join(product.root, profile, "Network", "Cookies"),
			} {
				if _, err := os.Stat(database); err != nil {
					continue
				}
				result.storesFound++
				session, scan := chromiumSession(product, profile, database, now)
				result.expiredCookies += scan.expiredCookies
				result.invalidCookies += scan.invalidCookies
				result.unreadableStores += scan.unreadableStores
				if len(session.cookies) > 0 {
					result.sessions = append(result.sessions, session)
				}
			}
		}
	}

	firefoxScan := firefoxSessions(filepath.Join(home, "Library/Application Support/Firefox"), now)
	result.storesFound += firefoxScan.storesFound
	result.expiredCookies += firefoxScan.expiredCookies
	result.invalidCookies += firefoxScan.invalidCookies
	result.unreadableStores += firefoxScan.unreadableStores
	result.sessions = append(result.sessions, firefoxScan.sessions...)
	return result
}

func chromiumProfiles(root string) []string {
	entries, err := os.ReadDir(root)
	if err != nil {
		return nil
	}
	var profiles []string
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		if entry.Name() == "Default" || strings.HasPrefix(entry.Name(), "Profile ") {
			profiles = append(profiles, entry.Name())
		}
	}
	sort.Strings(profiles)
	return profiles
}

func chromiumSession(product chromiumProduct, profile string, database string, now time.Time) (browserSession, sessionScan) {
	var scan sessionScan
	snapshot, cleanup, err := snapshotSQLite(database)
	if err != nil {
		scan.unreadableStores++
		return browserSession{}, scan
	}
	defer cleanup()

	query := "SELECT name, hex(encrypted_value), value, expires_utc FROM cookies WHERE host_key IN ('opencode.ai', '.opencode.ai') AND name IN ('auth', '__Host-auth')"
	output, err := exec.Command("/usr/bin/sqlite3", "-readonly", "-separator", "\t", snapshot, query).Output()
	if err != nil {
		scan.unreadableStores++
		return browserSession{}, scan
	}

	password := ""
	passwordLoaded := false
	cookies := map[string]string{}
	for _, line := range strings.Split(strings.TrimSuffix(string(output), "\n"), "\n") {
		if line == "" {
			continue
		}
		fields := strings.SplitN(line, "\t", 4)
		if len(fields) != 4 {
			scan.invalidCookies++
			continue
		}
		if chromeCookieExpired(fields[3], now) {
			scan.expiredCookies++
			continue
		}
		value := fields[2]
		if value == "" && fields[1] != "" {
			if !passwordLoaded {
				password, err = chromiumSafeStoragePassword(product)
				passwordLoaded = true
			}
			if err == nil {
				value, err = decryptChromiumCookie(fields[1], password)
			}
		}
		if err != nil || value == "" {
			scan.invalidCookies++
			continue
		}
		cookies[fields[0]] = value
	}

	return browserSession{label: product.name + " " + profile, cookies: sortedCookies(cookies)}, scan
}

func chromiumSafeStoragePassword(product chromiumProduct) (string, error) {
	for _, service := range product.keychainServices {
		arguments := []string{"find-generic-password", "-w", "-s", service}
		if product.keychainAccount != "" {
			arguments = append(arguments, "-a", product.keychainAccount)
		}
		output, err := exec.Command("/usr/bin/security", arguments...).Output()
		if err != nil {
			output, err = exec.Command("/usr/bin/security", "find-generic-password", "-w", "-s", service).Output()
		}
		if err == nil && strings.TrimSpace(string(output)) != "" {
			return strings.TrimSpace(string(output)), nil
		}
	}
	return "", errors.New("safe storage password was not found")
}

func decryptChromiumCookie(encoded string, password string) (string, error) {
	encrypted, err := hex.DecodeString(encoded)
	if err != nil || len(encrypted) < 3 || string(encrypted[:3]) != "v10" {
		return "", errors.New("unsupported encrypted cookie")
	}
	key, err := pbkdf2.Key(sha1.New, password, []byte("saltysalt"), 1003, 16)
	if err != nil {
		return "", err
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return "", err
	}
	ciphertext := encrypted[3:]
	if len(ciphertext) == 0 || len(ciphertext)%aes.BlockSize != 0 {
		return "", errors.New("invalid encrypted cookie length")
	}
	plaintext := make([]byte, len(ciphertext))
	previous := []byte("                ")
	for offset := 0; offset < len(ciphertext); offset += aes.BlockSize {
		block.Decrypt(plaintext[offset:offset+aes.BlockSize], ciphertext[offset:offset+aes.BlockSize])
		for index := 0; index < aes.BlockSize; index++ {
			plaintext[offset+index] ^= previous[index]
		}
		previous = ciphertext[offset : offset+aes.BlockSize]
	}
	padding := int(plaintext[len(plaintext)-1])
	if padding == 0 || padding > aes.BlockSize || padding > len(plaintext) {
		return "", errors.New("invalid encrypted cookie padding")
	}
	for _, value := range plaintext[len(plaintext)-padding:] {
		if int(value) != padding {
			return "", errors.New("invalid encrypted cookie padding")
		}
	}
	plaintext = plaintext[:len(plaintext)-padding]
	if start := bytes.Index(plaintext, []byte("Fe26.2")); start >= 0 {
		plaintext = plaintext[start:]
	} else if len(plaintext) > 32 {
		// Current Chromium stores may prefix the value with a host digest.
		plaintext = plaintext[32:]
	}
	if len(plaintext) == 0 || !utf8.Valid(plaintext) || bytes.IndexFunc(plaintext, func(r rune) bool { return r < 0x20 || r == 0x7f }) >= 0 {
		return "", errors.New("invalid decrypted cookie")
	}
	return string(plaintext), nil
}

func chromeCookieExpired(value string, now time.Time) bool {
	microseconds, err := strconv.ParseInt(value, 10, 64)
	if err != nil || microseconds == 0 {
		return false
	}
	const windowsToUnixSeconds = 11_644_473_600
	expires := time.Unix(microseconds/1_000_000-windowsToUnixSeconds, microseconds%1_000_000*1_000)
	return !expires.After(now)
}

func firefoxSessions(root string, now time.Time) sessionScan {
	var result sessionScan
	configuration, err := os.ReadFile(filepath.Join(root, "profiles.ini"))
	if os.IsNotExist(err) {
		return result
	}
	if err != nil {
		result.unreadableStores++
		return result
	}
	for _, profile := range firefoxProfilePaths(string(configuration), root) {
		database := filepath.Join(profile.path, "cookies.sqlite")
		if _, err := os.Stat(database); err != nil {
			continue
		}
		result.storesFound++
		snapshot, cleanup, err := snapshotSQLite(database)
		if err != nil {
			result.unreadableStores++
			continue
		}
		query := "SELECT name, value, expiry FROM moz_cookies WHERE host IN ('opencode.ai', '.opencode.ai') AND name IN ('auth', '__Host-auth')"
		output, queryErr := exec.Command("/usr/bin/sqlite3", "-readonly", "-separator", "\t", snapshot, query).Output()
		cleanup()
		if queryErr != nil {
			result.unreadableStores++
			continue
		}
		cookies := map[string]string{}
		for _, line := range strings.Split(strings.TrimSuffix(string(output), "\n"), "\n") {
			if line == "" {
				continue
			}
			fields := strings.SplitN(line, "\t", 3)
			if len(fields) != 3 {
				result.invalidCookies++
				continue
			}
			expires, parseErr := strconv.ParseInt(fields[2], 10, 64)
			if parseErr == nil && expires > 0 && !time.Unix(expires, 0).After(now) {
				result.expiredCookies++
				continue
			}
			if fields[1] == "" {
				result.invalidCookies++
				continue
			}
			cookies[fields[0]] = fields[1]
		}
		if sessionCookies := sortedCookies(cookies); len(sessionCookies) > 0 {
			result.sessions = append(result.sessions, browserSession{label: "Firefox " + profile.name, cookies: sessionCookies})
		}
	}
	return result
}

type firefoxProfile struct {
	name string
	path string
}

func firefoxProfilePaths(configuration string, root string) []firefoxProfile {
	var result []firefoxProfile
	section := ""
	values := map[string]string{}
	flush := func() {
		if !strings.HasPrefix(section, "Profile") || values["Path"] == "" {
			return
		}
		path := values["Path"]
		if values["IsRelative"] != "0" {
			path = filepath.Join(root, path)
		}
		name := values["Name"]
		if name == "" {
			name = filepath.Base(path)
		}
		result = append(result, firefoxProfile{name: name, path: path})
	}
	for _, rawLine := range strings.Split(configuration, "\n") {
		line := strings.TrimSpace(rawLine)
		if strings.HasPrefix(line, "[") && strings.HasSuffix(line, "]") {
			flush()
			section = strings.TrimSuffix(strings.TrimPrefix(line, "["), "]")
			values = map[string]string{}
			continue
		}
		if key, value, ok := strings.Cut(line, "="); ok {
			values[strings.TrimSpace(key)] = strings.TrimSpace(value)
		}
	}
	flush()
	sort.Slice(result, func(left, right int) bool { return result[left].name < result[right].name })
	return result
}

func sortedCookies(values map[string]string) []browserCookie {
	names := make([]string, 0, len(values))
	for name := range values {
		names = append(names, name)
	}
	sort.Strings(names)
	result := make([]browserCookie, 0, len(names))
	for _, name := range names {
		result = append(result, browserCookie{name: name, value: values[name]})
	}
	return result
}

func snapshotSQLite(source string) (string, func(), error) {
	directory, err := os.MkdirTemp("", "prism-opencode-")
	if err != nil {
		return "", func() {}, err
	}
	cleanup := func() { _ = os.RemoveAll(directory) }
	destination := filepath.Join(directory, filepath.Base(source))
	for _, suffix := range []string{"", "-wal", "-shm"} {
		if err := copyFile(source+suffix, destination+suffix); err != nil {
			if suffix != "" && os.IsNotExist(err) {
				continue
			}
			cleanup()
			return "", func() {}, err
		}
	}
	return destination, cleanup, nil
}

func copyFile(source string, destination string) error {
	input, err := os.Open(source)
	if err != nil {
		return err
	}
	defer input.Close()
	output, err := os.OpenFile(destination, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return err
	}
	_, copyErr := io.Copy(output, input)
	closeErr := output.Close()
	if copyErr != nil {
		return copyErr
	}
	return closeErr
}
