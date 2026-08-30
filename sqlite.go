// Package sqlite connects rio to SQLite through the pure-Go modernc.org/sqlite
// driver. It adds DSN defaults and translates SQLite constraint errors to rio
// sentinel errors while retaining the driver error in the chain.
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

const driverName = "sqlite"

// Open opens a SQLite database with the rio.SQLite dialect and SQLite error
// translation. It adds foreign_keys(1), busy_timeout(5000), and
// _time_format=sqlite unless the DSN already specifies them. Invalid paths
// and DSNs surface on first use or Ping.
//
// Each connection to a plain ":memory:" database is isolated; to share one,
// use "file:app?mode=memory&cache=shared" and SetMaxOpenConns(1).
func Open(dsn string, opts ...rio.Option) (*rio.DB, error) {
	db, err := sql.Open(driverName, withDefaultPragmas(dsn))
	if err != nil {
		return nil, fmt.Errorf("sqlite: open: %w", err)
	}
	return New(db, opts...), nil
}

// New wraps db with the rio.SQLite dialect and SQLite error translation. It
// does not configure the pool or enable foreign key enforcement. A
// rio.WithErrorTranslator option overrides the built-in translator.
func New(db *sql.DB, opts ...rio.Option) *rio.DB {
	return rio.New(db, rio.SQLite,
		append([]rio.Option{rio.WithErrorTranslator(translate)}, opts...)...)
}

// translate maps constraint errors to rio sentinels, nil otherwise.
func translate(err error) error {
	var se *driver.Error
	if !errors.As(err, &se) {
		return nil
	}
	switch se.Code() {
	case sqlite3.SQLITE_CONSTRAINT_UNIQUE,
		sqlite3.SQLITE_CONSTRAINT_PRIMARYKEY:
		return rio.ErrDuplicateKey
	case sqlite3.SQLITE_CONSTRAINT_FOREIGNKEY:
		return rio.ErrForeignKeyViolated
	}
	return nil
}

// The driver applies these to every connection.
var defaultPragmas = [...]string{"busy_timeout(5000)", "foreign_keys(1)"}

// _time_format=sqlite makes bound time.Time readable by SQLite date functions;
// read-side conversion stays opt-in (it can turn INTEGER scans into time.Time).
var defaultParams = [...][2]string{
	{"_time_format", "sqlite"},
}

// withDefaultPragmas appends missing defaults without overriding explicit
// values. It rewrites an empty DSN as "file:" so the driver parses the query.
func withDefaultPragmas(dsn string) string {
	if dsn == "" {
		dsn = "file:"
	}
	var rawQuery string
	pos := strings.IndexByte(dsn, '?')
	switch {
	case pos > 0:
		rawQuery = dsn[pos+1:]
	case pos == 0:
		// A leading '?' is part of the file name, not a query delimiter.
		return dsn
	}
	q, err := url.ParseQuery(rawQuery)
	if err != nil {
		// Preserve malformed DSNs for the driver to reject.
		return dsn
	}
	var add []string
	for _, def := range defaultPragmas {
		isSet := false
		for _, v := range q["_pragma"] {
			if pragmaName(v) == pragmaName(def) {
				isSet = true
				break
			}
		}
		if !isSet {
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
		sep = "&"
		if rawQuery == "" {
			sep = "" // the DSN already ends in "?"
		}
	}
	return dsn + sep + strings.Join(add, "&")
}

func pragmaName(v string) string {
	v = strings.TrimSpace(v)
	for i := 0; i < len(v); i++ {
		c := v[i]
		isLetter := c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z'
		isDigit := c >= '0' && c <= '9'
		if c == '_' || isLetter || isDigit {
			continue
		}
		v = v[:i]
		break
	}
	return strings.ToLower(v)
}
