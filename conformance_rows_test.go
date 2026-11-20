package gomysql_test

import (
	"database/sql"
	"encoding/json"
	"math"
	"os"
	"reflect"
	"testing"
	"time"

	"github.com/darxnet/gomysql"
)

// queryRows runs q in the text protocol, or in the binary one.
func queryRows(t *testing.T, c *gomysql.Conn, binary bool, q string, args ...any) *gomysql.Rows {
	t.Helper()

	if !binary {
		rows, err := c.Query(q, args...)
		if err != nil {
			t.Fatal(err)
		}

		return rows
	}

	stmt, err := c.Prepare(q)
	if err != nil {
		t.Fatal(err)
	}

	rows, err := stmt.Query(args...)
	if err != nil {
		t.Fatal(err)
	}

	return rows
}

func TestConformanceColumnTypes(t *testing.T) {
	t.Parallel()

	c, tbl := confSetup(t)
	defer confRecover(t)

	mustExec(t, c, "CREATE TABLE "+tbl+" (i INT NOT NULL, u BIGINT UNSIGNED, d DECIMAL(10,2) NOT NULL, "+
		"v VARCHAR(10), b VARBINARY(8), t TEXT, dt DATETIME(3), tm TIME NOT NULL, bt BIT(3), e ENUM('a')) "+
		"DEFAULT CHARSET=utf8mb4")
	mustExec(t, c, "INSERT INTO "+tbl+" VALUES (1, 2, 3.5, 'v', 'b', 't', NOW(), '01:00:00', b'101', 'a')")

	type want struct {
		name     string
		scan     reflect.Type
		nullable bool
	}

	wants := []want{
		{"INT", reflect.TypeFor[int32](), false},
		{"BIGINT UNSIGNED", reflect.TypeFor[sql.Null[uint64]](), true},
		{"DECIMAL", reflect.TypeFor[string](), false},
		{"VARCHAR", reflect.TypeFor[sql.Null[string]](), true},
		{"VARBINARY", reflect.TypeFor[[]byte](), true},
		{"TEXT", reflect.TypeFor[sql.Null[string]](), true},
		{"DATETIME", reflect.TypeFor[sql.Null[time.Time]](), true},
		{"TIME", reflect.TypeFor[time.Duration](), false},
		{"BIT", reflect.TypeFor[sql.Null[uint64]](), true},
		{"ENUM", reflect.TypeFor[sql.Null[string]](), true},
	}

	for _, binary := range []bool{false, true} {
		rows := queryRows(t, c, binary, "SELECT i, u, d, v, b, t, dt, tm, bt, e FROM "+tbl)

		types, err := rows.ColumnTypes()
		if err != nil || len(types) != len(wants) {
			t.Fatalf("binary %v: %d types, %v", binary, len(types), err)
		}

		for i, ct := range types {
			nullable, _ := ct.Nullable()
			if got := (want{ct.DatabaseTypeName(), ct.ScanType(), nullable}); got != wants[i] {
				t.Errorf("binary %v, %s: %v, want %v", binary, ct.Name(), got, wants[i])
			}
		}

		if p, s, ok := types[2].DecimalSize(); p != 10 || s != 2 || !ok {
			t.Errorf("binary %v: DecimalSize %d, %d, %v; want 10, 2", binary, p, s, ok)
		}

		if n, ok := types[3].Length(); n != 40 || !ok {
			t.Errorf("binary %v: Length of VARCHAR(10) %d, %v; want 40 bytes of utf8mb4", binary, n, ok)
		}

		// Every column scans into its ScanType.
		dest := make([]any, len(types))
		for i, ct := range types {
			dest[i] = reflect.New(ct.ScanType()).Interface()
		}

		if !rows.Next() {
			t.Fatal("no row", rows.Err())
		}

		if err := rows.Scan(dest...); err != nil {
			t.Errorf("binary %v: Scan into ScanType: %v", binary, err)
		}

		if err := rows.Close(); err != nil {
			t.Fatal(err)
		}
	}
}

// Location: time.Time arguments keep their instant whatever their zone,
// and values read are in Config.Location.
func TestConformanceLocation(t *testing.T) {
	t.Parallel()

	_, tbl := confSetup(t)
	defer confRecover(t)

	kyiv := time.FixedZone("EET", 2*60*60)
	when := time.Date(2026, 11, 18, 15, 31, 33, 123456000, kyiv)

	for _, loc := range []*time.Location{nil, kyiv} {
		c, err := gomysql.Connect(gomysql.Config{
			Network: "tcp", Address: os.Getenv("GOMYSQL_ADDR"), User: "b", Password: "b",
			Database: "gomysql", Location: loc,
		})
		if err != nil {
			t.Fatal(err)
		}

		var stmt *gomysql.Stmt

		mustExec(t, c, "CREATE TABLE IF NOT EXISTS "+tbl+" (id INT, dt DATETIME(6))")
		mustExec(t, c, "DELETE FROM "+tbl)

		// Arguments of Conn.Exec are interpolated, which is broken for
		// time.Time until phase 9 (TestConformanceInterpolation/time-arg).
		stmt, err = c.Prepare("INSERT INTO " + tbl + " VALUES (?, ?)")
		if err != nil {
			t.Fatal(err)
		}

		for id, arg := range []time.Time{when, when.In(time.UTC)} {
			if _, err = stmt.Exec(id, arg); err != nil {
				t.Fatal(err)
			}
		}

		want := loc
		if want == nil {
			want = time.UTC
		}

		for _, binary := range []bool{false, true} {
			rows := queryRows(t, c, binary, "SELECT dt FROM "+tbl+" ORDER BY id")

			for rows.Next() {
				var got time.Time
				if err := rows.Scan(&got); err != nil || !got.Equal(when) || got.Location() != want {
					t.Errorf("Location %v, binary %v: %v, %v; want %v in %v", loc, binary, got, err, when, want)
				}
			}

			if err := rows.Close(); err != nil {
				t.Fatal(err)
			}
		}

		_ = c.Close()
	}
}

// TIME into time.Duration, BIT into integers, and what an any gets.
func TestConformanceConversions(t *testing.T) {
	t.Parallel()

	c, tbl := confSetup(t)
	defer confRecover(t)

	mustExec(t, c, "CREATE TABLE "+tbl+" (tm TIME, tm1 TIME(1), bt BIT(10), b1 BIT(1), "+
		"u BIGINT UNSIGNED, v VARCHAR(8), vb VARBINARY(8), j JSON) DEFAULT CHARSET=utf8mb4")
	mustExec(t, c, "INSERT INTO "+tbl+" VALUES ('-838:59:59', '12:00:00.5', b'1000000001', b'1', "+
		"18446744073709551615, 'x', 'y', '{\"a\": [1, 2]}')")

	for _, binary := range []bool{false, true} {
		rows := queryRows(t, c, binary, "SELECT tm, tm1, bt, b1, u, v, vb, j, j AS j2, bt AS bt2, b1 AS b2 FROM "+tbl)
		if !rows.Next() {
			t.Fatal("no row", rows.Err())
		}

		var (
			tm, tm1 time.Duration
			bt      uint64
			b1      bool
			u, v    any
			vb      any
			j       struct{ A []int }
			jAny    any
			btAny   any
			b1Null  sql.NullBool
		)

		if err := rows.Scan(&tm, &tm1, &bt, &b1, &u, &v, &vb, &j, &jAny, &btAny, &b1Null); err != nil {
			t.Fatalf("binary %v: %v", binary, err)
		}

		// MariaDB sends JSON as LONGTEXT: an any gets a string.
		if s, ok := jAny.(string); ok {
			jAny = json.RawMessage(s)
		}

		got := []any{tm, tm1, bt, b1, u, v, vb, j, string(jAny.(json.RawMessage)), btAny, b1Null}
		want := []any{
			-(838*time.Hour + 59*time.Minute + 59*time.Second), 12*time.Hour + 500*time.Millisecond,
			uint64(513), true, uint64(math.MaxUint64), "x", []byte("y"), struct{ A []int }{[]int{1, 2}},
			`{"a": [1, 2]}`, uint64(513), sql.NullBool{Bool: true, Valid: true},
		}

		if !reflect.DeepEqual(got, want) {
			t.Errorf("binary %v:\n got %#v\nwant %#v", binary, got, want)
		}

		if err := rows.Close(); err != nil {
			t.Fatal(err)
		}
	}
}
