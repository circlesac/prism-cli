# Prism CLI

Public Go CLI for registering provider credentials through Prism. It does not
contain the private Worker or require Node/Bun/cvlt at runtime. The CLI performs
OAuth locally, then sends the credential to Prism using the same Circles
identity that owns the personal Vault.

## Install

```sh
brew install circlesac/tap/prism
```

Or install the latest release directly:

```sh
curl -fsSL https://github.com/circlesac/prism-cli/releases/latest/download/install.sh | sh
```

The direct installer defaults to `~/.local/bin`; set `INSTALL_DIR` to override
it.

## Circles authentication

Prism uses the shared Circles credential chain:

1. an explicitly selected `--profile`;
2. `CIRCLES_AUTH_TOKEN`;
3. `CIRCLES_PROFILE`;
4. the shared current profile selected by `crcl use` (the first login becomes
   current automatically);
5. the legacy `default` profile in `~/.crcl`.

`CRCL_AUTH_TOKEN` and `CRCL_PROFILE` remain compatibility aliases. `crcl login`
can provision a shared profile, but the `crcl` executable is not required when
Prism runs. Expired OAuth credentials are refreshed and written back by the
shared provider.

## Provider credentials

Credentials are personal in this release:

```sh
prism chatgpt usage
prism chatgpt auth login
prism chatgpt auth list
prism chatgpt auth remove <credential-id>

prism copilot auth login
prism gemini auth login
```

Static credentials are read from hidden stdin and never accepted as command-line
arguments:

```sh
prism groq auth add --name personal
prism mistral auth add
prism gemini-ai auth add
prism deepseek auth add
prism opencode-go auth add
prism cloudflare auth add --provider-account-id <cloudflare-account-id>
prism vercel auth add --owner-id <vercel-owner-id>
prism gemini-app auth add

prism groq auth list
prism groq auth remove <credential-id>
```

Add `--profile <name>` to any command to choose a Circles profile. Login opens
OpenAI authorization in a browser, verifies PKCE and state on a loopback
callback, then Prism derives the provider-native account ID and display name
again before upserting the credential into the caller's personal Vault.
Provider tokens are not written to Prism KV or a local Prism credential store.
New records use Vault's application-plaintext service integration so Prism can
refresh them; existing E2EE records stay E2EE and are updated in place.

First-party production profiles use `prism.circles.ac`. Profiles configured
with `api-dev.circles.ac` and `auth-dev.circles.ac` use
`prism-dev.circles.ac`:

```sh
prism chatgpt auth login --profile dev-personal
prism chatgpt auth list --profile dev-personal
```

OAuth account IDs and names are derived from provider tokens or profiles.
Static credentials have no caller-selected account ID: Prism returns the Vault
item ID used by `auth remove`, while `--name` is only a friendly label. Each
credential is a separate `API_CREDENTIAL` item. The CLI intentionally has no
`set-default` command.

`prism chatgpt usage` reads normalized account quota snapshots from
`GET /usage/chatgpt`. It prints every registered account and limit without
exposing OAuth tokens or provider account UUIDs.

## Build and test

```sh
go test -mod=vendor -race ./...
go vet -mod=vendor ./...
go build -mod=vendor -o prism .
```

The official Circles Go credential provider is pinned and vendored from the
public `github.com/circlesac/credentials-go` module for reproducible releases.
Official release binaries include the Gemini desktop OAuth client. Source-built
binaries can enable Gemini login with `PRISM_GEMINI_OAUTH_CLIENT_ID` and
`PRISM_GEMINI_OAUTH_CLIENT_SECRET`.
