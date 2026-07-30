# Circles credentials for Go

Official Go implementation of the
[Circles credential-provider contract](https://github.com/circlesac/credentials-go/blob/main/credentials/SPEC.md).
The source of truth lives at `packages/credentials-go` in `circlesac/api` and
is published to the public `github.com/circlesac/credentials-go` module. The
package name is `credentials`.

```go
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
