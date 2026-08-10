package opencodego

import (
	"context"
	"errors"
	"fmt"
	"html"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/circlesac/prism-cli/internal/api"
)

const openCodeBaseURL = "https://opencode.ai"

type browserCookie struct {
	name  string
	value string
}

type browserSession struct {
	label   string
	cookies []browserCookie
}

type sessionScan struct {
	sessions         []browserSession
	storesFound      int
	expiredCookies   int
	invalidCookies   int
	unreadableStores int
	unsupported      bool
}

var scanBrowserSessions = browserSessions

// Fetch reads OpenCode's authenticated browser session without persisting it
// and returns the Go plan usage shown by opencode.ai.
func Fetch(ctx context.Context) (api.ProviderUsage, error) {
	now := time.Now()
	scan := scanBrowserSessions(now)
	if scan.unsupported {
		return api.ProviderUsage{}, errors.New("OpenCode browser session reading is currently supported on macOS")
	}
	if len(scan.sessions) == 0 {
		switch {
		case scan.unreadableStores > 0:
			return api.ProviderUsage{}, errors.New("OpenCode browser sessions could not be read")
		case scan.invalidCookies > 0:
			return api.ProviderUsage{}, errors.New("OpenCode browser login cookies could not be decrypted")
		case scan.expiredCookies > 0:
			return api.ProviderUsage{}, errors.New("OpenCode browser login sessions are expired")
		default:
			return api.ProviderUsage{}, errors.New("OpenCode login session was not found in a supported browser")
		}
	}

	return fetchFromSessions(ctx, &http.Client{Timeout: 30 * time.Second}, openCodeBaseURL, now, scan.sessions)
}

func fetchFromSessions(
	ctx context.Context,
	client *http.Client,
	baseURL string,
	now time.Time,
	sessions []browserSession,
) (api.ProviderUsage, error) {
	workspaceSessions := map[string][]browserSession{}
	requestSucceeded := false
	for _, session := range sessions {
		body, err := fetchPage(ctx, client, baseURL+"/zen", session)
		if err != nil {
			continue
		}
		requestSucceeded = true
		for _, workspaceID := range extractWorkspaceIDs(body) {
			workspaceSessions[workspaceID] = append(workspaceSessions[workspaceID], session)
		}
	}
	if len(workspaceSessions) == 0 {
		if !requestSucceeded {
			return api.ProviderUsage{}, errors.New("OpenCode could not be reached")
		}
		return api.ProviderUsage{}, errors.New("OpenCode browser login is not valid or no workspace is available")
	}

	workspaceIDs := make([]string, 0, len(workspaceSessions))
	for workspaceID := range workspaceSessions {
		workspaceIDs = append(workspaceIDs, workspaceID)
	}
	sort.Strings(workspaceIDs)

	accounts := make([]api.UsageAccount, 0, len(workspaceIDs))
	for index, workspaceID := range workspaceIDs {
		var lastErr error
		var limits []api.UsageLimit
		for _, session := range workspaceSessions[workspaceID] {
			body, err := fetchPage(
				ctx,
				client,
				baseURL+"/workspace/"+url.PathEscape(workspaceID)+"/go",
				session,
			)
			if err != nil {
				lastErr = err
				continue
			}
			limits, err = parseUsage(body, now)
			if err != nil {
				lastErr = err
				continue
			}
			lastErr = nil
			break
		}

		name := "-"
		if len(workspaceIDs) > 1 {
			name = fmt.Sprintf("%d", index+1)
		}
		plan := "Go"
		account := api.UsageAccount{
			ID:         workspaceID,
			Name:       name,
			Plan:       &plan,
			ObservedAt: now.UTC().Format(time.RFC3339),
			Limits:     limits,
		}
		if lastErr != nil {
			account.Error = &api.UsageError{
				Code:    "usage_unavailable",
				Message: "OpenCode returned malformed Go usage data",
			}
		}
		accounts = append(accounts, account)
	}

	return api.ProviderUsage{Provider: "opencode-go", Accounts: accounts}, nil
}

func fetchPage(ctx context.Context, client *http.Client, endpoint string, session browserSession) (string, error) {
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return "", err
	}
	parts := make([]string, 0, len(session.cookies))
	for _, cookie := range session.cookies {
		parts = append(parts, cookie.name+"="+cookie.value)
	}
	request.Header.Set("Cookie", strings.Join(parts, "; "))
	request.Header.Set("User-Agent", "prism-cli")

	response, err := client.Do(request)
	if err != nil {
		return "", err
	}
	defer response.Body.Close()
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		_, _ = io.Copy(io.Discard, response.Body)
		return "", fmt.Errorf("OpenCode returned HTTP %d", response.StatusCode)
	}
	body, err := io.ReadAll(io.LimitReader(response.Body, 4<<20))
	if err != nil {
		return "", err
	}
	return string(body), nil
}

var workspaceIDPattern = regexp.MustCompile(`\bwrk_[A-Za-z0-9_-]+\b`)

func extractWorkspaceIDs(document string) []string {
	seen := map[string]bool{}
	var result []string
	for _, workspaceID := range workspaceIDPattern.FindAllString(document, -1) {
		if !seen[workspaceID] {
			seen[workspaceID] = true
			result = append(result, workspaceID)
		}
	}
	sort.Strings(result)
	return result
}

func parseUsage(document string, now time.Time) ([]api.UsageLimit, error) {
	document = html.UnescapeString(document)
	windows := []struct {
		key           string
		name          string
		window        string
		windowSeconds int
	}{
		{key: "rollingUsage", name: "rolling", window: "5h", windowSeconds: 18_000},
		{key: "weeklyUsage", name: "weekly", window: "7d", windowSeconds: 604_800},
		{key: "monthlyUsage", name: "monthly", window: "30d", windowSeconds: 2_592_000},
	}

	limits := make([]api.UsageLimit, 0, len(windows))
	for _, usageWindow := range windows {
		object, err := usageObject(document, usageWindow.key)
		if err != nil {
			return nil, err
		}
		status, err := stringField(object, "status")
		if err != nil {
			return nil, fmt.Errorf("%s: %w", usageWindow.key, err)
		}
		resetInSeconds, err := numberField(object, "resetInSec")
		if err != nil {
			return nil, fmt.Errorf("%s: %w", usageWindow.key, err)
		}
		usedPercent, err := numberField(object, "usagePercent")
		if err != nil {
			return nil, fmt.Errorf("%s: %w", usageWindow.key, err)
		}
		usedPercent = min(100, max(0, usedPercent))
		resetAt := now.Add(time.Duration(resetInSeconds * float64(time.Second))).UTC().Format(time.RFC3339)
		windowSeconds := usageWindow.windowSeconds
		status = strings.ToLower(status)
		limits = append(limits, api.UsageLimit{
			Name:             usageWindow.name,
			Window:           usageWindow.window,
			UsedPercent:      usedPercent,
			RemainingPercent: 100 - usedPercent,
			LimitReached:     usedPercent >= 100 || status == "exhausted" || status == "limit_reached",
			ResetAt:          &resetAt,
			WindowSeconds:    &windowSeconds,
		})
	}
	return limits, nil
}

func usageObject(document string, key string) (string, error) {
	keyPattern := regexp.MustCompile(`(?s)[\\"']?` + regexp.QuoteMeta(key) + `[\\"']?\s*:\s*(\$R\[\d+\]|\{)`)
	match := keyPattern.FindStringSubmatchIndex(document)
	if match == nil {
		return "", fmt.Errorf("missing %s", key)
	}
	token := document[match[2]:match[3]]
	start := match[2]
	if strings.HasPrefix(token, "$R[") {
		assignment := regexp.MustCompile(regexp.QuoteMeta(token) + `\s*=\s*\(?\s*\{`).FindStringIndex(document)
		if assignment == nil {
			return "", fmt.Errorf("missing %s object", key)
		}
		start = assignment[1] - 1
	}
	return balancedObject(document, start)
}

func balancedObject(document string, start int) (string, error) {
	if start < 0 || start >= len(document) || document[start] != '{' {
		return "", errors.New("invalid usage object")
	}
	depth := 0
	quote := byte(0)
	escaped := false
	for index := start; index < len(document); index++ {
		character := document[index]
		if quote != 0 {
			if escaped {
				escaped = false
				continue
			}
			if character == '\\' {
				escaped = true
				continue
			}
			if character == quote {
				quote = 0
			}
			continue
		}
		switch character {
		case '\'', '"':
			quote = character
		case '{':
			depth++
		case '}':
			depth--
			if depth == 0 {
				return document[start : index+1], nil
			}
		}
	}
	return "", errors.New("unterminated usage object")
}

func numberField(object string, name string) (float64, error) {
	pattern := regexp.MustCompile(`[\\"']?` + regexp.QuoteMeta(name) + `[\\"']?\s*:\s*[\\"']?(-?[0-9]+(?:\.[0-9]+)?)[\\"']?`)
	match := pattern.FindStringSubmatch(object)
	if match == nil {
		return 0, fmt.Errorf("missing %s", name)
	}
	value, err := strconv.ParseFloat(match[1], 64)
	if err != nil {
		return 0, fmt.Errorf("invalid %s", name)
	}
	return value, nil
}

func stringField(object string, name string) (string, error) {
	pattern := regexp.MustCompile(`[\\"']?` + regexp.QuoteMeta(name) + `[\\"']?\s*:\s*[\\"']([^\\"']+)[\\"']`)
	match := pattern.FindStringSubmatch(object)
	if match == nil {
		return "", fmt.Errorf("missing %s", name)
	}
	return match[1], nil
}
