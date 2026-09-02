# sqlite

[![Doc](https://pkg.go.dev/badge/github.com/go-rio/sqlite.svg)](https://pkg.go.dev/github.com/go-rio/sqlite)
[![Go](https://img.shields.io/github/go-mod/go-version/go-rio/sqlite)](https://go.dev/)
[![Release](https://img.shields.io/github/release/go-rio/sqlite.svg)](https://github.com/go-rio/sqlite/releases)
[![Test](https://github.com/go-rio/sqlite/actions/workflows/test.yml/badge.svg)](https://github.com/go-rio/sqlite/actions/workflows/test.yml)
[![License](https://img.shields.io/github/license/go-rio/sqlite)](https://opensource.org/license/MIT)

SQLite driver module for [rio](https://github.com/go-rio/rio). It drives the
pure-Go [modernc.org/sqlite](https://pkg.go.dev/modernc.org/sqlite) library
directly on rio's native channel: no database/sql in the path, a
prepared-statement cache on every connection, and rows decoded by storage
class straight into rio's scan cells. `OpenSQL` adds a thin database/sql
handle for tools such as [go-rio/migrate](https://github.com/go-rio/migrate).
rio renders the SQL.

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

Requires Go 1.27; the library is pure Go, so no C toolchain is involved.

## Features

### Constructors

`Open` parses the DSN and returns a `*rio.DB` on rio's native channel.
Nothing opens until first use, so an invalid path surfaces then.
`rio.WithStmtCache` is unsupported: every connection already caches its
prepared statements.

`OpenSQL` returns a plain `*sql.DB` over the same connection layer, one
SQLite connection per database/sql connection. Transactions, prepared
statements, affected-row counts, and `LastInsertId` behave as with any
driver. `db.Unwrap()` on an `Open` handle serves the same kind of view with
connections of its own, and is nil for private memory and temporary
databases, which no second connection can reach.

### DSN

A path, `:memory:`, or a `file:` URI. SQLite reads the URI parameters
(`mode`, `cache`, `vfs`, `immutable`, ...). The module reads these:

| Parameter | Effect |
|---|---|
| `_pragma=name(value)` | Runs `PRAGMA name(value)` on every new connection; repeatable. |
| `_txlock` | `BEGIN` mode of read-write transactions: `immediate` (default), `deferred`, or `exclusive`. |
| `_busy_timeout` (`_timeout`), `_auto_vacuum` (`_vacuum`), `_foreign_keys` (`_fk`), `_journal_mode` (`_journal`), `_synchronous` (`_sync`), `_query_only` | Shorthand for the pragma of the same name. |

`busy_timeout(5000)` and `foreign_keys(1)` apply unless the DSN sets them
through either form. Pragmas run in the modernc driver's order:
`busy_timeout`, `auto_vacuum`, the `_pragma` list, `foreign_keys`,
`journal_mode`, `synchronous`, `query_only`. Any other underscore parameter
is rejected.

### Connections

Connections open on demand up to `PoolOf(db).MaxConns()`, `GOMAXPROCS` by
default, and stay open with their statement caches; `SetMaxConns` changes
the cap at any time, and acquirers beyond it wait in arrival order until
their context ends. Private memory databases (`:memory:`, `mode=memory`),
temporary databases (an empty DSN), and shared-cache DSNs run on one
connection: a second connection would reach a different database, or
contend on shared-cache locks the module does not retry.

SQLite permits one writer at a time. For concurrent readers and writers,
use a file-backed database with WAL and a longer busy timeout:

```go
db, err := sqlite.Open("app.db?_journal_mode=WAL&_busy_timeout=10000")
sqlite.PoolOf(db).SetMaxConns(8)
```

WAL is persistent and creates `-wal`/`-shm` sidecar files, so it is not on
by default; check filesystem support first.

### Transactions and cancellation

Transactions begin in the DSN's `_txlock` mode, so writers wait on
`busy_timeout` at `BEGIN` instead of failing a deferred lock upgrade with
`SQLITE_BUSY`; read-only transactions begin deferred. Every isolation level
is accepted, SQLite being serializable. A `COMMIT` that fails leaves the
transaction open in SQLite, so the connection rolls back before it returns
to the pool. Nested `rio.Tx` calls are savepoints.

A canceled context interrupts the running statement through
`sqlite3_interrupt`; the statement reports the context's error.

### Values

Arguments bind as SQLite storage classes: integers, floats, text, blobs,
`bool` as 0/1, `time.Time` as `2006-01-02 15:04:05.999999+00:00` text at
UTC, nil as NULL; other Go types go through database/sql's default
conversion first (`driver.Valuer`, pointers, narrower integers).

Rows decode by storage class. TEXT read into a `time.Time` field, or from a
column declared `DATE`, `DATETIME`, or `TIMESTAMP`, parses SQLite's
date-time forms (a date, with an optional time, fraction, and `Z` or `±HH:MM`
offset) to UTC; other text stays a string. Blobs are copied into the
destination.

### Dialect behavior

Row locks (`ForUpdate`, `ForShare`) are elided because SQLite locks writes
at the database level. A single insert backfills a lone generated key from
`sqlite3_last_insert_rowid` and uses `RETURNING` when omitted default or
readonly columns must be loaded; `InsertAll` backfills keys through
`RETURNING`, and `UpdateAllReturning`/`DeleteAllReturning` are available.
Upserts use `ON CONFLICT`. The bind ceiling is 999 parameters, so large key
sets chunk.

### Error translation

| SQLite error | rio error |
|---|---|
| Unique, primary key, or rowid violation | `rio.ErrDuplicateKey` |
| Foreign key violation | `rio.ErrForeignKeyViolated` |

Every SQLite failure is a `*sqlite.Error` reachable through `errors.As`;
`Code` is the extended result code.

### Migrations

`OpenSQL` is the handle [go-rio/migrate](https://github.com/go-rio/migrate)
consumes:

```go
sqlDB, err := sqlite.OpenSQL("app.db")
m, err := migrate.New(sqlDB, migrate.SQLite)
```

## Contributing

Read [CONTRIBUTING.md](CONTRIBUTING.md): a clone and `go test ./...` is the
whole setup; the suite runs in-process.

## Contributors

Thanks to everyone who has filed issues and opened pull requests on
[go-rio/sqlite](https://github.com/go-rio/sqlite/graphs/contributors).

## License

The [MIT License](LICENSE). Copyright (c) 2026-now TreeNewBee.
