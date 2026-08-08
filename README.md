# sqlite

[![Doc](https://pkg.go.dev/badge/github.com/go-rio/sqlite.svg)](https://pkg.go.dev/github.com/go-rio/sqlite)
[![Go](https://img.shields.io/github/go-mod/go-version/go-rio/sqlite)](https://go.dev/)
[![Release](https://img.shields.io/github/release/go-rio/sqlite.svg)](https://github.com/go-rio/sqlite/releases)
[![Test](https://github.com/go-rio/sqlite/actions/workflows/test.yml/badge.svg)](https://github.com/go-rio/sqlite/actions/workflows/test.yml)
[![License](https://img.shields.io/github/license/go-rio/sqlite)](https://opensource.org/license/MIT)

SQLite driver module for [rio](https://github.com/go-rio/rio), backed by the
pure-Go [modernc.org/sqlite](https://pkg.go.dev/modernc.org/sqlite) driver. It
adds SQLite connection defaults and error translation; rio owns SQL rendering.

## Getting started

```sh
go get github.com/go-rio/sqlite
```

```go
import (
	"github.com/go-rio/rio"
	"github.com/go-rio/sqlite"
)

db, err := sqlite.Open("app.db")
if err != nil {
	log.Fatal(err)
}
defer db.Close()

users, err := rio.From[User]().Where("age > ?", 18).All(ctx, db)
```

## DSN defaults

`Open` appends two pragmas and one driver parameter unless the DSN already sets
that key:

| Parameter | Default | Why |
|---|---|---|
| `_pragma=foreign_keys` | `1` | Enforces foreign keys and enables `rio.ErrForeignKeyViolated` translation. |
| `_pragma=busy_timeout` | `5000` | Waits up to five seconds on a locked database before returning `SQLITE_BUSY`. |
| `_time_format` | `sqlite` | Makes `time.Time` values bound directly through `database/sql` readable by SQLite date functions. |

Explicit values win:

```go
db, err := sqlite.Open("app.db?_pragma=busy_timeout(10000)")
```

rio encodes its own `time.Time` writes independently. `Open` leaves
`_texttotime` and `_inttotime` opt-in because they can turn an `INTEGER` scan
value into `time.Time`. `New` does not modify the DSN.

## Pools and in-memory databases

`Open` and `New` leave connection limits to the caller. Use
`sqlite.New(sqlDB)` to wrap an existing `*sql.DB` without changing it.

A plain `:memory:` DSN gives each pooled connection its own private empty
database. Use a shared-cache DSN and one open connection when all operations
must see the same in-memory state:

```go
db, _ := sqlite.Open("file:app?mode=memory&cache=shared")
db.Unwrap().SetMaxOpenConns(1)
```

## SQLite write behavior

SQLite permits one writer at a time. A single connection serializes writes:

```go
db.Unwrap().SetMaxOpenConns(1)
```

For concurrent readers and writers, use a file-backed database with immediate
write transactions, WAL, and a suitable busy timeout:

```go
db, err := sqlite.Open(
	"app.db?_txlock=immediate&_pragma=journal_mode(WAL)&_pragma=busy_timeout(10000)",
)
```

WAL is persistent and creates `-wal` and `-shm` sidecar files, so `Open` does
not enable it by default; check filesystem support before using it.

rio's SQLite dialect omits `FOR UPDATE` because SQLite locks writes at the
database level. A single insert uses `LastInsertId` when only its generated key
needs backfill and keeps `RETURNING` for omitted default columns. Upserts use
`ON CONFLICT`.

## Error translation

| SQLite error | rio error |
|---|---|
| Unique or primary key violation | `rio.ErrDuplicateKey` |
| Foreign key violation | `rio.ErrForeignKeyViolated` |

The driver's `*sqlite.Error` remains available through `errors.As`.

## Contributing

Use Go 1.27 or newer, then run `go test ./...`, `go test -race ./...`, and
`go vet ./...` before opening a pull request.

## License

[MIT](LICENSE)
