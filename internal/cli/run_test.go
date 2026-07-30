package cli

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	credentials "github.com/circlesac/credentials-go"
)

func TestHelpDocumentsOnlySupportedChatGPTCommands(t *testing.T) {
	var stdout bytes.Buffer
	if err := Run(context.Background(), []string{"help"}, &stdout, &bytes.Buffer{}, "test"); err != nil {
		t.Fatal(err)
	}
	output := stdout.String()
	for _, command := range []string{"auth login", "auth list", "auth remove"} {
		if !strings.Contains(output, command) {
			t.Fatalf("help did not contain %q", command)
		}
	}
	if strings.Contains(output, "set-default") {
		t.Fatal("help exposed the unsupported ChatGPT set-default command")
	}
}

func TestCommonOptionsMayAppearBeforeOrAfterAccountID(t *testing.T) {
	options, positionals, err := parseCommonOptions([]string{
		"--org", "circlesac",
		"account-123",
		"--profile=dev",
	})
	if err != nil {
		t.Fatal(err)
	}
	if options.org != "circlesac" ||
		options.profile != "dev" ||
		!options.profileSet ||
		len(positionals) != 1 ||
		positionals[0] != "account-123" {
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
	vaultURL, err := vaultURLForProfile(profile)
	if err != nil || vaultURL != "https://vault.crcl.es" {
		t.Fatalf("vault URL = %q, %v", vaultURL, err)
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

func TestVaultURLFollowsTheCirclesProfileStage(t *testing.T) {
	tests := []struct {
		name    string
		profile *credentials.StoredProfile
		want    string
		wantErr bool
	}{
		{name: "no profile", want: "https://vault.circles.ac"},
		{
			name: "production",
			profile: &credentials.StoredProfile{Name: "prod", Config: credentials.ProfileConfig{
				APIURL: "https://api.circles.ac", AuthURL: "https://auth.circles.ac",
			}},
			want: "https://vault.circles.ac",
		},
		{
			name: "development",
			profile: &credentials.StoredProfile{Name: "dev", Config: credentials.ProfileConfig{
				APIURL: "https://api-dev.circles.ac", AuthURL: "https://auth-dev.circles.ac",
			}},
			want: "https://vault.crcl.es",
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
			got, err := vaultURLForProfile(test.profile)
			if test.wantErr {
				if err == nil {
					t.Fatalf("vaultURLForProfile() = %q, want error", got)
				}
				return
			}
			if err != nil || got != test.want {
				t.Fatalf("vaultURLForProfile() = %q, %v; want %q", got, err, test.want)
			}
		})
	}
}
