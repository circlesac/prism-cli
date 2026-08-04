# Circles credentials

Official shared credential-provider implementations for Circles clients. Every
language package follows the canonical
[Circles credential-provider contract](https://github.com/circlesac/crcl-cli/blob/main/docs/credentials/SPEC.md)
and runs the same versioned JSON cases.

| Package | Runtime | Distribution |
| --- | --- | --- |
| [`packages/credentials`](packages/credentials) | Node.js and Bun | [`@circlesac/credentials`](https://www.npmjs.com/package/@circlesac/credentials) |
| [repository root](.) | Go | [`go.circles.ac/credentials`](https://pkg.go.dev/go.circles.ac/credentials) |

The canonical specification and cases live under `docs/credentials` in
`circlesac/crcl-cli`. This repository mirrors the JSON cases under
`credentials/` so every implementation can test without a network dependency.

New language implementations belong under `packages/credentials-<language>`
and must execute the same contract cases. A client should consume one of these
packages instead of reimplementing profile selection, migration, refresh, or
credential storage.

The Go module is the repository root because `go.circles.ac/credentials` is a
vanity module path that supports Go 1.22 and later. The Worker under `worker/`
maps that path directly to this repository. Keeping the module in a nested
package would require the optional `go-import` subdirectory field introduced
in Go 1.25.

```go
import "go.circles.ac/credentials"
```

## Development

```sh
cd packages/credentials
bun install --frozen-lockfile
bun run typecheck
bun run test
bun run build

cd ../..
go test -race ./...
go vet ./...

cd worker
bun install --frozen-lockfile
bun run test
bun run check
```

## Go import Worker

`worker/` owns the `go.circles.ac` custom domain and serves the vanity import
metadata. Deploy it from this repository after the Worker checks pass:

```sh
cd worker
bun run deploy
```

Go releases use root `v*` tags. Node package releases use
`packages/credentials/v*` tags so the two package versions can advance
independently.
