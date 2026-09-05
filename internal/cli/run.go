package cli

import (
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"io"
	"math"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	credentials "github.com/circlesac/credentials/go"
	"github.com/circlesac/prism-cli/internal/anthropic"
	"github.com/circlesac/prism-cli/internal/api"
	"github.com/circlesac/prism-cli/internal/chatgpt"
	"github.com/circlesac/prism-cli/internal/copilot"
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
var fetchAnthropicUsage = func(ctx context.Context, options commonOptions) (api.ProviderUsage, error) {
	client, err := prismClient(ctx, options)
	if err != nil {
		return api.ProviderUsage{}, err
	}
	return client.Usage(ctx, "anthropic")
}
var fetchCopilotUsage = func(ctx context.Context, options commonOptions) (api.ProviderUsage, error) {
	client, err := prismClient(ctx, options)
	if err != nil {
		return api.ProviderUsage{}, err
	}
	return client.Usage(ctx, "copilot")
}

type commonOptions struct {
	profile           string
	profileSet        bool
	name              string
	account           string
	creditID          string
	confirm           bool
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
	if args[0] == "exec" {
		if hasOption(args[1:], "--help") || hasOption(args[1:], "-h") {
			printExecHelp(stdout)
			return nil
		}
		return runExecCommand(ctx, args[1:], stdout, stderr)
	}
	if args[0] == "relay" {
		return runRelayCommand(ctx, args[1:], stdout, stderr)
	}
	if args[0] == "claude" {
		return runClaudeCommand(ctx, args[1:], stdout, stderr)
	}
	if args[0] == "gemini" && isGeminiCLIInvocation(args[1:]) {
		return runGeminiCommand(ctx, args[1:], stdout, stderr)
	}
	if args[0] == "codex" {
		return runCodexCommand(args[1:], stdout)
	}
	if args[0] == "cursor" {
		return runCursorCommand(ctx, args[1:], stdout, stderr)
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
		if command == "usage" || command == "reset" {
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
		} else if providerName == "gemini" {
			usage, err = fetchGeminiUsage(ctx)
		} else if providerName == "anthropic" {
			usage, err = fetchAnthropicUsage(ctx, options)
		} else if providerName == "copilot" {
			usage, err = fetchCopilotUsage(ctx, options)
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
		printRemoveConfirmation(stdout, providerName, positionals[0])
	case "reset":
		idempotencyKey, err := newIdempotencyKey()
		if err != nil {
			return err
		}
		response, err := client.ConsumeChatGPTReset(ctx, api.ChatGPTResetRequest{
			Account:        options.account,
			CreditID:       options.creditID,
			IdempotencyKey: idempotencyKey,
		})
		if err != nil {
			return err
		}
		fmt.Fprintf(stdout, "ChatGPT reset for %s: %s", response.Account.Name, response.Outcome)
		if response.WindowsReset != nil {
			fmt.Fprintf(stdout, " (%d windows)", *response.WindowsReset)
		}
		if !response.UsageRefreshed {
			fmt.Fprint(stdout, "; usage refresh unavailable")
		}
		fmt.Fprintln(stdout, ".")
	}
	return nil
}

func printRemoveConfirmation(output io.Writer, provider string, credentialID string) {
	if provider == "anthropic" {
		fmt.Fprintf(output, "Removed anthropic credential %s from Prism. Anthropic provider-side revocation was not called; use Anthropic account security settings if needed.\n", credentialID)
		return
	}
	fmt.Fprintf(output, "Removed %s credential %s.\n", provider, credentialID)
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
	anthropicResults := make(chan usageResult, 1)
	copilotResults := make(chan usageResult, 1)
	openCodeResults := make(chan usageResult, 1)
	cursorResults := make(chan usageResult, 1)
	geminiResults := make(chan usageResult, 1)
	go func() {
		usage, fetchErr := fetchChatGPTUsage(ctx, options)
		chatGPTResults <- usageResult{usage: usage, err: fetchErr}
	}()
	go func() {
		usage, fetchErr := fetchAnthropicUsage(ctx, options)
		anthropicResults <- usageResult{usage: usage, err: fetchErr}
	}()
	go func() {
		usage, fetchErr := fetchCopilotUsage(ctx, options)
		copilotResults <- usageResult{usage: usage, err: fetchErr}
	}()
	go func() {
		usage, fetchErr := fetchOpenCodeGoUsage(ctx)
		openCodeResults <- usageResult{usage: usage, err: fetchErr}
	}()
	go func() {
		usage, fetchErr := fetchAllCursorUsage(ctx, "")
		cursorResults <- usageResult{usage: usage, err: fetchErr}
	}()
	go func() {
		usage, fetchErr := fetchGeminiUsage(ctx)
		geminiResults <- usageResult{usage: usage, err: fetchErr}
	}()

	results := []struct {
		name   string
		result usageResult
	}{
		{name: "ChatGPT", result: <-chatGPTResults},
		{name: "Claude", result: <-anthropicResults},
		{name: "Copilot", result: <-copilotResults},
		{name: "OpenCode", result: <-openCodeResults},
		{name: "Cursor", result: <-cursorResults},
		{name: "Gemini", result: <-geminiResults},
	}
	succeeded := 0
	var failures []string
	providers := make([]usageTableProvider, 0, len(results))
	for _, provider := range results {
		providers = append(providers, usageTableProvider{
			name:  provider.name,
			usage: provider.result.usage,
			err:   provider.result.err,
		})
		if provider.result.err != nil {
			failures = append(failures, provider.name)
			continue
		}
		succeeded++
	}
	printUsageTableAt(output, providers, time.Now(), true)
	if succeeded == 0 {
		return fmt.Errorf("usage is unavailable for %s", joinNames(failures))
	}
	return nil
}

func validateCommand(provider string, command string, positionals []string, options commonOptions) error {
	switch command {
	case "usage":
		if provider != "chatgpt" && provider != "anthropic" && provider != "copilot" && provider != "opencode-go" && provider != "gemini" {
			return fmt.Errorf("%s usage is not supported", provider)
		}
		if len(positionals) != 0 {
			return fmt.Errorf("unexpected argument %q", positionals[0])
		}
		if options.name != "" || options.providerAccountID != "" || options.ownerID != "" {
			return errors.New("usage accepts only --profile")
		}
	case "reset":
		if provider != "chatgpt" {
			return fmt.Errorf("%s reset is not supported", provider)
		}
		if len(positionals) != 0 {
			return fmt.Errorf("unexpected argument %q", positionals[0])
		}
		if strings.TrimSpace(options.account) == "" {
			return errors.New("chatgpt reset requires --account <name-or-email>")
		}
		if !options.confirm {
			return errors.New("chatgpt reset consumes a credit; pass --confirm to continue")
		}
		if options.name != "" || options.providerAccountID != "" || options.ownerID != "" {
			return errors.New("chatgpt reset accepts only --profile, --account, --credit-id, and --confirm")
		}
	case "login":
		if provider != "chatgpt" && provider != "anthropic" && provider != "copilot" && provider != "gemini" {
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

type usageTableProvider struct {
	name  string
	usage api.ProviderUsage
	err   error
}

func printUsageAt(output io.Writer, usage api.ProviderUsage, now time.Time) {
	if len(usage.Accounts) == 0 {
		fmt.Fprintf(output, "No %s credentials are registered.\n", usage.Provider)
		return
	}
	printUsageTableAt(output, []usageTableProvider{{usage: usage}}, now, false)
}

func printUsageTableAt(output io.Writer, providers []usageTableProvider, now time.Time, showProvider bool) {
	rows := [][]string{{"PROVIDER", "ACCOUNT", "PLAN", "LIMIT", "WINDOW", "USED", "REMAINING", "RESET", "PACE"}}
	if !showProvider {
		rows[0] = rows[0][1:]
		rows[0][0] = "NAME"
	}
	accountSeparators := map[int]bool{}
	for _, provider := range providers {
		providerName := provider.name
		if provider.err != nil {
			if len(rows) > 1 {
				accountSeparators[len(rows)] = true
			}
			rows = appendUsageRow(rows, showProvider, providerName, "-", "-", "-", "-", "-", "-", "ERROR: "+provider.err.Error(), "-")
			continue
		}
		if len(provider.usage.Accounts) == 0 {
			if len(rows) > 1 {
				accountSeparators[len(rows)] = true
			}
			rows = appendUsageRow(rows, showProvider, providerName, "-", "-", "-", "-", "-", "-", "No accounts", "-")
			continue
		}
		for _, account := range provider.usage.Accounts {
			if len(rows) > 1 && (account.Error != nil || len(account.Limits) > 0) {
				accountSeparators[len(rows)] = true
			}
			plan := "-"
			if account.Plan != nil && *account.Plan != "" {
				plan = *account.Plan
			}
			if account.Error != nil {
				rows = appendUsageRow(rows, showProvider, providerName, account.Name, plan, "-", "-", "-", "-", "ERROR: "+account.Error.Message, "-")
				providerName = ""
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
				rows = appendUsageRow(rows, showProvider, providerName, name, rowPlan, limit.Name, limit.Window, formatUsagePercent(limit.UsedPercent), formatUsagePercent(limit.RemainingPercent), reset, usagePace(limit, account.Status, now))
				providerName = ""
			}
			if account.ResetCredits != nil {
				name := account.Name
				rowPlan := plan
				if len(account.Limits) > 0 {
					name = ""
					rowPlan = ""
				}
				reset := formatResetCreditExpiry(account.ResetCredits.Credits, now)
				rows = appendUsageRow(rows, showProvider, providerName, name, rowPlan, "reset credits", "available", "-", strconv.Itoa(account.ResetCredits.AvailableCount), reset, "USE BEFORE EXPIRY")
				providerName = ""
			}
		}
	}
	usedColumn := 4
	if showProvider {
		usedColumn++
	}
	printTable(output, rows, map[int]bool{usedColumn: true, usedColumn + 1: true}, accountSeparators)
}

func formatResetCreditExpiry(credits []api.UsageResetCredit, now time.Time) string {
	if len(credits) == 0 {
		return "-"
	}
	values := make([]string, 0, len(credits))
	for _, credit := range credits {
		if credit.ExpiresAt == nil || *credit.ExpiresAt == "" {
			values = append(values, "no expiry")
			continue
		}
		values = append(values, formatUsageReset(*credit.ExpiresAt, now))
	}
	return strings.Join(values, "\n")
}

func appendUsageRow(rows [][]string, showProvider bool, values ...string) [][]string {
	if !showProvider {
		values = values[1:]
	}
	return append(rows, values)
}

func joinNames(values []string) string {
	switch len(values) {
	case 0:
		return ""
	case 1:
		return values[0]
	case 2:
		return values[0] + " and " + values[1]
	default:
		return strings.Join(values[:len(values)-1], ", ") + ", and " + values[len(values)-1]
	}
}

func formatUsagePercent(value float64) string {
	return strings.TrimSuffix(strconv.FormatFloat(value, 'f', 1, 64), ".0") + "%"
}

func usagePace(limit api.UsageLimit, status string, now time.Time) string {
	if status == "stale" {
		return "⚠️ STALE"
	}
	if limit.LimitReached || limit.UsedPercent >= 100 {
		return "⛔ LIMIT REACHED"
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
		return "⏳ TOO EARLY"
	}

	projected := limit.UsedPercent / elapsed.Seconds() * window.Seconds()
	if limit.UsedPercent <= 0 {
		return "✅ OK · ~100% left at reset"
	}
	if projected >= 100 {
		rate := limit.UsedPercent / elapsed.Seconds()
		if math.Abs(projected-100) < 1e-9 {
			return "🔥 RUNS OUT at reset"
		}
		runOut := time.Duration((100 - limit.UsedPercent) / rate * float64(time.Second))
		if runOut >= remaining {
			return "🔥 RUNS OUT at reset"
		}
		if runOut <= 0 {
			return "🔥 RUNS OUT"
		}
		return "🔥 RUNS OUT in " + strings.TrimPrefix(formatUsageTimeRemaining(runOut), "in ")
	}
	if projected <= 90 {
		remainingPercent := max(0, int(math.Round(100-projected)))
		return fmt.Sprintf("✅ OK · ~%d%% left at reset", remainingPercent)
	}
	spare := max(1, int(math.Round(100-projected)))
	return fmt.Sprintf("⚠️ CLOSE · ~%d%% spare at reset", spare)
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
			row[column] = strings.NewReplacer("\r", " ", "\t", " ").Replace(value)
			for _, line := range strings.Split(row[column], "\n") {
				widths[column] = max(widths[column], tableDisplayWidth(line))
			}
		}
	}
	printTableBorder(output, widths, "┌", "┬", "┐")
	for index, row := range rows {
		if separatorsBefore[index] {
			printTableBorder(output, widths, "├", "┼", "┤")
		}
		height := 1
		for _, value := range row {
			height = max(height, len(strings.Split(value, "\n")))
		}
		for lineIndex := 0; lineIndex < height; lineIndex++ {
			fmt.Fprint(output, "│")
			for column, value := range row {
				lines := strings.Split(value, "\n")
				line := ""
				if lineIndex < len(lines) {
					line = lines[lineIndex]
				}
				padding := widths[column] - tableDisplayWidth(line)
				if rightAligned[column] && index > 0 {
					fmt.Fprintf(output, " %s%s │", strings.Repeat(" ", padding), line)
				} else {
					fmt.Fprintf(output, " %s%s │", line, strings.Repeat(" ", padding))
				}
			}
			fmt.Fprintln(output)
		}
		if index == 0 {
			printTableBorder(output, widths, "├", "┼", "┤")
		}
	}
	printTableBorder(output, widths, "└", "┴", "┘")
}

func tableDisplayWidth(value string) int {
	width := utf8.RuneCountInString(value)
	for _, emoji := range []string{"✅", "🔥", "⛔", "⏳"} {
		width += strings.Count(value, emoji)
	}
	return width
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
	case "anthropic":
		fmt.Fprintln(output, "Opening a browser for Anthropic login...")
		grant, err := (anthropic.OAuth{}).Login(ctx)
		if err != nil {
			return err
		}
		saved, err := client.Save(ctx, "anthropic", "", grant)
		if err != nil {
			return err
		}
		fmt.Fprintf(output, "Saved Anthropic credential %s (%s).\n", saved.Name, saved.ID)
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
		case argument == "--account":
			index++
			if index >= len(args) || args[index] == "" {
				return commonOptions{}, nil, errors.New("--account requires a value")
			}
			options.account = args[index]
		case strings.HasPrefix(argument, "--account="):
			options.account = strings.TrimPrefix(argument, "--account=")
		case argument == "--credit-id":
			index++
			if index >= len(args) || args[index] == "" {
				return commonOptions{}, nil, errors.New("--credit-id requires a value")
			}
			options.creditID = args[index]
		case strings.HasPrefix(argument, "--credit-id="):
			options.creditID = strings.TrimPrefix(argument, "--credit-id=")
		case argument == "--confirm":
			options.confirm = true
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

func newIdempotencyKey() (string, error) {
	var bytes [16]byte
	if _, err := rand.Read(bytes[:]); err != nil {
		return "", errors.New("could not generate reset idempotency key")
	}
	bytes[6] = (bytes[6] & 0x0f) | 0x40
	bytes[8] = (bytes[8] & 0x3f) | 0x80
	return fmt.Sprintf("%08x-%04x-%04x-%04x-%012x",
		bytes[0:4], bytes[4:6], bytes[6:8], bytes[8:10], bytes[10:16]), nil
}

func printHelp(output io.Writer) {
	fmt.Fprintln(output, `Prism provider credential manager and client configuration tool

Usage:
  prism exec --api chat|completions|responses|messages [options]
  prism relay claude|codex --port <port> [--profile <name>]
  prism claude [--account <alias-or-id>] [claude arguments...]
  prism codex enable|disable|status
  prism cursor [--account <name-or-email>] [cursor arguments...]
  prism usage [--profile <name>]
  prism chatgpt usage [--profile <name>]
  prism chatgpt reset --account <name-or-email> [--credit-id <id>] --confirm [--profile <name>]
  prism copilot usage [--profile <name>]
  prism opencode-go usage
  prism chatgpt auth login [--profile <name>]
  prism anthropic auth login [--profile <name>]
  prism claude login [--profile <name>]
  prism copilot auth login [--profile <name>]
  prism gemini [Antigravity CLI arguments...]
  prism <provider> auth add [--name <name>] [provider options]
  prism <provider> auth list [--profile <name>]
  prism <provider> auth remove <credential-id> [--profile <name>]
  prism version

Static providers: groq, mistral, deepseek, opencode-go, cloudflare, vercel,
and gemini-app. Secret values are read from hidden stdin and are never
accepted as command-line options. Cloudflare requires --provider-account-id;
Vercel accepts --owner-id and prompts separately for an optional session cookie.

Run 'crcl login' before using Prism.`)
}

func printProviderAuthHelp(output io.Writer, provider string) {
	if provider == "gemini" {
		fmt.Fprintln(output, `Antigravity CLI signs in automatically on the first
'prism gemini' run. It has no auth subcommand, and Prism does not import or
proxy Antigravity OAuth credentials.`)
		return
	}
	if provider == "chatgpt" || provider == "anthropic" || provider == "copilot" || provider == "gemini" {
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
	if provider == "chatgpt" {
		fmt.Fprintln(output, "Usage:\n  prism chatgpt usage [--profile <name>]\n  prism chatgpt reset --account <name-or-email> [--credit-id <id>] --confirm [--profile <name>]")
		return
	}
	fmt.Fprintf(output, "Usage:\n  prism %s usage [--profile <name>]\n", provider)
}
