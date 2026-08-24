package cursor

import (
	"context"
	"encoding/base64"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestFetchUsageShowsSeparateCursorAndOtherModelPools(t *testing.T) {
	t.Setenv("CURSOR_CONFIG_DIR", t.TempDir())
	token := testToken(t, "auth0|user_example")
	server := httptest.NewServer(http.HandlerFunc(func(output http.ResponseWriter, request *http.Request) {
		if !strings.Contains(request.Header.Get("Cookie"), "user_example%3A%3A") {
			t.Errorf("cookie = %q", request.Header.Get("Cookie"))
		}
		switch request.URL.Path {
		case "/api/usage-summary":
			fmt.Fprint(output, `{
  "billingCycleStart":"2026-08-01T00:00:00Z",
  "billingCycleEnd":"2026-09-01T00:00:00Z",
  "membershipType":"pro_plus",
  "individualUsage":{"plan":{"autoPercentUsed":"12.5","apiPercentUsed":34}}
}`)
		case "/api/auth/me":
			fmt.Fprint(output, `{"sub":"user_example","email":"person@example.com"}`)
		default:
			http.NotFound(output, request)
		}
	}))
	defer server.Close()

	usage, err := FetchUsage(context.Background(), UsageOptions{
		HTTPClient: server.Client(), BaseURL: server.URL, Token: token,
		Now: time.Date(2026, 8, 24, 0, 0, 0, 0, time.UTC),
	})
	if err != nil {
		t.Fatal(err)
	}
	if usage.Provider != "cursor" || len(usage.Accounts) != 1 || len(usage.Accounts[0].Limits) != 2 {
		t.Fatalf("usage = %#v", usage)
	}
	account := usage.Accounts[0]
	if account.Name != "person@example.com" || account.Plan == nil || *account.Plan != "pro_plus" {
		t.Fatalf("account = %#v", account)
	}
	if account.Limits[0].Name != "Cursor Models" || account.Limits[0].UsedPercent != 12.5 || account.Limits[0].RemainingPercent != 87.5 {
		t.Fatalf("Cursor Models = %#v", account.Limits[0])
	}
	if account.Limits[1].Name != "Other Models" || account.Limits[1].UsedPercent != 34 || account.Limits[1].RemainingPercent != 66 {
		t.Fatalf("Other Models = %#v", account.Limits[1])
	}
	if account.Limits[0].ResetAt == nil || *account.Limits[0].ResetAt != "2026-09-01T00:00:00Z" || account.Limits[0].WindowSeconds == nil || *account.Limits[0].WindowSeconds != 31*24*60*60 {
		t.Fatalf("window = %#v", account.Limits[0])
	}
}

func TestFetchUsageRejectsAuthenticationWithoutReportingZero(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(output http.ResponseWriter, _ *http.Request) {
		http.Error(output, "unauthorized", http.StatusUnauthorized)
	}))
	defer server.Close()
	_, err := FetchUsage(context.Background(), UsageOptions{HTTPClient: server.Client(), BaseURL: server.URL, Token: testToken(t, "user_example")})
	if err == nil || !strings.Contains(err.Error(), "rejected") {
		t.Fatalf("error = %v", err)
	}
}

func TestFetchUsageRejectsMissingQuotaWithoutReportingZero(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(output http.ResponseWriter, request *http.Request) {
		if request.URL.Path == "/api/usage-summary" {
			fmt.Fprint(output, `{"individualUsage":{"plan":{}}}`)
			return
		}
		fmt.Fprint(output, `{}`)
	}))
	defer server.Close()
	_, err := FetchUsage(context.Background(), UsageOptions{HTTPClient: server.Client(), BaseURL: server.URL, Token: testToken(t, "user_example")})
	if err == nil || !strings.Contains(err.Error(), "did not include") {
		t.Fatalf("error = %v", err)
	}
}

func TestFetchUsageNamesTheAccountFromTheOfficialCursorConfig(t *testing.T) {
	directory := t.TempDir()
	t.Setenv("CURSOR_CONFIG_DIR", directory)
	server := httptest.NewServer(http.HandlerFunc(func(output http.ResponseWriter, request *http.Request) {
		if request.URL.Path != "/api/usage-summary" {
			http.Error(output, "identity is unavailable", http.StatusInternalServerError)
			return
		}
		fmt.Fprint(output, `{"individualUsage":{"plan":{"autoPercentUsed":1,"apiPercentUsed":2}}}`)
	}))
	defer server.Close()
	options := UsageOptions{HTTPClient: server.Client(), BaseURL: server.URL, Token: testToken(t, "auth0|user_example")}

	config := filepath.Join(directory, "cli-config.json")
	if err := os.WriteFile(config, []byte(`{"authInfo":{"authId":"auth0|user_example","email":"person@example.com"}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	usage, err := FetchUsage(context.Background(), options)
	if err != nil || usage.Accounts[0].Name != "person@example.com" {
		t.Fatalf("account = %#v, error = %v", usage.Accounts, err)
	}

	if err := os.WriteFile(config, []byte(`{"authInfo":{"authId":"auth0|other_user","email":"other@example.com"}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	usage, err = FetchUsage(context.Background(), options)
	if err != nil || usage.Accounts[0].Name != "Cursor Agent" {
		t.Fatalf("account = %#v, error = %v", usage.Accounts, err)
	}
}

func TestFetchUsageRejectsPartialQuotaWithoutReportingZero(t *testing.T) {
	t.Setenv("CURSOR_CONFIG_DIR", t.TempDir())
	for _, plan := range []string{`{"autoPercentUsed":12}`, `{"apiPercentUsed":12}`, `{"autoPercentUsed":12,"apiPercentUsed":-1}`, `{"autoPercentUsed":"+Inf","apiPercentUsed":12}`} {
		server := httptest.NewServer(http.HandlerFunc(func(output http.ResponseWriter, request *http.Request) {
			if request.URL.Path == "/api/usage-summary" {
				fmt.Fprintf(output, `{"individualUsage":{"plan":%s}}`, plan)
				return
			}
			fmt.Fprint(output, `{}`)
		}))
		_, err := FetchUsage(context.Background(), UsageOptions{
			HTTPClient: server.Client(), BaseURL: server.URL, Token: testToken(t, "user_example"),
		})
		server.Close()
		if err == nil || !strings.Contains(err.Error(), "did not include the ") {
			t.Fatalf("plan %s error = %v", plan, err)
		}
	}
}

func TestFetchUsageReportsOverLimitPoolWithoutSyntheticRemainder(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(output http.ResponseWriter, request *http.Request) {
		if request.URL.Path == "/api/usage-summary" {
			fmt.Fprint(output, `{"individualUsage":{"plan":{"autoPercentUsed":140,"apiPercentUsed":0}}}`)
			return
		}
		fmt.Fprint(output, `{}`)
	}))
	defer server.Close()
	usage, err := FetchUsage(context.Background(), UsageOptions{
		HTTPClient: server.Client(), BaseURL: server.URL, Token: testToken(t, "user_example"),
	})
	if err != nil {
		t.Fatal(err)
	}
	limits := usage.Accounts[0].Limits
	if limits[0].UsedPercent != 100 || limits[0].RemainingPercent != 0 || !limits[0].LimitReached {
		t.Fatalf("Cursor Models = %#v", limits[0])
	}
	if limits[1].UsedPercent != 0 || limits[1].RemainingPercent != 100 || limits[1].LimitReached {
		t.Fatalf("Other Models = %#v", limits[1])
	}
}

func testToken(t *testing.T, subject string) string {
	t.Helper()
	payload := base64.RawURLEncoding.EncodeToString([]byte(fmt.Sprintf(`{"sub":%q}`, subject)))
	return "header." + payload + ".signature"
}
