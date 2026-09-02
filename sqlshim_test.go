package sqlite

import (
	"context"
	"database/sql"
	"errors"
	"path/filepath"
	"testing"
	"time"

	sqlite3 "modernc.org/sqlite/lib"
)

func openTestSQL(t *testing.T, dsn string) *sql.DB {
	t.Helper()
	db, err := OpenSQL(dsn)
	if err != nil {
		t.Fatalf("OpenSQL(%q): %v", dsn, err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return db
}

func TestOpenSQLEndToEnd(t *testing.T) {
	ctx := context.Background()
	db := openTestSQL(t, tempDSN(t, ""))
	if err := db.PingContext(ctx); err != nil {
		t.Fatalf("Ping: %v", err)
	}
	if _, err := db.ExecContext(ctx, testSchema); err != nil {
		t.Fatalf("schema: %v", err)
	}
	res, err := db.ExecContext(ctx, "INSERT INTO users (email, name) VALUES (?, ?)", "a@example.com", "A")
	if err != nil {
		t.Fatal(err)
	}
	id, _ := res.LastInsertId()
	n, _ := res.RowsAffected()
	if id != 1 || n != 1 {
		t.Fatalf("LastInsertId = %d, RowsAffected = %d", id, n)
	}
	stmt, err := db.PrepareContext(ctx, "INSERT INTO users (email, name) VALUES (?, ?)")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := stmt.ExecContext(ctx, "b@example.com", "B"); err != nil {
		t.Fatal(err)
	}
	if err := stmt.Close(); err != nil {
		t.Fatal(err)
	}
	var (
		gotID    int64
		email    string
		f        float64
		blob     []byte
		nothing  *string
		emailB   string
		affected int64
	)
	row := db.QueryRowContext(ctx, "SELECT id, email, 1.5, x'0102', NULL FROM users ORDER BY id LIMIT 1")
	if err := row.Scan(&gotID, &email, &f, &blob, &nothing); err != nil {
		t.Fatal(err)
	}
	if gotID != 1 || email != "a@example.com" || f != 1.5 || string(blob) != "\x01\x02" || nothing != nil {
		t.Fatalf("scanned %d %q %v %v %v", gotID, email, f, blob, nothing)
	}
	if err := db.QueryRowContext(ctx, "SELECT email FROM users WHERE id = 2").Scan(&emailB); err != nil || emailB != "b@example.com" {
		t.Fatalf("prepared insert: %q, %v", emailB, err)
	}
	if err := db.QueryRowContext(ctx, "SELECT count(*) FROM users").Scan(&affected); err != nil || affected != 2 {
		t.Fatalf("count = %d, %v", affected, err)
	}
	_, err = db.ExecContext(ctx, "INSERT INTO users (email, name) VALUES (?, ?)", "a@example.com", "dup")
	var se *Error
	if !errors.As(err, &se) || se.Code() != sqlite3.SQLITE_CONSTRAINT_UNIQUE {
		t.Fatalf("duplicate error = %v", err)
	}
}

func TestOpenSQLTimeColumns(t *testing.T) {
	ctx := context.Background()
	db := openTestSQL(t, ":memory:")
	if _, err := db.ExecContext(ctx, "CREATE TABLE stamps (at DATETIME, raw TEXT)"); err != nil {
		t.Fatal(err)
	}
	stamp := time.Date(2026, 1, 2, 3, 4, 5, 600000000, time.FixedZone("x", 3600))
	if _, err := db.ExecContext(ctx, "INSERT INTO stamps VALUES (?, ?)", stamp, "2026-01-02 03:04:05"); err != nil {
		t.Fatal(err)
	}
	var at time.Time
	var raw string
	if err := db.QueryRowContext(ctx, "SELECT at, raw FROM stamps").Scan(&at, &raw); err != nil {
		t.Fatal(err)
	}
	if !at.Equal(stamp) || at.Location() != time.UTC || raw != "2026-01-02 03:04:05" {
		t.Fatalf("scanned %v (%v), %q", at, at.Location(), raw)
	}
	var text string
	if err := db.QueryRowContext(ctx, "SELECT at || '' FROM stamps").Scan(&text); err != nil {
		t.Fatal(err)
	}
	if text != "2026-01-02 02:04:05.6+00:00" {
		t.Fatalf("stored time text = %q", text)
	}
}

func TestOpenSQLTransactions(t *testing.T) {
	ctx := context.Background()
	db := openTestSQL(t, tempDSN(t, ""))
	if _, err := db.ExecContext(ctx, testSchema); err != nil {
		t.Fatal(err)
	}
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := tx.ExecContext(ctx, "INSERT INTO users (email, name) VALUES ('a@example.com', 'A')"); err != nil {
		t.Fatal(err)
	}
	if err := tx.Rollback(); err != nil {
		t.Fatal(err)
	}
	var n int
	if err := db.QueryRowContext(ctx, "SELECT count(*) FROM users").Scan(&n); err != nil || n != 0 {
		t.Fatalf("after rollback: %d, %v", n, err)
	}
	tx, err = db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		t.Fatal(err)
	}
	if err := tx.QueryRowContext(ctx, "SELECT count(*) FROM users").Scan(&n); err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	conn, err := db.Conn(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := conn.ExecContext(ctx, "INSERT INTO users (email, name) VALUES ('c@example.com', 'C')"); err != nil {
		t.Fatal(err)
	}
	if err := conn.Close(); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRowContext(ctx, "SELECT count(*) FROM users").Scan(&n); err != nil || n != 1 {
		t.Fatalf("after pinned insert: %d, %v", n, err)
	}
}

func TestOpenSQLSharesWithOpen(t *testing.T) {
	ctx := context.Background()
	db := openTestDB(t, "file:shim?mode=memory&cache=shared")
	mustExec(t, db, testSchema)
	sqlDB := openTestSQL(t, "file:shim?mode=memory&cache=shared")
	var n int
	if err := sqlDB.QueryRowContext(ctx, "SELECT count(*) FROM sqlite_master WHERE name = 'users'").Scan(&n); err != nil || n != 1 {
		t.Fatalf("OpenSQL does not see Open's shared-cache table: %d, %v", n, err)
	}
	bad := openTestSQL(t, filepath.Join(t.TempDir(), "missing", "x.db"))
	if err := bad.PingContext(ctx); err == nil {
		t.Fatal("Ping on an unopenable path should fail")
	}
}
