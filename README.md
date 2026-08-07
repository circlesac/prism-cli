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
Usage reset times are shown in the local timezone with the remaining time.

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

Route Codex App and CLI tasks through Prism:

```sh
prism codex enable
prism codex status
```

`prism codex enable` selects `gpt-5.6-sol`, uses the model's default reasoning
effort, and updates the shared user-level Codex configuration. Open a new Codex
task after enabling it. The App and CLI then use the same Prism configuration,
and the CLI can still be started normally:

```sh
codex
```

Verify the setup:

```sh
codex exec --ephemeral 'Do not use any tools. Reply with exactly PRISM_OK.'
prism chatgpt usage
```

Restore the Codex settings that were present before Prism was enabled:

```sh
prism codex disable
```

Prism only restores settings that still match what `enable` applied. If a
managed setting was edited afterward, `prism codex status` reports drift and
`disable` leaves the changed setting untouched.

## Claude Code

Install Claude Code so that `claude` is available on `PATH`, then use the Claude Code interface and tools with any model supported by Prism:

```sh
prism claude --model gpt-5.6-sol
```

Use Claude Code's Ultracode dynamic workflow with GPT-5.6 Sol:

```sh
prism claude --model gpt-5.6-sol --effort ultracode
```

These examples use `gpt-5.6-sol`; replace it with another Prism-supported
model when needed. `prism claude` launches the installed Claude Code CLI and
passes its arguments through unchanged.

Verify the setup:

```sh
prism claude --model gpt-5.6-sol --print --tools "" -- \
  'Do not use any tools. Reply with exactly PRISM_CLAUDE_MODEL_OK.'
```

A successful request ends with `PRISM_CLAUDE_MODEL_OK`. Run `claude --help` to see all Claude Code options.
