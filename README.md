# prism-cli

Public Go CLI for Prism credential registration and account management.

The CLI resolves Circles authentication through the official
[`credentials-go`](https://github.com/circlesac/credentials-go)
provider. It never invokes `crcl`, accepts no credential value on the command
line, and preserves the shared `~/.crcl` profile and environment precedence.

```console
$ go install github.com/circlesac/prism-cli/cmd/prism@latest
```

```console
$ prism auth status
Authenticated with jwt from profile "default".

$ prism auth status --profile dev
Authenticated with api_key from profile "dev".
```

Only non-secret credential kind and source metadata are printed. Provider
credential registration and Vault-backed account commands are tracked in
[`circlesac/prism#19`](https://github.com/circlesac/prism/issues/19).
