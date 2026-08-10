package opencodego

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestParseUsageSupportsQuotedUnquotedAndSolidReferences(t *testing.T) {
	now := time.Date(2026, 8, 10, 0, 0, 0, 0, time.UTC)
	document := `<script>
$R[12]={"usagePercent":6,"status":"active","resetInSec":300};
const usage={rollingUsage:{resetInSec:60,usagePercent:2,status:"active"},
"weeklyUsage":{"status":"active","usagePercent":"4","resetInSec":"120"},
monthlyUsage:$R[12]};
</script>`
	limits, err := parseUsage(document, now)
	if err != nil {
		t.Fatal(err)
	}
	if len(limits) != 3 {
		t.Fatalf("limits = %#v", limits)
	}
	wants := []struct {
		name       string
		window     string
		used       float64
		reset      string
		windowSecs int
	}{
		{name: "rolling", window: "5h", used: 2, reset: "2026-08-10T00:01:00Z", windowSecs: 18_000},
		{name: "weekly", window: "7d", used: 4, reset: "2026-08-10T00:02:00Z", windowSecs: 604_800},
		{name: "monthly", window: "30d", used: 6, reset: "2026-08-10T00:05:00Z", windowSecs: 2_592_000},
	}
	for index, want := range wants {
		got := limits[index]
		if got.Name != want.name || got.Window != want.window || got.UsedPercent != want.used ||
			got.RemainingPercent != 100-want.used || got.ResetAt == nil || *got.ResetAt != want.reset ||
			got.WindowSeconds == nil || *got.WindowSeconds != want.windowSecs {
			t.Fatalf("limit %d = %#v, want %#v", index, got, want)
		}
	}
}

func TestParseUsageRejectsEachMissingWindow(t *testing.T) {
	window := func(name string) string {
		return name + `:{status:"active",resetInSec:60,usagePercent:2}`
	}
	for _, missing := range []string{"rollingUsage", "weeklyUsage", "monthlyUsage"} {
		t.Run(missing, func(t *testing.T) {
			var parts []string
			for _, name := range []string{"rollingUsage", "weeklyUsage", "monthlyUsage"} {
				if name != missing {
					parts = append(parts, window(name))
				}
			}
			if _, err := parseUsage("{"+strings.Join(parts, ",")+"}", time.Now()); err == nil || !strings.Contains(err.Error(), missing) {
				t.Fatalf("error = %v", err)
			}
		})
	}
}

func TestExtractWorkspaceIDsDeduplicatesAndSorts(t *testing.T) {
	got := extractWorkspaceIDs(`href="/workspace/wrk_EXAMPLE_TWO/go" wrk_EXAMPLE_ONE wrk_EXAMPLE_TWO`)
	want := []string{"wrk_EXAMPLE_ONE", "wrk_EXAMPLE_TWO"}
	if len(got) != len(want) || got[0] != want[0] || got[1] != want[1] {
		t.Fatalf("workspace IDs = %#v", got)
	}
}

func TestFetchFromSessionsDiscoversAndDeduplicatesWorkspaces(t *testing.T) {
	var zenRequests int
	server := httptest.NewServer(http.HandlerFunc(func(output http.ResponseWriter, request *http.Request) {
		if !strings.Contains(request.Header.Get("Cookie"), "auth=example-session") {
			http.Error(output, "unauthorized", http.StatusUnauthorized)
			return
		}
		switch request.URL.Path {
		case "/zen":
			zenRequests++
			_, _ = output.Write([]byte(`wrk_EXAMPLE wrk_EXAMPLE`))
		case "/workspace/wrk_EXAMPLE/go":
			_, _ = output.Write([]byte(`{
rollingUsage:{status:"active",resetInSec:60,usagePercent:1},
weeklyUsage:{status:"active",resetInSec:120,usagePercent:2},
monthlyUsage:{status:"active",resetInSec:180,usagePercent:3}}
`))
		default:
			http.NotFound(output, request)
		}
	}))
	defer server.Close()

	now := time.Date(2026, 8, 10, 0, 0, 0, 0, time.UTC)
	usage, err := fetchFromSessions(context.Background(), server.Client(), server.URL, now, []browserSession{
		{label: "Chrome Default", cookies: []browserCookie{{name: "auth", value: "example-session"}}},
		{label: "Firefox example-user", cookies: []browserCookie{{name: "auth", value: "example-session"}}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if zenRequests != 2 || len(usage.Accounts) != 1 || len(usage.Accounts[0].Limits) != 3 {
		t.Fatalf("zen requests = %d, usage = %#v", zenRequests, usage)
	}
	if usage.Provider != "opencode-go" || usage.Accounts[0].ID != "wrk_EXAMPLE" || usage.Accounts[0].Name != "-" {
		t.Fatalf("usage = %#v", usage)
	}
}

func TestFetchFromSessionsClassifiesRejectedLogin(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(output http.ResponseWriter, _ *http.Request) {
		http.Error(output, "unauthorized", http.StatusUnauthorized)
	}))
	defer server.Close()

	_, err := fetchFromSessions(context.Background(), server.Client(), server.URL, time.Now(), []browserSession{{
		label: "Chrome Default", cookies: []browserCookie{{name: "auth", value: "rejected-session"}},
	}})
	if !errors.Is(err, errOpenCodeSessionInvalid) {
		t.Fatalf("error = %v", err)
	}
}

func TestFetchSeparatesSessionDiscoveryFailures(t *testing.T) {
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	original := scanBrowserSessions
	defer func() { scanBrowserSessions = original }()
	for _, test := range []struct {
		name string
		scan sessionScan
		want string
	}{
		{name: "not found", scan: sessionScan{}, want: "was not found"},
		{name: "expired", scan: sessionScan{storesFound: 1, expiredCookies: 1}, want: "are expired"},
		{name: "invalid", scan: sessionScan{storesFound: 1, invalidCookies: 1}, want: "could not be decrypted"},
		{name: "unreadable", scan: sessionScan{storesFound: 1, unreadableStores: 1}, want: "could not be read"},
	} {
		t.Run(test.name, func(t *testing.T) {
			scanBrowserSessions = func(time.Time) sessionScan { return test.scan }
			_, err := Fetch(context.Background())
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("error = %v", err)
			}
		})
	}
}

func TestMalformedUsageBecomesAnAccountError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(output http.ResponseWriter, request *http.Request) {
		if request.URL.Path == "/zen" {
			_, _ = output.Write([]byte(`wrk_EXAMPLE`))
			return
		}
		_, _ = output.Write([]byte(`rollingUsage:{status:"active"}`))
	}))
	defer server.Close()
	usage, err := fetchFromSessions(context.Background(), server.Client(), server.URL, time.Now(), []browserSession{{
		label: "Chrome Default", cookies: []browserCookie{{name: "auth", value: "example-session"}},
	}})
	if err != nil {
		t.Fatal(err)
	}
	if len(usage.Accounts) != 1 || usage.Accounts[0].Error == nil || usage.Accounts[0].Error.Code != "usage_unavailable" {
		t.Fatalf("usage = %#v", usage)
	}
}
