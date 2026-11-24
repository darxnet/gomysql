//go:build go1.27

// The tests compare the ScanColumn path of Go 1.27 with the Next path;
// before Go 1.27 both are the Next path.

package sqldriver

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"encoding/json"
	"fmt"
	"os"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/darxnet/gomysql"
)

// The wrappers hide RowsColumnScanner (Go 1.27) from database/sql, so that
// it scans through Next and convertAssign, as before Go 1.27.

type nextOnlyConnector struct{ c driver.Connector }

func (n nextOnlyConnector) Connect(ctx context.Context) (driver.Conn, error) {
	c, err := n.c.Connect(ctx)
	if err != nil {
		return nil, err
	}

	return nextOnlyConn{c.(*conn)}, nil //nolint:forcetypeassert // our connector
}

func (n nextOnlyConnector) Driver() driver.Driver { return n.c.Driver() }

type nextOnlyConn struct{ *conn }

func (c nextOnlyConn) QueryContext(ctx context.Context, q string, args []driver.NamedValue) (driver.Rows, error) {
	r, err := c.conn.QueryContext(ctx, q, args)
	if err != nil {
		return nil, err
	}

	return struct{ driver.Rows }{r}, nil
}

func (c nextOnlyConn) PrepareContext(ctx context.Context, q string) (driver.Stmt, error) {
	s, err := c.conn.PrepareContext(ctx, q)
	if err != nil {
		return nil, err
	}

	return nextOnlyStmt{s.(*stmt)}, nil //nolint:forcetypeassert // our statement
}

type nextOnlyStmt struct{ *stmt }

func (s nextOnlyStmt) QueryContext(ctx context.Context, args []driver.NamedValue) (driver.Rows, error) {
	r, err := s.stmt.QueryContext(ctx, args)
	if err != nil {
		return nil, err
	}

	return struct{ driver.Rows }{r}, nil
}

// diffDBs opens the same database twice: scanning with ScanColumn (on Go
// 1.27) and through Next only.
func diffDBs(t *testing.T, loc *time.Location) (scanning, nextOnly *sql.DB) {
	t.Helper()

	addr := os.Getenv("GOMYSQL_ADDR")
	if addr == "" {
		t.Skip("GOMYSQL_ADDR not set")
	}

	config := gomysql.Config{
		Network: "tcp", Address: addr, User: "b", Password: "b", Database: "gomysql",
		AllowPublicKeyRetrieval: true, Location: loc,
	}

	scanning, nextOnly = sql.OpenDB(NewConnector(config)), sql.OpenDB(nextOnlyConnector{NewConnector(config)})
	t.Cleanup(func() { _, _ = scanning.Close(), nextOnly.Close() })

	return scanning, nextOnly
}

type (
	namedInt   int64
	namedUint8 uint8
	namedStr   string
	namedBytes []byte
	namedFloat float64
)

// recorder is an sql.Scanner that records what it gets.
type recorder struct{ got string }

func (r *recorder) Scan(v any) error {
	if b, ok := v.([]byte); ok {
		v = "bytes " + string(b)
	}

	r.got = fmt.Sprintf("%T %v", v, v)

	return nil
}

// diffDests are the destinations of the diff tests: the fast paths of
// ScanColumn and those it leaves to database/sql.
//
//nolint:gochecknoglobals // test tables
var diffDests = []func() any{
	func() any { return new(int64) }, func() any { return new(int) }, func() any { return new(int32) },
	func() any { return new(int16) }, func() any { return new(int8) }, func() any { return new(uint64) },
	func() any { return new(uint) }, func() any { return new(uint32) }, func() any { return new(uint16) },
	func() any { return new(uint8) }, func() any { return new(string) }, func() any { return new([]byte) },
	func() any { return new(sql.RawBytes) }, func() any { return new(any) }, func() any { return new(float64) },
	func() any { return new(float32) }, func() any { return new(bool) }, func() any { return new(time.Time) },
	func() any { return new(time.Duration) }, func() any { return new(json.RawMessage) },
	func() any { return new(sql.NullInt64) }, func() any { return new(sql.NullInt32) },
	func() any { return new(sql.NullString) }, func() any { return new(sql.NullTime) },
	func() any { return new(sql.NullFloat64) }, func() any { return new(sql.NullBool) },
	func() any { return &sql.NullInt64{Int64: 7, Valid: true} }, func() any { return new(sql.Null[uint32]) },
	func() any { return new(sql.Null[time.Time]) }, func() any { return new(*int64) },
	func() any { return new(*string) }, func() any { return new(*time.Time) }, func() any { return new(**int) },
	func() any { return new(namedInt) }, func() any { return new(namedUint8) }, func() any { return new(namedStr) },
	func() any { return new(namedBytes) }, func() any { return new(namedFloat) }, func() any { return new(recorder) },
}

// diffExprs are expressions for TestScanColumnMatchesNext: integers at the ends
// of each range, floats, text that is or is not a number, binary strings,
// dates around daylight saving time, times, JSON and DECIMAL.
//
//nolint:gochecknoglobals // test tables
var diffExprs = []string{
	"NULL", "0", "1", "-1", "255", "-129", "70000", "-70000", "4294967295", "4294967296",
	"-9223372036854775808", "18446744073709551615", "CAST(9223372036854775808 AS UNSIGNED)",
	"CAST(5 AS UNSIGNED)", "CAST(-5 AS SIGNED)", "1.5", "CAST(1.5 AS DOUBLE)", "CAST(2 AS FLOAT)",
	"CAST(0.1 AS FLOAT)", "CAST(1e300 AS DOUBLE)", "CAST(1e-7 AS DOUBLE)", "CAST(-1 AS DECIMAL)",
	"CAST(1 AS DECIMAL(5,2))", "'abc'", "''", "'123'", "'-7'", "'1.5'", "'  12'", "'+12'", "'0x10'",
	"'true'", "'2024-01-02'", "_binary'xy'", "x'00ff'", "DATE '2024-01-02'",
	"TIMESTAMP '2024-01-02 03:04:05.123456'", "CAST('2024-11-03 01:30:00' AS DATETIME)",
	"CAST('2024-03-10 02:30:00.5' AS DATETIME(1))", "TIME '12:34:56'", "TIME '-838:59:59'",
	"CAST('00:00:00.5' AS TIME(2))", "JSON_OBJECT('a', 1)", "YEAR(NOW())",
}

// ScanColumn gives the values and the errors of the Next path, for every
// expression into every destination, in both protocols, in a Location
// with daylight saving time.
func TestScanColumnMatchesNext(t *testing.T) {
	t.Parallel()

	ny, err := time.LoadLocation("America/New_York")
	if err != nil {
		t.Skip(err)
	}

	scanning, nextOnly := diffDBs(t, ny)

	n := 0

	for _, expr := range diffExprs {
		for _, dest := range diffDests {
			for _, prepared := range []bool{false, true} {
				q := "SELECT " + expr
				got, gotErr := scanOne(scanning, prepared, q, dest())
				want, wantErr := scanOne(nextOnly, prepared, q, dest())

				if got != want || fmt.Sprint(gotErr) != fmt.Sprint(wantErr) {
					t.Errorf("%s into %T, prepared %v: ScanColumn %s, %v; Next %s, %v",
						expr, dest(), prepared, got, gotErr, want, wantErr)
				}

				n++
			}
		}
	}

	t.Logf("%d cases", n)
}

// scanOne scans the first column of q into dest and formats what it got.
func scanOne(db *sql.DB, prepared bool, q string, dest any) (string, error) {
	rows, closeRows, err := query(db, prepared, q) //nolint:sqlclosecheck // closeRows
	if err != nil {
		return "", err
	}
	defer closeRows()

	if !rows.Next() {
		return "", fmt.Errorf("no row: %w", rows.Err())
	}

	err = rows.Scan(dest)

	return show(dest), err
}

// query runs q as text or as a prepared statement; closeRows closes both.
func query(db *sql.DB, prepared bool, q string) (rows *sql.Rows, closeRows func(), err error) {
	if !prepared {
		rows, err = db.Query(q) //nolint:rowserrcheck // the callers check
		if err != nil {
			return nil, nil, err
		}

		return rows, func() { _ = rows.Close() }, nil
	}

	st, err := db.Prepare(q)
	if err != nil {
		return nil, nil, err
	}

	rows, err = st.Query() //nolint:rowserrcheck // the callers check
	if err != nil {
		_ = st.Close()

		return nil, nil, err
	}

	return rows, func() { _, _ = rows.Close(), st.Close() }, nil
}

// show formats what dest points to.
func show(dest any) string {
	e := reflect.ValueOf(dest)
	if e.Kind() != reflect.Pointer || e.IsNil() {
		return fmt.Sprintf("%#v", dest)
	}

	var prefix strings.Builder

	for e = e.Elem(); e.Kind() == reflect.Pointer && !e.IsNil(); e = e.Elem() {
		prefix.WriteByte('*')
	}

	if e.Kind() == reflect.Pointer {
		return prefix.String() + "nil"
	}

	if tm, ok := reflect.TypeAssert[time.Time](e); ok {
		return prefix.String() + tm.Format(time.RFC3339Nano) + " " + tm.Location().String()
	}

	return prefix.String() + fmt.Sprintf("%#v", e.Interface())
}

// diffTable has columns of every type, filled to their limits, a zero row
// and a NULL row.
const diffTable = `(
	u8 TINYINT UNSIGNED, i8 TINYINT, u16 SMALLINT UNSIGNED, u24 MEDIUMINT UNSIGNED, u32 INT UNSIGNED,
	i32 INT, u64 BIGINT UNSIGNED, i64 BIGINT, zf INT(8) UNSIGNED ZEROFILL, f FLOAT, d DOUBLE,
	dc DECIMAL(20,5), b1 BIT(1), b64 BIT(64), b9 BIT(9), dt DATETIME(6), ts TIMESTAMP(3) NULL, da DATE,
	tm TIME(3), tm2 TIME, yr YEAR, vc VARCHAR(50), vb VARBINARY(50), bl BLOB, tx TEXT, js JSON,
	en ENUM('a','b'), st SET('x','y'), ch CHAR(3), bi BINARY(4), nn INT)`

//nolint:gochecknoglobals // test tables
var diffRows = []string{
	`(255,-128,65535,16777215,4294967295,-2147483648,18446744073709551615,-9223372036854775808,42,1.1,1.1,
	123.45,b'1',b'1111111111111111111111111111111111111111111111111111111111111111',b'100000001',
	'2024-01-02 03:04:05.123456','2024-06-02 03:04:05.123','2024-01-02','-12:34:56.789','838:59:59',2024,
	'héllo',x'00ff10','blob','text','{"a":[1,2]}','b','x,y','ab ',x'01020304',NULL)`,
	`(0,0,0,0,0,0,0,0,0,0,0,0,b'0',b'0',b'0','1000-01-01 00:00:00',NULL,'1000-01-01','00:00:00','00:00:00',
	1901,'','','','','null','a','','','',NULL)`,
	`(1,127,256,70000,300,2147483647,9223372036854775808,9223372036854775807,99999999,-3.4e38,1e308,
	-99999.99999,b'1',b'1000000000000000000000000000000000000000000000000000000000000000',b'11',
	'9999-12-31 23:59:59.999999','2038-01-19 03:14:07.999','9999-12-31','-838:59:59.000','-00:00:01',2155,
	'123','456',' 7 ','1e3','123','a','x','-1','12  ',7)`,
	`(NULL,NULL,NULL,NULL,NULL,NULL,NULL,NULL,NULL,NULL,NULL,NULL,NULL,NULL,NULL,NULL,NULL,NULL,NULL,NULL,
	NULL,NULL,NULL,NULL,NULL,NULL,NULL,NULL,NULL,NULL,NULL)`,
}

// ScanColumn gives the values and the errors of the Next path on rows of
// many columns: each column into each destination, the others into *any,
// then the whole row into one destination type; in UTC and in +03:30.
// Rows of many columns found a FLOAT flag leaking into the next DOUBLE.
func TestScanColumnManyColumns(t *testing.T) {
	t.Parallel()

	for _, loc := range []*time.Location{time.UTC, time.FixedZone("+03:30", 3*3600+1800)} {
		scanning, nextOnly := diffDBs(t, loc)

		table := fmt.Sprintf("scan_diff_%d", time.Now().UnixNano())
		if _, err := scanning.Exec("CREATE TABLE " + table + " " + diffTable); err != nil {
			t.Fatal(err)
		}

		t.Cleanup(func() { _, _ = scanning.Exec("DROP TABLE " + table) })

		for _, row := range diffRows {
			if _, err := scanning.Exec("INSERT INTO " + table + " VALUES " + row); err != nil { //nolint:gosec // test data
				t.Fatal(err)
			}
		}

		n, differ := 0, 0

		for _, prepared := range []bool{false, true} {
			for _, dest := range diffDests {
				got := scanTable(t, scanning, prepared, table, dest)
				want := scanTable(t, nextOnly, prepared, table, dest)

				if len(got) != len(want) {
					t.Fatalf("into %T, prepared %v: %d results, want %d", dest(), prepared, len(got), len(want))
				}

				for i := range got {
					n++

					if got[i] != want[i] {
						differ++
						t.Errorf("%v, into %T, prepared %v:\n  ScanColumn %s\n  Next       %s",
							loc, dest(), prepared, got[i], want[i])
					}
				}
			}
		}

		t.Logf("%v: %d comparisons, %d differ", loc, n, differ)
	}
}

// scanTable scans every row of table: each column into dest with the
// others into *any, recording the next column too (a rescan after an
// error), then all the columns into dest.
//
//nolint:gocognit // one loop per way of scanning
func scanTable(t *testing.T, db *sql.DB, prepared bool, table string, dest func() any) []string {
	t.Helper()

	//nolint:sqlclosecheck,unqueryvet // closed by closeRows; every column is the point
	rows, closeRows, err := query(db, prepared, "SELECT * FROM "+table)
	if err != nil {
		t.Fatal(err)
	}
	defer closeRows()

	cols, err := rows.Columns()
	if err != nil {
		t.Fatal(err)
	}

	_, raw := dest().(*sql.RawBytes)

	var out []string

	for rows.Next() {
		// After sql.RawBytes, database/sql allows no other Scan of the row.
		for c := range cols {
			if raw {
				break
			}

			dests := make([]any, len(cols))
			for k := range dests {
				dests[k] = new(any)
			}

			dests[c] = dest()
			err := rows.Scan(dests...)
			out = append(out, fmt.Sprintf("%s: %s, %v", cols[c], show(dests[c]), err))

			if c+1 < len(cols) {
				out = append(out, "  next "+show(dests[c+1]))
			}
		}

		dests := make([]any, len(cols))
		for k := range dests {
			dests[k] = dest()
		}

		err := rows.Scan(dests...)

		all := make([]string, len(dests))
		for k := range dests {
			all[k] = show(dests[k])
		}

		out = append(out, fmt.Sprintf("all: %v: %s", err, strings.Join(all, " | ")))

		if raw {
			break
		}
	}

	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}

	return out
}

// A nil destination pointer gives the error of database/sql on both
// paths, not a write through nil.
func TestNilDestinations(t *testing.T) {
	t.Parallel()

	scanning, nextOnly := diffDBs(t, nil)

	cases := []struct {
		q    string
		dest any
	}{
		{"SELECT 5", (*int64)(nil)},
		{"SELECT 5", (*uint8)(nil)},
		{"SELECT 'abc'", (*string)(nil)},
		{"SELECT 'abc'", (*[]byte)(nil)},
		{"SELECT 'abc'", (*sql.RawBytes)(nil)},
		{"SELECT CAST(1.5 AS DOUBLE)", (*float64)(nil)},
		{"SELECT CAST(1.5 AS DECIMAL(3,1))", (*float64)(nil)},
	}

	for _, c := range cases {
		for _, prepared := range []bool{false, true} {
			_, gotErr := scanOne(scanning, prepared, c.q, c.dest)
			_, wantErr := scanOne(nextOnly, prepared, c.q, c.dest)

			if gotErr == nil || fmt.Sprint(gotErr) != fmt.Sprint(wantErr) {
				t.Errorf("%s into %T, prepared %v: %v, want %v", c.q, c.dest, prepared, gotErr, wantErr)
			}
		}
	}
}
