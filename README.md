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

## Usage

Show ChatGPT, Anthropic, Copilot, OpenCode Go, Cursor, and Gemini subscription usage together:

```sh
prism usage
```

Each provider is fetched independently, so an unavailable login does not hide
usage from the other providers.

## Cursor

Install the official Cursor Agent without replacing an existing
`~/.local/bin/agent`, then register one or more accounts with Cursor's
supported browser flow:

```sh
prism cursor install
prism cursor login
prism cursor login --name work-admin
prism cursor auth list
prism cursor status
prism cursor models
```

Run Cursor Agent through Prism while passing its arguments through unchanged:

```sh
prism cursor --mode ask -p 'Reply with exactly CURSOR_OK.'
prism cursor --account work-admin --mode ask -p 'Reply with exactly CURSOR_OK.'
```

Prism disables Cursor Agent's self-updater on every launch. Use
`prism cursor update` (or `prism cursor upgrade`) to install the latest official
package while preserving the separate `agent` command.

Show the monthly Cursor Models and Other Models pools separately:

```sh
prism cursor usage
```

Without `--account`, Prism rotates across the registered accounts. Usage shows
all registered accounts; pass `--account` to show one. To migrate the Cursor
login that was active before this feature, run:

```sh
prism cursor auth import
```

Each login remains in an isolated profile supported by the official Cursor
Agent, and Prism never prints its token.
Because Cursor does not publish a standalone usage CLI contract, usage is a
read-only best-effort integration and reports an explicit error when the login
or quota response is unavailable.

## Gemini subscription (Antigravity CLI)

Sign in once with the official Antigravity CLI (`agy`) using the Google account
that owns the Gemini subscription, then run it through Prism:

```sh
agy
prism gemini usage
prism gemini -p 'Reply with exactly GEMINI_OK.'
```

`prism gemini` uses the signed-in `agy` profile directly. Prism never reads or
passes `GEMINI_API_KEY`/`GOOGLE_API_KEY`, and AI Studio API-key authentication is
intentionally unsupported because it can incur usage-based charges.
Before every Gemini run and usage check, Prism also forces
`useG1Credits: false` in Antigravity settings so purchased or promotional AI
credits cannot be consumed after the subscription quota is exhausted.

`prism usage` and `prism gemini usage` show the Antigravity five-hour and weekly
subscription windows. Use `/usage` inside `agy` for the same live quota panel.

The default model is `gemini-3.7-flash-low`. For harder software-engineering or
multi-step tool-use tasks, select `gemini-3.1-pro-high` explicitly:

```sh
prism gemini --model gemini-3.1-pro-high -p 'Review this repository.'
```

## Anthropic

Register each Claude subscription account separately and show its current quota:

```sh
prism anthropic auth login
prism anthropic auth list
prism anthropic usage
prism anthropic auth remove <credential-id>
```

`prism claude login` is a short alias for `prism anthropic auth login`. The
browser handles account selection, SSO, and MFA. Prism does not import the
credential used by an existing Claude Code login.

This stores and refreshes Anthropic credentials for account and usage management,
and `prism claude` forwards requests through a local bridge while keeping
Claude Code’s existing OAuth/login mode.

`prism anthropic auth remove` deletes the Prism grant and its routing/usage
state. Anthropic does not document a revocation endpoint for this grant, so the
command does not claim provider-side revocation; use Anthropic account security
settings when provider-side invalidation is required.

## ChatGPT

```sh
prism chatgpt auth login
prism chatgpt auth list
prism chatgpt usage
prism chatgpt auth remove <credential-id>
```

Run `prism chatgpt auth login` again to add another ChatGPT account.
Usage reset times are shown in the local timezone with the remaining time.

## OpenCode Go

Show usage from an OpenCode login already present in Chrome, Chromium, Comet,
Arc, Edge, Brave, or Firefox on macOS:

```sh
prism opencode-go usage
```

Prism reads the browser session only for this request. It does not save or
print browser cookies. The output includes rolling, weekly, and monthly reset
times and whether the current usage pace is likely to exhaust each limit.

## Other providers

```sh
prism copilot auth login

prism groq auth add --name personal
prism mistral auth add
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
passes its arguments through unchanged. Claude models use the registered
Anthropic account pool automatically. To start a session on one account, pass
its alias or redacted id before the Claude Code arguments:

```sh
prism claude --account work-admin --model claude-fable-5
```

Verify the setup:

```sh
prism claude --model gpt-5.6-sol --print --tools "" -- \
  'Do not use any tools. Reply with exactly PRISM_CLAUDE_MODEL_OK.'
```

A successful request ends with `PRISM_CLAUDE_MODEL_OK`. Run `claude --help` to see all Claude Code options.
