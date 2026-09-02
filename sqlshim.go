package sqlite

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"fmt"
	"io"

	sqlite3 "modernc.org/sqlite/lib"
)

// OpenSQL opens a plain database/sql handle on the same DSN as Open; it is
// the handle go-rio/migrate consumes. Each database/sql connection owns one
// SQLite connection with its own statement cache. Transactions, prepared
// statements, affected-row counts, and LastInsertId behave as usual; TEXT
// in DATE, DATETIME, and TIMESTAMP columns scans as time.Time. Nothing
// opens until first use; only a malformed DSN fails here.
func OpenSQL(dsn string) (*sql.DB, error) {
	cfg, err := parseDSN(dsn)
	if err != nil {
		return nil, fmt.Errorf("sqlite: open sql: %w", err)
	}
	return sql.OpenDB(shimConnector{conn: cfg.conn, begin: cfg.begin}), nil
}

// shimConnector opens one SQLite connection per database/sql connection.
type shimConnector struct {
	conn  connConfig
	begin string
}

func (c shimConnector) Connect(context.Context) (driver.Conn, error) {
	conn, err := openConn(&c.conn)
	if err != nil {
		return nil, err
	}
	return &shimConn{c: conn, begin: c.begin}, nil
}

func (c shimConnector) Driver() driver.Driver { return shimDriver{} }

// shimDriver backs shimConnector.Driver; sql.OpenDB never opens by DSN, so
// Open only errors.
type shimDriver struct{}

func (shimDriver) Open(string) (driver.Conn, error) {
	return nil, errors.New("sqlite: use OpenSQL")
}

type shimConn struct {
	c     *conn
	begin string
}

// Prepare returns a statement that compiles through the connection's cache
// on each use, so its parameter count is unknown until then.
func (c *shimConn) Prepare(query string) (driver.Stmt, error) {
	return &shimStmt{c: c, query: query}, nil
}

func (c *shimConn) Close() error {
	c.c.close()
	return nil
}

func (c *shimConn) Begin() (driver.Tx, error) {
	return c.BeginTx(context.Background(), driver.TxOptions{})
}

// BeginTx begins in the DSN's _txlock mode, deferred when read-only; every
// isolation level is accepted.
func (c *shimConn) BeginTx(ctx context.Context, opts driver.TxOptions) (driver.Tx, error) {
	begin := c.begin
	if opts.ReadOnly {
		begin = "BEGIN"
	}
	if _, _, err := c.c.exec(ctx, begin, nil); err != nil {
		return nil, err
	}
	return shimTx{c: c.c}, nil
}

func (c *shimConn) ExecContext(ctx context.Context, query string, args []driver.NamedValue) (driver.Result, error) {
	n, id, err := c.c.exec(ctx, query, namedToAny(args))
	if err != nil {
		return nil, err
	}
	return shimResult{rows: n, id: id}, nil
}

func (c *shimConn) QueryContext(ctx context.Context, query string, args []driver.NamedValue) (driver.Rows, error) {
	r, err := newRows(ctx, c.c, nil, query, namedToAny(args))
	if err != nil {
		return nil, err
	}
	return &shimRows{r: r}, nil
}

type shimTx struct {
	c *conn
}

func (t shimTx) Commit() error   { return t.c.endTx(context.Background(), "COMMIT") }
func (t shimTx) Rollback() error { return t.c.endTx(context.Background(), "ROLLBACK") }

type shimStmt struct {
	c     *shimConn
	query string
}

func (s *shimStmt) Close() error  { return nil }
func (s *shimStmt) NumInput() int { return -1 }

func (s *shimStmt) Exec(args []driver.Value) (driver.Result, error) {
	return s.ExecContext(context.Background(), valuesToNamed(args))
}

func (s *shimStmt) Query(args []driver.Value) (driver.Rows, error) {
	return s.QueryContext(context.Background(), valuesToNamed(args))
}

func (s *shimStmt) ExecContext(ctx context.Context, args []driver.NamedValue) (driver.Result, error) {
	return s.c.ExecContext(ctx, s.query, args)
}

func (s *shimStmt) QueryContext(ctx context.Context, args []driver.NamedValue) (driver.Rows, error) {
	return s.c.QueryContext(ctx, s.query, args)
}

type shimResult struct {
	rows, id int64
}

func (r shimResult) LastInsertId() (int64, error) { return r.id, nil }
func (r shimResult) RowsAffected() (int64, error) { return r.rows, nil }

// shimRows hands each column out as its storage class: int64, float64,
// string (time.Time for declared time columns), a []byte view valid until
// the next row, or nil.
type shimRows struct {
	r *nativeRows
}

func (r *shimRows) Columns() []string { return r.r.Columns() }

func (r *shimRows) Close() error {
	r.r.Close()
	return r.r.Err()
}

func (r *shimRows) Next(dest []driver.Value) error {
	if !r.r.Next() {
		if err := r.r.Err(); err != nil {
			return err
		}
		return io.EOF
	}
	c, s := r.r.c, r.r.s
	for i := range dest {
		col := int32(i)
		switch sqlite3.Xsqlite3_column_type(c.tls, s.p, col) {
		case sqlite3.SQLITE_INTEGER:
			dest[i] = sqlite3.Xsqlite3_column_int64(c.tls, s.p, col)
		case sqlite3.SQLITE_FLOAT:
			dest[i] = sqlite3.Xsqlite3_column_double(c.tls, s.p, col)
		case sqlite3.SQLITE_TEXT:
			b := c.text(s.p, col)
			if s.declTime[i] {
				if t, ok := parseTime(b); ok {
					dest[i] = t
					continue
				}
			}
			dest[i] = string(b)
		case sqlite3.SQLITE_BLOB:
			dest[i] = c.blob(s.p, col)
		default:
			dest[i] = nil
		}
	}
	return nil
}

func namedToAny(args []driver.NamedValue) []any {
	if len(args) == 0 {
		return nil
	}
	out := make([]any, len(args))
	for i, a := range args {
		out[i] = a.Value
	}
	return out
}

func valuesToNamed(args []driver.Value) []driver.NamedValue {
	out := make([]driver.NamedValue, len(args))
	for i, v := range args {
		out[i] = driver.NamedValue{Ordinal: i + 1, Value: v}
	}
	return out
}

var _ interface {
	driver.ConnBeginTx
	driver.ExecerContext
	driver.QueryerContext
} = (*shimConn)(nil)

var _ interface {
	driver.StmtExecContext
	driver.StmtQueryContext
} = (*shimStmt)(nil)
