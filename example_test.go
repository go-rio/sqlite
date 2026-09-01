package sqlite_test

import (
	"context"
	"database/sql"
	"log"

	"github.com/go-rio/rio"
	"github.com/go-rio/sqlite"
)

type User struct {
	ID    int64
	Email string
	Age   int
}

func ExampleOpen() {
	db, err := sqlite.Open("app.db")
	if err != nil {
		log.Fatal(err)
	}
	defer db.Close()

	ctx := context.Background()
	if err := rio.Insert(ctx, db, &User{Email: "a@example.com", Age: 30}); err != nil {
		log.Fatal(err)
	}
	users, err := rio.From[User]().Where("age > ?", 18).All(ctx, db)
	if err != nil {
		log.Fatal(err)
	}
	log.Printf("%d adults", len(users))
}

// A plain ":memory:" DSN gives every pooled connection its own database;
// the shared-cache form with one connection is the test-suite shape.
func ExampleOpen_sharedMemory() {
	db, err := sqlite.Open("file:app?mode=memory&cache=shared")
	if err != nil {
		log.Fatal(err)
	}
	defer db.Close()
	db.Unwrap().SetMaxOpenConns(1)
}

// WAL lets readers proceed while one writer holds the lock; a longer busy
// timeout keeps queued writers waiting instead of failing.
func ExampleOpen_wal() {
	db, err := sqlite.Open("app.db?_pragma=journal_mode(WAL)&_pragma=busy_timeout(10000)")
	if err != nil {
		log.Fatal(err)
	}
	defer db.Close()
}

// New wraps a pool the caller opened; the DSN defaults are the caller's
// responsibility.
func ExampleNew() {
	raw, err := sql.Open("sqlite", "app.db?_pragma=foreign_keys(1)")
	if err != nil {
		log.Fatal(err)
	}

	db := sqlite.New(raw, rio.WithoutStmtCache())
	defer db.Close()
}
