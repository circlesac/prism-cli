package cli

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/url"
	"os"
	"strings"
	"time"

	credentials "github.com/circlesac/credentials-go"
	"github.com/circlesac/prism-cli/internal/chatgpt"
	"github.com/circlesac/prism-cli/internal/copilot"
	"github.com/circlesac/prism-cli/internal/gemini"
	"github.com/circlesac/prism-cli/internal/secret"
	"github.com/circlesac/prism-cli/internal/vault"
)

type commonOptions struct {
	org               string
	profile           string
	profileSet        bool
	name              string
	providerAccountID string
	ownerID           string
	help              bool
}

func Run(
	ctx context.Context,
	args []string,
	stdout io.Writer,
	stderr io.Writer,
	version string,
) error {
	if len(args) == 0 || args[0] == "help" || args[0] == "--help" || args[0] == "-h" {
		printHelp(stdout)
		return nil
	}
	if args[0] == "version" || args[0] == "--version" {
		fmt.Fprintln(stdout, version)
		return nil
	}
	if len(args) < 3 || args[1] != "auth" {
		return errors.New("unknown command; run 'prism help'")
	}
	providerName := strings.ToLower(args[0])
	if !vault.SupportedProvider(providerName) {
		return fmt.Errorf("unsupported provider %q", providerName)
	}
	command := args[2]
	options, positionals, err := parseCommonOptions(args[3:])
	if err != nil {
		return err
	}
	if options.help {
		printProviderAuthHelp(stdout, providerName)
		return nil
	}
	if err := validateCommand(providerName, command, positionals, options); err != nil {
		return err
	}

	var credentialProvider *credentials.Provider
	if options.profileSet {
		credentialProvider, err = credentials.New(credentials.WithProfile(options.profile))
	} else {
		credentialProvider, err = credentials.New()
	}
	if err != nil {
		return err
	}
	circlesCredential, err := credentialProvider.Resolve(ctx)
	if err != nil {
		return err
	}
	selectedProfile := ""
	if circlesCredential.Source.Type == credentials.SourceProfile {
		profile, err := credentialProvider.GetProfile(ctx)
		if err != nil {
			return err
		}
		if _, err = vaultURLForProfile(profile); err != nil {
			return err
		}
		selectedProfile = circlesCredential.Source.Profile
	}
	bridge, err := vault.StartConnectBridge(ctx, selectedProfile, options.org, os.Stdin, stderr)
	if err != nil {
		return err
	}
	defer bridge.Close()
	vaultClient := vault.Client{BaseURL: bridge.Host, Token: bridge.Token}

	switch command {
	case "login":
		return loginProvider(ctx, providerName, vaultClient, stdout)
	case "add":
		accountID := positionals[0]
		bundle, err := readProviderCredential(providerName, options, os.Stdin, stderr)
		if err != nil {
			return err
		}
		alias := options.name
		if alias == "" {
			alias = accountID
		}
		bundle["alias"] = alias
		if err := vaultClient.UpsertProvider(ctx, providerName, accountID, alias, bundle); err != nil {
			return err
		}
		fmt.Fprintf(stdout, "Saved %s account %s.\n", providerName, accountID)
	case "list":
		accounts, err := vaultClient.ListProvider(ctx, providerName)
		if err != nil {
			return err
		}
		if len(accounts) == 0 {
			fmt.Fprintf(stdout, "No %s accounts are registered in this Vault namespace.\n", providerName)
			return nil
		}
		fmt.Fprintln(stdout, "ACCOUNT ID\tNAME")
		for _, account := range accounts {
			fmt.Fprintf(stdout, "%s\t%s\n", account.ID, account.Alias)
		}
	case "remove":
		removed, err := vaultClient.RemoveProvider(ctx, providerName, positionals[0])
		if err != nil {
			return err
		}
		if !removed {
			return fmt.Errorf("%s account %q was not found", providerName, positionals[0])
		}
		fmt.Fprintf(stdout, "Removed %s account %s.\n", providerName, positionals[0])
	}
	return nil
}

func validateCommand(provider string, command string, positionals []string, options commonOptions) error {
	switch command {
	case "login":
		if provider != "chatgpt" && provider != "copilot" && provider != "gemini" {
			return fmt.Errorf("%s uses 'auth add', not 'auth login'", provider)
		}
		if len(positionals) != 0 {
			return fmt.Errorf("unexpected argument %q", positionals[0])
		}
		if options.name != "" || options.providerAccountID != "" || options.ownerID != "" {
			return errors.New("OAuth account identity is determined from the provider callback")
		}
	case "add":
		if provider == "chatgpt" || provider == "copilot" || provider == "gemini" {
			return fmt.Errorf("%s uses 'auth login', not 'auth add'", provider)
		}
		if len(positionals) != 1 {
			return fmt.Errorf("usage: prism %s auth add <account-id> [options]", provider)
		}
		if provider == "cloudflare" && options.providerAccountID == "" {
			return errors.New("cloudflare auth add requires --provider-account-id")
		}
	case "list":
		if len(positionals) != 0 {
			return fmt.Errorf("unexpected argument %q", positionals[0])
		}
	case "remove":
		if len(positionals) != 1 {
			return fmt.Errorf("usage: prism %s auth remove <account-id> [--org <slug>] [--profile <name>]", provider)
		}
	default:
		return errors.New("unknown provider auth command; use login/add, list, or remove")
	}
	return nil
}

func loginProvider(ctx context.Context, provider string, client vault.Client, output io.Writer) error {
	switch provider {
	case "chatgpt":
		fmt.Fprintln(output, "Opening a browser for ChatGPT login...")
		bundle, err := (chatgpt.OAuth{}).Login(ctx)
		if err != nil {
			return err
		}
		if err := client.UpsertChatGPT(ctx, bundle); err != nil {
			return err
		}
		fmt.Fprintf(output, "Saved ChatGPT account %s (%s).\n", bundle.Alias, bundle.AccountID)
	case "copilot":
		fmt.Fprintln(output, "Starting GitHub Copilot device login...")
		bundle, err := (copilot.OAuth{}).Login(ctx, output)
		if err != nil {
			return err
		}
		if err := client.UpsertProvider(ctx, "copilot", bundle.Username, bundle.Alias, bundle); err != nil {
			return err
		}
		fmt.Fprintf(output, "Saved Copilot account %s.\n", bundle.Username)
	case "gemini":
		fmt.Fprintln(output, "Opening a browser for Gemini Code Assist login...")
		bundle, err := (gemini.OAuth{}).Login(ctx)
		if err != nil {
			return err
		}
		if err := client.UpsertProvider(ctx, "gemini", bundle.ProjectID, bundle.Alias, bundle); err != nil {
			return err
		}
		fmt.Fprintf(output, "Saved Gemini account %s (%s).\n", bundle.Alias, bundle.ProjectID)
	}
	return nil
}

func readProviderCredential(
	provider string,
	options commonOptions,
	input io.Reader,
	output io.Writer,
) (map[string]any, error) {
	if provider == "gemini-app" {
		psid, err := secret.Read("__Secure-1PSID: ", input, output, true)
		if err != nil {
			return nil, err
		}
		psidts, err := secret.Read("__Secure-1PSIDTS: ", input, output, true)
		if err != nil {
			return nil, err
		}
		return map[string]any{
			"psid":            psid,
			"psidts":          psidts,
			"alias":           options.name,
			"last_rotated_at": time.Now().UnixMilli(),
		}, nil
	}
	apiKey, err := secret.Read("API key: ", input, output, true)
	if err != nil {
		return nil, err
	}
	bundle := map[string]any{"api_key": apiKey, "alias": options.name}
	if provider == "cloudflare" {
		bundle["account_id"] = options.providerAccountID
	}
	if provider == "vercel" {
		bundle["owner_id"] = options.ownerID
		sessionCookie, err := secret.Read("Session cookie (optional): ", input, output, false)
		if err != nil {
			return nil, err
		}
		if sessionCookie != "" {
			bundle["session_cookie"] = sessionCookie
		}
	}
	return bundle, nil
}

func vaultURLForProfile(profile *credentials.StoredProfile) (string, error) {
	if profile == nil {
		return "https://vault.circles.ac", nil
	}
	stage := ""
	for _, endpoint := range []string{profile.Config.APIURL, profile.Config.AuthURL} {
		if endpoint == "" {
			continue
		}
		parsed, err := url.Parse(endpoint)
		if err != nil {
			return "", fmt.Errorf("profile %q has an invalid Circles endpoint", profile.Name)
		}
		detected := ""
		switch strings.ToLower(parsed.Hostname()) {
		case "api.circles.ac", "auth.circles.ac":
			detected = "production"
		case "api-dev.circles.ac", "auth-dev.circles.ac":
			detected = "development"
		default:
			return "", fmt.Errorf("profile %q uses an unsupported Circles endpoint", profile.Name)
		}
		if stage != "" && stage != detected {
			return "", fmt.Errorf("profile %q mixes production and development Circles endpoints", profile.Name)
		}
		stage = detected
	}
	if stage == "development" {
		return "https://vault.crcl.es", nil
	}
	return "https://vault.circles.ac", nil
}

func parseCommonOptions(args []string) (commonOptions, []string, error) {
	var options commonOptions
	var positionals []string
	for index := 0; index < len(args); index++ {
		argument := args[index]
		switch {
		case argument == "--help" || argument == "-h":
			options.help = true
		case argument == "--org":
			index++
			if index >= len(args) || args[index] == "" {
				return commonOptions{}, nil, errors.New("--org requires a value")
			}
			options.org = args[index]
		case strings.HasPrefix(argument, "--org="):
			options.org = strings.TrimPrefix(argument, "--org=")
			if options.org == "" {
				return commonOptions{}, nil, errors.New("--org requires a value")
			}
		case argument == "--profile":
			index++
			if index >= len(args) || args[index] == "" {
				return commonOptions{}, nil, errors.New("--profile requires a value")
			}
			options.profile = args[index]
			options.profileSet = true
		case strings.HasPrefix(argument, "--profile="):
			options.profile = strings.TrimPrefix(argument, "--profile=")
			if options.profile == "" {
				return commonOptions{}, nil, errors.New("--profile requires a value")
			}
			options.profileSet = true
		case argument == "--name":
			index++
			if index >= len(args) || args[index] == "" {
				return commonOptions{}, nil, errors.New("--name requires a value")
			}
			options.name = args[index]
		case strings.HasPrefix(argument, "--name="):
			options.name = strings.TrimPrefix(argument, "--name=")
		case argument == "--provider-account-id":
			index++
			if index >= len(args) || args[index] == "" {
				return commonOptions{}, nil, errors.New("--provider-account-id requires a value")
			}
			options.providerAccountID = args[index]
		case strings.HasPrefix(argument, "--provider-account-id="):
			options.providerAccountID = strings.TrimPrefix(argument, "--provider-account-id=")
		case argument == "--owner-id":
			index++
			if index >= len(args) || args[index] == "" {
				return commonOptions{}, nil, errors.New("--owner-id requires a value")
			}
			options.ownerID = args[index]
		case strings.HasPrefix(argument, "--owner-id="):
			options.ownerID = strings.TrimPrefix(argument, "--owner-id=")
		case strings.HasPrefix(argument, "-"):
			return commonOptions{}, nil, fmt.Errorf("unknown option %q", argument)
		default:
			positionals = append(positionals, argument)
		}
	}
	return options, positionals, nil
}

func printHelp(output io.Writer) {
	fmt.Fprintln(output, `Prism provider credential manager

Usage:
  prism chatgpt auth login [--org <slug>] [--profile <name>]
  prism copilot auth login [--org <slug>] [--profile <name>]
  prism gemini auth login [--org <slug>] [--profile <name>]
  prism <provider> auth add <account-id> [--name <name>] [provider options]
  prism <provider> auth list [--org <slug>] [--profile <name>]
  prism <provider> auth remove <account-id> [--org <slug>] [--profile <name>]
  prism version

Static providers: gemini-ai, groq, mistral, deepseek, opencode-go, cloudflare,
vercel, and gemini-app. Secret values are read from hidden stdin and are never
accepted as command-line options. Cloudflare requires --provider-account-id;
Vercel accepts --owner-id and prompts separately for an optional session cookie.

Personal Vault is the default. Use --org only for organization credentials.
Circles authentication comes from ~/.crcl or CIRCLES_AUTH_TOKEN.`)
}

func printProviderAuthHelp(output io.Writer, provider string) {
	if provider == "chatgpt" || provider == "copilot" || provider == "gemini" {
		fmt.Fprintf(output, `Usage:
  prism %s auth login [--org <slug>] [--profile <name>]
  prism %s auth list [--org <slug>] [--profile <name>]
  prism %s auth remove <account-id> [--org <slug>] [--profile <name>]
`, provider, provider, provider)
		return
	}
	fmt.Fprintf(output, `Usage:
  prism %s auth add <account-id> [--name <name>] [--org <slug>] [--profile <name>]
  prism %s auth list [--org <slug>] [--profile <name>]
  prism %s auth remove <account-id> [--org <slug>] [--profile <name>]
`, provider, provider, provider)
}
