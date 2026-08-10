package cli

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	credentials "github.com/circlesac/credentials/go"
	"github.com/circlesac/prism-cli/internal/api"
)

func TestHelpDocumentsSupportedCommandsWithoutInternalDetails(t *testing.T) {
	var stdout bytes.Buffer
	if err := Run(context.Background(), []string{"help"}, &stdout, &bytes.Buffer{}, "test"); err != nil {
		t.Fatal(err)
	}
	output := stdout.String()
	for _, command := range []string{"prism claude", "prism codex", "chatgpt usage", "opencode-go usage", "auth login", "auth list", "auth remove"} {
		if !strings.Contains(output, command) {
			t.Fatalf("help did not contain %q", command)
		}
	}
	if strings.Contains(output, "set-default") {
		t.Fatal("help exposed the unsupported ChatGPT set-default command")
	}
	for _, internalDetail := range []string{"Vault", "CIRCLES_AUTH_TOKEN", "prism-dev", "E2EE", "application-plaintext"} {
		if strings.Contains(output, internalDetail) {
			t.Fatalf("help exposed internal detail %q", internalDetail)
		}
	}
}

func TestOpenCodeGoUsageUsesTheLocalBrowserWithoutCirclesCredentials(t *testing.T) {
	original := fetchOpenCodeGoUsage
	defer func() { fetchOpenCodeGoUsage = original }()
	called := false
	plan := "Go"
	fetchOpenCodeGoUsage = func(context.Context) (api.ProviderUsage, error) {
		called = true
		return api.ProviderUsage{Provider: "opencode-go", Accounts: []api.UsageAccount{{
			ID: "wrk_EXAMPLE", Name: "OpenCode workspace", Plan: &plan,
			Limits: []api.UsageLimit{{Name: "rolling", Window: "5h", UsedPercent: 2, RemainingPercent: 98}},
		}}}, nil
	}
	t.Setenv("HOME", t.TempDir())
	t.Setenv("CIRCLES_AUTH_TOKEN", "")
	var output bytes.Buffer
	if err := Run(context.Background(), []string{"opencode-go", "usage"}, &output, &bytes.Buffer{}, "test"); err != nil {
		t.Fatal(err)
	}
	if !called || !strings.Contains(output.String(), "OpenCode workspace") || !strings.Contains(output.String(), "rolling") {
		t.Fatalf("called = %v, output = %q", called, output.String())
	}
}

func TestChatGPTUsageOutputShowsEveryLimitAndPartialErrors(t *testing.T) {
	plan := "pro"
	reset := "2026-08-11T00:24:55.000Z"
	windowSeconds := 7 * 24 * 60 * 60
	usage := api.ProviderUsage{Provider: "chatgpt", Accounts: []api.UsageAccount{
		{
			Name: "person@example.com",
			Plan: &plan,
			Limits: []api.UsageLimit{
				{
					Name: "default", Window: "primary", UsedPercent: 88,
					RemainingPercent: 12, ResetAt: &reset, WindowSeconds: &windowSeconds,
				},
				{
					Name: "GPT-5.3-Codex-Spark", Window: "primary", UsedPercent: 0,
					RemainingPercent: 100,
				},
			},
		},
		{
			Name:  "other@example.com",
			Error: &api.UsageError{Code: "usage_unavailable", Message: "ChatGPT usage is unavailable"},
		},
	}}
	var output bytes.Buffer
	printUsageAt(&output, usage, time.Date(2026, 8, 7, 9, 24, 55, 0, time.FixedZone("KST", 9*60*60)))
	want := `┌────────────────────┬──────┬─────────────────────┬─────────┬──────┬───────────┬─────────────────────────────────────┬────────────────────┐
│ NAME               │ PLAN │ LIMIT               │ WINDOW  │ USED │ REMAINING │ RESET                               │ PACE               │
├────────────────────┼──────┼─────────────────────┼─────────┼──────┼───────────┼─────────────────────────────────────┼────────────────────┤
│ person@example.com │ pro  │ default             │ primary │  88% │       12% │ 2026-08-11 09:24 KST (in 4d)        │ RUNS OUT in 9h 49m │
│                    │      │ GPT-5.3-Codex-Spark │ primary │   0% │      100% │ -                                   │ -                  │
│ other@example.com  │ -    │ -                   │ -       │    - │         - │ ERROR: ChatGPT usage is unavailable │ -                  │
└────────────────────┴──────┴─────────────────────┴─────────┴──────┴───────────┴─────────────────────────────────────┴────────────────────┘
`
	if output.String() != want {
		t.Fatalf("output =\n%s\nwant:\n%s", output.String(), want)
	}
}

func TestUsagePaceProjectsQuotaAtReset(t *testing.T) {
	now := time.Date(2026, 8, 7, 12, 0, 0, 0, time.UTC)
	windowSeconds := 7 * 24 * 60 * 60
	resetAt := func(elapsedFraction float64) *string {
		value := now.Add(time.Duration(float64(time.Duration(windowSeconds)*time.Second) * (1 - elapsedFraction))).Format(time.RFC3339)
		return &value
	}
	window := func() *int {
		value := windowSeconds
		return &value
	}

	tests := []struct {
		name  string
		limit api.UsageLimit
		want  string
	}{
		{
			name:  "healthy",
			limit: api.UsageLimit{UsedPercent: 30, ResetAt: resetAt(0.5), WindowSeconds: window()},
			want:  "OK · ~40% left at reset",
		},
		{
			name:  "close",
			limit: api.UsageLimit{UsedPercent: 46, ResetAt: resetAt(0.5), WindowSeconds: window()},
			want:  "CLOSE · ~8% spare at reset",
		},
		{
			name:  "running out",
			limit: api.UsageLimit{UsedPercent: 60, ResetAt: resetAt(0.5), WindowSeconds: window()},
			want:  "RUNS OUT in 2d 8h",
		},
		{
			name:  "limit reached",
			limit: api.UsageLimit{UsedPercent: 100, LimitReached: true, ResetAt: resetAt(0.5), WindowSeconds: window()},
			want:  "LIMIT REACHED",
		},
		{
			name:  "too early",
			limit: api.UsageLimit{UsedPercent: 1, ResetAt: resetAt(0.005), WindowSeconds: window()},
			want:  "TOO EARLY",
		},
		{
			name:  "missing duration",
			limit: api.UsageLimit{UsedPercent: 30, ResetAt: resetAt(0.5)},
			want:  "-",
		},
		{
			name:  "exactly at limit",
			limit: api.UsageLimit{UsedPercent: 50, ResetAt: resetAt(0.5), WindowSeconds: window()},
			want:  "RUNS OUT at reset",
		},
		{
			name:  "unused",
			limit: api.UsageLimit{UsedPercent: 0, ResetAt: resetAt(0.5), WindowSeconds: window()},
			want:  "OK · ~100% left at reset",
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := usagePace(test.limit, now); got != test.want {
				t.Fatalf("usagePace() = %q, want %q", got, test.want)
			}
		})
	}
}

func TestUsageResetFormattingUsesLocalTimeAndFriendlyRemainingTime(t *testing.T) {
	location := time.FixedZone("KST", 9*60*60)
	now := time.Date(2026, 8, 7, 12, 0, 0, 0, location)
	for _, test := range []struct {
		name  string
		reset time.Time
		want  string
	}{
		{name: "minutes", reset: now.Add(42*time.Minute + 30*time.Second), want: "2026-08-07 12:42 KST (in 42m)"},
		{name: "hours", reset: now.Add(time.Hour + 37*time.Minute), want: "2026-08-07 13:37 KST (in 1h 37m)"},
		{name: "days", reset: now.Add(2*24*time.Hour + 5*time.Hour), want: "2026-08-09 17:00 KST (in 2d 5h)"},
		{name: "past", reset: now.Add(-3 * time.Minute), want: "2026-08-07 11:57 KST (3m ago)"},
	} {
		t.Run(test.name, func(t *testing.T) {
			got := formatUsageReset(test.reset.UTC().Format(time.RFC3339), now)
			if got != test.want {
				t.Fatalf("formatUsageReset() = %q, want %q", got, test.want)
			}
		})
	}
	if got := formatUsageReset("not-a-time", now); got != "not-a-time" {
		t.Fatalf("invalid reset = %q", got)
	}
}

func TestCommonOptionsMayAppearBeforeOrAfterCredentialID(t *testing.T) {
	options, positionals, err := parseCommonOptions([]string{
		"01j00000000000000000000002",
		"--profile=dev",
	})
	if err != nil {
		t.Fatal(err)
	}
	if options.profile != "dev" ||
		!options.profileSet ||
		len(positionals) != 1 ||
		positionals[0] != "01j00000000000000000000002" {
		t.Fatalf("options = %#v, positionals = %#v", options, positionals)
	}
}

func TestPublicCredentialProviderSupportsHeadlessUseWithoutDiskWrites(t *testing.T) {
	home := t.TempDir()
	secret := "headless-circles-secret"
	provider, err := credentials.New(
		credentials.WithHomeDir(home),
		credentials.WithEnvironment(map[string]string{"CIRCLES_AUTH_TOKEN": secret}),
	)
	if err != nil {
		t.Fatal(err)
	}
	credential, err := provider.Resolve(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if credential.Value != secret ||
		credential.Kind != credentials.KindAPIKey ||
		credential.Source != (credentials.Source{Type: credentials.SourceEnvironment}) {
		t.Fatalf("credential kind/source = %s/%#v", credential.Kind, credential.Source)
	}
	if _, err := os.Stat(filepath.Join(home, ".crcl")); !os.IsNotExist(err) {
		t.Fatalf("environment credential wrote to disk: %v", err)
	}
}

func TestPublicCredentialProviderUsesTheSharedCurrentProfile(t *testing.T) {
	home := t.TempDir()
	credentialDirectory := filepath.Join(home, ".crcl")
	if err := os.MkdirAll(credentialDirectory, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(
		filepath.Join(credentialDirectory, "config"),
		[]byte("[__circles__]\ncurrent_profile = dev:person@example.com\n\n[dev:person@example.com]\napi_url = https://api-dev.circles.ac\nauth_url = https://auth-dev.circles.ac\n"),
		0o600,
	); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(
		filepath.Join(credentialDirectory, "credentials"),
		[]byte("[default]\napi_key = default-key\n\n[dev:person@example.com]\napi_key = profile-key\n"),
		0o600,
	); err != nil {
		t.Fatal(err)
	}
	provider, err := credentials.New(
		credentials.WithHomeDir(home),
		credentials.WithEnvironment(map[string]string{}),
	)
	if err != nil {
		t.Fatal(err)
	}
	credential, err := provider.Resolve(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if credential.Value != "profile-key" || credential.Source != (credentials.Source{Type: credentials.SourceProfile, Profile: "dev:person@example.com"}) {
		t.Fatalf("credential = %#v", credential)
	}
	profile, err := provider.GetProfile(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	prismURL, err := prismURLForProfile(profile)
	if err != nil || prismURL != "https://prism-dev.circles.ac" {
		t.Fatalf("Prism URL = %q, %v", prismURL, err)
	}
}

func TestCredentialValueIsNotAcceptedAsCommandLineOption(t *testing.T) {
	secret := "command-line-secret"
	err := Run(
		context.Background(),
		[]string{"chatgpt", "auth", "list", "--token", secret},
		&bytes.Buffer{},
		&bytes.Buffer{},
		"test",
	)
	if err == nil {
		t.Fatal("credential command-line option was accepted")
	}
	if strings.Contains(err.Error(), secret) {
		t.Fatal("rejected credential value was echoed in the error")
	}
}

func TestPrismURLFollowsTheCirclesProfileStage(t *testing.T) {
	tests := []struct {
		name    string
		profile *credentials.StoredProfile
		want    string
		wantErr bool
	}{
		{name: "no profile", want: "https://prism.circles.ac"},
		{
			name: "production",
			profile: &credentials.StoredProfile{Name: "prod", Config: credentials.ProfileConfig{
				APIURL: "https://api.circles.ac", AuthURL: "https://auth.circles.ac",
			}},
			want: "https://prism.circles.ac",
		},
		{
			name: "development",
			profile: &credentials.StoredProfile{Name: "dev", Config: credentials.ProfileConfig{
				APIURL: "https://api-dev.circles.ac", AuthURL: "https://auth-dev.circles.ac",
			}},
			want: "https://prism-dev.circles.ac",
		},
		{
			name: "mixed stages",
			profile: &credentials.StoredProfile{Name: "mixed", Config: credentials.ProfileConfig{
				APIURL: "https://api.circles.ac", AuthURL: "https://auth-dev.circles.ac",
			}},
			wantErr: true,
		},
		{
			name: "unknown endpoint",
			profile: &credentials.StoredProfile{Name: "custom", Config: credentials.ProfileConfig{
				APIURL: "https://api.example.com",
			}},
			wantErr: true,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got, err := prismURLForProfile(test.profile)
			if test.wantErr {
				if err == nil {
					t.Fatalf("prismURLForProfile() = %q, want error", got)
				}
				return
			}
			if err != nil || got != test.want {
				t.Fatalf("prismURLForProfile() = %q, %v; want %q", got, err, test.want)
			}
		})
	}
}

func TestStaticProviderSecretsComeFromInputNotOptions(t *testing.T) {
	options := commonOptions{
		name:              "work",
		providerAccountID: "cf-account-123",
	}
	bundle, err := readProviderCredential(
		"cloudflare",
		options,
		bytes.NewBufferString("cloudflare-secret\n"),
		&bytes.Buffer{},
	)
	if err != nil {
		t.Fatal(err)
	}
	if bundle["api_key"] != "cloudflare-secret" || bundle["account_id"] != "cf-account-123" {
		t.Fatalf("bundle = %#v", bundle)
	}
	if err := validateCommand("cloudflare", "add", nil, commonOptions{}); err == nil {
		t.Fatal("Cloudflare account ID option was not required")
	}
}

func TestGeminiAppReadsBothCookiesFromSeparateLines(t *testing.T) {
	bundle, err := readProviderCredential(
		"gemini-app",
		commonOptions{name: "personal"},
		bytes.NewBufferString("psid-secret\npsidts-secret\n"),
		&bytes.Buffer{},
	)
	if err != nil {
		t.Fatal(err)
	}
	if bundle["psid"] != "psid-secret" || bundle["psidts"] != "psidts-secret" {
		t.Fatalf("bundle = %#v", bundle)
	}
	if lastRotatedAt, ok := bundle["last_rotated_at"].(int64); !ok || lastRotatedAt <= 0 {
		t.Fatalf("last_rotated_at = %#v", bundle["last_rotated_at"])
	}
}

func TestOAuthLoginRejectsCallerChosenAccountIdentity(t *testing.T) {
	err := validateCommand("chatgpt", "login", nil, commonOptions{name: "chosen"})
	if err == nil || !strings.Contains(err.Error(), "provider callback") {
		t.Fatalf("error = %v", err)
	}
}
