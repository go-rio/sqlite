package sqlite

import (
	"context"
	"database/sql"
	"fmt"
	"time"

	"github.com/go-rio/rio"
	"modernc.org/libc"
	sqlite3 "modernc.org/sqlite/lib"
)

// nativeDB is the pool's rio.NativeDB face.
type nativeDB struct {
	p *Pool
}

func (d *nativeDB) Query(ctx context.Context, sqlText string, args []any) (rio.NativeRows, error) {
	c, err := d.p.acquire(ctx)
	if err != nil {
		return nil, err
	}
	r, err := newRows(ctx, c, d.p, sqlText, args)
	if err != nil {
		d.p.release(c)
		return nil, err
	}
	return r, nil
}

func (d *nativeDB) Exec(ctx context.Context, sqlText string, args []any) (int64, error) {
	n, _, err := d.ExecLastInsert(ctx, sqlText, args)
	return n, err
}

func (d *nativeDB) ExecLastInsert(ctx context.Context, sqlText string, args []any) (int64, int64, error) {
	c, err := d.p.acquire(ctx)
	if err != nil {
		return 0, 0, err
	}
	n, id, err := c.exec(ctx, sqlText, args)
	d.p.release(c)
	return n, id, err
}

// Begin starts a transaction in the DSN's _txlock mode; a read-only one
// begins deferred. Every isolation level is accepted: SQLite is serializable.
func (d *nativeDB) Begin(ctx context.Context, opts *sql.TxOptions) (rio.NativeTx, error) {
	c, err := d.p.acquire(ctx)
	if err != nil {
		return nil, err
	}
	begin := d.p.begin
	if opts != nil && opts.ReadOnly {
		begin = "BEGIN"
	}
	if _, _, err := c.exec(ctx, begin, nil); err != nil {
		d.p.release(c)
		return nil, err
	}
	return &nativeTx{c: c, p: d.p}, nil
}

func (d *nativeDB) Close() error { return d.p.Close() }

// nativeTx is one transaction on a pool connection, held until Commit or
// Rollback returns it.
type nativeTx struct {
	c    *conn
	p    *Pool
	done bool
}

func (t *nativeTx) Query(ctx context.Context, sqlText string, args []any) (rio.NativeRows, error) {
	return newRows(ctx, t.c, nil, sqlText, args)
}

func (t *nativeTx) Exec(ctx context.Context, sqlText string, args []any) (int64, error) {
	n, _, err := t.c.exec(ctx, sqlText, args)
	return n, err
}

func (t *nativeTx) ExecLastInsert(ctx context.Context, sqlText string, args []any) (int64, int64, error) {
	return t.c.exec(ctx, sqlText, args)
}

func (t *nativeTx) Commit(ctx context.Context) error   { return t.end(ctx, "COMMIT") }
func (t *nativeTx) Rollback(ctx context.Context) error { return t.end(ctx, "ROLLBACK") }

// end runs the terminal statement once and returns the connection.
func (t *nativeTx) end(ctx context.Context, stmt string) error {
	if t.done {
		return sql.ErrTxDone
	}
	err := t.c.endTx(ctx, stmt)
	t.done = true
	t.p.release(t.c)
	return err
}

// sink is the part of rio.NativeCell the decoder feeds; scanSink adapts a
// plain sql.Scanner destination to it.
type sink interface {
	SetInt64(int64) error
	SetFloat64(float64) error
	SetString(string) error
	SetBytes([]byte) error
	SetTime(time.Time) error
	SetNull() error
}

type scanSink struct {
	sql.Scanner
}

func (s scanSink) SetInt64(v int64) error     { return s.Scan(v) }
func (s scanSink) SetFloat64(v float64) error { return s.Scan(v) }
func (s scanSink) SetString(v string) error   { return s.Scan(v) }
func (s scanSink) SetBytes(v []byte) error    { return s.Scan(v) }
func (s scanSink) SetTime(v time.Time) error  { return s.Scan(v) }
func (s scanSink) SetNull() error             { return s.Scan(nil) }

// nativeRows steps one statement; a pooled connection returns to the pool
// on Close.
type nativeRows struct {
	c      *conn
	s      *stmt // nil: no statement, an empty result
	p      *Pool // nil for transaction rows
	ctx    context.Context
	stop   func() bool
	err    error
	done   bool
	sinks  []sink
	asTime []bool // text decodes as time: time cell or declared time column
}

func newRows(ctx context.Context, c *conn, p *Pool, sqlText string, args []any) (*nativeRows, error) {
	stop := c.watch(ctx)
	s, err := c.query(ctx, sqlText, args)
	if err != nil {
		c.unwatch(stop)
		return nil, err
	}
	return &nativeRows{c: c, s: s, p: p, ctx: ctx, stop: stop}, nil
}

func (r *nativeRows) Columns() []string {
	if r.s == nil {
		return nil
	}
	return r.s.names
}

func (r *nativeRows) Next() bool {
	if r.done || r.s == nil {
		return false
	}
	switch rc := sqlite3.Xsqlite3_step(r.c.tls, r.s.p); rc {
	case sqlite3.SQLITE_ROW:
		return true
	case sqlite3.SQLITE_DONE:
	default:
		r.err = r.c.stepErr(r.ctx, rc)
	}
	r.done = true
	return false
}

func (r *nativeRows) Err() error { return r.err }

func (r *nativeRows) Close() {
	if r.c == nil {
		return
	}
	if r.s != nil {
		r.c.done(r.s)
	}
	r.c.unwatch(r.stop)
	if r.p != nil {
		r.p.release(r.c)
	}
	r.c = nil
}

// Scan decodes the current row by storage class: INTEGER, FLOAT, BLOB, and
// NULL go straight to the cell; TEXT parses as time for time cells and
// declared time columns, falling back to the string.
func (r *nativeRows) Scan(dest ...any) error {
	if r.sinks == nil {
		if err := r.classify(dest); err != nil {
			return err
		}
	}
	c, p := r.c, r.s.p
	for i, sink := range r.sinks {
		col := int32(i)
		var err error
		switch sqlite3.Xsqlite3_column_type(c.tls, p, col) {
		case sqlite3.SQLITE_INTEGER:
			err = sink.SetInt64(sqlite3.Xsqlite3_column_int64(c.tls, p, col))
		case sqlite3.SQLITE_FLOAT:
			err = sink.SetFloat64(sqlite3.Xsqlite3_column_double(c.tls, p, col))
		case sqlite3.SQLITE_TEXT:
			b := c.text(p, col)
			if r.asTime[i] {
				if t, ok := parseTime(b); ok {
					err = sink.SetTime(t)
					break
				}
			}
			err = sink.SetString(string(b))
		case sqlite3.SQLITE_BLOB:
			err = sink.SetBytes(c.blob(p, col))
		default:
			err = sink.SetNull()
		}
		if err != nil {
			return err
		}
	}
	return nil
}

// classify resolves the destinations once; rio passes the same slots for
// every row.
func (r *nativeRows) classify(dest []any) error {
	r.sinks = make([]sink, len(dest))
	r.asTime = make([]bool, len(dest))
	for i, d := range dest {
		switch d := d.(type) {
		case rio.NativeCell:
			r.sinks[i] = d
			r.asTime[i] = d.ScanKind() == rio.NativeKindTime || r.s.declTime[i]
		case sql.Scanner:
			r.sinks[i] = scanSink{d}
			r.asTime[i] = r.s.declTime[i]
		default:
			return fmt.Errorf("sqlite: cannot scan column %d into %T", i, d)
		}
	}
	return nil
}

// text and blob view the current row's column in SQLite's memory, valid
// until the next step or reset.
func (c *conn) text(p uintptr, col int32) []byte {
	z := sqlite3.Xsqlite3_column_text(c.tls, p, col)
	return libc.GoBytes(z, int(sqlite3.Xsqlite3_column_bytes(c.tls, p, col)))
}

func (c *conn) blob(p uintptr, col int32) []byte {
	z := sqlite3.Xsqlite3_column_blob(c.tls, p, col)
	return libc.GoBytes(z, int(sqlite3.Xsqlite3_column_bytes(c.tls, p, col)))
}

// parseTime decodes SQLite's date-time text forms — a date, optionally
// followed by a time with seconds and fraction and by Z or a ±HH:MM
// offset — to UTC. Anything else reports false.
func parseTime(b []byte) (time.Time, bool) {
	n := len(b)
	if n < 10 || b[4] != '-' || b[7] != '-' {
		return time.Time{}, false
	}
	year, ok1 := digits(b[0:4])
	month, ok2 := digits(b[5:7])
	day, ok3 := digits(b[8:10])
	if !ok1 || !ok2 || !ok3 || month < 1 || month > 12 || day < 1 {
		return time.Time{}, false
	}
	var hour, minute, sec, nsec, offset int
	i := 10
	if i < n {
		if b[i] != ' ' && b[i] != 'T' || n < i+6 || b[i+3] != ':' {
			return time.Time{}, false
		}
		var ok4, ok5 bool
		hour, ok4 = digits(b[i+1 : i+3])
		minute, ok5 = digits(b[i+4 : i+6])
		if !ok4 || !ok5 || hour > 23 || minute > 59 {
			return time.Time{}, false
		}
		i += 6
		if i < n && b[i] == ':' {
			var ok bool
			if n < i+3 {
				return time.Time{}, false
			}
			if sec, ok = digits(b[i+1 : i+3]); !ok || sec > 59 {
				return time.Time{}, false
			}
			i += 3
			if i < n && b[i] == '.' {
				start := i + 1
				scale := 1_000_000_000
				for i = start; i < n && b[i] >= '0' && b[i] <= '9'; i++ {
					if scale > 1 {
						scale /= 10
						nsec = nsec*10 + int(b[i]-'0')
					}
				}
				if i == start || i-start > 9 {
					return time.Time{}, false
				}
				nsec *= scale
			}
		}
	}
	if i < n {
		switch b[i] {
		case 'Z':
			i++
		case '+', '-':
			if n < i+6 || b[i+3] != ':' {
				return time.Time{}, false
			}
			oh, ok6 := digits(b[i+1 : i+3])
			om, ok7 := digits(b[i+4 : i+6])
			if !ok6 || !ok7 || oh > 23 || om > 59 {
				return time.Time{}, false
			}
			offset = (oh*60 + om) * 60
			if b[i] == '-' {
				offset = -offset
			}
			i += 6
		}
	}
	if i != n {
		return time.Time{}, false
	}
	t := time.Date(year, time.Month(month), day, hour, minute, sec, nsec, time.UTC)
	if t.Day() != day {
		return time.Time{}, false
	}
	return t.Add(-time.Duration(offset) * time.Second), true
}

func digits(b []byte) (int, bool) {
	v := 0
	for _, c := range b {
		if c < '0' || c > '9' {
			return 0, false
		}
		v = v*10 + int(c-'0')
	}
	return v, true
}
