// Package bench compares gomysql with database/sql and go-sql-driver/mysql.
// It is a separate module so that gomysql itself has no dependencies.
package bench

import (
	"database/sql"
	"os"
	"syscall"
	"testing"
	"time"

	"github.com/darxnet/gomysql"
	_ "github.com/go-sql-driver/mysql"
)

// GOMYSQL_ADDR=127.0.0.1:3306 go test -run '^$' -bench Driver -benchmem
//
// The server comes from ../scripts/testdb.sh; the first run fills the tables
// (1M rows each), later runs reuse them.
func benchAddr(b *testing.B) string {
	b.Helper()

	addr := os.Getenv("GOMYSQL_ADDR")
	if addr == "" {
		b.Skip("GOMYSQL_ADDR not set")
	}

	fillOnce.Do(func() { fillErr = fill(addr) })

	if fillErr != nil {
		b.Fatal(fillErr)
	}

	return addr
}

const (
	qAU = "SELECT audienceID, userID, keepDays FROM au"
	qCM = "SELECT id, ext, ts, keepDays FROM cm"
)

func openCC(b *testing.B) *gomysql.Conn {
	c, err := gomysql.Connect(gomysql.Config{Network: "tcp", Address: benchAddr(b), User: "b", Password: "b", Database: "gomysql"})
	if err != nil {
		b.Fatal(err)
	}

	b.Cleanup(func() { _ = c.Close() })

	return c
}

func openStd(b *testing.B) *sql.DB {
	db, err := sql.Open("mysql", "b:b@tcp("+benchAddr(b)+")/gomysql")
	if err != nil {
		b.Fatal(err)
	}

	db.SetMaxOpenConns(1)
	b.Cleanup(func() { _ = db.Close() })

	if err := db.Ping(); err != nil {
		b.Fatal(err)
	}

	return db
}

type scanner interface {
	Next() bool
	Scan(dest ...any) error
	Err() error
	Close() error
}

func drain(b *testing.B, rows scanner, dest ...any) int {
	n := 0

	for rows.Next() {
		if err := rows.Scan(dest...); err != nil {
			b.Fatal(err)
		}
		n++
	}

	if err := rows.Err(); err != nil {
		b.Fatal(err)
	}

	_ = rows.Close()

	return n
}

func cpuTime() time.Duration {
	var ru syscall.Rusage
	_ = syscall.Getrusage(syscall.RUSAGE_SELF, &ru)

	return time.Duration(ru.Utime.Nano() + ru.Stime.Nano())
}

func report(b *testing.B, rows int, cpu0 time.Duration) {
	b.ReportMetric(float64(b.Elapsed().Nanoseconds())/float64(rows), "ns/row")
	b.ReportMetric(float64((cpuTime()-cpu0).Nanoseconds())/float64(rows), "cpu-ns/row")
}

func benchCC(b *testing.B, q string, dest ...any) {
	c := openCC(b)
	total := 0
	cpu0 := cpuTime()

	b.ReportAllocs()

	for b.Loop() {
		stmt, err := c.Prepare(q)
		if err != nil {
			b.Fatal(err)
		}

		rows, err := stmt.Query()
		if err != nil {
			b.Fatal(err)
		}

		total += drain(b, rows, dest...)
		_ = stmt.Close()
	}

	report(b, total, cpu0)
}

func benchStdPrepared(b *testing.B, q string, dest ...any) {
	db := openStd(b)
	total := 0
	cpu0 := cpuTime()

	b.ReportAllocs()

	for b.Loop() {
		stmt, err := db.Prepare(q)
		if err != nil {
			b.Fatal(err)
		}

		rows, err := stmt.Query()
		if err != nil {
			b.Fatal(err)
		}

		total += drain(b, rows, dest...)
		_ = stmt.Close()
	}

	report(b, total, cpu0)
}

func benchStdText(b *testing.B, q string, dest ...any) {
	db := openStd(b)
	total := 0
	cpu0 := cpuTime()

	b.ReportAllocs()

	for b.Loop() {
		rows, err := db.Query(q)
		if err != nil {
			b.Fatal(err)
		}

		total += drain(b, rows, dest...)
	}

	report(b, total, cpu0)
}

func BenchmarkDriverInts(b *testing.B) {
	var (
		aud  uint32
		user uint64
		keep uint16
	)

	b.Run("gomysql", func(b *testing.B) { benchCC(b, qAU, &aud, &user, &keep) })
	b.Run("std-prepared", func(b *testing.B) { benchStdPrepared(b, qAU, &aud, &user, &keep) })
	b.Run("std-text", func(b *testing.B) { benchStdText(b, qAU, &aud, &user, &keep) })
}

func BenchmarkDriverRawBytes(b *testing.B) {
	var (
		id   uint64
		ext  sql.RawBytes
		ts   uint32
		keep uint16
	)

	b.Run("gomysql", func(b *testing.B) { benchCC(b, qCM, &id, &ext, &ts, &keep) })
	b.Run("std-prepared", func(b *testing.B) { benchStdPrepared(b, qCM, &id, &ext, &ts, &keep) })
	b.Run("std-text", func(b *testing.B) { benchStdText(b, qCM, &id, &ext, &ts, &keep) })
}

func BenchmarkDriverString(b *testing.B) {
	var (
		id   uint64
		ext  string
		ts   uint32
		keep uint16
	)

	b.Run("gomysql", func(b *testing.B) { benchCC(b, qCM, &id, &ext, &ts, &keep) })
	b.Run("std-prepared", func(b *testing.B) { benchStdPrepared(b, qCM, &id, &ext, &ts, &keep) })
	b.Run("std-text", func(b *testing.B) { benchStdText(b, qCM, &id, &ext, &ts, &keep) })
}
