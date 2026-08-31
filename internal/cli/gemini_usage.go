package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"

	"github.com/circlesac/prism-cli/internal/api"
)

type antigravityUsageEnvelope struct {
	Status  string `json:"status"`
	Error   string `json:"error"`
	Command struct {
		Data struct {
			Groups []struct {
				Name    string `json:"name"`
				Buckets []struct {
					Name              string  `json:"name"`
					Window            string  `json:"window"`
					RemainingFraction float64 `json:"remaining_fraction"`
					ResetTime         string  `json:"reset_time"`
				} `json:"buckets"`
			} `json:"groups"`
		} `json:"data"`
	} `json:"command"`
}

var fetchGeminiUsage = fetchAntigravityUsage

func runGeminiUsage(ctx context.Context, stdout io.Writer, stderr io.Writer) error {
	usage, err := fetchGeminiUsage(ctx)
	if err != nil {
		return err
	}
	printUsage(stdout, usage)
	return nil
}

func fetchAntigravityUsage(ctx context.Context) (api.ProviderUsage, error) {
	if err := disableAntigravityCreditOverages(); err != nil {
		return api.ProviderUsage{}, err
	}
	executable, err := findGeminiCLIExecutable()
	if err != nil {
		return api.ProviderUsage{}, err
	}
	commandArgs := append(append([]string{}, executable.prefix...), "-p", "/usage", "--output-format", "json")
	command := exec.CommandContext(ctx, executable.path, commandArgs...)
	command.Env = subscriptionGeminiEnvironment(os.Environ())
	var stdout bytes.Buffer
	var stderr bytes.Buffer
	command.Stdout = &stdout
	command.Stderr = &stderr
	if err := command.Run(); err != nil {
		message := strings.TrimSpace(stderr.String())
		if message == "" {
			message = err.Error()
		}
		return api.ProviderUsage{}, fmt.Errorf("Antigravity usage unavailable: %s", message)
	}
	var envelope antigravityUsageEnvelope
	if err := json.Unmarshal(stdout.Bytes(), &envelope); err != nil {
		return api.ProviderUsage{}, errors.New("Antigravity returned invalid usage JSON")
	}
	if envelope.Status != "" && envelope.Status != "SUCCESS" {
		if envelope.Error == "" {
			envelope.Error = "Antigravity usage request failed"
		}
		return api.ProviderUsage{}, errors.New(envelope.Error)
	}
	accounts := []api.UsageAccount{{Name: "Google Gemini subscription", Status: "fresh"}}
	plan := "Google Gemini subscription"
	accounts[0].Plan = &plan
	for _, group := range envelope.Command.Data.Groups {
		for _, bucket := range group.Buckets {
			remaining := bucket.RemainingFraction * 100
			if remaining < 0 {
				remaining = 0
			}
			if remaining > 100 {
				remaining = 100
			}
			limit := api.UsageLimit{
				Name:             group.Name + " — " + bucket.Name,
				Window:           bucket.Window,
				UsedPercent:      100 - remaining,
				RemainingPercent: remaining,
				LimitReached:     remaining <= 0,
			}
			if bucket.ResetTime != "" {
				reset := bucket.ResetTime
				limit.ResetAt = &reset
			}
			switch bucket.Window {
			case "5h":
				seconds := 5 * 60 * 60
				limit.WindowSeconds = &seconds
			case "weekly":
				seconds := 7 * 24 * 60 * 60
				limit.WindowSeconds = &seconds
			}
			accounts[0].Limits = append(accounts[0].Limits, limit)
		}
	}
	if len(accounts[0].Limits) == 0 {
		return api.ProviderUsage{}, errors.New("Antigravity usage returned no quota windows")
	}
	return api.ProviderUsage{Provider: "gemini", Accounts: accounts}, nil
}
