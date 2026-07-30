# Circles credentials for Go

Official Go implementation of the
[Circles credential-provider contract](../../credentials/SPEC.md). The module
path is `github.com/circlesac/api/packages/credentials-go` and the package name
is `credentials`.

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
