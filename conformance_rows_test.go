package gomysql_test

import (
	"database/sql"
	"encoding/json"
	"errors"
	"math"
	"os"
	"reflect"
	"strings"
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
			AllowPublicKeyRetrieval: true,
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

// Interpolated strings come back unchanged, with and without
// NO_BACKSLASH_ESCAPES, and so do the arguments database/sql converts.
func TestConformanceInterpolationRoundTrip(t *testing.T) {
	t.Parallel()

	c := confConn(t, "b", "b")
	defer confRecover(t)

	tricky := []string{
		"", "'", "''", `\`, `\'`, `'\`, `"`, "\x00", "\n\r\x1a", "?", "-- ?", "/* ? */", "#", "`",
		"ünï©ødé 😀", "\xff\xfe", "a\\'; DROP TABLE t; --",
	}

	for _, mode := range []string{"", "NO_BACKSLASH_ESCAPES"} {
		mustExec(t, c, "SET SESSION sql_mode = '"+mode+"'")

		for _, s := range tricky {
			var got, gotBytes string
			if err := c.QueryRow("SELECT ?, ?", s, []byte(s)).Scan(&got, &gotBytes); err != nil || got != s || gotBytes != s {
				t.Errorf("sql_mode %q: %q, %q, %v; want %q", mode, got, gotBytes, err, s)
			}
		}
	}

	type id int64

	type name string

	args := []any{
		id(-5), name("x"), ptrTo(int64(7)), (*string)(nil), sql.NullInt64{}, sql.NullString{String: "y", Valid: true},
	}

	for _, binary := range []bool{false, true} {
		rows := queryRows(t, c, binary, "SELECT ?, ?, ?, ?, ?, ?", args...)
		if !rows.Next() {
			t.Fatal("no row", rows.Err())
		}

		var (
			a      int64
			b      string
			p      int64
			n1, n2 sql.NullString
			s      string
		)

		if err := rows.Scan(&a, &b, &p, &n1, &n2, &s); err != nil {
			t.Fatalf("binary %v: %v", binary, err)
		}

		if a != -5 || b != "x" || p != 7 || n1.Valid || n2.Valid || s != "y" {
			t.Errorf("binary %v: %d %q %d %v %v %q", binary, a, b, p, n1, n2, s)
		}

		_ = rows.Close()
	}

	// MySQL refuses JSON from a binary string, so json.RawMessage is text.
	mustExec(t, c, "CREATE TEMPORARY TABLE json_arg (j JSON)")

	stmt, err := c.Prepare("INSERT INTO json_arg VALUES (?)")
	if err != nil {
		t.Fatal(err)
	}

	for _, exec := range []func(...any) (*gomysql.Result, error){
		func(args ...any) (*gomysql.Result, error) { return c.Exec("INSERT INTO json_arg VALUES (?)", args...) },
		stmt.Exec,
	} {
		if _, err := exec(json.RawMessage(`{"a": 1}`)); err != nil {
			t.Errorf("json.RawMessage: %v", err)
		}
	}
}

func ptrTo[T any](v T) *T { return &v }

// :name placeholders take sql.Named, struct and map arguments in both
// protocols; a slice becomes a list in the text protocol.
func TestConformanceNamedArgs(t *testing.T) {
	t.Parallel()

	c := confConn(t, "b", "b")
	defer confRecover(t)

	type user struct {
		ID   int64  `db:"id"`
		Name string `db:"name"`
	}

	for _, args := range [][]any{
		{sql.Named("id", 7), sql.Named("name", "o'k")},
		{user{ID: 7, Name: "o'k"}},
		{&user{ID: 7, Name: "o'k"}},
		{map[string]any{"id": 7, "name": "o'k"}},
	} {
		for _, binary := range []bool{false, true} {
			var (
				id, again int64
				name      string
			)

			rows := queryRows(t, c, binary, "SELECT :id, :name, :id /* :x */", args...)
			if !rows.Next() {
				t.Fatal("no row", rows.Err())
			}

			if err := rows.Scan(&id, &name, &again); err != nil || id != 7 || name != "o'k" || again != 7 {
				t.Errorf("binary %v, %T: %d %q %d %v", binary, args[0], id, name, again, err)
			}

			_ = rows.Close()
		}
	}

	var n int
	if err := c.QueryRow("SELECT COUNT(*) FROM (SELECT 1 AS v UNION SELECT 2 UNION SELECT 3) t "+
		"WHERE v IN (:vs) AND ? = 1", sql.Named("vs", []int{1, 3})).Scan(&n); err == nil {
		t.Error("mixed placeholders: no error")
	}

	if err := c.QueryRow("SELECT COUNT(*) FROM (SELECT 1 AS v UNION SELECT 2 UNION SELECT 3) t WHERE v IN (?)",
		[]int{1, 3}).Scan(&n); err != nil || n != 2 {
		t.Errorf("IN (?): %d, %v", n, err)
	}

	stmt, err := c.Prepare("SELECT 1 IN (?)")
	if err != nil {
		t.Fatal(err)
	}

	if _, err := stmt.Exec([]int{1}); err == nil || !strings.Contains(err.Error(), "fixed number of placeholders") {
		t.Errorf("slice in Stmt: %v", err)
	}
}

func TestConformanceStructs(t *testing.T) {
	t.Parallel()

	c := confConn(t, "b", "b")
	defer confRecover(t)

	type user struct {
		ID       int64
		UserName string
		Note     sql.NullString `db:"note"`
	}

	const q = "SELECT 1 AS id, 'ann' AS user_name, NULL AS note UNION ALL SELECT 2, 'bob', 'x'"

	want := []user{{1, "ann", sql.NullString{}}, {2, "bob", sql.NullString{String: "x", Valid: true}}}

	for _, binary := range []bool{false, true} {
		got, err := gomysql.Structs[user](queryRows(t, c, binary, q), nil)
		if err != nil || !reflect.DeepEqual(got, want) {
			t.Errorf("binary %v: Structs %+v, %v", binary, got, err)
		}

		// Appended into the same memory.
		again, err := gomysql.AppendStructs(got[:0])(queryRows(t, c, binary, q), nil)
		if err != nil || !reflect.DeepEqual(again, want) || &again[0] != &got[0] {
			t.Errorf("binary %v: AppendStructs %+v, %v", binary, again, err)
		}

		ids, err := gomysql.Values[uint64](queryRows(t, c, binary, "SELECT 3 UNION ALL SELECT 4"), nil)
		if err != nil || !reflect.DeepEqual(ids, []uint64{3, 4}) {
			t.Errorf("binary %v: Values %v, %v", binary, ids, err)
		}
	}

	ids, err := gomysql.AppendValues([]int64{1})(c.Query("SELECT 2 UNION ALL SELECT 3"))
	if err != nil || !reflect.DeepEqual(ids, []int64{1, 2, 3}) {
		t.Errorf("AppendValues %v, %v", ids, err)
	}

	if got, err := gomysql.Structs[user](c.Query(q + " LIMIT 0")); err != nil || got != nil {
		t.Errorf("no rows: %v, %v", got, err)
	}

	if _, err := gomysql.Structs[user](c.Query("SELECT nope")); err == nil {
		t.Error("query error: none")
	}

	// A scan error stops at that row; the connection stays usable.
	if got, err := gomysql.Structs[user](c.Query(q + " UNION ALL SELECT 'x', 'c', NULL")); err == nil {
		t.Errorf("bad id: %+v", got)
	}

	if u, err := gomysql.Struct[user](c.QueryRow(q + " LIMIT 1 OFFSET 1")); err != nil || u != want[1] {
		t.Errorf("Struct %+v, %v", u, err)
	}

	if _, err := gomysql.Struct[user](c.QueryRow(q + " LIMIT 0")); !errors.Is(err, sql.ErrNoRows) {
		t.Errorf("Struct without a row: %v", err)
	}

	if n, err := gomysql.Value[int64](c.QueryRow("SELECT COUNT(*) FROM (" + q + ") t")); err != nil || n != 2 {
		t.Errorf("Value %d, %v", n, err)
	}

	// :user_name finds UserName, as the column does.
	var name string
	if err := c.QueryRow("SELECT :user_name", user{UserName: "ann"}).Scan(&name); err != nil || name != "ann" {
		t.Errorf(":user_name %q, %v", name, err)
	}
}
