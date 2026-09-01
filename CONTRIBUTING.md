# Contributing to go-rio/sqlite

## Prerequisites

- Go 1.27 or newer. The driver is pure Go: no C toolchain, no server.

## Setup

```sh
git clone https://github.com/go-rio/sqlite
cd sqlite
go build ./...
```

## Tests

The whole suite runs in-process against temporary and in-memory databases:

```sh
go vet ./...
go test ./...
go test -race ./...
```

## Pull requests

- Every change ships with a test; one test file per source file
  (`sqlite.go` ↔ `sqlite_test.go`).
- Comments state contracts. Exported identifiers get a doc comment naming
  purpose, constraints, and error cases; internal comments are one line, two
  at most; no history or narrative.
- Commit subjects carry a conventional prefix (`feat:`, `fix:`, `docs:`,
  `test:`, `chore:`).
- Keep `gofmt` and `go vet` clean.

## Releases

Maintainers tag signed releases (`git tag -s vX.Y.Z`) after the rio core
version they depend on is tagged, and record every user-visible change in
[CHANGELOG.md](CHANGELOG.md).
