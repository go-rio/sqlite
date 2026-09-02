package sqlite

import (
	"context"
	"errors"
	"iter"
	"path/filepath"
	"runtime"
	"slices"
	"strconv"
	"sync"
	"testing"
	"time"

	"github.com/go-rio/rio"
	sqlite3 "modernc.org/sqlite/lib"
)

type user struct {
	ID    int64
	Email string
	Name  string
}

type post struct {
	ID     int64
	UserID int64
	Title  string
}

const testSchema = `
CREATE TABLE users (
	id    INTEGER PRIMARY KEY,
	email TEXT NOT NULL UNIQUE,
	name  TEXT NOT NULL
);
CREATE TABLE posts (
	id      INTEGER PRIMARY KEY,
	user_id INTEGER NOT NULL REFERENCES users (id),
	title   TEXT NOT NULL
);`

func openTestDB(t *testing.T, dsn string, opts ...rio.Option) *rio.DB {
	t.Helper()
	db, err := Open(dsn, opts...)
	if err != nil {
		t.Fatalf("Open(%q): %v", dsn, err)
	}
	t.Cleanup(func() {
		if err := db.Close(); err != nil {
			t.Errorf("Close: %v", err)
		}
	})
	return db
}

func mustExec(t *testing.T, db *rio.DB, stmt string, args ...any) {
	t.Helper()
	if _, err := rio.Exec(context.Background(), db, stmt, args...); err != nil {
		t.Fatalf("exec %q: %v", stmt, err)
	}
}

func tempDSN(t *testing.T, query string) string {
	t.Helper()
	return "file:" + filepath.Join(t.TempDir(), "test.db") + query
}

func TestOpenEndToEnd(t *testing.T) {
	ctx := context.Background()
	db := openTestDB(t, ":memory:")
	mustExec(t, db, testSchema)

	alice := user{Email: "alice@example.com", Name: "Alice"}
	if err := rio.Insert(ctx, db, &alice); err != nil {
		t.Fatalf("Insert: %v", err)
	}
	if alice.ID == 0 {
		t.Fatal("Insert did not backfill the primary key")
	}
	got, err := rio.Find[user](ctx, db, alice.ID)
	if err != nil {
		t.Fatalf("Find: %v", err)
	}
	if *got != alice {
		t.Fatalf("Find returned %+v, want %+v", *got, alice)
	}
	alice.Name = "Alice B"
	if err := rio.Update(ctx, db, &alice); err != nil {
		t.Fatalf("Update: %v", err)
	}
	first, err := rio.From[user]().Where("email = ?", alice.Email).First(ctx, db)
	if err != nil {
		t.Fatalf("First: %v", err)
	}
	if *first != alice {
		t.Fatalf("First returned %+v, want %+v", *first, alice)
	}
	if err := rio.Delete(ctx, db, &alice); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	n, err := rio.From[user]().Count(ctx, db)
	if err != nil || n != 0 {
		t.Fatalf("Count after Delete = %d, %v; want 0", n, err)
	}
}

func TestOpenRejects(t *testing.T) {
	for _, dsn := range []string{"x.db?_timezone=UTC", "x.db?_txlock=bogus", "x.db?%zz"} {
		if _, err := Open(dsn); err == nil {
			t.Errorf("Open(%q) should fail", dsn)
		}
		if _, err := OpenSQL(dsn); err == nil {
			t.Errorf("OpenSQL(%q) should fail", dsn)
		}
	}
	defer func() {
		if recover() == nil {
			t.Fatal("rio.WithStmtCache should panic on the native channel")
		}
	}()
	_, _ = Open(":memory:", rio.WithStmtCache())
}

func TestParseDSN(t *testing.T) {
	defaults := []string{"PRAGMA busy_timeout(5000)", "PRAGMA foreign_keys(1)"}
	cases := []struct {
		dsn             string
		name, vfs       string
		begin           string
		pragmas         []string
		single, private bool
	}{
		{dsn: "", begin: "BEGIN IMMEDIATE", pragmas: defaults, single: true, private: true},
		{dsn: ":memory:", name: ":memory:", begin: "BEGIN IMMEDIATE", pragmas: defaults, single: true, private: true},
		{dsn: "app.db", name: "app.db", begin: "BEGIN IMMEDIATE", pragmas: defaults},
		{
			dsn:  "/tmp/x.db?_journal_mode=WAL&_busy_timeout=100&_txlock=deferred&vfs=unix-none",
			name: "/tmp/x.db", vfs: "unix-none", begin: "BEGIN",
			pragmas: []string{"PRAGMA foreign_keys(1)", "PRAGMA busy_timeout = 100", "PRAGMA journal_mode = WAL"},
		},
		{
			dsn:     "file:x?mode=memory&cache=shared&_fk=0&_pragma=synchronous(OFF)&_txlock=exclusive",
			name:    "file:x?mode=memory&cache=shared&_fk=0&_pragma=synchronous(OFF)&_txlock=exclusive",
			begin:   "BEGIN EXCLUSIVE",
			pragmas: []string{"PRAGMA busy_timeout(5000)", "PRAGMA synchronous(OFF)", "PRAGMA foreign_keys = 0"},
			single:  true,
		},
		{dsn: "file:app.db?immutable=1", name: "file:app.db?immutable=1", begin: "BEGIN IMMEDIATE", pragmas: defaults},
	}
	for _, tc := range cases {
		cfg, err := parseDSN(tc.dsn)
		if err != nil {
			t.Fatalf("parseDSN(%q): %v", tc.dsn, err)
		}
		same := cfg.conn.name == tc.name && cfg.conn.vfs == tc.vfs && cfg.begin == tc.begin &&
			slices.Equal(cfg.conn.pragmas, tc.pragmas) && cfg.single == tc.single && cfg.private == tc.private
		if !same {
			t.Errorf("parseDSN(%q) = %+v", tc.dsn, cfg)
		}
	}
}

func TestPoolShape(t *testing.T) {
	ctx := context.Background()
	for _, tc := range []struct {
		dsn          string
		single, view bool
	}{
		{":memory:", true, false},
		{"", true, false},
		{"file:shape?mode=memory&cache=shared", true, true},
		{tempDSN(t, ""), false, true},
	} {
		db := openTestDB(t, tc.dsn)
		want := runtime.GOMAXPROCS(0)
		if tc.single {
			want = 1
		}
		if got := PoolOf(db).MaxConns(); got != want {
			t.Errorf("%q: MaxConns = %d, want %d", tc.dsn, got, want)
		}
		if (db.Unwrap() != nil) != tc.view {
			t.Errorf("%q: Unwrap non-nil = %v, want %v", tc.dsn, db.Unwrap() != nil, tc.view)
		}
		mustExec(t, db, "CREATE TABLE seen (x INTEGER)")
		if n, err := rio.From[user]().Count(ctx, db); err == nil || n != 0 {
			t.Errorf("%q: counting a missing table should fail", tc.dsn)
		}
		if !tc.view {
			continue
		}
		var n int
		if err := db.Unwrap().QueryRow("SELECT count(*) FROM sqlite_master WHERE name = 'seen'").Scan(&n); err != nil || n != 1 {
			t.Errorf("%q: the view does not see the native table: %d, %v", tc.dsn, n, err)
		}
	}
	sqlDB, err := OpenSQL(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	if PoolOf(rio.New(sqlDB, rio.SQLite)) != nil {
		t.Fatal("PoolOf should be nil on the database/sql channel")
	}
}

func TestPoolWaitsForConnections(t *testing.T) {
	ctx := context.Background()
	db := openTestDB(t, tempDSN(t, ""))
	PoolOf(db).SetMaxConns(1)
	mustExec(t, db, testSchema)
	for _, name := range []string{"a", "b"} {
		mustExec(t, db, "INSERT INTO users (email, name) VALUES (?, ?)", name+"@example.com", name)
	}
	next, stop := iter.Pull2(rio.From[user]().OrderBy("id").Rows(ctx, db))
	if _, err, ok := next(); !ok || err != nil {
		t.Fatalf("first row: %v, %v", err, ok)
	}
	short, cancel := context.WithTimeout(ctx, 50*time.Millisecond)
	defer cancel()
	if _, err := rio.From[user]().Count(short, db); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("Count on a busy pool = %v, want DeadlineExceeded", err)
	}
	done := make(chan int64, 1)
	go func() {
		n, _ := rio.From[user]().Count(ctx, db)
		done <- n
	}()
	time.Sleep(20 * time.Millisecond)
	stop()
	if n := <-done; n != 2 {
		t.Fatalf("Count after release = %d, want 2", n)
	}
}

func TestTxLock(t *testing.T) {
	ctx := context.Background()
	for _, tc := range []struct {
		query string
		busy  bool
	}{
		{"?_pragma=busy_timeout(50)", true},
		{"?_pragma=busy_timeout(50)&_txlock=deferred", false},
	} {
		db := openTestDB(t, tempDSN(t, tc.query))
		PoolOf(db).SetMaxConns(2)
		mustExec(t, db, testSchema)
		err := db.Tx(ctx, func(tx *rio.Tx) error {
			return db.Tx(ctx, func(*rio.Tx) error { return nil })
		})
		var se *Error
		gotBusy := errors.As(err, &se) && se.Code() == sqlite3.SQLITE_BUSY
		if gotBusy != tc.busy {
			t.Fatalf("%s: second transaction error = %v, want busy %v", tc.query, err, tc.busy)
		}
	}
}

func TestErrorTranslation(t *testing.T) {
	ctx := context.Background()
	db := openTestDB(t, ":memory:")
	mustExec(t, db, testSchema)
	if err := rio.Insert(ctx, db, &user{Email: "dup@example.com", Name: "One"}); err != nil {
		t.Fatal(err)
	}
	err := rio.Insert(ctx, db, &user{Email: "dup@example.com", Name: "Two"})
	var se *Error
	if !errors.Is(err, rio.ErrDuplicateKey) || !errors.As(err, &se) || se.Code() != sqlite3.SQLITE_CONSTRAINT_UNIQUE {
		t.Fatalf("duplicate insert error = %v", err)
	}
	if err := rio.Insert(ctx, db, &post{UserID: 999, Title: "orphan"}); !errors.Is(err, rio.ErrForeignKeyViolated) {
		t.Fatalf("orphan insert error = %v", err)
	}
	db = openTestDB(t, "file:nofk?mode=memory&_fk=0")
	mustExec(t, db, testSchema)
	if err := rio.Insert(ctx, db, &post{UserID: 999, Title: "orphan"}); err != nil {
		t.Fatalf("_fk=0 should disable enforcement: %v", err)
	}
}

func TestConcurrentAccess(t *testing.T) {
	ctx := context.Background()
	db := openTestDB(t, tempDSN(t, "?_journal_mode=WAL"))
	PoolOf(db).SetMaxConns(4)
	mustExec(t, db, testSchema)
	var wg sync.WaitGroup
	errs := make(chan error, 64)
	for g := range 8 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := range 25 {
				u := user{Email: strconv.Itoa(g*100+i) + "@example.com", Name: "n"}
				if err := rio.Insert(ctx, db, &u); err != nil {
					errs <- err
					return
				}
				if _, err := rio.From[user]().Where("id = ?", u.ID).First(ctx, db); err != nil {
					errs <- err
					return
				}
			}
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Fatal(err)
	}
	if n, _ := rio.From[user]().Count(ctx, db); n != 200 {
		t.Fatalf("Count = %d, want 200", n)
	}
}
