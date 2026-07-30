# Prism CLI

Connect LLM provider accounts to Prism.

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

See [Use Prism with Codex](docs/codex.md).
