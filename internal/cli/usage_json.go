package cli

import (
	"encoding/json"
	"errors"
	"io"

	"github.com/circlesac/prism-cli/internal/api"
)

type usageJSONProvider struct {
	Provider    string             `json:"provider"`
	DisplayName string             `json:"display_name"`
	Accounts    []api.UsageAccount `json:"accounts"`
	Error       *api.UsageError    `json:"error"`
}

type usageJSONDocument struct {
	GeneratedAt string              `json:"generated_at"`
	Providers   []usageJSONProvider `json:"providers"`
}

func usageProviderDisplayName(provider string) string {
	switch provider {
	case "chatgpt":
		return "ChatGPT"
	case "anthropic":
		return "Claude"
	case "copilot":
		return "Copilot"
	case "opencode-go":
		return "OpenCode"
	case "cursor":
		return "Cursor"
	case "gemini":
		return "Gemini"
	}
	return provider
}

func usageJSONProviderFor(provider string, usage api.ProviderUsage, err error) usageJSONProvider {
	entry := usageJSONProvider{
		Provider:    provider,
		DisplayName: usageProviderDisplayName(provider),
		Accounts:    []api.UsageAccount{},
	}
	if err != nil {
		entry.Error = &api.UsageError{Code: "usage_unavailable", Message: err.Error()}
		return entry
	}
	for _, account := range usage.Accounts {
		if account.Limits == nil {
			account.Limits = []api.UsageLimit{}
		}
		entry.Accounts = append(entry.Accounts, account)
	}
	return entry
}

func printUsageJSON(output io.Writer, document any) error {
	encoder := json.NewEncoder(output)
	encoder.SetEscapeHTML(false)
	encoder.SetIndent("", "  ")
	if err := encoder.Encode(document); err != nil {
		return errors.New("could not encode the usage JSON document")
	}
	return nil
}
