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

	"github.com/go-rio/rio"
	driver "modernc.org/sqlite"
	sqlite3 "modernc.org/sqlite/lib"
)

// user and post drive the end-to-end tests. rio's conventions map user to
// table "users" with auto-increment primary key "id", and post to "posts"
// with the foreign key column "user_id".
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

// openTestDB opens a database through Open and caps the pool at a single
// connection: every SQLite connection to ":memory:" gets its own private
// database, so tests must not let the pool grow.
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

	// Insert backfills the auto-increment primary key via RETURNING.
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

	// The query builder works through the same handle.
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

	// A second row with the same unique email (extended result code 2067)
	// translates to rio.ErrDuplicateKey.
	dup := user{Email: "alice@example.com", Name: "Alice again"}
	err := rio.Insert(ctx, db, &dup)
	if !errors.Is(err, rio.ErrDuplicateKey) {
		t.Fatalf("duplicate email: got %v, want rio.ErrDuplicateKey", err)
	}

	// The driver error stays in the chain for errors.As.
	var de *driver.Error
	if !errors.As(err, &de) {
		t.Fatalf("duplicate email: driver error missing from chain: %v", err)
	}
	if de.Code() != sqlite3.SQLITE_CONSTRAINT_UNIQUE {
		t.Fatalf("duplicate email: driver code = %d, want %d", de.Code(), sqlite3.SQLITE_CONSTRAINT_UNIQUE)
	}

	// An explicit duplicate primary key (extended result code 1555)
	// translates to the same sentinel.
	pkDup := user{ID: alice.ID, Email: "other@example.com", Name: "Other"}
	if err := rio.Insert(ctx, db, &pkDup); !errors.Is(err, rio.ErrDuplicateKey) {
		t.Fatalf("duplicate primary key: got %v, want rio.ErrDuplicateKey", err)
	}
}

func TestForeignKeysEnforced(t *testing.T) {
	ctx := context.Background()
	db := openTestDB(t, ":memory:")
	mustExec(t, db, testSchema)

	// Without the foreign_keys pragma SQLite would accept this row silently;
	// getting rio.ErrForeignKeyViolated proves Open turned enforcement on.
	orphan := post{UserID: 9001, Title: "orphan"}
	if err := rio.Insert(ctx, db, &orphan); !errors.Is(err, rio.ErrForeignKeyViolated) {
		t.Fatalf("orphan insert: got %v, want rio.ErrForeignKeyViolated", err)
	}

	// A valid parent makes the same insert succeed.
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

	// File-backed so the pool's connections contend over a real shared file,
	// with the README's production concurrency settings (WAL,
	// synchronous=NORMAL, _txlock=immediate, wide busy_timeout). Open's
	// defaults add only what's missing here: foreign_keys.
	db, err := Open(filepath.Join(t.TempDir(), "concurrent.db") +
		"?_txlock=immediate&_pragma=busy_timeout(10000)&_pragma=journal_mode(WAL)&_pragma=synchronous(NORMAL)")
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
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := range rows {
				u := user{Email: fmt.Sprintf("w%d-%d@example.com", w, i), Name: "writer"}
				if err := rio.Insert(ctx, db, &u); err != nil {
					errs <- fmt.Errorf("writer %d row %d: %w", w, i, err)
					return
				}
			}
		}()
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
	// An empty DSN is SQLite's private, per-connection temporary database.
	// Appending "?_pragma=..." to it would make the driver read the whole
	// string as a literal file name — pragmas silently skipped, a garbage
	// file in the cwd. Run from a fresh directory so any such file is caught.
	t.Chdir(t.TempDir())
	ctx := context.Background()

	db, err := Open("")
	if err != nil {
		t.Fatalf(`Open(""): %v`, err)
	}
	db.Unwrap().SetMaxOpenConns(1)
	mustExec(t, db, testSchema)

	// The default pragmas apply.
	for pragma, want := range map[string]int64{"foreign_keys": 1, "busy_timeout": 5000} {
		var got int64
		if err := db.Unwrap().QueryRow("PRAGMA " + pragma).Scan(&got); err != nil {
			t.Fatalf("PRAGMA %s: %v", pragma, err)
		}
		if got != want {
			t.Errorf("PRAGMA %s = %d, want %d", pragma, got, want)
		}
	}

	// Foreign key enforcement is really live, and the temporary database
	// is writable like any other.
	orphan := post{UserID: 9001, Title: "orphan"}
	if err := rio.Insert(ctx, db, &orphan); !errors.Is(err, rio.ErrForeignKeyViolated) {
		t.Fatalf("orphan insert: got %v, want rio.ErrForeignKeyViolated", err)
	}
	owner := user{Email: "owner@example.com", Name: "Owner"}
	if err := rio.Insert(ctx, db, &owner); err != nil {
		t.Fatalf("Insert: %v", err)
	}

	// Nothing is left behind in the working directory, before or after
	// Close — the temporary database lives (and dies) elsewhere.
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

	// Bring-your-own-pool path: New must not touch the DSN, so foreign keys
	// are enabled by hand here.
	sqlDB, err := sql.Open("sqlite", ":memory:?_pragma=foreign_keys(1)")
	if err != nil {
		t.Fatalf("sql.Open: %v", err)
	}
	sqlDB.SetMaxOpenConns(1)
	db := New(sqlDB)
	t.Cleanup(func() {
		if err := db.Close(); err != nil {
			t.Errorf("Close: %v", err)
		}
	})
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

// times is the default write-side time format appended after the pragmas.
const times = "&_time_format=sqlite"

func TestWithDefaultPragmas(t *testing.T) {
	tests := []struct {
		dsn  string
		want string
	}{
		// The empty DSN (private temporary database) is respelled as a
		// "file:" URI: appending "?..." directly would make the '?' the
		// first byte, which the driver reads as a literal file name.
		{"", "file:?_pragma=busy_timeout(5000)&_pragma=foreign_keys(1)" + times},
		{":memory:", ":memory:?_pragma=busy_timeout(5000)&_pragma=foreign_keys(1)" + times},
		{"app.db", "app.db?_pragma=busy_timeout(5000)&_pragma=foreign_keys(1)" + times},
		{"file:app.db?mode=ro", "file:app.db?mode=ro&_pragma=busy_timeout(5000)&_pragma=foreign_keys(1)" + times},
		{"app.db?", "app.db?_pragma=busy_timeout(5000)&_pragma=foreign_keys(1)" + times},
		{"app.db?_pragma=foreign_keys(0)", "app.db?_pragma=foreign_keys(0)&_pragma=busy_timeout(5000)" + times},
		{"app.db?_pragma=busy_timeout(1)&_pragma=foreign_keys(1)", "app.db?_pragma=busy_timeout(1)&_pragma=foreign_keys(1)" + times},
		// URL-encoded and "name = value" pragma spellings are still detected.
		{"app.db?_pragma=busy_timeout%285000%29", "app.db?_pragma=busy_timeout%285000%29&_pragma=foreign_keys(1)" + times},
		{"app.db?_pragma=foreign_keys+%3D+ON", "app.db?_pragma=foreign_keys+%3D+ON&_pragma=busy_timeout(5000)" + times},
		// An explicit time format wins; pragma defaults still apply.
		{"app.db?_time_format=sqlite",
			"app.db?_time_format=sqlite&_pragma=busy_timeout(5000)&_pragma=foreign_keys(1)"},
		// The driver treats a leading '?' as part of the file name.
		{"?weird", "?weird"},
		// A malformed query passes through for the driver to reject.
		{"app.db?_pragma=%zz", "app.db?_pragma=%zz"},
	}
	for _, tt := range tests {
		if got := withDefaultPragmas(tt.dsn); got != tt.want {
			t.Errorf("withDefaultPragmas(%q) = %q, want %q", tt.dsn, got, tt.want)
		}
	}
}
