# Circles credentials for Go

Official Go implementation of the
[Circles credential-provider contract](https://github.com/circlesac/credentials/blob/main/SPEC.md).
The package name is `credentials` and the module is published as
`github.com/circlesac/credentials-go`.

```go
import "github.com/circlesac/credentials-go"

provider, err := credentials.New()
if err != nil {
    return err
}
credential, err := provider.Resolve(ctx)
if err != nil {
    return err
}

request.Header.Set("Authorization", "Bearer "+credential.Value)
```

The provider reads canonical and compatibility environment variables and named
profiles, migrates legacy `crcl` profiles without deleting them, refreshes OAuth
tokens directly, and uses atomic credential writes with a cross-process lock.
`SetCurrentProfile` shares the selected profile across Circles clients while
explicit and environment-selected profiles keep precedence.

The language-neutral cases under `schemas/` mirror
[`circlesac/credentials/schemas`](https://github.com/circlesac/credentials/tree/main/schemas).
CI rejects drift between the canonical cases and this Go module's copy.

## Development

```sh
go test -race ./...
go vet ./...
```

Releases use root `v*` tags.
