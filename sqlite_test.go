package sqlite

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/go-rio/rio"
	driver "modernc.org/sqlite"
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

// One connection: each connection gets its own in-memory database.
func openTestDB(t *testing.T, dsn string) *rio.DB {
	t.Helper()
	db, err := Open(dsn)
	if err != nil {
		t.Fatalf("Open(%q): %v", dsn, err)
	}
	db.Unwrap().SetMaxOpenConns(1)
	t.Cleanup(func() {
		if err := db.Close(); err != nil {
			t.Errorf("Close: %v", err)
		}
	})
	return db
}

// wrapTestDB opens dsn without Open's DSN defaults and wraps it with New.
func wrapTestDB(t *testing.T, dsn string, opts ...rio.Option) *rio.DB {
	t.Helper()
	sqlDB, err := sql.Open("sqlite", dsn)
	if err != nil {
		t.Fatalf("sql.Open(%q): %v", dsn, err)
	}
	sqlDB.SetMaxOpenConns(1)
	db := New(sqlDB, opts...)
	t.Cleanup(func() {
		if err := db.Close(); err != nil {
			t.Errorf("Close: %v", err)
		}
	})
	return db
}

func mustExec(t *testing.T, db *rio.DB, ddl string) {
	t.Helper()
	if _, err := db.Unwrap().Exec(ddl); err != nil {
		t.Fatalf("exec schema: %v", err)
	}
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

	first, err := rio.From[user]().Where("email = ?", alice.Email).First(ctx, db)
	if err != nil {
		t.Fatalf("First: %v", err)
	}
	if *first != alice {
		t.Fatalf("First returned %+v, want %+v", *first, alice)
	}
}

func TestDuplicateKeyTranslation(t *testing.T) {
	ctx := context.Background()
	db := openTestDB(t, ":memory:")
	mustExec(t, db, testSchema)

	alice := user{Email: "alice@example.com", Name: "Alice"}
	if err := rio.Insert(ctx, db, &alice); err != nil {
		t.Fatalf("Insert: %v", err)
	}

	dup := user{Email: "alice@example.com", Name: "Alice again"}
	err := rio.Insert(ctx, db, &dup)
	if !errors.Is(err, rio.ErrDuplicateKey) {
		t.Fatalf("duplicate email: got %v, want rio.ErrDuplicateKey", err)
	}

	var de *driver.Error
	if !errors.As(err, &de) {
		t.Fatalf("duplicate email: driver error missing from chain: %v", err)
	}
	if de.Code() != sqlite3.SQLITE_CONSTRAINT_UNIQUE {
		t.Fatalf("duplicate email: driver code = %d, want %d", de.Code(), sqlite3.SQLITE_CONSTRAINT_UNIQUE)
	}

	pkDup := user{ID: alice.ID, Email: "other@example.com", Name: "Other"}
	if err := rio.Insert(ctx, db, &pkDup); !errors.Is(err, rio.ErrDuplicateKey) {
		t.Fatalf("duplicate primary key: got %v, want rio.ErrDuplicateKey", err)
	}

	// Without an INTEGER PRIMARY KEY alias, a duplicate rowid is its own code.
	mustExec(t, db, "CREATE TABLE notes (body TEXT NOT NULL)")
	const note = "INSERT INTO notes (rowid, body) VALUES (1, 'note')"
	if _, err := rio.Exec(ctx, db, note); err != nil {
		t.Fatalf("Exec: %v", err)
	}
	_, err = rio.Exec(ctx, db, note)
	if !errors.Is(err, rio.ErrDuplicateKey) {
		t.Fatalf("duplicate rowid: got %v, want rio.ErrDuplicateKey", err)
	}
	if !errors.As(err, &de) {
		t.Fatalf("duplicate rowid: driver error missing from chain: %v", err)
	}
	if de.Code() != sqlite3.SQLITE_CONSTRAINT_ROWID {
		t.Fatalf("duplicate rowid: driver code = %d, want %d", de.Code(), sqlite3.SQLITE_CONSTRAINT_ROWID)
	}
}

func TestForeignKeysEnforced(t *testing.T) {
	ctx := context.Background()
	db := openTestDB(t, ":memory:")
	mustExec(t, db, testSchema)

	orphan := post{UserID: 9001, Title: "orphan"}
	if err := rio.Insert(ctx, db, &orphan); !errors.Is(err, rio.ErrForeignKeyViolated) {
		t.Fatalf("orphan insert: got %v, want rio.ErrForeignKeyViolated", err)
	}

	owner := user{Email: "owner@example.com", Name: "Owner"}
	if err := rio.Insert(ctx, db, &owner); err != nil {
		t.Fatalf("Insert owner: %v", err)
	}
	ok := post{UserID: owner.ID, Title: "hello"}
	if err := rio.Insert(ctx, db, &ok); err != nil {
		t.Fatalf("Insert post: %v", err)
	}
}

func TestConcurrentWrites(t *testing.T) {
	ctx := context.Background()

	// Use a file-backed database so writers contend over shared state.
	db, err := Open(filepath.Join(t.TempDir(), "concurrent.db") +
		"?_pragma=busy_timeout(10000)&_pragma=journal_mode(WAL)&_pragma=synchronous(NORMAL)")
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() {
		if err := db.Close(); err != nil {
			t.Errorf("Close: %v", err)
		}
	})
	mustExec(t, db, testSchema)

	const writers, rows = 2, 50
	errs := make(chan error, writers)
	var wg sync.WaitGroup
	for w := range writers {
		wg.Go(func() {
			for i := range rows {
				u := user{Email: fmt.Sprintf("w%d-%d@example.com", w, i), Name: "writer"}
				if err := rio.Insert(ctx, db, &u); err != nil {
					errs <- fmt.Errorf("writer %d row %d: %w", w, i, err)
					return
				}
			}
		})
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Error(err)
	}

	n, err := rio.From[user]().Count(ctx, db)
	if err != nil {
		t.Fatalf("Count: %v", err)
	}
	if n != writers*rows {
		t.Fatalf("Count = %d, want %d", n, writers*rows)
	}
}

func TestOpenEmptyDSN(t *testing.T) {
	// A fresh directory exposes regressions that turn the empty DSN into a file.
	t.Chdir(t.TempDir())
	ctx := context.Background()

	db, err := Open("")
	if err != nil {
		t.Fatalf(`Open(""): %v`, err)
	}
	db.Unwrap().SetMaxOpenConns(1)
	mustExec(t, db, testSchema)

	for pragma, want := range map[string]int64{"foreign_keys": 1, "busy_timeout": 5000} {
		var got int64
		if err := db.Unwrap().QueryRow("PRAGMA " + pragma).Scan(&got); err != nil {
			t.Fatalf("PRAGMA %s: %v", pragma, err)
		}
		if got != want {
			t.Errorf("PRAGMA %s = %d, want %d", pragma, got, want)
		}
	}

	orphan := post{UserID: 9001, Title: "orphan"}
	if err := rio.Insert(ctx, db, &orphan); !errors.Is(err, rio.ErrForeignKeyViolated) {
		t.Fatalf("orphan insert: got %v, want rio.ErrForeignKeyViolated", err)
	}
	owner := user{Email: "owner@example.com", Name: "Owner"}
	if err := rio.Insert(ctx, db, &owner); err != nil {
		t.Fatalf("Insert: %v", err)
	}

	if err := db.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	entries, err := os.ReadDir(".")
	if err != nil {
		t.Fatalf("ReadDir: %v", err)
	}
	for _, e := range entries {
		t.Errorf(`Open("") left %q in the working directory`, e.Name())
	}
}

func TestNewInstallsTranslator(t *testing.T) {
	ctx := context.Background()

	// New does not configure an existing pool, so enable foreign keys here.
	db := wrapTestDB(t, ":memory:?_pragma=foreign_keys(1)")
	mustExec(t, db, testSchema)

	alice := user{Email: "alice@example.com", Name: "Alice"}
	if err := rio.Insert(ctx, db, &alice); err != nil {
		t.Fatalf("Insert: %v", err)
	}
	dup := user{Email: "alice@example.com", Name: "Alice again"}
	if err := rio.Insert(ctx, db, &dup); !errors.Is(err, rio.ErrDuplicateKey) {
		t.Fatalf("duplicate email: got %v, want rio.ErrDuplicateKey", err)
	}
	orphan := post{UserID: 9001, Title: "orphan"}
	if err := rio.Insert(ctx, db, &orphan); !errors.Is(err, rio.ErrForeignKeyViolated) {
		t.Fatalf("orphan insert: got %v, want rio.ErrForeignKeyViolated", err)
	}
}

func TestNewWithoutStmtCache(t *testing.T) {
	ctx := context.Background()
	db := wrapTestDB(t, ":memory:", rio.WithoutStmtCache())
	mustExec(t, db, testSchema)

	alice := user{Email: "alice@example.com", Name: "Alice"}
	if err := rio.Insert(ctx, db, &alice); err != nil {
		t.Fatalf("Insert: %v", err)
	}
	got, err := rio.Find[user](ctx, db, alice.ID)
	if err != nil {
		t.Fatalf("Find: %v", err)
	}
	if *got != alice {
		t.Fatalf("Find returned %+v, want %+v", *got, alice)
	}
}

func TestOpenPragmaDefaultsAndOverrides(t *testing.T) {
	tests := []struct {
		name   string
		dsn    string
		pragma string
		want   int64
	}{
		{"default foreign_keys on", ":memory:", "foreign_keys", 1},
		{"default busy_timeout 5000", ":memory:", "busy_timeout", 5000},
		{"user foreign_keys wins", ":memory:?_pragma=foreign_keys(0)", "foreign_keys", 0},
		{"user busy_timeout wins", ":memory:?_pragma=busy_timeout(1234)", "busy_timeout", 1234},
		{"other default still applied", ":memory:?_pragma=foreign_keys(0)", "busy_timeout", 5000},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			db := openTestDB(t, tt.dsn)
			var got int64
			if err := db.Unwrap().QueryRow("PRAGMA " + tt.pragma).Scan(&got); err != nil {
				t.Fatalf("PRAGMA %s: %v", tt.pragma, err)
			}
			if got != tt.want {
				t.Fatalf("PRAGMA %s = %d, want %d", tt.pragma, got, tt.want)
			}
		})
	}
}

func TestOpenBindsTimesInUTC(t *testing.T) {
	at := time.Date(2024, 1, 2, 3, 4, 5, 0, time.FixedZone("UTC+8", 8*3600))
	const layout = "2006-01-02 15:04:05.999999999-07:00"
	tests := []struct {
		name string
		dsn  string
		want string
	}{
		{"default UTC", ":memory:", "2024-01-01 19:04:05+00:00"},
		{"user timezone wins", ":memory:?_timezone=Local", at.In(time.Local).Format(layout)},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			db := openTestDB(t, tt.dsn)
			mustExec(t, db, "CREATE TABLE stamps (at TEXT NOT NULL)")
			if _, err := db.Unwrap().Exec("INSERT INTO stamps (at) VALUES (?)", at); err != nil {
				t.Fatalf("Exec: %v", err)
			}
			var got string
			if err := db.Unwrap().QueryRow("SELECT at FROM stamps").Scan(&got); err != nil {
				t.Fatalf("Scan: %v", err)
			}
			if got != tt.want {
				t.Fatalf("stored %q, want %q", got, tt.want)
			}
		})
	}
}

func TestOpenBeginsImmediate(t *testing.T) {
	tests := []struct {
		name     string
		params   string
		wantBusy bool
	}{
		{"default immediate", "", true},
		{"user txlock wins", "&_txlock=deferred", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctx := context.Background()
			// A zero busy timeout fails a contended BEGIN instead of waiting.
			db, err := Open(filepath.Join(t.TempDir(), "lock.db") + "?_pragma=busy_timeout(0)" + tt.params)
			if err != nil {
				t.Fatalf("Open: %v", err)
			}
			t.Cleanup(func() {
				if err := db.Close(); err != nil {
					t.Errorf("Close: %v", err)
				}
			})

			holder, err := db.Unwrap().BeginTx(ctx, nil)
			if err != nil {
				t.Fatalf("first BeginTx: %v", err)
			}
			t.Cleanup(func() { _ = holder.Rollback() })
			contender, err := db.Unwrap().BeginTx(ctx, nil)
			if err == nil {
				t.Cleanup(func() { _ = contender.Rollback() })
			}
			var de *driver.Error
			gotBusy := errors.As(err, &de) && de.Code() == sqlite3.SQLITE_BUSY
			if gotBusy != tt.wantBusy {
				t.Fatalf("second BeginTx: err = %v, want busy = %t", err, tt.wantBusy)
			}
		})
	}
}

const params = "&_time_format=sqlite&_timezone=UTC&_txlock=immediate"

func TestWithDefaultPragmas(t *testing.T) {
	tests := []struct {
		dsn  string
		want string
	}{
		{"", "file:?_pragma=busy_timeout(5000)&_pragma=foreign_keys(1)" + params},
		{":memory:", ":memory:?_pragma=busy_timeout(5000)&_pragma=foreign_keys(1)" + params},
		{"app.db", "app.db?_pragma=busy_timeout(5000)&_pragma=foreign_keys(1)" + params},
		{"file:app.db?mode=ro", "file:app.db?mode=ro&_pragma=busy_timeout(5000)&_pragma=foreign_keys(1)" + params},
		{"app.db?", "app.db?_pragma=busy_timeout(5000)&_pragma=foreign_keys(1)" + params},
		{"app.db?_pragma=foreign_keys(0)", "app.db?_pragma=foreign_keys(0)&_pragma=busy_timeout(5000)" + params},
		{"app.db?_pragma=busy_timeout(1)&_pragma=foreign_keys(1)", "app.db?_pragma=busy_timeout(1)&_pragma=foreign_keys(1)" + params},
		{"app.db?_pragma=busy_timeout%285000%29", "app.db?_pragma=busy_timeout%285000%29&_pragma=foreign_keys(1)" + params},
		{"app.db?_pragma=foreign_keys+%3D+ON", "app.db?_pragma=foreign_keys+%3D+ON&_pragma=busy_timeout(5000)" + params},
		{"app.db?_time_format=sqlite",
			"app.db?_time_format=sqlite&_pragma=busy_timeout(5000)&_pragma=foreign_keys(1)&_timezone=UTC&_txlock=immediate"},
		{"app.db?_timezone=Local",
			"app.db?_timezone=Local&_pragma=busy_timeout(5000)&_pragma=foreign_keys(1)&_time_format=sqlite&_txlock=immediate"},
		{"app.db?_txlock=deferred",
			"app.db?_txlock=deferred&_pragma=busy_timeout(5000)&_pragma=foreign_keys(1)&_time_format=sqlite&_timezone=UTC"},
		{"?weird", "?weird"},
		{"app.db?_pragma=%zz", "app.db?_pragma=%zz"},
	}
	for _, tt := range tests {
		if got := withDefaultPragmas(tt.dsn); got != tt.want {
			t.Errorf("withDefaultPragmas(%q) = %q, want %q", tt.dsn, got, tt.want)
		}
	}
}
