package sqlite

import (
	"context"
	"database/sql"
	"errors"
	"iter"
	"slices"
	"strconv"
	"testing"
	"time"

	"github.com/go-rio/rio"
)

type kind struct {
	ID    int64
	I     int64
	F     float64
	S     string
	B     []byte
	Flag  bool
	Stamp time.Time
	Opt   *string
	Seen  sql.Null[time.Time]
}

const kindSchema = `CREATE TABLE kinds (
	id INTEGER PRIMARY KEY,
	i INTEGER, f REAL, s TEXT, b BLOB, flag INTEGER,
	stamp DATETIME, opt TEXT, seen TIMESTAMP
)`

type stamped struct {
	ID    int64
	Stamp time.Time
}

func TestNativeStorageClasses(t *testing.T) {
	ctx := context.Background()
	db := openTestDB(t, ":memory:")
	mustExec(t, db, kindSchema)

	opt := "present"
	stamp := time.Date(2026, 9, 2, 11, 1, 49, 123456000, time.UTC)
	rows := []kind{
		{I: -7, F: 1.5, S: "text", B: []byte{1, 2, 3}, Flag: true, Stamp: stamp, Opt: &opt,
			Seen: sql.Null[time.Time]{V: stamp, Valid: true}},
		{S: "", B: []byte{}, Stamp: stamp},
	}
	for i := range rows {
		if err := rio.Insert(ctx, db, &rows[i]); err != nil {
			t.Fatalf("Insert %d: %v", i, err)
		}
	}
	got, err := rio.From[kind]().OrderBy("id").All(ctx, db)
	if err != nil {
		t.Fatalf("All: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("got %d rows", len(got))
	}
	k := got[0]
	isSame := k.I == -7 && k.F == 1.5 && k.S == "text" && string(k.B) == "\x01\x02\x03" && k.Flag &&
		k.Stamp.Equal(stamp) && k.Opt != nil && *k.Opt == "present" && k.Seen.Valid && k.Seen.V.Equal(stamp)
	if !isSame {
		t.Fatalf("row 0 = %+v", k)
	}
	if k.Stamp.Location() != time.UTC {
		t.Fatalf("Stamp location = %v, want UTC", k.Stamp.Location())
	}
	k = got[1]
	if k.S != "" || len(k.B) != 0 || k.Flag || k.Opt != nil || k.Seen.Valid {
		t.Fatalf("row 1 = %+v", k)
	}
	var kinds []string
	for _, col := range []string{"s", "b", "opt", "seen"} {
		v, err := rio.Raw[string]("SELECT typeof("+col+") FROM kinds WHERE id = 2").First(ctx, db)
		if err != nil {
			t.Fatalf("typeof %s: %v", col, err)
		}
		kinds = append(kinds, *v)
	}
	if want := []string{"text", "blob", "null", "null"}; !slices.Equal(kinds, want) {
		t.Fatalf("storage classes = %v, want %v", kinds, want)
	}
}

func TestNativeTimeTextForms(t *testing.T) {
	ctx := context.Background()
	db := openTestDB(t, ":memory:")
	mustExec(t, db, "CREATE TABLE stampeds (id INTEGER PRIMARY KEY, stamp DATETIME)")
	cases := []struct {
		text string
		want time.Time
	}{
		{"2026-01-02 03:04:05", time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)},
		{"2026-01-02T03:04:05Z", time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)},
		{"2026-01-02 03:04:05.123456+00:00", time.Date(2026, 1, 2, 3, 4, 5, 123456000, time.UTC)},
		{"2026-01-02 03:04:05.5-01:30", time.Date(2026, 1, 2, 4, 34, 5, 500000000, time.UTC)},
		{"2026-01-02T03:04:05.123456789+08:00", time.Date(2026, 1, 1, 19, 4, 5, 123456789, time.UTC)},
		{"2026-01-02 03:04", time.Date(2026, 1, 2, 3, 4, 0, 0, time.UTC)},
		{"2026-01-02", time.Date(2026, 1, 2, 0, 0, 0, 0, time.UTC)},
	}
	for i, tc := range cases {
		mustExec(t, db, "INSERT INTO stampeds (id, stamp) VALUES (?, ?)", i+1, tc.text)
	}
	got, err := rio.From[stamped]().OrderBy("id").All(ctx, db)
	if err != nil {
		t.Fatalf("All: %v", err)
	}
	for i, tc := range cases {
		if !got[i].Stamp.Equal(tc.want) || got[i].Stamp.Location() != time.UTC {
			t.Errorf("%q decoded as %v, want %v", tc.text, got[i].Stamp, tc.want)
		}
	}
	mustExec(t, db, "INSERT INTO stampeds (id, stamp) VALUES (99, 'not a time')")
	if _, err := rio.Find[stamped](ctx, db, 99); err == nil {
		t.Fatal("garbage time text should fail to scan")
	}
}

func TestNativeTransactions(t *testing.T) {
	ctx := context.Background()
	db := openTestDB(t, ":memory:")
	mustExec(t, db, testSchema)

	err := db.Tx(ctx, func(tx *rio.Tx) error {
		if err := rio.Insert(ctx, tx, &user{Email: "a@example.com", Name: "A"}); err != nil {
			return err
		}
		return tx.Tx(ctx, func(sp *rio.Tx) error {
			_ = rio.Insert(ctx, sp, &user{Email: "b@example.com", Name: "B"})
			return errors.New("undo the savepoint")
		})
	})
	if err == nil || err.Error() != "undo the savepoint" {
		t.Fatalf("Tx error = %v", err)
	}
	if n, _ := rio.From[user]().Count(ctx, db); n != 0 {
		t.Fatalf("rolled-back transaction left %d rows", n)
	}
	err = db.Tx(ctx, func(tx *rio.Tx) error {
		return rio.Insert(ctx, tx, &user{Email: "a@example.com", Name: "A"})
	})
	if err != nil {
		t.Fatalf("Tx: %v", err)
	}
	err = db.TxWith(ctx, &sql.TxOptions{ReadOnly: true}, func(tx *rio.Tx) error {
		n, err := rio.From[user]().Count(ctx, tx)
		if err == nil && n != 1 {
			return errors.New("read-only transaction saw " + strconv.FormatInt(n, 10) + " rows")
		}
		return err
	})
	if err != nil {
		t.Fatalf("TxWith: %v", err)
	}

	nd := &nativeDB{p: PoolOf(db)}
	tx, err := nd.Begin(ctx, nil)
	if err != nil {
		t.Fatalf("Begin: %v", err)
	}
	if err := tx.Commit(ctx); err != nil {
		t.Fatalf("Commit: %v", err)
	}
	if err := tx.Commit(ctx); !errors.Is(err, sql.ErrTxDone) {
		t.Fatalf("second Commit = %v, want ErrTxDone", err)
	}
	if err := tx.Rollback(ctx); !errors.Is(err, sql.ErrTxDone) {
		t.Fatalf("Rollback after Commit = %v, want ErrTxDone", err)
	}
}

func TestNativeCancelInterrupts(t *testing.T) {
	db := openTestDB(t, ":memory:")
	short, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
	defer cancel()
	const endless = "WITH RECURSIVE c(x) AS (SELECT 1 UNION ALL SELECT x + 1 FROM c) SELECT count(*) FROM c"
	if _, err := rio.Raw[int64](endless).First(short, db); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("endless query = %v, want DeadlineExceeded", err)
	}
	v, err := rio.Raw[int64]("SELECT 42").First(context.Background(), db)
	if err != nil || *v != 42 {
		t.Fatalf("query after an interrupt = %v, %v", v, err)
	}
}

func TestNativeStatementCacheBounded(t *testing.T) {
	ctx := context.Background()
	db := openTestDB(t, ":memory:")
	for i := range stmtCacheSize + 16 {
		if _, err := rio.Raw[int64]("SELECT "+strconv.Itoa(i)).First(ctx, db); err != nil {
			t.Fatal(err)
		}
	}
	c := PoolOf(db).idle[0]
	if len(c.stmts) != stmtCacheSize {
		t.Fatalf("cache holds %d statements, want %d", len(c.stmts), stmtCacheSize)
	}
	if c.stmts["SELECT 0"] != nil || c.stmts["SELECT "+strconv.Itoa(stmtCacheSize+15)] == nil {
		t.Fatal("eviction is not least-recently-used")
	}
}

func TestNativeRowsKeepStatementPrivate(t *testing.T) {
	ctx := context.Background()
	db := openTestDB(t, ":memory:")
	mustExec(t, db, testSchema)
	for _, name := range []string{"a", "b", "c"} {
		mustExec(t, db, "INSERT INTO users (email, name) VALUES (?, ?)", name+"@example.com", name)
	}
	err := db.Tx(ctx, func(tx *rio.Tx) error {
		q := rio.From[user]().OrderBy("id")
		next, stop := iter.Pull2(q.Rows(ctx, tx))
		defer stop()
		first, err, _ := next()
		if err != nil {
			return err
		}
		all, err := q.All(ctx, tx)
		if err != nil {
			return err
		}
		second, err, _ := next()
		if err != nil {
			return err
		}
		if first.Name != "a" || second.Name != "b" || len(all) != 3 {
			return errors.New("interleaved statements disturbed each other")
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}

func TestNativeMultiStatementText(t *testing.T) {
	ctx := context.Background()
	db := openTestDB(t, ":memory:")
	res, err := rio.Exec(ctx, db, "CREATE TABLE a (x INTEGER); INSERT INTO a VALUES (1); INSERT INTO a VALUES (2), (3)")
	if err != nil {
		t.Fatal(err)
	}
	if n, _ := res.RowsAffected(); n != 2 {
		t.Fatalf("RowsAffected = %d, want the last statement's 2", n)
	}
	if _, err := rio.Exec(ctx, db, "INSERT INTO a VALUES (?); INSERT INTO a VALUES (?)", 9, 10); err != nil {
		t.Fatal(err)
	}
	n, err := rio.Raw[int64]("INSERT INTO a VALUES (4); SELECT count(*) FROM a").First(ctx, db)
	if err != nil || *n != 6 {
		t.Fatalf("count = %v, %v; want 6", n, err)
	}
	if _, err := rio.Exec(ctx, db, "-- nothing to run"); err != nil {
		t.Fatalf("comment-only exec: %v", err)
	}
	empty, err := rio.Raw[int64]("SELECT 1; -- trailing comment").All(ctx, db)
	if err != nil || len(empty) != 0 {
		t.Fatalf("trailing comment query = %v, %v; want no rows", empty, err)
	}
}

func TestParseTime(t *testing.T) {
	valid := map[string]time.Time{
		"2026-01-02":                       time.Date(2026, 1, 2, 0, 0, 0, 0, time.UTC),
		"2026-01-02 03:04":                 time.Date(2026, 1, 2, 3, 4, 0, 0, time.UTC),
		"2026-01-02T03:04:05":              time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC),
		"2026-01-02 03:04:05.7":            time.Date(2026, 1, 2, 3, 4, 5, 700000000, time.UTC),
		"2026-01-02 03:04:05.123456789Z":   time.Date(2026, 1, 2, 3, 4, 5, 123456789, time.UTC),
		"2026-01-02 03:04:05.123456+00:00": time.Date(2026, 1, 2, 3, 4, 5, 123456000, time.UTC),
		"2026-01-02T03:04:05-05:30":        time.Date(2026, 1, 2, 8, 34, 5, 0, time.UTC),
		"2024-02-29 00:00:00":              time.Date(2024, 2, 29, 0, 0, 0, 0, time.UTC),
	}
	for text, want := range valid {
		got, ok := parseTime([]byte(text))
		if !ok || !got.Equal(want) || got.Location() != time.UTC {
			t.Errorf("parseTime(%q) = %v, %v; want %v", text, got, ok, want)
		}
	}
	invalid := []string{
		"", "20260102", "2026-13-01", "2026-02-30", "2026-01-02 24:00", "2026-01-02 03:60",
		"2026-01-02 03:04:60", "2026-01-02 03:04:05+0800", "2026-01-02x", "2026-01-02 03:04:05.",
		"2026-01-02 03:04:05.1234567890", "2026-01-02 03:04:05 +00:00", "2026-1-02",
	}
	for _, text := range invalid {
		if got, ok := parseTime([]byte(text)); ok {
			t.Errorf("parseTime(%q) = %v, want rejection", text, got)
		}
	}
}
