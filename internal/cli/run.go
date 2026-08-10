package cli

import (
	"context"
	"errors"
	"fmt"
	"io"
	"math"
	"net/url"
	"os"
	"strings"
	"time"
	"unicode/utf8"

	credentials "github.com/circlesac/credentials/go"
	"github.com/circlesac/prism-cli/internal/api"
	"github.com/circlesac/prism-cli/internal/chatgpt"
	"github.com/circlesac/prism-cli/internal/copilot"
	"github.com/circlesac/prism-cli/internal/gemini"
	"github.com/circlesac/prism-cli/internal/opencodego"
	"github.com/circlesac/prism-cli/internal/secret"
)

var fetchOpenCodeGoUsage = opencodego.Fetch
var fetchChatGPTUsage = func(ctx context.Context, options commonOptions) (api.ProviderUsage, error) {
	client, err := prismClient(ctx, options)
	if err != nil {
		return api.ProviderUsage{}, err
	}
	return client.Usage(ctx, "chatgpt")
}

type commonOptions struct {
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
	if args[0] == "usage" {
		return runCombinedUsage(ctx, args[1:], stdout)
	}
	if args[0] == "claude" {
		return runClaudeCommand(ctx, args[1:], stdout, stderr)
	}
	if args[0] == "codex" {
		return runCodexCommand(args[1:], stdout)
	}
	providerName := strings.ToLower(args[0])
	if !api.SupportedProvider(providerName) {
		return fmt.Errorf("unsupported provider %q", providerName)
	}
	if len(args) < 2 {
		return errors.New("unknown command; run 'prism help'")
	}
	command := args[1]
	commandArgs := args[2:]
	if command == "auth" {
		if len(args) < 3 {
			return errors.New("unknown provider auth command; use login/add, list, or remove")
		}
		command = args[2]
		commandArgs = args[3:]
	}
	options, positionals, err := parseCommonOptions(commandArgs)
	if err != nil {
		return err
	}
	if options.help {
		if command == "usage" {
			printProviderUsageHelp(stdout, providerName)
		} else {
			printProviderAuthHelp(stdout, providerName)
		}
		return nil
	}
	if err := validateCommand(providerName, command, positionals, options); err != nil {
		return err
	}
	if command == "usage" {
		var usage api.ProviderUsage
		if providerName == "opencode-go" {
			usage, err = fetchOpenCodeGoUsage(ctx)
		} else {
			usage, err = fetchChatGPTUsage(ctx, options)
		}
		if err != nil {
			return err
		}
		printUsage(stdout, usage)
		return nil
	}

	client, err := prismClient(ctx, options)
	if err != nil {
		return err
	}

	switch command {
	case "login":
		return loginProvider(ctx, providerName, client, stdout)
	case "add":
		bundle, err := readProviderCredential(providerName, options, os.Stdin, stderr)
		if err != nil {
			return err
		}
		saved, err := client.Save(ctx, providerName, options.name, bundle)
		if err != nil {
			return err
		}
		fmt.Fprintf(stdout, "Saved %s credential %s (%s).\n", providerName, saved.Name, saved.ID)
	case "list":
		accounts, err := client.List(ctx, providerName)
		if err != nil {
			return err
		}
		if len(accounts) == 0 {
			fmt.Fprintf(stdout, "No %s credentials are registered.\n", providerName)
			return nil
		}
		fmt.Fprintln(stdout, "ID\tNAME")
		for _, account := range accounts {
			fmt.Fprintf(stdout, "%s\t%s\n", account.ID, account.Name)
		}
	case "remove":
		if err := client.Remove(ctx, positionals[0]); err != nil {
			return err
		}
		fmt.Fprintf(stdout, "Removed %s credential %s.\n", providerName, positionals[0])
	}
	return nil
}

func runCombinedUsage(ctx context.Context, args []string, output io.Writer) error {
	options, positionals, err := parseCommonOptions(args)
	if err != nil {
		return err
	}
	if options.help {
		fmt.Fprintln(output, "Usage:\n  prism usage [--profile <name>]")
		return nil
	}
	if len(positionals) != 0 {
		return fmt.Errorf("unexpected argument %q", positionals[0])
	}
	if options.name != "" || options.providerAccountID != "" || options.ownerID != "" {
		return errors.New("usage accepts only --profile")
	}

	type usageResult struct {
		usage api.ProviderUsage
		err   error
	}
	chatGPTResults := make(chan usageResult, 1)
	openCodeResults := make(chan usageResult, 1)
	go func() {
		usage, fetchErr := fetchChatGPTUsage(ctx, options)
		chatGPTResults <- usageResult{usage: usage, err: fetchErr}
	}()
	go func() {
		usage, fetchErr := fetchOpenCodeGoUsage(ctx)
		openCodeResults <- usageResult{usage: usage, err: fetchErr}
	}()

	results := []struct {
		name   string
		result usageResult
	}{
		{name: "ChatGPT", result: <-chatGPTResults},
		{name: "OpenCode Go", result: <-openCodeResults},
	}
	succeeded := 0
	var failures []string
	for index, provider := range results {
		if index > 0 {
			fmt.Fprintln(output)
		}
		fmt.Fprintln(output, provider.name)
		if provider.result.err != nil {
			fmt.Fprintf(output, "ERROR: %s\n", provider.result.err)
			failures = append(failures, provider.name)
			continue
		}
		printUsage(output, provider.result.usage)
		succeeded++
	}
	if succeeded == 0 {
		return fmt.Errorf("usage is unavailable for %s", strings.Join(failures, " and "))
	}
	return nil
}

func validateCommand(provider string, command string, positionals []string, options commonOptions) error {
	switch command {
	case "usage":
		if provider != "chatgpt" && provider != "opencode-go" {
			return fmt.Errorf("%s usage is not supported", provider)
		}
		if len(positionals) != 0 {
			return fmt.Errorf("unexpected argument %q", positionals[0])
		}
		if options.name != "" || options.providerAccountID != "" || options.ownerID != "" {
			return errors.New("usage accepts only --profile")
		}
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
		if len(positionals) != 0 {
			return fmt.Errorf("unexpected argument %q", positionals[0])
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
			return fmt.Errorf("usage: prism %s auth remove <credential-id> [--profile <name>]", provider)
		}
	default:
		return errors.New("unknown provider auth command; use login/add, list, or remove")
	}
	return nil
}

func printUsage(output io.Writer, usage api.ProviderUsage) {
	printUsageAt(output, usage, time.Now())
}

func printUsageAt(output io.Writer, usage api.ProviderUsage, now time.Time) {
	if len(usage.Accounts) == 0 {
		fmt.Fprintf(output, "No %s credentials are registered.\n", usage.Provider)
		return
	}
	rows := [][]string{{"NAME", "PLAN", "LIMIT", "WINDOW", "USED", "REMAINING", "RESET", "PACE"}}
	accountSeparators := map[int]bool{}
	for _, account := range usage.Accounts {
		if len(rows) > 1 && (account.Error != nil || len(account.Limits) > 0) {
			accountSeparators[len(rows)] = true
		}
		plan := "-"
		if account.Plan != nil && *account.Plan != "" {
			plan = *account.Plan
		}
		if account.Error != nil {
			rows = append(rows, []string{account.Name, plan, "-", "-", "-", "-", "ERROR: " + account.Error.Message, "-"})
			continue
		}
		for index, limit := range account.Limits {
			name := account.Name
			rowPlan := plan
			if index > 0 {
				name = ""
				rowPlan = ""
			}
			reset := "-"
			if limit.ResetAt != nil {
				reset = formatUsageReset(*limit.ResetAt, now)
			}
			pace := usagePace(limit, now)
			rows = append(rows, []string{
				name,
				rowPlan,
				limit.Name,
				limit.Window,
				fmt.Sprintf("%g%%", limit.UsedPercent),
				fmt.Sprintf("%g%%", limit.RemainingPercent),
				reset,
				pace,
			})
		}
	}
	printTable(output, rows, map[int]bool{4: true, 5: true}, accountSeparators)
}

func usagePace(limit api.UsageLimit, now time.Time) string {
	if limit.LimitReached || limit.UsedPercent >= 100 {
		return "LIMIT REACHED"
	}
	if limit.WindowSeconds == nil || limit.ResetAt == nil || *limit.WindowSeconds <= 0 {
		return "-"
	}

	reset, err := time.Parse(time.RFC3339, *limit.ResetAt)
	if err != nil {
		return "-"
	}
	window := time.Duration(*limit.WindowSeconds) * time.Second
	remaining := reset.Sub(now)
	if remaining <= 0 {
		return "-"
	}

	elapsed := window - remaining
	if elapsed < maxDuration(time.Minute, window/100) {
		return "TOO EARLY"
	}

	projected := limit.UsedPercent / elapsed.Seconds() * window.Seconds()
	if limit.UsedPercent <= 0 {
		return "OK · ~100% left at reset"
	}
	if projected >= 100 {
		rate := limit.UsedPercent / elapsed.Seconds()
		if math.Abs(projected-100) < 1e-9 {
			return "RUNS OUT at reset"
		}
		runOut := time.Duration((100 - limit.UsedPercent) / rate * float64(time.Second))
		if runOut >= remaining {
			return "RUNS OUT at reset"
		}
		if runOut <= 0 {
			return "RUNS OUT"
		}
		return "RUNS OUT in " + strings.TrimPrefix(formatUsageTimeRemaining(runOut), "in ")
	}
	if projected <= 90 {
		remainingPercent := max(0, int(math.Round(100-projected)))
		return fmt.Sprintf("OK · ~%d%% left at reset", remainingPercent)
	}
	spare := max(1, int(math.Round(100-projected)))
	return fmt.Sprintf("CLOSE · ~%d%% spare at reset", spare)
}

func maxDuration(a, b time.Duration) time.Duration {
	if a > b {
		return a
	}
	return b
}

func formatUsageReset(value string, now time.Time) string {
	reset, err := time.Parse(time.RFC3339, value)
	if err != nil {
		return value
	}
	return fmt.Sprintf(
		"%s (%s)",
		reset.In(now.Location()).Format("2006-01-02 15:04 MST"),
		formatUsageTimeRemaining(reset.Sub(now)),
	)
}

func formatUsageTimeRemaining(remaining time.Duration) string {
	if remaining == 0 {
		return "now"
	}
	past := remaining < 0
	if past {
		remaining = -remaining
	}
	if remaining < time.Minute {
		if past {
			return "just now"
		}
		return "in <1m"
	}
	remaining = remaining.Truncate(time.Minute)
	days := remaining / (24 * time.Hour)
	remaining %= 24 * time.Hour
	hours := remaining / time.Hour
	minutes := remaining % time.Hour / time.Minute
	value := ""
	switch {
	case days > 0:
		value = fmt.Sprintf("%dd", days)
		if hours > 0 {
			value += fmt.Sprintf(" %dh", hours)
		}
	case hours > 0:
		value = fmt.Sprintf("%dh", hours)
		if minutes > 0 {
			value += fmt.Sprintf(" %dm", minutes)
		}
	default:
		value = fmt.Sprintf("%dm", minutes)
	}
	if past {
		return value + " ago"
	}
	return "in " + value
}

func printTable(output io.Writer, rows [][]string, rightAligned map[int]bool, separatorsBefore map[int]bool) {
	widths := make([]int, len(rows[0]))
	for _, row := range rows {
		for column, value := range row {
			row[column] = strings.NewReplacer("\r", " ", "\n", " ", "\t", " ").Replace(value)
			widths[column] = max(widths[column], utf8.RuneCountInString(row[column]))
		}
	}
	printTableBorder(output, widths, "┌", "┬", "┐")
	for index, row := range rows {
		if separatorsBefore[index] {
			printTableBorder(output, widths, "├", "┼", "┤")
		}
		fmt.Fprint(output, "│")
		for column, value := range row {
			padding := widths[column] - utf8.RuneCountInString(value)
			if rightAligned[column] && index > 0 {
				fmt.Fprintf(output, " %s%s │", strings.Repeat(" ", padding), value)
			} else {
				fmt.Fprintf(output, " %s%s │", value, strings.Repeat(" ", padding))
			}
		}
		fmt.Fprintln(output)
		if index == 0 {
			printTableBorder(output, widths, "├", "┼", "┤")
		}
	}
	printTableBorder(output, widths, "└", "┴", "┘")
}

func printTableBorder(output io.Writer, widths []int, left string, middle string, right string) {
	fmt.Fprint(output, left)
	for column, width := range widths {
		if column > 0 {
			fmt.Fprint(output, middle)
		}
		fmt.Fprint(output, strings.Repeat("─", width+2))
	}
	fmt.Fprintln(output, right)
}

func loginProvider(ctx context.Context, provider string, client api.Client, output io.Writer) error {
	switch provider {
	case "chatgpt":
		fmt.Fprintln(output, "Opening a browser for ChatGPT login...")
		bundle, err := (chatgpt.OAuth{}).Login(ctx)
		if err != nil {
			return err
		}
		saved, err := client.Save(ctx, "chatgpt", "", bundle)
		if err != nil {
			return err
		}
		fmt.Fprintf(output, "Saved ChatGPT credential %s (%s).\n", saved.Name, saved.ID)
	case "copilot":
		fmt.Fprintln(output, "Starting GitHub Copilot device login...")
		bundle, err := (copilot.OAuth{}).Login(ctx, output)
		if err != nil {
			return err
		}
		saved, err := client.Save(ctx, "copilot", "", bundle)
		if err != nil {
			return err
		}
		fmt.Fprintf(output, "Saved Copilot credential %s (%s).\n", saved.Name, saved.ID)
	case "gemini":
		fmt.Fprintln(output, "Opening a browser for Gemini Code Assist login...")
		bundle, err := (gemini.OAuth{}).Login(ctx)
		if err != nil {
			return err
		}
		saved, err := client.Save(ctx, "gemini", "", bundle)
		if err != nil {
			return err
		}
		fmt.Fprintf(output, "Saved Gemini credential %s (%s).\n", saved.Name, saved.ID)
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

func prismURLForProfile(profile *credentials.StoredProfile) (string, error) {
	if profile == nil {
		return "https://prism.circles.ac", nil
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
		return "https://prism-dev.circles.ac", nil
	}
	return "https://prism.circles.ac", nil
}

func prismClient(ctx context.Context, options commonOptions) (api.Client, error) {
	var credentialProvider *credentials.Provider
	var err error
	if options.profileSet {
		credentialProvider, err = credentials.New(credentials.WithProfile(options.profile))
	} else {
		credentialProvider, err = credentials.New()
	}
	if err != nil {
		return api.Client{}, err
	}
	circlesCredential, err := credentialProvider.Resolve(ctx)
	if err != nil {
		return api.Client{}, err
	}
	var selectedProfile *credentials.StoredProfile
	if circlesCredential.Source.Type == credentials.SourceProfile {
		selectedProfile, err = credentialProvider.GetProfile(ctx)
		if err != nil {
			return api.Client{}, err
		}
	}
	prismURL, err := prismURLForProfile(selectedProfile)
	if err != nil {
		return api.Client{}, err
	}
	return api.Client{BaseURL: prismURL, Token: circlesCredential.Value}, nil
}

func parseCommonOptions(args []string) (commonOptions, []string, error) {
	var options commonOptions
	var positionals []string
	for index := 0; index < len(args); index++ {
		argument := args[index]
		switch {
		case argument == "--help" || argument == "-h":
			options.help = true
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
	fmt.Fprintln(output, `Prism provider credential manager and client configuration tool

Usage:
  prism claude [claude arguments...]
  prism codex enable|disable|status
  prism usage [--profile <name>]
  prism chatgpt usage [--profile <name>]
  prism opencode-go usage
  prism chatgpt auth login [--profile <name>]
  prism copilot auth login [--profile <name>]
  prism gemini auth login [--profile <name>]
  prism <provider> auth add [--name <name>] [provider options]
  prism <provider> auth list [--profile <name>]
  prism <provider> auth remove <credential-id> [--profile <name>]
  prism version

Static providers: gemini-ai, groq, mistral, deepseek, opencode-go, cloudflare,
vercel, and gemini-app. Secret values are read from hidden stdin and are never
accepted as command-line options. Cloudflare requires --provider-account-id;
Vercel accepts --owner-id and prompts separately for an optional session cookie.

Run 'crcl login' before using Prism.`)
}

func printProviderAuthHelp(output io.Writer, provider string) {
	if provider == "chatgpt" || provider == "copilot" || provider == "gemini" {
		fmt.Fprintf(output, `Usage:
  prism %s auth login [--profile <name>]
  prism %s auth list [--profile <name>]
  prism %s auth remove <credential-id> [--profile <name>]
`, provider, provider, provider)
		return
	}
	fmt.Fprintf(output, `Usage:
  prism %s auth add [--name <name>] [--profile <name>]
  prism %s auth list [--profile <name>]
  prism %s auth remove <credential-id> [--profile <name>]
`, provider, provider, provider)
}

func printProviderUsageHelp(output io.Writer, provider string) {
	fmt.Fprintf(output, "Usage:\n  prism %s usage [--profile <name>]\n", provider)
}
