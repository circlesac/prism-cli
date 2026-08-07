# Prism CLI

Connect LLM provider accounts and clients to Prism.

## Install

```sh
brew install circlesac/tap/crcl circlesac/tap/prism
```

If `crcl` is already installed, Prism CLI can also be installed directly:

```sh
curl -fsSL https://github.com/circlesac/prism-cli/releases/latest/download/install.sh | sh
```

## Sign in

```sh
crcl login
```

## ChatGPT

```sh
prism chatgpt auth login
prism chatgpt auth list
prism chatgpt usage
prism chatgpt auth remove <credential-id>
```

Run `prism chatgpt auth login` again to add another ChatGPT account.

## Other providers

```sh
prism copilot auth login
prism gemini auth login

prism groq auth add --name personal
prism mistral auth add
prism gemini-ai auth add
prism deepseek auth add
prism opencode-go auth add
prism cloudflare auth add --provider-account-id <cloudflare-account-id>
prism vercel auth add --owner-id <vercel-owner-id>
prism gemini-app auth add
```

Use the same `auth list` and `auth remove` commands for each provider:

```sh
prism groq auth list
prism groq auth remove <credential-id>
```

## Codex

Find the `crcl` executable:

```sh
command -v crcl
```

Add the following to `~/.codex/config.toml`. Replace `/opt/homebrew/bin/crcl`
with the path printed by the command above.

```toml
model = "gpt-5.6-sol"
model_provider = "prism"
model_reasoning_effort = "xhigh"

[model_providers.prism]
name = "Prism"
base_url = "https://prism.circles.ac/v1"
wire_api = "responses"

[model_providers.prism.auth]
command = "/opt/homebrew/bin/crcl"
args = ["auth", "token"]
```

Open a new Codex task after changing the configuration. The CLI can be started
normally:

```sh
codex
```

Verify the setup:

```sh
codex exec --ephemeral 'Do not use any tools. Reply with exactly PRISM_OK.'
prism chatgpt usage
```

## Claude Code

Install Claude Code so that `claude` is available on `PATH`, then use the Claude Code interface and tools with any model supported by Prism:

```sh
prism claude --model gpt-5.6-sol
```

Use Claude Code's Ultracode dynamic workflow with GPT-5.6 Sol:

```sh
prism claude --model gpt-5.6-sol --effort ultracode
```

These examples use `gpt-5.6-sol`; replace it with another Prism-supported model when needed. Pass a Circles profile before any Claude Code arguments:

```sh
prism claude --profile <name> --model gpt-5.6-sol
```

Verify the setup:

```sh
prism claude --model gpt-5.6-sol --print --tools "" -- \
  'Do not use any tools. Reply with exactly PRISM_CLAUDE_MODEL_OK.'
```

A successful request ends with `PRISM_CLAUDE_MODEL_OK`. Run `claude --help` to see all Claude Code options.
