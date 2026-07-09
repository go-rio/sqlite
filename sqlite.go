// Package sqlite connects rio, the zero-surprise Go ORM, to SQLite through
// the pure-Go modernc.org/sqlite driver — no cgo required.
//
// Driver modules are deliberately thin. This package contains exactly three
// things, and nothing else:
//
//   - Constructors: Open (from a DSN) and New (bring your own *sql.DB), both
//     returning a *rio.DB speaking the built-in rio.SQLite dialect.
//   - Precise error translation: SQLite constraint failures become
//     rio.ErrDuplicateKey and rio.ErrForeignKeyViolated, with the driver
//     error kept in the chain for errors.As.
//   - DSN hygiene: Open enables foreign key enforcement and a busy timeout
//     unless the DSN sets those pragmas itself.
//
// All SQL grammar lives in github.com/go-rio/rio; this package never
// implements a dialect.
package sqlite

import (
	"database/sql"
	"errors"
	"fmt"
	"net/url"
	"strings"

	"github.com/go-rio/rio"
	driver "modernc.org/sqlite"
	sqlite3 "modernc.org/sqlite/lib"
)

// driverName is the name modernc.org/sqlite registers with database/sql.
const driverName = "sqlite"

// Open opens a SQLite database and returns a *rio.DB speaking the rio.SQLite
// dialect with this package's error translator installed.
//
// Before handing the DSN to modernc.org/sqlite, Open appends the default
// pragmas described on defaultPragmas — foreign_keys(1) and
// busy_timeout(5000). A _pragma the DSN already sets is respected, never
// overridden.
//
// Like database/sql itself, Open validates nothing eagerly: a bad path or
// DSN surfaces on first use (or on an explicit Ping).
func Open(dsn string, opts ...rio.Option) (*rio.DB, error) {
	db, err := sql.Open(driverName, withDefaultPragmas(dsn))
	if err != nil {
		return nil, fmt.Errorf("sqlite: open: %w", err)
	}
	return New(db, opts...), nil
}

// New wraps an existing *sql.DB — bring your own connection pool — in a
// *rio.DB speaking the rio.SQLite dialect with this package's error
// translator installed. A rio.WithErrorTranslator among opts wins over the
// built-in translator.
//
// New performs no DSN hygiene, because the pool already exists. In
// particular, foreign key enforcement is whatever the caller's DSN says —
// and SQLite's historical default is off, in which case
// rio.ErrForeignKeyViolated can never happen (see Open).
func New(db *sql.DB, opts ...rio.Option) *rio.DB {
	return rio.New(db, rio.SQLite,
		append([]rio.Option{rio.WithErrorTranslator(translate)}, opts...)...)
}

// translate maps modernc.org/sqlite errors to rio's sentinel errors. It
// returns nil for errors it does not recognize, per the
// rio.WithErrorTranslator contract. rio keeps the driver error in the chain,
// so errors.As still reaches the *sqlite.Error after translation.
func translate(err error) error {
	var se *driver.Error
	if !errors.As(err, &se) {
		return nil
	}
	switch se.Code() {
	case sqlite3.SQLITE_CONSTRAINT_UNIQUE, // 2067
		sqlite3.SQLITE_CONSTRAINT_PRIMARYKEY: // 1555
		return rio.ErrDuplicateKey
	case sqlite3.SQLITE_CONSTRAINT_FOREIGNKEY: // 787
		return rio.ErrForeignKeyViolated
	}
	return nil
}

// defaultPragmas are the connection pragmas Open appends when the DSN does
// not set the same pragma itself. modernc.org/sqlite executes each
// _pragma=name(value) query parameter as PRAGMA name(value) on every new
// connection, so the defaults hold across the entire connection pool.
//
//   - foreign_keys(1): SQLite keeps foreign key enforcement OFF by default
//     for historical compatibility — FOREIGN KEY constraints parse but never
//     fire. Without this pragma, rio's promised rio.ErrForeignKeyViolated
//     would never happen on SQLite; turning it on is one of the core reasons
//     this package exists.
//   - busy_timeout(5000): without a busy timeout, a writer that finds the
//     database file locked by another connection fails immediately with
//     SQLITE_BUSY; with it, SQLite retries for up to five seconds first.
var defaultPragmas = [...]string{"busy_timeout(5000)", "foreign_keys(1)"}

// defaultParams are non-pragma driver parameters Open appends when the DSN
// does not set the same key itself.
//
//   - _time_format=sqlite: time.Time values bound directly (not through
//     rio, which binds canonical text itself) are stored in SQLite's own
//     datetime format instead of Go's time.Time.String() form, which
//     SQLite's date functions cannot parse. Pure write-side, no downside.
//
// The driver's read-side conversions (_texttotime=1, _inttotime=1) are NOT
// defaulted: they rewrite scanned values by declared column type, so an
// INTEGER unix-timestamp column mapped to an int64 field — a completely
// ordinary SQLite pattern — would arrive as time.Time and fail to scan.
// Add them to your DSN explicitly if code reading through Unwrap() wants
// time.Time out of DATETIME columns.
var defaultParams = [...][2]string{
	{"_time_format", "sqlite"},
}

// withDefaultPragmas appends the missing defaultPragmas to dsn. A pragma the
// user set explicitly — with any value — is respected and never duplicated.
// The user's DSN text is preserved byte for byte; defaults are only ever
// appended.
func withDefaultPragmas(dsn string) string {
	var rawQuery string
	pos := strings.IndexByte(dsn, '?')
	switch {
	case pos > 0:
		rawQuery = dsn[pos+1:]
	case pos == 0:
		// The driver reads a query part only after the first byte, so a DSN
		// that starts with '?' is a plain file name; appending anything would
		// rename the file. Leave it alone.
		return dsn
	}
	q, err := url.ParseQuery(rawQuery)
	if err != nil {
		// A malformed query cannot be amended safely. Pass it through
		// untouched; the driver reports the parse error on first use.
		return dsn
	}
	var add []string
	for _, def := range defaultPragmas {
		set := false
		for _, v := range q["_pragma"] {
			if pragmaName(v) == pragmaName(def) {
				set = true
				break
			}
		}
		if !set {
			add = append(add, "_pragma="+def)
		}
	}
	for _, def := range defaultParams {
		if !q.Has(def[0]) {
			add = append(add, def[0]+"="+def[1])
		}
	}
	if len(add) == 0 {
		return dsn
	}
	sep := "?"
	if pos > 0 {
		if rawQuery == "" {
			sep = "" // the DSN already ends in "?"
		} else {
			sep = "&"
		}
	}
	return dsn + sep + strings.Join(add, "&")
}

// pragmaName extracts the lower-cased pragma name from a _pragma value such
// as "busy_timeout(5000)", "foreign_keys(1)" or "journal_mode = WAL".
func pragmaName(v string) string {
	v = strings.TrimSpace(v)
	for i := 0; i < len(v); i++ {
		c := v[i]
		if c == '_' || c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' {
			continue
		}
		v = v[:i]
		break
	}
	return strings.ToLower(v)
}
