package sqlite_test

import (
	"context"
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

// A plain ":memory:" DSN is private to its single connection; the
// shared-cache form is reachable from OpenSQL and Unwrap as well.
func ExampleOpen_sharedMemory() {
	db, err := sqlite.Open("file:app?mode=memory&cache=shared")
	if err != nil {
		log.Fatal(err)
	}
	defer db.Close()
}

// WAL lets readers proceed while one writer holds the lock; a longer busy
// timeout keeps queued writers waiting instead of failing, and the pool cap
// bounds how many connections contend.
func ExampleOpen_wal() {
	db, err := sqlite.Open("app.db?_journal_mode=WAL&_busy_timeout=10000")
	if err != nil {
		log.Fatal(err)
	}
	defer db.Close()
	sqlite.PoolOf(db).SetMaxConns(8)
}

// OpenSQL is the plain database/sql handle go-rio/migrate consumes.
func ExampleOpenSQL() {
	sqlDB, err := sqlite.OpenSQL("app.db")
	if err != nil {
		log.Fatal(err)
	}
	defer sqlDB.Close()

	if _, err := sqlDB.Exec("CREATE TABLE IF NOT EXISTS users (id INTEGER PRIMARY KEY, email TEXT, age INTEGER)"); err != nil {
		log.Fatal(err)
	}
}
