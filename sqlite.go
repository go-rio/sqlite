// Package sqlite is the SQLite driver module for rio, built on the pure-Go
// modernc.org/sqlite library and driving it directly. Open serves rio's
// native channel; OpenSQL serves a thin database/sql handle for tools such
// as go-rio/migrate. Constraint errors translate to rio sentinels with the
// SQLite Error retained in the chain.
//
// Importing the package turns on two process-wide library settings where
// the platform has them: database files take Linux OFD locks, which survive
// a close of another descriptor of the same file in the process, and
// commits sync with fdatasync(2). A set MODERNC_SQLITE_OFD_LOCK decides the
// lock kind instead; modernc.org/sqlite's OFDLocking, before the first
// connection, and Fdatasync switch either back.
package sqlite

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"net/url"
	"runtime"
	"slices"
	"strings"
	"sync"

	"github.com/go-rio/rio"
	sqlite3 "modernc.org/sqlite/lib"
)

// defaultPragmas apply to every connection unless the DSN sets them.
var defaultPragmas = [...]string{"busy_timeout(5000)", "foreign_keys(1)"}

// pragmaAliases maps each pragma with shorthand DSN keys to those keys.
var pragmaAliases = map[string][]string{
	"busy_timeout": {"_busy_timeout", "_timeout"},
	"auto_vacuum":  {"_auto_vacuum", "_vacuum"},
	"foreign_keys": {"_foreign_keys", "_fk"},
	"journal_mode": {"_journal_mode", "_journal"},
	"synchronous":  {"_synchronous", "_sync"},
	"query_only":   {"_query_only"},
}

var errPoolClosed = errors.New("sqlite: pool is closed")

// Open opens a SQLite database on rio's native channel: statements run
// directly against the embedded library, and every connection keeps a
// prepared-statement cache. Nothing opens until first use, so invalid paths
// surface then. rio.WithStmtCache is unsupported.
//
// The DSN is a path, ":memory:", or a file: URI whose parameters (mode,
// cache, vfs, immutable) SQLite reads. Driver parameters: _pragma
// (repeatable, "name(value)"), _txlock (immediate by default; deferred or
// exclusive), and the shorthands _busy_timeout, _auto_vacuum, _foreign_keys,
// _journal_mode, _synchronous, and _query_only; other underscore parameters
// are rejected, as is a pragma value holding more than one statement.
// busy_timeout(5000) and foreign_keys(1) apply unless set.
//
// Connections open on demand up to PoolOf(db).MaxConns, GOMAXPROCS at
// first; private memory, temporary, and shared-cache databases run on one
// connection. Unwrap returns a database/sql view with connections of its
// own to the same database, nil when no second connection can reach it.
func Open(dsn string, opts ...rio.Option) (*rio.DB, error) {
	cfg, err := parseDSN(dsn)
	if err != nil {
		return nil, fmt.Errorf("sqlite: open: %w", err)
	}
	p := &Pool{conn: cfg.conn, begin: cfg.begin, max: runtime.GOMAXPROCS(0)}
	if cfg.single {
		p.max = 1
	}
	var view *sql.DB
	if !cfg.private {
		view = sql.OpenDB(shimConnector{conn: cfg.conn, begin: cfg.begin})
	}
	merged := make([]rio.Option, 0, len(opts)+1)
	merged = append(merged, rio.WithErrorTranslator(translate))
	merged = append(merged, opts...)
	return rio.NewNative(rio.NativeConfig{DB: &nativeDB{p: p}, Handle: p, SQLView: view}, rio.SQLite, merged...), nil
}

// PoolOf returns the native pool behind db, nil for other constructions.
func PoolOf(db *rio.DB) *Pool {
	p, _ := db.Native().(*Pool)
	return p
}

// Pool is the native channel's connection pool. Connections open on demand
// up to the maximum and stay open, each with its own prepared-statement
// cache; acquirers beyond the maximum wait in arrival order.
type Pool struct {
	conn  connConfig
	begin string // BEGIN statement for read-write transactions

	mu      sync.Mutex
	idle    []*conn
	open    int
	max     int
	waiters []chan *conn // nil hands back: retry
	closed  bool
}

// MaxConns returns the connection cap.
func (p *Pool) MaxConns() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.max
}

// SetMaxConns caps open connections at n (at least 1); surplus connections
// close as they return. Private memory, temporary, and shared-cache
// databases must stay at 1: another connection reaches a different
// database, or contends on shared-cache locks the channel does not retry.
func (p *Pool) SetMaxConns(n int) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.max = max(n, 1)
}

// Close closes idle connections now and busy ones as they return.
func (p *Pool) Close() error {
	p.mu.Lock()
	p.closed = true
	idle, waiters := p.idle, p.waiters
	p.idle, p.waiters = nil, nil
	p.open -= len(idle)
	p.mu.Unlock()
	for _, ch := range waiters {
		ch <- nil
	}
	for _, c := range idle {
		c.close()
	}
	return nil
}

// acquire hands out an idle connection, opens one under the cap, or waits
// for a return.
func (p *Pool) acquire(ctx context.Context) (*conn, error) {
	p.mu.Lock()
	if p.closed {
		p.mu.Unlock()
		return nil, errPoolClosed
	}
	if n := len(p.idle); n > 0 {
		c := p.idle[n-1]
		p.idle = p.idle[:n-1]
		p.mu.Unlock()
		return c, nil
	}
	if p.open < p.max {
		p.open++
		p.mu.Unlock()
		c, err := openConn(&p.conn)
		if err != nil {
			p.mu.Lock()
			p.open--
			p.mu.Unlock()
			return nil, err
		}
		return c, nil
	}
	ch := make(chan *conn, 1)
	p.waiters = append(p.waiters, ch)
	p.mu.Unlock()
	select {
	case c := <-ch:
		if c == nil {
			return p.acquire(ctx)
		}
		return c, nil
	case <-ctx.Done():
		p.mu.Lock()
		i := slices.Index(p.waiters, ch)
		if i >= 0 {
			p.waiters = slices.Delete(p.waiters, i, i+1)
		}
		p.mu.Unlock()
		if i < 0 {
			// Handed a connection after all.
			if c := <-ch; c != nil {
				p.release(c)
			}
		}
		return nil, ctx.Err()
	}
}

func (p *Pool) release(c *conn) {
	p.mu.Lock()
	if len(p.waiters) > 0 {
		ch := p.waiters[0]
		p.waiters = slices.Delete(p.waiters, 0, 1)
		p.mu.Unlock()
		ch <- c
		return
	}
	if p.closed || p.open > p.max {
		p.open--
		p.mu.Unlock()
		c.close()
		return
	}
	p.idle = append(p.idle, c)
	p.mu.Unlock()
}

// dsnConfig is a parsed DSN.
type dsnConfig struct {
	conn    connConfig
	begin   string // BEGIN statement for read-write transactions
	single  bool   // one connection: private memory, temporary, or shared cache
	private bool   // no other connection reaches the same database
}

// parseDSN splits dsn into what sqlite3_open_v2 receives and what the
// module applies itself.
func parseDSN(dsn string) (dsnConfig, error) {
	name, rawQuery := dsn, ""
	if pos := strings.IndexByte(dsn, '?'); pos > 0 {
		rawQuery = dsn[pos+1:]
		if !strings.HasPrefix(dsn, "file:") {
			name = dsn[:pos]
		}
	}
	q, err := url.ParseQuery(rawQuery)
	if err != nil {
		return dsnConfig{}, err
	}
	for key := range q {
		if strings.HasPrefix(key, "_") && !dsnKey(key) {
			return dsnConfig{}, fmt.Errorf("unsupported DSN parameter %q", key)
		}
	}
	cfg := dsnConfig{begin: "BEGIN IMMEDIATE"}
	switch mode := strings.ToUpper(q.Get("_txlock")); mode {
	case "", "IMMEDIATE":
	case "DEFERRED":
		cfg.begin = "BEGIN"
	case "EXCLUSIVE":
		cfg.begin = "BEGIN EXCLUSIVE"
	default:
		return dsnConfig{}, fmt.Errorf("unknown _txlock %q", q.Get("_txlock"))
	}
	cfg.conn = connConfig{name: name, vfs: q.Get("vfs"), pragmas: dsnPragmas(q)}
	for _, p := range cfg.conn.pragmas {
		if !singleStatement(p) {
			return dsnConfig{}, fmt.Errorf("DSN pragma %q is more than one statement", p)
		}
	}
	path := strings.TrimPrefix(name, "file:")
	if pos := strings.IndexByte(path, '?'); pos >= 0 {
		path = path[:pos]
	}
	memory := path == ":memory:" || q.Get("mode") == "memory"
	shared := q.Get("cache") == "shared"
	cfg.private = path == "" || memory && !shared
	cfg.single = cfg.private || shared
	return cfg, nil
}

// dsnKey reports whether key is a driver parameter.
func dsnKey(key string) bool {
	if key == "_pragma" || key == "_txlock" {
		return true
	}
	for _, keys := range pragmaAliases {
		if slices.Contains(keys, key) {
			return true
		}
	}
	return false
}

// dsnPragmas renders the PRAGMA statements for q: the defaults it leaves
// unset, then the shorthands and the _pragma list in the order the modernc
// driver applies them.
func dsnPragmas(q url.Values) []string {
	var out []string
	for _, def := range defaultPragmas {
		if !pragmaSet(q, pragmaName(def)) {
			out = append(out, "PRAGMA "+def)
		}
	}
	shorthand := func(pragma string) {
		for _, key := range pragmaAliases[pragma] {
			if v := q.Get(key); v != "" {
				out = append(out, "PRAGMA "+pragma+" = "+v)
				return
			}
		}
	}
	shorthand("busy_timeout")
	shorthand("auto_vacuum")
	for _, v := range q["_pragma"] {
		out = append(out, "PRAGMA "+v)
	}
	shorthand("foreign_keys")
	shorthand("journal_mode")
	shorthand("synchronous")
	shorthand("query_only")
	return out
}

// pragmaSet reports whether q sets the pragma name through _pragma or a
// shorthand key.
func pragmaSet(q url.Values, name string) bool {
	named := func(v string) bool { return pragmaName(v) == name }
	return slices.ContainsFunc(q["_pragma"], named) || slices.ContainsFunc(pragmaAliases[name], q.Has)
}

// pragmaName extracts the lower-cased name from a "name(value)" pragma entry.
func pragmaName(v string) string {
	v = strings.TrimSpace(v)
	for i := range len(v) {
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

// singleStatement reports whether SQLite's tokenizer finds nothing but blanks,
// semicolons, and comments after the first statement of sql, read up to a NUL.
func singleStatement(sql string) bool {
	sql, _, _ = strings.Cut(sql, "\x00")
	ended := false
	for i := 0; i < len(sql); {
		c := sql[i]
		switch {
		case c == '-' && strings.HasPrefix(sql[i:], "--"):
			n := strings.IndexByte(sql[i:], '\n')
			if n < 0 {
				return true
			}
			i += n + 1
		case c == '/' && i+2 < len(sql) && sql[i+1] == '*':
			n := strings.Index(sql[i+2:], "*/")
			if n < 0 {
				return true
			}
			i += n + 4
		case c == ';':
			ended = true
			i++
		case c == ' ' || c == '\t' || c == '\n' || c == '\f' || c == '\r':
			i++
		case ended:
			return false
		case c == '\'' || c == '"' || c == '`' || c == '[':
			// A doubled quote lexes as two adjacent quoted tokens.
			end := c
			if c == '[' {
				end = ']'
			}
			n := strings.IndexByte(sql[i+1:], end)
			if n < 0 {
				return true
			}
			i += n + 2
		default:
			i++
		}
	}
	return true
}

// translate maps constraint errors to rio sentinels, nil otherwise.
func translate(err error) error {
	var se *Error
	if !errors.As(err, &se) {
		return nil
	}
	switch se.Code() {
	case sqlite3.SQLITE_CONSTRAINT_UNIQUE,
		sqlite3.SQLITE_CONSTRAINT_PRIMARYKEY,
		sqlite3.SQLITE_CONSTRAINT_ROWID:
		return rio.ErrDuplicateKey
	case sqlite3.SQLITE_CONSTRAINT_FOREIGNKEY:
		return rio.ErrForeignKeyViolated
	}
	return nil
}
