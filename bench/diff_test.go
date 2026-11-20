package bench

import (
	"database/sql"
	"fmt"
	"math"
	"os"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/darxnet/gomysql"
)

// Differential tests: every value is written with go-sql-driver, read back
// through database/sql + go-sql-driver (the reference) and through gomysql in
// text and binary protocol into the same destination type, and compared.
//
// GOMYSQL_ADDR=127.0.0.1:3306 go test -run Diff -v

type diffCase struct {
	typ    string
	values []any
	dests  []func() any // new destination pointers, one Scan per type
}

func ints[T any]() func() any { return func() any { return new(T) } }

var diffCases = []diffCase{
	{"TINYINT", []any{0, 1, -1, math.MinInt8, math.MaxInt8}, []func() any{ints[int64](), ints[int8](), ints[int]()}},
	{"TINYINT UNSIGNED", []any{0, 1, math.MaxUint8}, []func() any{ints[uint64](), ints[uint8]()}},
	{"SMALLINT", []any{0, -1, math.MinInt16, math.MaxInt16}, []func() any{ints[int64](), ints[int16]()}},
	{"SMALLINT UNSIGNED", []any{0, math.MaxUint16}, []func() any{ints[uint64](), ints[uint16]()}},
	{"MEDIUMINT", []any{0, -1, -1 << 23, 1<<23 - 1}, []func() any{ints[int64](), ints[int32]()}},
	{"MEDIUMINT UNSIGNED", []any{0, 1<<24 - 1}, []func() any{ints[uint64](), ints[uint32]()}},
	{"INT", []any{0, -1, math.MinInt32, math.MaxInt32}, []func() any{ints[int64](), ints[int32]()}},
	{"INT UNSIGNED", []any{0, uint64(math.MaxUint32)}, []func() any{ints[uint64](), ints[uint32]()}},
	{"BIGINT", []any{0, -1, int64(math.MinInt64), int64(math.MaxInt64)}, []func() any{ints[int64]()}},
	{"BIGINT UNSIGNED", []any{0, uint64(math.MaxUint64)}, []func() any{ints[uint64]()}},
	{"DOUBLE", []any{0.0, -1.5, math.MaxFloat64, math.SmallestNonzeroFloat64}, []func() any{ints[float64]()}},
	{"FLOAT", []any{0.0, -1.5, float32(3.25)}, []func() any{ints[float32](), ints[float64]()}},
	{"DECIMAL(20,6)", []any{"0.000000", "-12345678901234.123456"}, []func() any{ints[string]()}},
	{"VARCHAR(255)", []any{"", "abc", "ünï©ødé 😀", strings.Repeat("x", 255)}, []func() any{ints[string](), ints[[]byte]()}},
	{"TEXT", []any{"", strings.Repeat("y", 70000/2)}, []func() any{ints[string](), ints[[]byte]()}},
	{"VARBINARY(16)", []any{[]byte{}, []byte{0, 1, 0xff}}, []func() any{ints[[]byte](), ints[string]()}},
	{"BLOB", []any{[]byte{0, 0xfe, 'a'}}, []func() any{ints[[]byte]()}},
	{"DATE", []any{"2026-11-18", "1000-01-01"}, []func() any{ints[time.Time](), ints[string]()}},
	{"DATETIME", []any{"2026-11-18 13:31:33", "1000-01-01 00:00:00"}, []func() any{ints[time.Time](), ints[string]()}},
	{"DATETIME(6)", []any{"2026-11-18 13:31:33.123456"}, []func() any{ints[time.Time](), ints[string]()}},
	{"TIMESTAMP", []any{"2026-11-18 13:31:33"}, []func() any{ints[time.Time](), ints[string]()}},
	{"BIT(8)", []any{[]byte{0x5a}}, []func() any{ints[[]byte]()}},
	{"ENUM('a','b')", []any{"a", "b"}, []func() any{ints[string]()}},
	{"INT NULL", []any{nil, 7}, []func() any{ints[sql.NullInt64](), ints[*int64](), ints[any](), ints[[]byte]()}},
	{"VARCHAR(8) NULL", []any{nil, "x"}, []func() any{ints[sql.NullString](), ints[*string](), ints[sql.RawBytes]()}},
	{"DATETIME NULL", []any{nil, "2026-11-18 13:31:33"}, []func() any{ints[sql.NullTime](), ints[*time.Time]()}},

	// More destinations: any, sql.RawBytes, bool, named types, conversions
	// between kinds; errors must match too.
	{"TINYINT(1)", []any{0, 1, 2}, []func() any{ints[bool](), ints[any]()}},
	{"INT ", []any{0, -7, math.MaxInt32}, []func() any{
		ints[any](), ints[sql.RawBytes](), ints[string](), ints[float64](), ints[myInt](), ints[uint8](), ints[sql.NullFloat64](),
	}},
	{"BIGINT UNSIGNED ", []any{uint64(math.MaxUint64), 5}, []func() any{ints[any](), ints[string](), ints[int64]()}},
	{"DOUBLE ", []any{2.0, -1.5}, []func() any{ints[any](), ints[string](), ints[int64](), ints[sql.NullFloat64]()}},
	{"FLOAT ", []any{0.1, 3.25}, []func() any{ints[any](), ints[string]()}},
	{"DECIMAL(10,2)", []any{"1.50", "-3.00"}, []func() any{ints[float64](), ints[any](), ints[int64]()}},
	{"VARCHAR(16) ", []any{"42", "x", "true"}, []func() any{ints[any](), ints[sql.RawBytes](), ints[int64](), ints[bool](), ints[myString]()}},
	{"DATE ", []any{"2026-11-18", "1000-01-01"}, []func() any{ints[any](), ints[sql.NullTime](), ints[[]byte]()}},
	{"DATETIME(3)", []any{"2026-11-18 13:31:33.120", "1000-01-01 00:00:00"}, []func() any{ints[any](), ints[string](), ints[time.Time]()}},
	{"TIME", []any{"13:31:33", "-838:59:59", "00:00:00"}, []func() any{ints[string](), ints[[]byte]()}},
	{"TIME(3)", []any{"-01:02:03.450", "100:00:00.000"}, []func() any{ints[string]()}},
	{"YEAR", []any{2026, 1901}, []func() any{ints[int64](), ints[string]()}},
}

type (
	myInt    int64
	myString string
)

// knownDiffBugs: "TYPE/dest/protocol" → ROADMAP phase. Removed once fixed.
var knownDiffBugs = map[string]string{}

func TestDiff(t *testing.T) {
	addr := os.Getenv("GOMYSQL_ADDR")
	if addr == "" {
		t.Skip("GOMYSQL_ADDR not set")
	}

	// parseTime only for time.Time destinations: with it go-sql-driver also
	// formats DATE/DATETIME as RFC 3339 when scanning into a string.
	std := openRef(t, addr, "")
	stdTime := openRef(t, addr, "&parseTime=true&loc=UTC")

	for _, c := range diffCases {
		mustStd(t, std, "DROP TABLE IF EXISTS diff")
		mustStd(t, std, "CREATE TABLE diff (id INT AUTO_INCREMENT PRIMARY KEY, v "+c.typ+") DEFAULT CHARSET=utf8mb4")
		for _, v := range c.values {
			mustStd(t, std, "INSERT INTO diff (v) VALUES (?)", v)
		}

		for _, newDest := range c.dests {
			dt := reflect.TypeOf(newDest()).Elem().String()
			ref := std
			if wantsTime(c.typ, newDest()) {
				ref = stdTime
			}

			for _, proto := range []string{"text", "binary"} {
				want, wantErr := readStd(ref, proto, newDest)
				name := strings.TrimSpace(c.typ) + "/" + dt + "/" + proto
				t.Run(name, func(t *testing.T) {
					if reason, ok := knownDiffBugs[name]; ok && os.Getenv("GOMYSQL_KNOWN_BUGS") == "" {
						t.Skip("known bug, " + reason)
					}

					// A fresh connection: a failed Scan leaves the old one out of sync.
					cc, err := gomysql.Open(gomysql.Config{
						Protocol: "tcp", Address: addr, User: "b", Password: "b",
						DbName: "gomysql", Charset: gomysql.CharSet_utf8mb4_bin,
					})
					if err != nil {
						t.Fatal(err)
					}
					defer func() { _ = cc.Close() }()

					got, err := readCC(cc, proto, newDest)
					if (err != nil) != (wantErr != nil) {
						t.Fatalf("gomysql: %v (want %s, %v)", err, show(want), wantErr)
					}
					if err == nil && !reflect.DeepEqual(got, want) {
						t.Errorf("\n got %s\nwant %s", show(got), show(want))
					}
				})
			}
		}
	}
}

const diffQuery = "SELECT v FROM diff ORDER BY id"

func openRef(t *testing.T, addr, params string) *sql.DB {
	t.Helper()

	db, err := sql.Open("mysql", "b:b@tcp("+addr+")/gomysql?charset=utf8mb4"+params)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	db.SetMaxOpenConns(1)

	return db
}

func mustStd(t *testing.T, db *sql.DB, q string, args ...any) {
	t.Helper()

	if _, err := db.Exec(q, args...); err != nil {
		t.Fatalf("%s: %v", q, err)
	}
}

// wantsTime reports whether the reference should use parseTime: for
// time.Time destinations, and for any and sql.Scanner destinations of date
// columns, which gomysql fills with time.Time.
func wantsTime(typ string, dest any) bool {
	switch dest.(type) {
	case *time.Time, *sql.NullTime, **time.Time:
		return true
	case *any:
		return strings.HasPrefix(typ, "DATE") || strings.HasPrefix(typ, "TIMESTAMP")
	}

	return false
}

// readStd reads the rows through database/sql: in the text protocol, or
// in the binary one with an argument, which go-sql-driver sends as a
// prepared statement.
func readStd(db *sql.DB, proto string, newDest func() any) ([]any, error) {
	q, args := diffQuery, []any(nil)
	if proto == "binary" {
		q, args = "SELECT v FROM diff WHERE ? ORDER BY id", []any{1}
	}

	rows, err := db.Query(q, args...)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()

	var out []any
	for rows.Next() {
		d := newDest()
		if err := rows.Scan(d); err != nil {
			return out, err
		}
		out = append(out, deref(d))
	}

	return out, rows.Err()
}

func readCC(c *gomysql.Conn, proto string, newDest func() any) (out []any, err error) {
	defer func() {
		if r := recover(); r != nil {
			err = fmt.Errorf("panic: %v", r)
		}
	}()

	var rows interface {
		Next() bool
		Scan(dest ...any) error
		Err() error
		Close() error
	}

	switch proto {
	case "text":
		r, err := c.Query(diffQuery)
		if err != nil {
			return nil, err
		}
		rows = r
	default:
		st, err := c.Prepare(diffQuery)
		if err != nil {
			return nil, err
		}
		defer func() { _ = st.Close() }()
		r, err := st.Query()
		if err != nil {
			return nil, err
		}
		rows = r
	}
	defer func() { _ = rows.Close() }()

	for rows.Next() {
		d := newDest()
		if err := rows.Scan(d); err != nil {
			return out, err
		}
		out = append(out, deref(d))
	}

	return out, rows.Err()
}

func deref(p any) any {
	v := reflect.ValueOf(p).Elem().Interface()
	if b, ok := v.([]byte); ok {
		return append([]byte{}, b...) // detach from driver buffers
	}
	if b, ok := v.(sql.RawBytes); ok {
		return sql.RawBytes(append([]byte{}, b...))
	}
	if tm, ok := v.(time.Time); ok {
		return tm.UTC()
	}

	return v
}

func show(v []any) string {
	var sb strings.Builder
	for i, x := range v {
		if i > 0 {
			sb.WriteString(", ")
		}
		s := fmt.Sprintf("%#v", x)
		if len(s) > 60 {
			s = s[:60] + "…"
		}
		sb.WriteString(s)
	}

	return "[" + sb.String() + "]"
}
