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

## Default pragmas

`Open` appends two pragmas to the DSN unless the DSN already sets the same
pragma itself:

| Pragma | Default | Why |
|---|---|---|
| `foreign_keys` | `1` | SQLite ships with foreign key enforcement off — constraints parse but never fire. Without this pragma, `rio.ErrForeignKeyViolated` could never happen on SQLite. |
| `busy_timeout` | `5000` | Concurrent writers wait up to five seconds for the database lock instead of failing immediately with `SQLITE_BUSY`. |

Set either `_pragma` yourself to override a default:

```go
db, err := sqlite.Open("app.db?_pragma=busy_timeout(10000)")
```

`New` never touches pragmas — configure them in the DSN of the pool you pass
in.

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
