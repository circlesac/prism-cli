# Prism CLI

Public Go CLI for registering provider credentials in Circles Vault. It does
not contain the private Prism Worker or require Node/Bun at runtime. Vault
content is encrypted and decrypted by the official `cvlt` client through an
authenticated localhost bridge; install `cvlt` alongside Prism.

## Install

```sh
brew install circlesac/tap/prism
```

The Homebrew formula installs `cvlt` automatically. For a direct install,
install `cvlt` first from
[`circlesac/cvlt-cli`](https://github.com/circlesac/cvlt-cli).

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

## ChatGPT accounts

Personal Vault is the default:

```sh
prism chatgpt auth login
prism chatgpt auth list
prism chatgpt auth remove <account-id>
```

Select an organization Vault explicitly:

```sh
prism chatgpt auth login --org circlesac
prism chatgpt auth list --org circlesac
prism chatgpt auth remove <account-id> --org circlesac
```

Add `--profile <name>` to any command to choose a Circles profile. Login opens
OpenAI authorization in a browser, verifies PKCE and state on a loopback
callback, derives the ChatGPT account ID and display name from the returned
tokens, and upserts that account directly into the selected Vault namespace.
Provider tokens are not written to Prism KV or a local Prism credential store.
They travel only through the authenticated loopback bridge and are encrypted
by `cvlt` before leaving the machine.

First-party production profiles use `vault.circles.ac`. Profiles configured
with `api-dev.circles.ac` and `auth-dev.circles.ac` use the separate development
Vault at `vault.crcl.es`:

```sh
prism chatgpt auth login --profile dev-personal
prism chatgpt auth list --profile dev-personal
```

ChatGPT account selection remains automatic in the Worker, so the CLI
intentionally has no `set-default` command.

## Build and test

```sh
go test -mod=vendor -race ./...
go vet -mod=vendor ./...
go build -mod=vendor -o prism .
```

The official Circles Go credential provider is pinned and vendored from the
public `github.com/circlesac/credentials-go` module for reproducible releases.
