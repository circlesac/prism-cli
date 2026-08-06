package cli

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	credentials "github.com/circlesac/credentials-go"
	"github.com/circlesac/prism-cli/internal/api"
)

func TestHelpDocumentsOnlySupportedChatGPTCommands(t *testing.T) {
	var stdout bytes.Buffer
	if err := Run(context.Background(), []string{"help"}, &stdout, &bytes.Buffer{}, "test"); err != nil {
		t.Fatal(err)
	}
	output := stdout.String()
	for _, command := range []string{"chatgpt usage", "auth login", "auth list", "auth remove"} {
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

func TestChatGPTUsageOutputShowsEveryLimitAndPartialErrors(t *testing.T) {
	plan := "pro"
	reset := "2026-08-11T00:24:55.000Z"
	usage := api.ProviderUsage{Provider: "chatgpt", Accounts: []api.UsageAccount{
		{
			Name: "person@example.com",
			Plan: &plan,
			Limits: []api.UsageLimit{
				{
					Name: "default", Window: "primary", UsedPercent: 88,
					RemainingPercent: 12, ResetAt: &reset,
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
	printUsage(&output, usage)
	want := `┌────────────────────┬──────┬─────────────────────┬─────────┬──────┬───────────┬─────────────────────────────────────┐
│ NAME               │ PLAN │ LIMIT               │ WINDOW  │ USED │ REMAINING │ RESET                               │
├────────────────────┼──────┼─────────────────────┼─────────┼──────┼───────────┼─────────────────────────────────────┤
│ person@example.com │ pro  │ default             │ primary │  88% │       12% │ 2026-08-11 00:24 UTC                │
│                    │      │ GPT-5.3-Codex-Spark │ primary │   0% │      100% │ -                                   │
│ other@example.com  │ -    │ -                   │ -       │    - │         - │ ERROR: ChatGPT usage is unavailable │
└────────────────────┴──────┴─────────────────────┴─────────┴──────┴───────────┴─────────────────────────────────────┘
`
	if output.String() != want {
		t.Fatalf("output =\n%s\nwant:\n%s", output.String(), want)
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
