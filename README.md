# sqlite

[![Doc](https://pkg.go.dev/badge/github.com/go-rio/sqlite)](https://pkg.go.dev/github.com/go-rio/sqlite)
[![Go](https://img.shields.io/github/go-mod/go-version/go-rio/sqlite)](https://go.dev/)
[![Test](https://github.com/go-rio/sqlite/actions/workflows/test.yml/badge.svg)](https://github.com/go-rio/sqlite/actions)
[![License](https://img.shields.io/github/license/go-rio/sqlite)](https://opensource.org/license/MIT)

SQLite driver module for [rio](https://github.com/go-rio/rio), the
zero-surprise Go ORM. Built on the pure-Go
[modernc.org/sqlite](https://pkg.go.dev/modernc.org/sqlite) driver — no cgo.

Driver modules are deliberately thin: constructors, precise error translation
and DSN hygiene. All SQL grammar lives in rio itself.

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

Bring your own `*sql.DB` (and pool configuration) with `New`:

```go
sqlDB, err := sql.Open("sqlite", dsn) // modernc.org/sqlite
db := sqlite.New(sqlDB)
```

## Default DSN parameters

`Open` appends two pragmas and one driver parameter to the DSN unless the DSN
already sets the same key itself:

| Parameter | Default | Why |
|---|---|---|
| `_pragma=foreign_keys` | `1` | SQLite ships with foreign key enforcement off — constraints parse but never fire. Without this pragma, `rio.ErrForeignKeyViolated` could never happen on SQLite. |
| `_pragma=busy_timeout` | `5000` | Concurrent writers wait up to five seconds for the database lock instead of failing immediately with `SQLITE_BUSY`. |
| `_time_format` | `sqlite` | Anything writing `time.Time` outside rio (raw `database/sql` through `db.Unwrap()`) stores SQLite-parseable text instead of Go's `time.String()` form, keeping the column format uniform. rio's own writes always use its canonical text encoding, with or without this parameter. |

Set any key yourself to override a default:

```go
db, err := sqlite.Open("app.db?_pragma=busy_timeout(10000)")
```

`New` never touches the DSN — configure pragmas and driver parameters on the
pool you pass in.

## In-memory databases

A plain `:memory:` DSN gives **each pooled connection its own private empty
database** — SQLite's own behavior, and a classic footgun, because
`database/sql` opens several connections by default: a table created on one is
missing on the next. For an in-memory database that acts like a single shared
store, use a shared cache and pin the pool to one connection:

```go
db, _ := sqlite.Open("file:app?mode=memory&cache=shared")
db.Unwrap().SetMaxOpenConns(1) // rio never tunes the pool for you
```

A file-backed DSN has no such caveat.

## Error translation

Unique and primary key violations come back as `rio.ErrDuplicateKey`, foreign
key violations as `rio.ErrForeignKeyViolated`. The driver's own
`*sqlite.Error` stays in the chain, so `errors.As` keeps working:

```go
if err := rio.Insert(ctx, db, &user); errors.Is(err, rio.ErrDuplicateKey) {
	// email already taken
}
```

## License

[MIT](LICENSE)
