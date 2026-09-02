package sqlite

import (
	"context"
	"database/sql"
	"errors"
	"strconv"
	"testing"
	"time"
)

func openTestConn(t *testing.T) *conn {
	t.Helper()
	c, err := openConn(&connConfig{name: ":memory:"})
	if err != nil {
		t.Fatalf("openConn: %v", err)
	}
	t.Cleanup(c.close)
	return c
}

// scanRow reads one row of one text column through the plain-scanner path.
func scanRow(t *testing.T, c *conn, query string, args ...any) string {
	t.Helper()
	r, err := newRows(context.Background(), c, nil, query, args)
	if err != nil {
		t.Fatalf("query %q: %v", query, err)
	}
	defer r.Close()
	var ns sql.NullString
	if !r.Next() {
		t.Fatalf("query %q: no row (%v)", query, r.Err())
	}
	if err := r.Scan(&ns); err != nil {
		t.Fatalf("scan: %v", err)
	}
	return ns.String
}

func TestErrorFormat(t *testing.T) {
	e := &Error{code: 2067, msg: "UNIQUE constraint failed: users.email"}
	if e.Error() != "sqlite: UNIQUE constraint failed: users.email (2067)" || e.Code() != 2067 {
		t.Fatalf("Error = %q, Code = %d", e.Error(), e.Code())
	}
}

func TestConnBindConversions(t *testing.T) {
	ctx := context.Background()
	c := openTestConn(t)
	if _, _, err := c.exec(ctx, "CREATE TABLE t (v)", nil); err != nil {
		t.Fatal(err)
	}
	str := "ptr"
	stamp := time.Date(2026, 9, 2, 11, 1, 49, 500000, time.FixedZone("x", 3600))
	args := []any{uint8(7), float32(1.5), sql.NullString{}, &str, stamp, "", []byte{}, []byte(nil), true, int(3)}
	for i, a := range args {
		n, id, err := c.exec(ctx, "INSERT INTO t VALUES (?)", []any{a})
		if err != nil || n != 1 || id != int64(i+1) {
			t.Fatalf("insert %T: %d, %d, %v", a, n, id, err)
		}
	}
	got := scanRow(t, c, "SELECT group_concat(typeof(v) || ':' || coalesce(quote(v), 'NULL'), ' ') FROM t")
	want := "integer:7 real:1.5 null:NULL text:'ptr' text:'2026-09-02 10:01:49.0005+00:00' " +
		"text:'' blob:X'' null:NULL integer:1 integer:3"
	if got != want {
		t.Fatalf("stored values:\n got %s\nwant %s", got, want)
	}
	err := c.bindArg(0, 1, make(chan int))
	if err == nil {
		t.Fatal("binding a channel should fail")
	}
}

func TestConnArgumentCount(t *testing.T) {
	ctx := context.Background()
	c := openTestConn(t)
	if _, _, err := c.exec(ctx, "CREATE TABLE t (v)", nil); err != nil {
		t.Fatal(err)
	}
	for _, args := range [][]any{nil, {1, 2}} {
		var se *Error
		_, _, err := c.exec(ctx, "INSERT INTO t VALUES (?)", args)
		if !errors.As(err, &se) || se.msg != "expected 1 arguments, got "+strconv.Itoa(len(args)) {
			t.Fatalf("%d args: %v", len(args), err)
		}
	}
	if _, _, err := c.exec(ctx, "INSERT INTO t VALUES (?); INSERT INTO t VALUES (?)", []any{1, 2}); err != nil {
		t.Fatalf("arguments should spread across statements: %v", err)
	}
	if got := scanRow(t, c, "SELECT group_concat(v) FROM t"); got != "1,2" {
		t.Fatalf("spread arguments stored %q", got)
	}
}

func TestConnStatementCache(t *testing.T) {
	ctx := context.Background()
	c := openTestConn(t)
	const q = "SELECT 1"
	first, err := c.query(ctx, q, nil)
	if err != nil {
		t.Fatal(err)
	}
	second, err := c.query(ctx, q, nil)
	if err != nil {
		t.Fatal(err)
	}
	if second == first || second.cached || !first.cached {
		t.Fatal("an in-use statement must not be handed out twice")
	}
	c.done(second)
	c.done(first)
	third, err := c.query(ctx, q, nil)
	if err != nil || third != first {
		t.Fatalf("cache miss after release: %v, %v", third == first, err)
	}
	for i := range stmtCacheSize {
		if _, _, err := c.exec(ctx, "SELECT "+strconv.Itoa(i+2), nil); err != nil {
			t.Fatal(err)
		}
	}
	if first.cached || c.stmts[q] != nil || len(c.stmts) != stmtCacheSize {
		t.Fatal("the in-use statement should have been evicted without being finalized")
	}
	if !c.stmts["SELECT 2"].cached || c.head.sql != "SELECT "+strconv.Itoa(stmtCacheSize+1) {
		t.Fatal("LRU order is wrong")
	}
	if !third.inUse {
		t.Fatal("evicted statement stays in use until done")
	}
	c.done(third)
	if got := scanRow(t, c, q); got != "1" {
		t.Fatalf("SELECT 1 = %q", got)
	}
}

func TestConnInterrupt(t *testing.T) {
	c := openTestConn(t)
	short, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	const endless = "WITH RECURSIVE c(x) AS (SELECT 1 UNION ALL SELECT x + 1 FROM c) SELECT count(*) FROM c"
	if _, _, err := c.exec(short, endless, nil); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("exec = %v, want DeadlineExceeded", err)
	}
	if got := scanRow(t, c, "SELECT 'still fine'"); got != "still fine" {
		t.Fatalf("after interrupt: %q", got)
	}
	if _, _, err := c.exec(context.Background(), "SELECT * FROM missing", nil); err == nil {
		t.Fatal("expected an error for a missing table")
	}
}

func TestConnEmptyStatements(t *testing.T) {
	ctx := context.Background()
	c := openTestConn(t)
	for _, text := range []string{"/* nothing */", "", "   "} {
		if n, _, err := c.exec(ctx, text, nil); err != nil || n != 0 {
			t.Fatalf("exec(%q) = %d, %v", text, n, err)
		}
		s, err := c.query(ctx, text, nil)
		if err != nil || s != nil {
			t.Fatalf("query(%q) = %v, %v", text, s, err)
		}
	}
}
