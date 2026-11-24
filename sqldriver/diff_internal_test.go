package sqldriver

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"fmt"
	"os"
	"reflect"
	"testing"

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

// diffExprs and diffDests cover the conversions of ScanColumn: integers at
// the ends of each range, floats, text that is or is not a number, binary
// strings, BIT, dates and times, JSON, DECIMAL and NULL.
var (
	diffExprs = []string{
		"NULL", "0", "1", "-1", "255", "-129", "70000", "-70000", "4294967295", "4294967296",
		"-9223372036854775808", "18446744073709551615", "CAST(18446744073709551615 AS UNSIGNED)",
		"CAST(5 AS UNSIGNED)", "CAST(-5 AS SIGNED)", "1.5", "CAST(1.5 AS DOUBLE)", "CAST(2 AS FLOAT)",
		"CAST(1e300 AS DOUBLE)", "'abc'", "''", "'123'", "'-7'", "'true'", "_binary'xy'", "x'00ff'",
		"b'101'", "b'1'", "DATE '2024-01-02'", "TIMESTAMP '2024-01-02 03:04:05.123456'",
		"CAST('0000-00-00' AS DATE)", "TIME '12:34:56'", "JSON_OBJECT('a', 1)",
		"CAST(1 AS DECIMAL(5,2))", "YEAR(NOW())",
	}

	diffDests = []func() any{
		func() any { return new(int64) }, func() any { return new(int) }, func() any { return new(int32) },
		func() any { return new(int16) }, func() any { return new(int8) }, func() any { return new(uint64) },
		func() any { return new(uint) }, func() any { return new(uint32) }, func() any { return new(uint16) },
		func() any { return new(uint8) }, func() any { return new(string) }, func() any { return new([]byte) },
		func() any { return new(sql.RawBytes) }, func() any { return new(any) }, func() any { return new(float64) },
		func() any { return new(float32) }, func() any { return new(bool) }, func() any { return new(sql.NullInt64) },
		func() any { return new(sql.NullString) }, func() any { return new(sql.NullTime) },
		func() any { return new(sql.Null[uint32]) }, func() any { return new(*int64) }, func() any { return new(*string) },
	}
)

// ScanColumn gives the values and the errors of the Next path, for every
// expression into every destination, in both protocols.
func TestScanColumnDiff(t *testing.T) {
	t.Parallel()

	addr := os.Getenv("GOMYSQL_ADDR")
	if addr == "" {
		t.Skip("GOMYSQL_ADDR not set")
	}

	config := gomysql.Config{
		Network: "tcp", Address: addr, User: "b", Password: "b", Database: "gomysql",
		AllowPublicKeyRetrieval: true,
	}

	scanning := sql.OpenDB(NewConnector(config))
	nextOnly := sql.OpenDB(nextOnlyConnector{NewConnector(config)})

	defer func() { _, _ = scanning.Close(), nextOnly.Close() }()

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
	var (
		rows *sql.Rows
		err  error
	)

	if prepared {
		st, perr := db.Prepare(q)
		if perr != nil {
			return "", perr
		}
		defer func() { _ = st.Close() }()

		rows, err = st.Query()
	} else {
		rows, err = db.Query(q)
	}

	if err != nil {
		return "", err
	}
	defer func() { _ = rows.Close() }()

	if !rows.Next() {
		return "", fmt.Errorf("no row: %w", rows.Err())
	}

	err = rows.Scan(dest)

	e := reflect.ValueOf(dest).Elem()
	if e.Kind() == reflect.Pointer && !e.IsNil() {
		return fmt.Sprintf("ptr %#v", e.Elem().Interface()), err
	}

	return fmt.Sprintf("%#v", e.Interface()), err
}
