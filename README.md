# sqlite

[![Doc](https://pkg.go.dev/badge/github.com/go-rio/sqlite.svg)](https://pkg.go.dev/github.com/go-rio/sqlite)
[![Go](https://img.shields.io/github/go-mod/go-version/go-rio/sqlite)](https://go.dev/)
[![Release](https://img.shields.io/github/release/go-rio/sqlite.svg)](https://github.com/go-rio/sqlite/releases)
[![Test](https://github.com/go-rio/sqlite/actions/workflows/test.yml/badge.svg)](https://github.com/go-rio/sqlite/actions/workflows/test.yml)
[![License](https://img.shields.io/github/license/go-rio/sqlite)](https://opensource.org/license/MIT)

SQLite driver module for [rio](https://github.com/go-rio/rio), backed by the
pure-Go [modernc.org/sqlite](https://pkg.go.dev/modernc.org/sqlite) driver:
connection defaults plus error translation; rio owns SQL rendering.

## Getting started

```sh
go get github.com/go-rio/sqlite
```

```go
db, err := sqlite.Open("app.db")
if err != nil {
	log.Fatal(err)
}
defer db.Close()

users, err := rio.From[User]().Where("age > ?", 18).All(ctx, db)
```

## DSN defaults

`Open` appends these unless the DSN already sets the key; explicit values
win. `New` wraps an existing `*sql.DB` and does not modify the DSN.

| Parameter | Default | Effect |
|---|---|---|
| `_pragma=foreign_keys` | `1` | Enforces foreign keys; enables `rio.ErrForeignKeyViolated` translation. |
| `_pragma=busy_timeout` | `5000` | Waits up to five seconds on a locked database before `SQLITE_BUSY`. |
| `_time_format` | `sqlite` | Makes directly bound `time.Time` values readable by SQLite date functions. |

rio encodes its own `time.Time` writes independently. `_texttotime` and
`_inttotime` stay opt-in because they can turn an `INTEGER` scan value into
`time.Time`.

## Pools and write behavior

`Open` and `New` leave connection limits to the caller. A plain `:memory:`
DSN gives each pooled connection its own private empty database; to share
one in-memory database, use a shared-cache DSN and one connection:

```go
db, _ := sqlite.Open("file:app?mode=memory&cache=shared")
db.Unwrap().SetMaxOpenConns(1)
```

SQLite permits one writer at a time; a single connection serializes writes.
For concurrent readers and writers, use a file-backed database with
immediate write transactions, WAL, and a longer busy timeout:

```go
db, err := sqlite.Open("app.db?_txlock=immediate&_pragma=journal_mode(WAL)&_pragma=busy_timeout(10000)")
```

WAL is persistent and creates `-wal`/`-shm` sidecar files, so `Open` does
not enable it by default; check filesystem support first.

The dialect omits `FOR UPDATE` because SQLite locks writes at the database
level. A single insert uses `LastInsertId` when only its generated key needs
backfill and `RETURNING` for omitted default columns. Upserts use
`ON CONFLICT`.

## Error translation

| SQLite error | rio error |
|---|---|
| Unique or primary key violation | `rio.ErrDuplicateKey` |
| Foreign key violation | `rio.ErrForeignKeyViolated` |

The driver's `*sqlite.Error` stays available through `errors.As`.

## Contributing

Use Go 1.27 or newer, then run `go test ./...`, `go test -race ./...`, and
`go vet ./...` before opening a pull request.

## License

[MIT](LICENSE)
