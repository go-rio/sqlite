package sqlite

import (
	"context"
	"database/sql/driver"
	"strconv"
	"strings"
	"sync"
	"time"
	"unsafe"

	"modernc.org/libc"
	sqlite3 "modernc.org/sqlite/lib"
)

// stmtCacheSize bounds each connection's prepared-statement cache.
const stmtCacheSize = 512

// timeFormat is rio's canonical SQLite time text; raw time.Time arguments
// bind through it at UTC.
const timeFormat = "2006-01-02 15:04:05.999999+00:00"

// bindTransient is SQLITE_TRANSIENT: SQLite copies bound text and blobs
// before the bind call returns, so they can live on the libc stack.
const bindTransient = ^uintptr(0)

// openFlags omits the per-connection mutex: the pool hands a connection to
// one goroutine at a time, and sqlite3_interrupt is safe without it.
const openFlags = sqlite3.SQLITE_OPEN_READWRITE | sqlite3.SQLITE_OPEN_CREATE |
	sqlite3.SQLITE_OPEN_NOMUTEX | sqlite3.SQLITE_OPEN_URI

// Error is a SQLite failure reported by the native channel.
type Error struct {
	code int
	msg  string
}

// Error returns the SQLite message followed by the extended result code.
func (e *Error) Error() string { return "sqlite: " + e.msg + " (" + strconv.Itoa(e.code) + ")" }

// Code returns the extended result code, for example SQLITE_CONSTRAINT_UNIQUE.
func (e *Error) Code() int { return e.code }

// connConfig is what openConn needs from a DSN.
type connConfig struct {
	name    string // sqlite3_open_v2 filename; URIs keep their query
	vfs     string
	pragmas []string // complete PRAGMA statements, run in order after open
}

// conn is one SQLite connection: libc thread state, the database handle,
// and a prepared-statement cache. One goroutine uses it at a time; only
// interrupt runs concurrently.
type conn struct {
	tls   *libc.TLS
	db    uintptr
	out   [2]uintptr // sqlite3_prepare_v2 out-parameters: statement, tail
	stmts map[string]*stmt
	// LRU order, head most recently used.
	head, tail *stmt
	// intMu lets unwatch wait for an interrupt already running.
	intMu sync.Mutex
}

func openConn(cfg *connConfig) (*conn, error) {
	c := &conn{tls: libc.NewTLS(), stmts: make(map[string]*stmt)}
	name, err := libc.CString(cfg.name)
	if err != nil {
		c.tls.Close()
		return nil, err
	}
	defer libc.Xfree(c.tls, name)
	var vfs uintptr
	if cfg.vfs != "" {
		if vfs, err = libc.CString(cfg.vfs); err != nil {
			c.tls.Close()
			return nil, err
		}
		defer libc.Xfree(c.tls, vfs)
	}
	rc := sqlite3.Xsqlite3_open_v2(c.tls, name, uintptr(unsafe.Pointer(&c.out[0])), openFlags, vfs)
	c.db = c.out[0]
	if rc != sqlite3.SQLITE_OK {
		err := c.err()
		c.close()
		return nil, err
	}
	sqlite3.Xsqlite3_extended_result_codes(c.tls, c.db, 1)
	for _, p := range cfg.pragmas {
		if _, _, err := c.exec(context.Background(), p, nil); err != nil {
			c.close()
			return nil, err
		}
	}
	return c, nil
}

// close finalizes cached statements and releases the handle and thread state.
func (c *conn) close() {
	for s := c.head; s != nil; s = s.next {
		sqlite3.Xsqlite3_finalize(c.tls, s.p)
	}
	if c.db != 0 {
		sqlite3.Xsqlite3_close_v2(c.tls, c.db)
		c.db = 0
	}
	c.tls.Close()
}

// exec runs every statement in sql, each taking its parameters from args
// in order, and reports the change count and last inserted row id after
// the final one.
func (c *conn) exec(ctx context.Context, sql string, args []any) (changes, lastID int64, err error) {
	stop := c.watch(ctx)
	defer c.unwatch(stop)
	total := len(args)
	s, rest, err := c.prepare(sql)
	for s != nil && err == nil {
		args, err = c.bind(s, args)
		if err == nil && rest == "" && len(args) > 0 {
			err = argCountErr(total-len(args), total)
		}
		if err == nil {
			err = c.stepAll(ctx, s)
		}
		c.done(s)
		if err != nil || rest == "" {
			break
		}
		s, rest, err = c.prepareRaw(rest)
	}
	if err != nil {
		return 0, 0, err
	}
	return sqlite3.Xsqlite3_changes64(c.tls, c.db), sqlite3.Xsqlite3_last_insert_rowid(c.tls, c.db), nil
}

// query prepares sql, runs every statement but the last to completion, and
// returns the last one bound and ready to step; nil when sql holds no
// statement. Statements take their parameters from args in order. The
// caller ends the statement's use through done.
func (c *conn) query(ctx context.Context, sql string, args []any) (*stmt, error) {
	total := len(args)
	s, rest, err := c.prepare(sql)
	if err != nil {
		return nil, err
	}
	for s != nil && rest != "" {
		args, err = c.bind(s, args)
		if err == nil {
			err = c.stepAll(ctx, s)
		}
		c.done(s)
		if err != nil {
			return nil, err
		}
		if s, rest, err = c.prepareRaw(rest); err != nil {
			return nil, err
		}
	}
	if s == nil {
		return nil, nil
	}
	args, err = c.bind(s, args)
	if err == nil && len(args) > 0 {
		err = argCountErr(total-len(args), total)
	}
	if err != nil {
		c.done(s)
		return nil, err
	}
	s.inUse = true
	return s, nil
}

// endTx runs COMMIT or ROLLBACK. A failed COMMIT (SQLITE_BUSY) leaves the
// transaction open, so the connection is rolled back before it is reused.
func (c *conn) endTx(ctx context.Context, stmt string) error {
	_, _, err := c.exec(ctx, stmt, nil)
	if err != nil && sqlite3.Xsqlite3_get_autocommit(c.tls, c.db) == 0 {
		_, _, _ = c.exec(context.Background(), "ROLLBACK", nil)
	}
	return err
}

// prepare returns the statement for sql: from the cache, or freshly
// prepared and cached when sql is a single statement whose cache entry is
// not in use. Multi-statement text is never cached; rest then holds the
// text after the first statement.
func (c *conn) prepare(sql string) (s *stmt, rest string, err error) {
	cached := c.stmts[sql]
	if cached != nil && !cached.inUse {
		c.touch(cached)
		return cached, "", nil
	}
	s, rest, err = c.prepareRaw(sql)
	if err != nil || s == nil || rest != "" || cached != nil {
		return s, rest, err
	}
	c.insert(s)
	return s, "", nil
}

// prepareRaw compiles the first statement of sql; s is nil when sql holds
// only whitespace or comments. rest is the text after the statement.
func (c *conn) prepareRaw(sql string) (s *stmt, rest string, err error) {
	n := len(sql)
	z := c.tls.Alloc(n)
	copy(libc.GoBytes(z, n), sql)
	rc := sqlite3.Xsqlite3_prepare_v2(c.tls, c.db, z, int32(n),
		uintptr(unsafe.Pointer(&c.out[0])), uintptr(unsafe.Pointer(&c.out[1])))
	used := c.out[1] - z
	c.tls.Free(n)
	if rc != sqlite3.SQLITE_OK {
		return nil, "", c.err()
	}
	rest = sql[used:]
	if c.out[0] == 0 {
		return nil, rest, nil
	}
	s = &stmt{p: c.out[0], sql: sql, nparams: int(sqlite3.Xsqlite3_bind_parameter_count(c.tls, c.out[0]))}
	cols := int(sqlite3.Xsqlite3_column_count(c.tls, s.p))
	if cols == 0 {
		return s, rest, nil
	}
	s.names = make([]string, cols)
	s.declTime = make([]bool, cols)
	for i := range cols {
		s.names[i] = libc.GoString(sqlite3.Xsqlite3_column_name(c.tls, s.p, int32(i)))
		decl := libc.GoString(sqlite3.Xsqlite3_column_decltype(c.tls, s.p, int32(i)))
		s.declTime[i] = strings.EqualFold(decl, "DATETIME") || strings.EqualFold(decl, "TIMESTAMP") ||
			strings.EqualFold(decl, "DATE")
	}
	return s, rest, nil
}

// done ends one use of s: cached statements reset for reuse, others finalize.
func (c *conn) done(s *stmt) {
	s.inUse = false
	if s.cached {
		sqlite3.Xsqlite3_reset(c.tls, s.p)
		return
	}
	sqlite3.Xsqlite3_finalize(c.tls, s.p)
}

// bind binds the first s.nparams arguments to s and returns the rest.
func (c *conn) bind(s *stmt, args []any) ([]any, error) {
	if len(args) < s.nparams {
		return nil, argCountErr(s.nparams, len(args))
	}
	for i := range s.nparams {
		if err := c.bindArg(s.p, int32(i+1), args[i]); err != nil {
			return nil, err
		}
	}
	return args[s.nparams:], nil
}

func argCountErr(want, got int) error {
	return &Error{code: sqlite3.SQLITE_RANGE,
		msg: "expected " + strconv.Itoa(want) + " arguments, got " + strconv.Itoa(got)}
}

// bindArg binds one argument; values outside SQLite's storage classes go
// through database/sql's default conversion first.
func (c *conn) bindArg(p uintptr, i int32, a any) error {
	var rc int32
	switch v := a.(type) {
	case nil:
		rc = sqlite3.Xsqlite3_bind_null(c.tls, p, i)
	case int64:
		rc = sqlite3.Xsqlite3_bind_int64(c.tls, p, i, v)
	case string:
		rc = c.bindText(p, i, v)
	case []byte:
		if v == nil {
			rc = sqlite3.Xsqlite3_bind_null(c.tls, p, i)
			break
		}
		rc = c.bindBytes(p, i, v, sqlite3.Xsqlite3_bind_blob)
	case float64:
		rc = sqlite3.Xsqlite3_bind_double(c.tls, p, i, v)
	case bool:
		var n int64
		if v {
			n = 1
		}
		rc = sqlite3.Xsqlite3_bind_int64(c.tls, p, i, n)
	case int:
		rc = sqlite3.Xsqlite3_bind_int64(c.tls, p, i, int64(v))
	case time.Time:
		rc = c.bindText(p, i, v.UTC().Format(timeFormat))
	default:
		dv, err := driver.DefaultParameterConverter.ConvertValue(a)
		if err != nil {
			return err
		}
		return c.bindArg(p, i, dv)
	}
	if rc != sqlite3.SQLITE_OK {
		return c.err()
	}
	return nil
}

func (c *conn) bindText(p uintptr, i int32, s string) int32 {
	return c.bindBytes(p, i, s, sqlite3.Xsqlite3_bind_text)
}

// bindBytes copies v onto the libc stack for the duration of a transient
// bind; a zero-length value keeps a non-null pointer, so it binds as empty
// rather than NULL.
func (c *conn) bindBytes[V string | []byte](p uintptr, i int32, v V,
	bind func(*libc.TLS, uintptr, int32, uintptr, int32, uintptr) int32) int32 {
	n := len(v)
	z := c.tls.Alloc(n)
	copy(libc.GoBytes(z, n), v)
	rc := bind(c.tls, p, i, z, int32(n), bindTransient)
	c.tls.Free(n)
	return rc
}

// stepAll runs s to completion.
func (c *conn) stepAll(ctx context.Context, s *stmt) error {
	for {
		switch rc := sqlite3.Xsqlite3_step(c.tls, s.p); rc {
		case sqlite3.SQLITE_ROW:
		case sqlite3.SQLITE_DONE:
			return nil
		default:
			return c.stepErr(ctx, rc)
		}
	}
}

// stepErr reports a failed step; an interrupt requested through ctx
// surfaces as ctx's error.
func (c *conn) stepErr(ctx context.Context, rc int32) error {
	if rc == sqlite3.SQLITE_INTERRUPT && ctx.Err() != nil {
		return ctx.Err()
	}
	return c.err()
}

// err builds the Error for the connection's last failure.
func (c *conn) err() error {
	return &Error{
		code: int(sqlite3.Xsqlite3_extended_errcode(c.tls, c.db)),
		msg:  libc.GoString(sqlite3.Xsqlite3_errmsg(c.tls, c.db)),
	}
}

// watch arranges for ctx's cancellation to interrupt the connection; the
// returned stop goes to unwatch once the statement is done.
func (c *conn) watch(ctx context.Context) func() bool {
	if ctx.Done() == nil {
		return nil
	}
	return context.AfterFunc(ctx, c.interrupt)
}

// unwatch cancels a pending interrupt or waits for a running one, so no
// interrupt lands on a later statement.
func (c *conn) unwatch(stop func() bool) {
	if stop == nil || stop() {
		return
	}
	c.intMu.Lock()
	c.intMu.Unlock()
}

func (c *conn) interrupt() {
	c.intMu.Lock()
	sqlite3.Xsqlite3_interrupt(c.tls, c.db)
	c.intMu.Unlock()
}

// touch moves s to the front of the LRU order.
func (c *conn) touch(s *stmt) {
	if c.head == s {
		return
	}
	c.unlink(s)
	c.pushFront(s)
}

func (c *conn) insert(s *stmt) {
	if len(c.stmts) == stmtCacheSize {
		c.evict()
	}
	s.cached = true
	c.stmts[s.sql] = s
	c.pushFront(s)
}

// evict drops the least recently used statement; one still in use is
// finalized by its rows instead.
func (c *conn) evict() {
	s := c.tail
	c.unlink(s)
	delete(c.stmts, s.sql)
	s.cached = false
	if !s.inUse {
		sqlite3.Xsqlite3_finalize(c.tls, s.p)
	}
}

func (c *conn) pushFront(s *stmt) {
	s.prev = nil
	s.next = c.head
	if c.head != nil {
		c.head.prev = s
	} else {
		c.tail = s
	}
	c.head = s
}

func (c *conn) unlink(s *stmt) {
	if s.prev != nil {
		s.prev.next = s.next
	} else {
		c.head = s.next
	}
	if s.next != nil {
		s.next.prev = s.prev
	} else {
		c.tail = s.prev
	}
}

// stmt is one prepared statement with its column metadata. Cached
// statements belong to the connection's cache and reset after use; others
// finalize. inUse blocks reuse while rows are open on it.
type stmt struct {
	p          uintptr
	sql        string
	nparams    int
	names      []string
	declTime   []bool // declared DATE, DATETIME, or TIMESTAMP: text decodes as time
	cached     bool
	inUse      bool
	prev, next *stmt
}
