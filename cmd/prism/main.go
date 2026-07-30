package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"

	credentials "github.com/circlesac/credentials-go"
)

func main() {
	os.Exit(run(context.Background(), os.Args[1:], os.Stdout, os.Stderr))
}

func run(ctx context.Context, arguments []string, stdout, stderr io.Writer) int {
	profile, err := parseArguments(arguments)
	if err != nil {
		fmt.Fprintln(stderr, err)
		printUsage(stderr)
		return 2
	}

	options := []credentials.Option{}
	if profile != "" {
		options = append(options, credentials.WithProfile(profile))
	}
	provider, err := credentials.New(options...)
	if err != nil {
		printCredentialError(stderr, err)
		return 1
	}
	credential, err := provider.Resolve(ctx)
	if err != nil {
		printCredentialError(stderr, err)
		return 1
	}

	switch credential.Source.Type {
	case credentials.SourceProfile:
		fmt.Fprintf(stdout, "Authenticated with %s from profile %q.\n", credential.Kind, credential.Source.Profile)
	case credentials.SourceEnvironment:
		fmt.Fprintf(stdout, "Authenticated with %s from the environment.\n", credential.Kind)
	default:
		fmt.Fprintf(stdout, "Authenticated with %s from an explicit provider.\n", credential.Kind)
	}
	return 0
}

func parseArguments(arguments []string) (string, error) {
	if len(arguments) < 2 || arguments[0] != "auth" || arguments[1] != "status" {
		return "", errors.New("expected 'auth status'")
	}
	arguments = arguments[2:]
	profile := ""
	for len(arguments) > 0 {
		if arguments[0] != "--profile" || len(arguments) < 2 || arguments[1] == "" {
			return "", errors.New("unknown or incomplete option")
		}
		if profile != "" {
			return "", errors.New("--profile may be specified only once")
		}
		profile = arguments[1]
		arguments = arguments[2:]
	}
	return profile, nil
}

func printCredentialError(stderr io.Writer, err error) {
	var credentialFailure *credentials.Error
	if errors.As(err, &credentialFailure) {
		fmt.Fprintf(stderr, "Authentication failed (%s): %s\n", credentialFailure.Code, credentialFailure)
		return
	}
	fmt.Fprintln(stderr, "Authentication failed.")
}

func printUsage(output io.Writer) {
	fmt.Fprintln(output, "Usage: prism auth status [--profile NAME]")
}
