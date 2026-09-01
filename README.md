# sqlite

[![Doc](https://pkg.go.dev/badge/github.com/go-rio/sqlite.svg)](https://pkg.go.dev/github.com/go-rio/sqlite)
[![Go](https://img.shields.io/github/go-mod/go-version/go-rio/sqlite)](https://go.dev/)
[![Release](https://img.shields.io/github/release/go-rio/sqlite.svg)](https://github.com/go-rio/sqlite/releases)
[![Test](https://github.com/go-rio/sqlite/actions/workflows/test.yml/badge.svg)](https://github.com/go-rio/sqlite/actions/workflows/test.yml)
[![License](https://img.shields.io/github/license/go-rio/sqlite)](https://opensource.org/license/MIT)

SQLite driver module for [rio](https://github.com/go-rio/rio), backed by the
pure-Go [modernc.org/sqlite](https://pkg.go.dev/modernc.org/sqlite) driver:
connection defaults, error translation, and a prepared-statement cache that
is on by default. rio renders the SQL.

```go
db, err := sqlite.Open("app.db")
if err != nil {
	return err
}
defer db.Close()

if err := rio.Insert(ctx, db, &User{Email: "a@example.com", Age: 30}); err != nil {
	return err
}
users, err := rio.From[User]().Where("age > ?", 18).All(ctx, db)
```

## Getting started

```sh
go get github.com/go-rio/sqlite
```

```go
package main

import (
	"context"
	"log"

	"github.com/go-rio/rio"
	"github.com/go-rio/sqlite"
)

type User struct {
	ID    int64
	Email string
	Age   int
}

func main() {
	ctx := context.Background()
	db, err := sqlite.Open("app.db")
	if err != nil {
		log.Fatal(err)
	}
	defer db.Close()

	if err := rio.Insert(ctx, db, &User{Email: "a@example.com", Age: 30}); err != nil {
		log.Fatal(err)
	}
	users, err := rio.From[User]().Where("age > ?", 18).All(ctx, db)
	if err != nil {
		log.Fatal(err)
	}
	log.Printf("%d adults", len(users))
}
```

Requires Go 1.27; the driver is pure Go, so no C toolchain is involved.

## Features

### Constructors

`Open` appends the DSN defaults below, installs the error translator and the
statement cache, and does not connect: invalid paths surface on first use or
`db.Unwrap().Ping()`. `New` wraps an existing `*sql.DB` with the same dialect,
translator, and cache and does not modify the DSN.

### DSN defaults

`Open` appends these unless the DSN already sets the key; explicit values
win.

| Parameter | Default | Effect |
|---|---|---|
| `_pragma=foreign_keys` | `1` | Enforces foreign keys; enables `rio.ErrForeignKeyViolated` translation. |
| `_pragma=busy_timeout` | `5000` | Waits up to five seconds on a locked database before `SQLITE_BUSY`. |
| `_time_format` | `sqlite` | Makes directly bound `time.Time` values readable by SQLite date functions. |
| `_timezone` | `UTC` | Converts directly bound `time.Time` values to UTC before writing, so stored offsets stay uniform. |
| `_txlock` | `immediate` | Takes the write lock at `BEGIN`, so writers wait on `busy_timeout` instead of failing a deferred lock upgrade with `SQLITE_BUSY`. |

### Time values

rio writes its own `time.Time` values as `2006-01-02 15:04:05.999999+00:00`
text at UTC and reads that form back; the `_time_format` and `_timezone`
defaults make values bound through `db.Unwrap()` or a `driver.Valuer` land in
the same shape, so one `TEXT` column sorts and compares consistently.
`_texttotime` and `_inttotime` stay opt-in because they can turn an `INTEGER`
scan value into `time.Time`.

### Pools and write behavior

`Open` and `New` leave connection limits to the caller. A plain `:memory:`
DSN gives each pooled connection its own private empty database; to share
one in-memory database, use a shared-cache DSN and one connection:

```go
db, _ := sqlite.Open("file:app?mode=memory&cache=shared")
db.Unwrap().SetMaxOpenConns(1)
```

SQLite permits one writer at a time; a single connection serializes writes.
For concurrent readers and writers, use a file-backed database with WAL and
a longer busy timeout:

```go
db, err := sqlite.Open("app.db?_pragma=journal_mode(WAL)&_pragma=busy_timeout(10000)")
```

WAL is persistent and creates `-wal`/`-shm` sidecar files, so `Open` does
not enable it by default; check filesystem support first.

### Statement reuse

`Open` and `New` enable rio's bounded prepared-statement cache (one per
`*rio.DB` plus one per transaction); without it the driver re-prepares every
statement. Pass `rio.WithoutStmtCache()` to opt out:

```go
db, err := sqlite.Open("app.db", rio.WithoutStmtCache())
```

### Dialect behavior

Row locks (`ForUpdate`, `ForShare`) are elided because SQLite locks writes
at the database level. A single insert uses `LastInsertId` when only its
generated key needs backfill and `RETURNING` when omitted default or
readonly columns must be loaded; `InsertAll` backfills keys through
`RETURNING`, and `UpdateAllReturning`/`DeleteAllReturning` are available.
Upserts use `ON CONFLICT`. The bind ceiling is 999 parameters, so large key
sets chunk.

### Error translation

| SQLite error | rio error |
|---|---|
| Unique, primary key, or rowid violation | `rio.ErrDuplicateKey` |
| Foreign key violation | `rio.ErrForeignKeyViolated` |

The driver's `*sqlite.Error` stays available through `errors.As`.

## Contributing

Read [CONTRIBUTING.md](CONTRIBUTING.md): a clone and `go test ./...` is the
whole setup; the suite runs in-process.

## Contributors

Thanks to everyone who has filed issues and opened pull requests on
[go-rio/sqlite](https://github.com/go-rio/sqlite/graphs/contributors).

## License

The [MIT License](LICENSE). Copyright (c) 2026-now TreeNewBee.
