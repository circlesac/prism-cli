package cli

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/url"
	"os"
	"strings"

	credentials "github.com/circlesac/credentials-go"
	"github.com/circlesac/prism-cli/internal/chatgpt"
	"github.com/circlesac/prism-cli/internal/vault"
)

type commonOptions struct {
	org        string
	profile    string
	profileSet bool
	help       bool
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
	if len(args) < 3 || args[0] != "chatgpt" || args[1] != "auth" {
		return errors.New("unknown command; run 'prism help'")
	}

	command := args[2]
	options, positionals, err := parseCommonOptions(args[3:])
	if err != nil {
		return err
	}
	if options.help {
		printChatGPTAuthHelp(stdout)
		return nil
	}
	if command != "login" && command != "list" && command != "remove" {
		return errors.New("unknown ChatGPT auth command; use login, list, or remove")
	}
	if command == "remove" {
		if len(positionals) != 1 {
			return errors.New("usage: prism chatgpt auth remove <account-id> [--org <slug>] [--profile <name>]")
		}
	} else if len(positionals) != 0 {
		return fmt.Errorf("unexpected argument %q", positionals[0])
	}

	var provider *credentials.Provider
	if options.profileSet {
		provider, err = credentials.New(credentials.WithProfile(options.profile))
	} else {
		provider, err = credentials.New()
	}
	if err != nil {
		return err
	}
	credential, err := provider.Resolve(ctx)
	if err != nil {
		return err
	}
	selectedProfile := ""
	if credential.Source.Type == credentials.SourceProfile {
		profile, err := provider.GetProfile(ctx)
		if err != nil {
			return err
		}
		if _, err = vaultURLForProfile(profile); err != nil {
			return err
		}
		selectedProfile = credential.Source.Profile
	}
	bridge, err := vault.StartConnectBridge(ctx, selectedProfile, options.org, os.Stdin, stderr)
	if err != nil {
		return err
	}
	defer bridge.Close()
	vaultClient := vault.Client{
		BaseURL: bridge.Host,
		Token:   bridge.Token,
	}

	switch command {
	case "login":
		fmt.Fprintln(stdout, "Opening a browser for ChatGPT login...")
		bundle, err := (chatgpt.OAuth{}).Login(ctx)
		if err != nil {
			return err
		}
		fmt.Fprintln(stdout, "Saving the account directly to Circles Vault...")
		if err := vaultClient.UpsertChatGPT(ctx, bundle); err != nil {
			return err
		}
		if bundle.Alias != "" {
			fmt.Fprintf(stdout, "Saved ChatGPT account %s (%s).\n", bundle.Alias, bundle.AccountID)
		} else {
			fmt.Fprintf(stdout, "Saved ChatGPT account %s.\n", bundle.AccountID)
		}
	case "list":
		accounts, err := vaultClient.ListChatGPT(ctx)
		if err != nil {
			return err
		}
		if len(accounts) == 0 {
			fmt.Fprintln(stdout, "No ChatGPT accounts are registered in this Vault namespace.")
			return nil
		}
		fmt.Fprintln(stdout, "ACCOUNT ID\tNAME")
		for _, account := range accounts {
			fmt.Fprintf(stdout, "%s\t%s\n", account.ID, account.Alias)
		}
	case "remove":
		removed, err := vaultClient.RemoveChatGPT(ctx, positionals[0])
		if err != nil {
			return err
		}
		if !removed {
			return fmt.Errorf("ChatGPT account %q was not found", positionals[0])
		}
		fmt.Fprintf(stdout, "Removed ChatGPT account %s.\n", positionals[0])
	}
	_ = stderr
	return nil
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
  prism chatgpt auth list [--org <slug>] [--profile <name>]
  prism chatgpt auth remove <account-id> [--org <slug>] [--profile <name>]
  prism version

Personal Vault is the default. Use --org only when registering or managing an
organization's provider credentials. Prism resolves Circles authentication from
the shared ~/.crcl profile or CIRCLES_AUTH_TOKEN; credentials are never accepted
as command-line values.`)
}

func printChatGPTAuthHelp(output io.Writer) {
	fmt.Fprintln(output, `Usage:
  prism chatgpt auth login [--org <slug>] [--profile <name>]
  prism chatgpt auth list [--org <slug>] [--profile <name>]
  prism chatgpt auth remove <account-id> [--org <slug>] [--profile <name>]`)
}
