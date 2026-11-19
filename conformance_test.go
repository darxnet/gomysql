package gomysql_test

// Scenarios ported from github.com/go-sql-driver/mysql driver_test.go onto
// the native gomysql API. Their suite runs through database/sql and the
// driver's internals, so it cannot run against gomysql as is.
//
// GOMYSQL_ADDR=127.0.0.1:3306 go test -run Conformance -v
//
// scripts/testdb.sh starts a server and creates the users and database these
// tests expect. Known bugs are skipped (see knownBugs); set
// GOMYSQL_KNOWN_BUGS=1 to run them anyway.

import (
	"database/sql"
	"errors"
	"fmt"
	"os"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/darxnet/gomysql"
)

var tblSeq atomic.Uint32

// knownBugs lists subtests that fail on known bugs, with the ROADMAP phase
// that fixes them. A fix removes its entry, so the test then guards it.
var knownBugs = map[string]string{
	"TestConformanceCRUD/crud":                          "phase 6: bool dest",
	"TestConformanceInt/TINYINT":                        "phase 6: sign of negative ints",
	"TestConformanceInt/SMALLINT":                       "phase 6: sign of negative ints",
	"TestConformanceInt/MEDIUMINT":                      "phase 6: sign of negative ints",
	"TestConformanceInt/INT":                            "phase 6: sign of negative ints",
	"TestConformanceInt/BIGINT":                         "phase 6: sign of negative ints",
	"TestConformanceFloat/float":                        "phase 6: float dest",
	"TestConformanceBytesReuse/reuse":                   "phase 6: []byte dest reuse",
	"TestConformanceNULL/null":                          "phase 6: NULL",
	"TestConformanceLongData/long":                      "phase 4: responses over 16 MB",
	"TestConformanceRawBytesBig/rawbytes":               "phase 6: RawBytes in text protocol",
	"TestConformanceDateTime/datetime":                  "phase 6: time dest",
	"TestConformanceStmtArgs/args":                      "phase 5: EXECUTE column definitions",
	"TestConformanceStmtExec/stmtexec":                  "phase 5: Stmt.Exec",
	"TestConformanceInterpolation/escaped":              "phase 9: interpolation",
	"TestConformanceInterpolation/question-mark-in-arg": "phase 9: interpolation",
	"TestConformanceInterpolation/float-precision":      "phase 9: interpolation",
	"TestConformanceInterpolation/time-arg":             "phase 9: interpolation",
	"TestConformanceSQLInjection/1_OR_1=1":              "phase 9: interpolation",
	"TestConformanceSQLInjection/'_OR_'1'='1":           "phase 9: interpolation",
	"TestConformanceTx/tx":                              "phase 9: Tx.Exec args",
	"TestConformanceErrorRecovery/rows-close-early":     "phase 5: early Rows.Close",
	"TestConformanceQueryNoRows/do":                     "phase 5: Query without result set",
	"TestConformanceCompress/100000":                    "phase 4: compressed packets read with one Read",
	"TestConformanceAuth/c2":                            "phase 7: caching_sha2_password",
}

func knownBug(t *testing.T) {
	t.Helper()

	if reason, ok := knownBugs[t.Name()]; ok && os.Getenv("GOMYSQL_KNOWN_BUGS") == "" {
		t.Skip("known bug, " + reason)
	}
}

type confRows interface {
	Next() bool
	Scan(dest ...any) error
	Err() error
	Close() error
}

type confMode struct {
	name  string
	query func(c *gomysql.Conn, q string, args ...any) (confRows, error)
}

var confModes = []confMode{
	{"text", func(c *gomysql.Conn, q string, args ...any) (confRows, error) {
		r, err := c.Query(q, args...)
		if err != nil || r == nil {
			return nil, err
		}
		return r, nil
	}},
	{"binary", func(c *gomysql.Conn, q string, args ...any) (confRows, error) {
		st, err := c.Prepare(q)
		if err != nil {
			return nil, err
		}
		r, err := st.Query(args...)
		if err != nil || r == nil {
			return nil, err
		}
		return r, nil
	}},
}

func confConn(t *testing.T, user, pass string) *gomysql.Conn {
	t.Helper()

	addr := os.Getenv("GOMYSQL_ADDR")
	if addr == "" {
		t.Skip("GOMYSQL_ADDR not set")
	}

	c, err := gomysql.Open(gomysql.Config{
		Protocol: "tcp", Address: addr, User: user, Password: pass,
		DbName: "gomysql", Charset: gomysql.CharSet_utf8mb4_bin,
	})
	if err != nil {
		t.Fatalf("open: %v", err)
	}

	t.Cleanup(func() { _ = c.Close() })

	return c
}

// confSetup prepares a subtest: it skips known bugs, opens a fresh
// connection and returns a table name that is dropped afterwards.
func confSetup(t *testing.T) (c *gomysql.Conn, tbl string) {
	t.Helper()

	knownBug(t)

	c = confConn(t, "b", "b")
	tbl = fmt.Sprintf("conf_%d", tblSeq.Add(1))
	_, _ = c.Exec("DROP TABLE IF EXISTS " + tbl)

	t.Cleanup(func() {
		if cc, err := gomysql.Open(gomysql.Config{
			Protocol: "tcp", Address: os.Getenv("GOMYSQL_ADDR"), User: "b", Password: "b", DbName: "gomysql",
		}); err == nil {
			_, _ = cc.Exec("DROP TABLE IF EXISTS " + tbl)
			_ = cc.Close()
		}
	})

	return c, tbl
}

// confRecover turns a panic into a failure: defer confRecover(t).
func confRecover(t *testing.T) {
	t.Helper()

	if r := recover(); r != nil {
		t.Errorf("PANIC: %v", r)
	}
}

func mustExec(t *testing.T, c *gomysql.Conn, q string) *gomysql.Result {
	t.Helper()

	r, err := c.Exec(q)
	if err != nil {
		t.Fatalf("exec %q: %v", trunc(q), err)
	}

	return r
}

func trunc(s string) string {
	if len(s) > 80 {
		return s[:80] + "..."
	}

	return s
}

// scanOne runs q in mode m and scans the single row into dest.
func scanOne(t *testing.T, m confMode, c *gomysql.Conn, q string, dest ...any) error {
	t.Helper()

	rows, err := m.query(c, q)
	if err != nil {
		return fmt.Errorf("query: %w", err)
	}

	if rows == nil {
		return errors.New("query returned nil rows and nil error")
	}

	defer func() { _ = rows.Close() }()

	if !rows.Next() {
		return fmt.Errorf("no rows (err %w)", rows.Err())
	}

	if err := rows.Scan(dest...); err != nil {
		return fmt.Errorf("scan: %w", err)
	}

	if rows.Next() {
		return errors.New("unexpected second row")
	}

	return rows.Err()
}

// TestCRUD.
func TestConformanceCRUD(t *testing.T) {
	t.Parallel()

	t.Run("crud", func(t *testing.T) {
		t.Parallel()

		c, tbl := confSetup(t)
		defer confRecover(t)

		mustExec(t, c, "CREATE TABLE "+tbl+" (id INT AUTO_INCREMENT PRIMARY KEY, value BOOL)")

		res := mustExec(t, c, "INSERT INTO "+tbl+" (value) VALUES (1)")
		if n, _ := res.RowsAffected(); n != 1 {
			t.Errorf("insert RowsAffected = %d, want 1", n)
		}
		if id, _ := res.LastInsertId(); id != 1 {
			t.Errorf("LastInsertId = %d, want 1", id)
		}

		res = mustExec(t, c, "UPDATE "+tbl+" SET value = 0 WHERE value = 1")
		if n, _ := res.RowsAffected(); n != 1 {
			t.Errorf("update RowsAffected = %d, want 1", n)
		}

		var v bool
		for _, m := range confModes {
			if err := scanOne(t, m, c, "SELECT value FROM "+tbl, &v); err != nil {
				t.Errorf("%s: scan bool: %v", m.name, err)
			}
		}

		res = mustExec(t, c, "DELETE FROM "+tbl)
		if n, _ := res.RowsAffected(); n != 1 {
			t.Errorf("delete RowsAffected = %d, want 1", n)
		}
	})
}

// TestInt: signed and negative values in every integer column type.
func TestConformanceInt(t *testing.T) {
	t.Parallel()

	types := []string{"TINYINT", "SMALLINT", "MEDIUMINT", "INT", "BIGINT"}

	for _, typ := range types {
		t.Run(typ, func(t *testing.T) {
			t.Parallel()

			c, tbl := confSetup(t)
			defer confRecover(t)

			mustExec(t, c, "CREATE TABLE "+tbl+" (value "+typ+")")

			for _, in := range []int64{42, -42} {
				mustExec(t, c, "DELETE FROM "+tbl)
				mustExec(t, c, fmt.Sprintf("INSERT INTO %s VALUES (%d)", tbl, in))

				for _, m := range confModes {
					var o64 int64
					if err := scanOne(t, m, c, "SELECT value FROM "+tbl, &o64); err != nil {
						t.Errorf("%s %d int64: %v", m.name, in, err)
					} else if o64 != in {
						t.Errorf("%s int64: got %d, want %d", m.name, o64, in)
					}

					var oi int
					if err := scanOne(t, m, c, "SELECT value FROM "+tbl, &oi); err != nil {
						t.Errorf("%s %d int: %v", m.name, in, err)
					} else if int64(oi) != in {
						t.Errorf("%s int: got %d, want %d", m.name, oi, in)
					}
				}
			}
		})
	}
}

// TestUint64.
func TestConformanceUint64(t *testing.T) {
	t.Parallel()

	t.Run("max", func(t *testing.T) {
		t.Parallel()

		c, _ := confSetup(t)
		defer confRecover(t)

		for _, m := range confModes {
			var u uint64
			if err := scanOne(t, m, c, "SELECT 18446744073709551615", &u); err != nil {
				t.Errorf("%s: %v", m.name, err)
			} else if u != 1<<64-1 {
				t.Errorf("%s: got %d", m.name, u)
			}
		}
	})
}

// TestFloat32 and TestFloat64.
func TestConformanceFloat(t *testing.T) {
	t.Parallel()

	t.Run("float", func(t *testing.T) {
		t.Parallel()

		c, tbl := confSetup(t)
		defer confRecover(t)

		mustExec(t, c, "CREATE TABLE "+tbl+" (f FLOAT, d DOUBLE, n DECIMAL(10,4))")
		mustExec(t, c, "INSERT INTO "+tbl+" VALUES (42.25, 42.123456789, 12.3456)")

		for _, m := range confModes {
			var f32 float32
			var f64 float64
			var dec string
			if err := scanOne(t, m, c, "SELECT f, d, n FROM "+tbl, &f32, &f64, &dec); err != nil {
				t.Errorf("%s: %v", m.name, err)
				continue
			}
			if f32 != 42.25 || f64 != 42.123456789 || dec != "12.3456" {
				t.Errorf("%s: got %v %v %q", m.name, f32, f64, dec)
			}
		}
	})
}

// TestString: unicode in every string column type.
func TestConformanceString(t *testing.T) {
	t.Parallel()

	types := []string{"CHAR(255)", "VARCHAR(255)", "TINYTEXT", "TEXT", "MEDIUMTEXT", "LONGTEXT", "VARBINARY(255)", "BLOB"}
	in := "κόσμε üöäßñóùéàâÿœ'îë Árvíztűrő いろはにほへとち 你好 🍕" //nolint:gosmopolitan // test data

	for _, typ := range types {
		t.Run(typ, func(t *testing.T) {
			t.Parallel()

			c, tbl := confSetup(t)
			defer confRecover(t)

			mustExec(t, c, "CREATE TABLE "+tbl+" (value "+typ+") CHARACTER SET utf8mb4")
			mustExec(t, c, "INSERT INTO "+tbl+" VALUES ('"+strings.ReplaceAll(in, "'", "''")+"')")

			for _, m := range confModes {
				var s string
				if err := scanOne(t, m, c, "SELECT value FROM "+tbl, &s); err != nil {
					t.Errorf("%s: %v", m.name, err)
				} else if s != in {
					t.Errorf("%s: got %q", m.name, s)
				}
			}
		})
	}
}

// []byte dest reused for a shorter value must not keep the old tail.
func TestConformanceBytesReuse(t *testing.T) {
	t.Parallel()

	t.Run("reuse", func(t *testing.T) {
		t.Parallel()

		c, _ := confSetup(t)
		defer confRecover(t)

		for _, m := range confModes {
			rows, err := m.query(c, "SELECT 'abcdef' UNION ALL SELECT 'xy'")
			if err != nil {
				t.Fatal(err)
			}

			var b []byte
			var got []string
			for rows.Next() {
				if err := rows.Scan(&b); err != nil {
					t.Fatal(err)
				}
				got = append(got, string(b))
				b = b[:0]
			}
			_ = rows.Close()

			if strings.Join(got, ",") != "abcdef,xy" {
				t.Errorf("%s (b reset to b[:0]): got %q", m.name, got)
			}

			rows, _ = m.query(c, "SELECT 'abcdef' UNION ALL SELECT 'xy'")
			b = nil
			got = got[:0]
			for rows.Next() {
				_ = rows.Scan(&b)
				got = append(got, string(b))
			}
			_ = rows.Close()

			if strings.Join(got, ",") != "abcdef,xy" {
				t.Errorf("%s (b reused as is, database/sql semantics): got %q", m.name, got)
			}
		}
	})
}

// TestNULL: a NULL must reset the destination and not shift later columns.
func TestConformanceNULL(t *testing.T) {
	t.Parallel()

	t.Run("null", func(t *testing.T) {
		t.Parallel()

		c, _ := confSetup(t)
		defer confRecover(t)

		for _, m := range confModes {
			a, b, s := 7, 7, "stale"
			if err := scanOne(t, m, c, "SELECT 1, NULL, 3", &a, &b, &a); err != nil {
				t.Errorf("%s: %v", m.name, err)
			} else if a != 3 {
				t.Errorf("%s: column after NULL: got %d, want 3", m.name, a)
			}
			if b != 7 {
				t.Logf("%s: NULL changed int dest to %d", m.name, b)
			} else {
				t.Logf("%s: NULL leaves dest unchanged (b=%d), unlike database/sql which errors or zeroes", m.name, b)
			}

			if err := scanOne(t, m, c, "SELECT NULL, 'x'", &s, &s); err != nil {
				t.Errorf("%s: %v", m.name, err)
			} else if s != "x" {
				t.Errorf("%s: string after NULL: got %q, want x", m.name, s)
			}

			var ns sql.NullString
			if err := scanOne(t, m, c, "SELECT NULL", &ns); err != nil {
				t.Errorf("%s: sql.NullString unsupported: %v", m.name, err)
			}
		}
	})
}

// TestLongData: values bigger than one 16 MB packet, both directions.
func TestConformanceLongData(t *testing.T) {
	t.Parallel()

	t.Run("long", func(t *testing.T) {
		t.Parallel()

		c, tbl := confSetup(t)
		defer confRecover(t)

		var maxPacket int
		if err := c.QueryRow("SELECT @@max_allowed_packet").Scan(&maxPacket); err != nil {
			t.Fatal(err)
		}

		size := min(maxPacket-1024, 1<<25) // 32 MB: two full packets
		mustExec(t, c, "CREATE TABLE "+tbl+" (value LONGBLOB)")

		in := strings.Repeat("a", size)
		if _, err := c.Exec("INSERT INTO " + tbl + " VALUES ('" + in + "')"); err != nil {
			t.Fatalf("write %d bytes: %v", size, err)
		}

		for _, m := range confModes {
			var out string
			if err := scanOne(t, m, c, "SELECT value FROM "+tbl, &out); err != nil {
				t.Errorf("%s: %v", m.name, err)
			} else if out != in {
				t.Errorf("%s: length in %d, out %d", m.name, len(in), len(out))
			}
		}
	})
}

// TestRawBytesResultExceedsBuffer.
func TestConformanceRawBytesBig(t *testing.T) {
	t.Parallel()

	t.Run("rawbytes", func(t *testing.T) {
		t.Parallel()

		c, _ := confSetup(t)
		defer confRecover(t)

		exp := strings.Repeat("abc", 4096)
		for _, m := range confModes {
			rows, err := m.query(c, "SELECT '"+exp+"'")
			if err != nil {
				t.Fatal(err)
			}

			// RawBytes is valid only until the next Next, as in database/sql.
			var rb sql.RawBytes
			if !rows.Next() {
				t.Fatalf("%s: no rows", m.name)
			}
			if err := rows.Scan(&rb); err != nil {
				t.Errorf("%s: %v", m.name, err)
			} else if string(rb) != exp {
				t.Errorf("%s: mismatch, len %d", m.name, len(rb))
			}
			for rows.Next() {
			}
			_ = rows.Close()
		}
	})
}

// TestDateTime: DATE, DATETIME(6), TIMESTAMP into time.Time.
func TestConformanceDateTime(t *testing.T) {
	t.Parallel()

	t.Run("datetime", func(t *testing.T) {
		t.Parallel()

		c, tbl := confSetup(t)
		defer confRecover(t)

		mustExec(t, c, "CREATE TABLE "+tbl+" (d DATE, dt DATETIME, dt6 DATETIME(6), ts TIMESTAMP NULL)")
		mustExec(t, c, "INSERT INTO "+tbl+" VALUES ('2026-11-18', '2026-11-18 13:31:33', "+
			"'2026-11-18 13:31:33.123456', '2026-11-18 13:31:33')")

		for _, col := range []string{"d", "dt", "dt6", "ts"} {
			for _, m := range confModes {
				var tm time.Time
				if err := scanOne(t, m, c, "SELECT "+col+" FROM "+tbl, &tm); err != nil {
					t.Errorf("%s %s: %v", m.name, col, err)
				} else if tm.Year() != 2026 {
					t.Errorf("%s %s: got %v", m.name, col, tm)
				}
			}
		}
	})
}

// Prepared statements with arguments (go-sql-driver: TestCRUD/TestInt with ?).
func TestConformanceStmtArgs(t *testing.T) {
	t.Parallel()

	t.Run("args", func(t *testing.T) {
		t.Parallel()

		c, tbl := confSetup(t)
		defer confRecover(t)

		mustExec(t, c, "CREATE TABLE "+tbl+" (i BIGINT, u BIGINT UNSIGNED, s VARCHAR(64), n INT NULL, f DOUBLE)")
		st, err := c.Prepare("SELECT ?, ?, ?, ?, ?")
		if err != nil {
			t.Fatal(err)
		}

		rows, err := st.Query(int64(-5), uint64(1<<63), "it's", nil, 1.5)
		if err != nil {
			t.Fatal(err)
		}

		var (
			i int64
			u uint64
			s string
			n = 9
			f float64
		)
		if !rows.Next() {
			t.Fatal("no row", rows.Err())
		}
		if err := rows.Scan(&i, &u, &s, &n, &f); err != nil {
			t.Errorf("scan: %v", err)
		}
		_ = rows.Close()

		if i != -5 || u != 1<<63 || s != "it's" || f != 1.5 {
			t.Errorf("got %d %d %q %d %v", i, u, s, n, f)
		}
	})
}

// A prepared INSERT goes through Stmt.Query, the only execute call.
func TestConformanceStmtExec(t *testing.T) {
	t.Parallel()

	t.Run("stmtexec", func(t *testing.T) {
		t.Parallel()

		c, tbl := confSetup(t)
		defer confRecover(t)

		mustExec(t, c, "CREATE TABLE "+tbl+" (v INT)")
		st, err := c.Prepare("INSERT INTO " + tbl + " VALUES (?)")
		if err != nil {
			t.Fatal(err)
		}

		rows, err := st.Query(1)
		if err != nil {
			t.Fatal(err)
		}
		if rows == nil {
			t.Error("Stmt.Query of INSERT returns nil rows and nil error: rows.Next() would panic, and no RowsAffected")
		}
	})
}

// Text-protocol args are interpolated client side: TestInsertRetrieveEscapedData.
func TestConformanceInterpolation(t *testing.T) {
	t.Parallel()

	t.Run("escaped", func(t *testing.T) {
		t.Parallel()

		c, tbl := confSetup(t)
		defer confRecover(t)

		mustExec(t, c, "CREATE TABLE "+tbl+" (v VARCHAR(255))")

		v := "foo \x00\n\r\x1a\"'\\"
		if _, err := c.Exec("INSERT INTO "+tbl+" VALUES (?)", v); err != nil {
			t.Fatalf("Exec with string arg: %v", err)
		}

		var out string
		if err := c.QueryRow("SELECT v FROM " + tbl).Scan(&out); err != nil {
			t.Fatal(err)
		}
		if out != v {
			t.Errorf("%q != %q", out, v)
		}
	})

	t.Run("question-mark-in-arg", func(t *testing.T) {
		t.Parallel()

		c, tbl := confSetup(t)
		defer confRecover(t)

		mustExec(t, c, "CREATE TABLE "+tbl+" (a VARCHAR(16), b INT)")
		if _, err := c.Exec("INSERT INTO "+tbl+" VALUES (?, ?)", "a?b", 5); err != nil {
			t.Fatalf("Exec: %v", err)
		}

		var a string
		var b int
		if err := c.QueryRow("SELECT a, b FROM "+tbl).Scan(&a, &b); err != nil {
			t.Fatal(err)
		}
		if a != "a?b" || b != 5 {
			t.Errorf("got %q %d", a, b)
		}
	})

	t.Run("float-precision", func(t *testing.T) {
		t.Parallel()

		c, _ := confSetup(t)
		defer confRecover(t)

		var f float64
		if err := c.QueryRow("SELECT ? + 0", 1.0000001234).Scan(&f); err != nil {
			t.Fatal(err)
		}
		if f != 1.0000001234 {
			t.Errorf("got %v, want 1.0000001234", f)
		}
	})

	t.Run("time-arg", func(t *testing.T) {
		t.Parallel()

		c, _ := confSetup(t)
		defer confRecover(t)

		var s string
		tm := time.Date(2026, 11, 18, 13, 31, 33, 0, time.UTC)
		if err := c.QueryRow("SELECT CAST(? AS CHAR)", tm).Scan(&s); err != nil {
			t.Fatalf("time.Time arg: %v", err)
		}
		if s != "2026-11-18 13:31:33" {
			t.Errorf("got %q", s)
		}
	})
}

// TestSQLInjection.
func TestConformanceSQLInjection(t *testing.T) {
	t.Parallel()

	for _, arg := range []string{"1 OR 1=1", "' OR '1'='1"} {
		t.Run(arg, func(t *testing.T) {
			t.Parallel()

			c, tbl := confSetup(t)
			defer confRecover(t)

			mustExec(t, c, "CREATE TABLE "+tbl+" (v INT)")
			mustExec(t, c, "INSERT INTO "+tbl+" VALUES (1)")

			var v int
			err := c.QueryRow("SELECT v FROM "+tbl+" WHERE NULL = ?", arg).Scan(&v)
			switch {
			case errors.Is(err, sql.ErrNoRows):
			case err == nil:
				t.Errorf("injection succeeded with %q", arg)
			default:
				t.Errorf("query failed: %v", err)
			}
		})
	}
}

// Tx: Exec and QueryRow with arguments.
func TestConformanceTx(t *testing.T) {
	t.Parallel()

	t.Run("tx", func(t *testing.T) {
		t.Parallel()

		c, tbl := confSetup(t)
		defer confRecover(t)

		mustExec(t, c, "CREATE TABLE "+tbl+" (v INT) ENGINE=InnoDB")

		tx, err := c.Begin()
		if err != nil {
			t.Fatal(err)
		}

		if _, err := tx.Exec("INSERT INTO "+tbl+" VALUES (?)", 1); err != nil {
			t.Errorf("tx.Exec with arg: %v", err)
		}

		if err := tx.Rollback(); err != nil {
			t.Fatal(err)
		}

		var n int
		if err := c.QueryRow("SELECT COUNT(*) FROM " + tbl).Scan(&n); err != nil {
			t.Fatal(err)
		}
		if n != 0 {
			t.Errorf("rollback left %d rows", n)
		}
	})
}

// TestStmtMultiRows: the same Stmt queried twice, results interleaved.
func TestConformanceStmtMultiRows(t *testing.T) {
	t.Parallel()

	t.Run("interleave", func(t *testing.T) {
		t.Parallel()

		c, _ := confSetup(t)
		defer confRecover(t)

		st, err := c.Prepare("SELECT 1 UNION SELECT 0")
		if err != nil {
			t.Fatal(err)
		}

		r1, err := st.Query()
		if err != nil {
			t.Fatal(err)
		}

		r2, err := st.Query()
		if err != nil {
			t.Logf("second Query while first is open: %v (go-sql-driver buffers or uses another conn)", err)
			return
		}

		var a, b int
		r1.Next()
		err1 := r1.Scan(&a)
		r2.Next()
		err2 := r2.Scan(&b)
		if err1 != nil || err2 != nil || a != 1 || b != 1 {
			t.Errorf("interleaved rows: a=%d (%v) b=%d (%v)", a, err1, b, err2)
		}
	})
}

// Errors leave the connection usable; the error carries the MySQL code.
func TestConformanceErrorRecovery(t *testing.T) {
	t.Parallel()

	t.Run("syntax", func(t *testing.T) {
		t.Parallel()

		c, _ := confSetup(t)
		defer confRecover(t)

		_, err := c.Exec("SELEC 1")
		if err == nil {
			t.Fatal("expected error")
		}
		t.Logf("error: %v (%T)", err, err)
		if !strings.Contains(err.Error(), "1064") {
			t.Errorf("error lacks MySQL code 1064: %v", err)
		}

		var v int
		if err := c.QueryRow("SELECT 1").Scan(&v); err != nil || v != 1 {
			t.Errorf("connection unusable after error: %v", err)
		}

		if _, err := c.Prepare("SELEC 1"); err == nil {
			t.Error("prepare: expected error")
		}
		if err := c.QueryRow("SELECT 2").Scan(&v); err != nil || v != 2 {
			t.Errorf("connection unusable after prepare error: %v", err)
		}
	})

	t.Run("rows-close-early", func(t *testing.T) {
		t.Parallel()

		c, _ := confSetup(t)
		defer confRecover(t)

		for _, m := range confModes {
			rows, err := m.query(c, "SELECT 1 UNION ALL SELECT 2 UNION ALL SELECT 3")
			if err != nil {
				t.Fatal(err)
			}
			rows.Next()
			if err := rows.Close(); err != nil {
				t.Errorf("%s: close early: %v", m.name, err)
			}

			var v int
			if err := c.QueryRow("SELECT 5").Scan(&v); err != nil || v != 5 {
				t.Errorf("%s: after early close: %v %d", m.name, err, v)
			}
		}
	})
}

// TestEmptyQuery / OK packets through Query.
func TestConformanceQueryNoRows(t *testing.T) {
	t.Parallel()

	t.Run("do", func(t *testing.T) {
		t.Parallel()

		c, _ := confSetup(t)
		defer confRecover(t)

		for _, m := range confModes {
			rows, err := m.query(c, "DO 1")
			if err != nil {
				t.Errorf("%s: %v", m.name, err)
				continue
			}
			if rows == nil {
				t.Errorf("%s: nil rows for a statement without result set", m.name)
				continue
			}
			if rows.Next() {
				t.Errorf("%s: unexpected row", m.name)
			}
		}
	})
}

// TestPing and Close.
func TestConformancePingClose(t *testing.T) {
	t.Parallel()

	knownBug(t)

	addr := os.Getenv("GOMYSQL_ADDR")
	if addr == "" {
		t.Skip("GOMYSQL_ADDR not set")
	}

	c, err := gomysql.Open(gomysql.Config{Protocol: "tcp", Address: addr, User: "b", Password: "b", DbName: "gomysql"})
	if err != nil {
		t.Fatal(err)
	}
	if err := c.Ping(); err != nil {
		t.Errorf("ping: %v", err)
	}
	if err := c.Close(); err != nil {
		t.Errorf("Close on a healthy connection returns %v", err)
	}
}

// Auth plugins: caching_sha2_password is the MySQL 8 default.
func TestConformanceAuth(t *testing.T) {
	t.Parallel()

	for _, u := range []string{"b", "c2"} {
		t.Run(u, func(t *testing.T) {
			t.Parallel()
			knownBug(t)

			defer func() {
				if r := recover(); r != nil {
					t.Errorf("PANIC: %v", r)
				}
			}()

			if u == "c2" && !hasCachingSHA2(t) {
				t.Skip("server has no caching_sha2_password")
			}

			c := confConn(t, u, u)
			var v int
			if err := c.QueryRow("SELECT 1").Scan(&v); err != nil {
				t.Errorf("%v", err)
			}
		})
	}
}

// Server-side variables a pool would size itself from.
func TestConformanceServerLimits(t *testing.T) {
	t.Parallel()

	c := confConn(t, "lim", "lim")
	var maxUser, maxConn, wait, maxPacket int
	q := "SELECT @@max_user_connections, @@max_connections, @@wait_timeout, @@max_allowed_packet"
	if err := c.QueryRow(q).Scan(&maxUser, &maxConn, &wait, &maxPacket); err != nil {
		t.Fatal(err)
	}
	t.Logf("max_user_connections=%d max_connections=%d wait_timeout=%d max_allowed_packet=%d",
		maxUser, maxConn, wait, maxPacket)
	if maxUser != 7 {
		t.Errorf("session max_user_connections = %d, want the account limit 7", maxUser)
	}
}

// hasCachingSHA2 reports whether the server is MySQL or Percona 8.0+.
func hasCachingSHA2(t *testing.T) bool {
	t.Helper()

	var version string
	if err := confConn(t, "b", "b").QueryRow("SELECT VERSION()").Scan(&version); err != nil {
		t.Fatal(err)
	}

	return !strings.HasPrefix(version, "5.") && !strings.Contains(version, "MariaDB")
}

// Compressed protocol: small results (sent uncompressed below 50 bytes) and
// results large enough to be compressed.
func TestConformanceCompress(t *testing.T) {
	t.Parallel()

	for _, n := range []int{1, 1000, 100_000} {
		t.Run(strconv.Itoa(n), func(t *testing.T) {
			t.Parallel()
			knownBug(t)

			addr := os.Getenv("GOMYSQL_ADDR")
			if addr == "" {
				t.Skip("GOMYSQL_ADDR not set")
			}

			c, err := gomysql.Open(gomysql.Config{
				Protocol: "tcp", Address: addr, User: "b", Password: "b", DbName: "gomysql", IsCompress: true,
			})
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = c.Close() }()

			for range 3 {
				var s string
				if err := c.QueryRow("SELECT REPEAT('x', ?)", n).Scan(&s); err != nil {
					t.Fatal(err)
				}

				if len(s) != n {
					t.Fatalf("got %d bytes, want %d", len(s), n)
				}

				if err := c.Ping(); err != nil {
					t.Fatalf("ping: %v", err)
				}
			}
		})
	}
}

// Server errors are *gomysql.Error with the number and SQL state.
func TestConformanceServerError(t *testing.T) {
	t.Parallel()

	c := confConn(t, "b", "b")

	_, err := c.Exec("SELECT id FROM no_such_table")

	var e *gomysql.Error
	if !errors.As(err, &e) || e.Number != 1146 || string(e.SQLState[:]) != "42S02" {
		t.Fatalf("got %#v", err)
	}

	if !errors.Is(err, &gomysql.Error{Number: 1146}) {
		t.Error("errors.Is by Number")
	}

	// The connection stays usable after a server error.
	if err := c.Ping(); err != nil {
		t.Errorf("Ping after error: %v", err)
	}
}
