package cursor

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/circlesac/prism-cli/internal/api"
)

const cursorWebsiteURL = "https://cursor.com"

type usageSummary struct {
	BillingCycleStart any    `json:"billingCycleStart"`
	BillingCycleEnd   any    `json:"billingCycleEnd"`
	MembershipType    string `json:"membershipType"`
	IndividualUsage   struct {
		Plan map[string]any `json:"plan"`
	} `json:"individualUsage"`
}

type cursorIdentity struct {
	Email string `json:"email"`
	Sub   string `json:"sub"`
}

type UsageOptions struct {
	HTTPClient *http.Client
	BaseURL    string
	Token      string
	Now        time.Time
}

func FetchUsage(ctx context.Context, options UsageOptions) (api.ProviderUsage, error) {
	token := options.Token
	if token == "" {
		var err error
		token, err = readAccessToken(ctx)
		if err != nil {
			return api.ProviderUsage{}, err
		}
	}
	subject, userID, err := accessTokenIdentity(token)
	if err != nil {
		return api.ProviderUsage{}, errors.New("Cursor Agent login is unreadable; run 'prism cursor login' again")
	}
	baseURL := strings.TrimSuffix(options.BaseURL, "/")
	if baseURL == "" {
		baseURL = cursorWebsiteURL
	}
	client := options.HTTPClient
	if client == nil {
		client = &http.Client{Timeout: 15 * time.Second}
	}
	cookie := "WorkosCursorSessionToken=" + url.QueryEscape(userID+"::"+token)

	var summary usageSummary
	if err := fetchJSON(ctx, client, baseURL+"/api/usage-summary", cookie, &summary); err != nil {
		return api.ProviderUsage{}, err
	}
	identity := readLocalIdentity(subject)
	if identity.Email == "" {
		identityErr := fetchJSON(ctx, client, baseURL+"/api/auth/me", cookie, &identity)
		if identityErr != nil || identity.Sub != "" && identity.Sub != userID && identity.Sub != subject {
			identity = cursorIdentity{}
		}
	}
	now := options.Now
	if now.IsZero() {
		now = time.Now()
	}
	return parseUsageSummary(summary, identity, now)
}

func fetchJSON(ctx context.Context, client *http.Client, endpoint string, cookie string, output any) error {
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return errors.New("could not create the Cursor usage request")
	}
	request.Header.Set("Accept", "application/json")
	request.Header.Set("Cookie", cookie)
	response, err := client.Do(request)
	if err != nil {
		return errors.New("Cursor usage could not be reached")
	}
	defer response.Body.Close()
	if response.StatusCode == http.StatusUnauthorized || response.StatusCode == http.StatusForbidden {
		return errors.New("Cursor Agent login was rejected; run 'prism cursor login' again")
	}
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return fmt.Errorf("Cursor usage returned HTTP %d", response.StatusCode)
	}
	decoder := json.NewDecoder(io.LimitReader(response.Body, 1<<20))
	decoder.UseNumber()
	if err := decoder.Decode(output); err != nil {
		return errors.New("Cursor usage returned invalid JSON")
	}
	return nil
}

func parseUsageSummary(summary usageSummary, identity cursorIdentity, now time.Time) (api.ProviderUsage, error) {
	if summary.IndividualUsage.Plan == nil {
		return api.ProviderUsage{}, errors.New("Cursor usage did not include a personal plan")
	}
	resetAt, windowSeconds := usageWindow(summary.BillingCycleStart, summary.BillingCycleEnd, now)
	limits := make([]api.UsageLimit, 0, 2)
	for _, field := range []struct {
		key  string
		name string
	}{
		{key: "autoPercentUsed", name: "Cursor Models"},
		{key: "apiPercentUsed", name: "Other Models"},
	} {
		used, ok := number(summary.IndividualUsage.Plan[field.key])
		if !ok || math.IsNaN(used) || math.IsInf(used, 0) || used < 0 {
			return api.ProviderUsage{}, fmt.Errorf("Cursor usage did not include the %s quota", field.name)
		}
		limitReached := used >= 100
		used = min(100, used)
		limits = append(limits, api.UsageLimit{
			Name:             field.name,
			Window:           "monthly",
			UsedPercent:      used,
			RemainingPercent: 100 - used,
			LimitReached:     limitReached,
			ResetAt:          resetAt,
			WindowSeconds:    windowSeconds,
		})
	}
	name := strings.TrimSpace(identity.Email)
	if name == "" {
		name = "Cursor Agent"
	}
	var plan *string
	if value := strings.TrimSpace(summary.MembershipType); value != "" {
		plan = &value
	}
	return api.ProviderUsage{Provider: "cursor", Accounts: []api.UsageAccount{{
		ID:         identity.Sub,
		Name:       name,
		Plan:       plan,
		ObservedAt: now.UTC().Format(time.RFC3339),
		Status:     "active",
		Limits:     limits,
	}}}, nil
}

func usageWindow(startValue any, endValue any, now time.Time) (*string, *int) {
	end, endOK := timestamp(endValue)
	if !endOK {
		return nil, nil
	}
	reset := end.Format(time.RFC3339)
	start, startOK := timestamp(startValue)
	if !startOK || !end.After(start) {
		return &reset, nil
	}
	seconds := int(end.Sub(start).Seconds())
	if seconds <= 0 || seconds > 40*24*60*60 || end.Before(now.Add(-40*24*time.Hour)) {
		return &reset, nil
	}
	return &reset, &seconds
}

func timestamp(value any) (time.Time, bool) {
	switch typed := value.(type) {
	case string:
		if parsed, err := time.Parse(time.RFC3339, typed); err == nil {
			return parsed, true
		}
		if parsed, err := time.Parse("2006-01-02", typed); err == nil {
			return parsed, true
		}
		if numeric, err := strconv.ParseInt(typed, 10, 64); err == nil {
			return unixTimestamp(numeric)
		}
	case json.Number:
		if numeric, err := typed.Int64(); err == nil {
			return unixTimestamp(numeric)
		}
	case float64:
		return unixTimestamp(int64(typed))
	}
	return time.Time{}, false
}

func unixTimestamp(value int64) (time.Time, bool) {
	if value <= 0 {
		return time.Time{}, false
	}
	if value < 1_000_000_000_000 {
		return time.Unix(value, 0).UTC(), true
	}
	return time.UnixMilli(value).UTC(), true
}

func number(value any) (float64, bool) {
	switch typed := value.(type) {
	case json.Number:
		parsed, err := typed.Float64()
		return parsed, err == nil
	case float64:
		return typed, true
	case string:
		parsed, err := strconv.ParseFloat(typed, 64)
		return parsed, err == nil
	default:
		return 0, false
	}
}

func accessTokenIdentity(token string) (string, string, error) {
	parts := strings.Split(token, ".")
	if len(parts) != 3 {
		return "", "", errors.New("not a JWT")
	}
	payload, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return "", "", err
	}
	var claims struct {
		Subject string `json:"sub"`
	}
	if err := json.Unmarshal(payload, &claims); err != nil {
		return "", "", err
	}
	pieces := strings.Split(claims.Subject, "|")
	userID := strings.TrimSpace(pieces[len(pieces)-1])
	if userID == "" {
		return "", "", errors.New("JWT has no subject")
	}
	return claims.Subject, userID, nil
}

func readLocalIdentity(subject string) cursorIdentity {
	directory, err := configDirectory()
	if err != nil {
		return cursorIdentity{}
	}
	data, err := os.ReadFile(filepath.Join(directory, "cli-config.json"))
	if err != nil {
		return cursorIdentity{}
	}
	var config struct {
		AuthInfo struct {
			AuthID string `json:"authId"`
			Email  string `json:"email"`
		} `json:"authInfo"`
	}
	if json.Unmarshal(data, &config) != nil || config.AuthInfo.AuthID != subject {
		return cursorIdentity{}
	}
	return cursorIdentity{Email: strings.TrimSpace(config.AuthInfo.Email), Sub: subject}
}
