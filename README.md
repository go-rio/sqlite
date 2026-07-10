# sqlite

[![Doc](https://pkg.go.dev/badge/github.com/go-rio/sqlite.svg)](https://pkg.go.dev/github.com/go-rio/sqlite)
[![Go](https://img.shields.io/github/go-mod/go-version/go-rio/sqlite)](https://go.dev/)
[![Release](https://img.shields.io/github/release/go-rio/sqlite.svg)](https://github.com/go-rio/sqlite/releases)
[![Test](https://github.com/go-rio/sqlite/actions/workflows/test.yml/badge.svg)](https://github.com/go-rio/sqlite/actions/workflows/test.yml)
[![License](https://img.shields.io/github/license/go-rio/sqlite)](https://opensource.org/license/MIT)

SQLite driver module for [rio](https://github.com/go-rio/rio), built on the
pure-Go [modernc.org/sqlite](https://pkg.go.dev/modernc.org/sqlite) driver; no
cgo. Provides constructors, error translation, and DSN hygiene; all SQL grammar
lives in rio.

## Install

```sh
go get github.com/go-rio/sqlite
```

## Usage

```go
import (
	"github.com/go-rio/rio"
	"github.com/go-rio/sqlite"
)

db, err := sqlite.Open("app.db")
if err != nil {
	// ...
}
defer db.Close()

users, err := rio.From[User]().Where("age > ?", 18).All(ctx, db)
```

`New` wraps an existing `*sql.DB` (bring your own pool):

```go
sqlDB, err := sql.Open("sqlite", dsn) // modernc.org/sqlite
db := sqlite.New(sqlDB)
```

## Default DSN parameters

`Open` appends two pragmas and one driver parameter unless the DSN already sets
the same key.

| Parameter | Default | Why |
|---|---|---|
| `_pragma=foreign_keys` | `1` | SQLite ships with foreign key enforcement off; constraints parse but never fire. Without it, `rio.ErrForeignKeyViolated` never fires on SQLite. |
| `_pragma=busy_timeout` | `5000` | Concurrent writers wait up to five seconds for the lock instead of failing immediately with `SQLITE_BUSY`. |
| `_time_format` | `sqlite` | `time.Time` written outside rio (raw `database/sql` through `db.Unwrap()`) is stored as SQLite-parseable text instead of Go's `time.String()` form, keeping the column format uniform. rio's own writes use its canonical text encoding regardless. |

Override a default by setting the key yourself:

```go
db, err := sqlite.Open("app.db?_pragma=busy_timeout(10000)")
```

`New` never touches the DSN; configure these on the pool you pass in.

## In-memory databases

A plain `:memory:` DSN gives each pooled connection its own private empty
database (SQLite's behavior). Since `database/sql` opens several connections by
default, a table created on one is missing on the next. For a shared in-memory
store, use a shared cache and pin the pool to one connection:

```go
db, _ := sqlite.Open("file:app?mode=memory&cache=shared")
db.Unwrap().SetMaxOpenConns(1) // rio never tunes the pool for you
```

A file-backed DSN has no such caveat.

## Concurrent writes

SQLite allows one writer at a time; concurrent writers serialize on the
database lock. Two deployment shapes handle write concurrency.

**Configure the file for concurrency** (recommended for mixed read/write
load):

```go
db, err := sqlite.Open("app.db" +
	"?_txlock=immediate" +            // write txs take the lock up front: no
	                                  // non-retryable upgrade deadlocks
	"&_pragma=journal_mode(WAL)" +    // readers never block the writer
	"&_pragma=synchronous(NORMAL)" +  // WAL's recommended durability point
	"&_pragma=busy_timeout(10000)")
```

`journal_mode(WAL)` is persistent (it sticks to the database file) and creates
`-wal`/`-shm` sidecar files. It does not work on read-only media or most
network filesystems, which is why `Open` recommends rather than defaults it.

**Serialize writes yourself:** one connection, no contention, no busy handling.

```go
db.Unwrap().SetMaxOpenConns(1)
db.Unwrap().SetMaxIdleConns(1)
```

## Error translation

Unique and primary key violations return `rio.ErrDuplicateKey`; foreign key
violations return `rio.ErrForeignKeyViolated`. The driver's own `*sqlite.Error`
stays in the chain, so `errors.As` keeps working:

```go
if err := rio.Insert(ctx, db, &user); errors.Is(err, rio.ErrDuplicateKey) {
	// email already taken
}
```

## License

[MIT](LICENSE)
