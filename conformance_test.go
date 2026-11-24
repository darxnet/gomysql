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
	"context"
	"crypto/rand"
	"crypto/tls"
	"database/sql"
	"errors"
	"fmt"
	"math"
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
var knownBugs = map[string]string{}

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

	c, err := gomysql.Connect(gomysql.Config{
		Network: "tcp", Address: addr, User: user, Password: pass,
		Database: "gomysql", Collation: "utf8mb4_bin",
		AllowPublicKeyRetrieval: true,
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
		if cc, err := gomysql.Connect(gomysql.Config{
			Network: "tcp", Address: os.Getenv("GOMYSQL_ADDR"), User: "b", Password: "b", Database: "gomysql",
			AllowPublicKeyRetrieval: true,
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

// TestNULL: NULL as in database/sql: an error for a scalar destination,
// nil or Valid=false for the others; later columns are not shifted.
func TestConformanceNULL(t *testing.T) {
	t.Parallel()

	t.Run("null", func(t *testing.T) {
		t.Parallel()

		c, _ := confSetup(t)
		defer confRecover(t)

		for _, m := range confModes {
			var (
				a, last int
				ni      sql.NullInt64
				ns      sql.NullString
				p           = new(int)
				anyV    any = "stale"
				b           = []byte("stale")
				raw         = sql.RawBytes("stale")
			)

			err := scanOne(t, m, c, "SELECT 1, NULL, NULL, NULL, NULL, NULL, NULL, 3", //nolint:dupword // SQL
				&a, &ni, &ns, &p, &anyV, &b, &raw, &last)
			if err != nil {
				t.Errorf("%s: %v", m.name, err)
			} else if a != 1 || ni.Valid || ns.Valid || p != nil || anyV != nil || b != nil || raw != nil || last != 3 {
				t.Errorf("%s: got %d %v %v %v %v %q %q %d", m.name, a, ni, ns, p, anyV, b, raw, last)
			}

			var n int
			if err := scanOne(t, m, c, "SELECT NULL", &n); err == nil {
				t.Errorf("%s: NULL into *int: no error", m.name)
			}

			var s string
			if err := scanOne(t, m, c, "SELECT NULL", &s); err == nil {
				t.Errorf("%s: NULL into *string: no error", m.name)
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
			n sql.NullInt64
			f float64
		)
		if !rows.Next() {
			t.Fatal("no row", rows.Err())
		}
		if err := rows.Scan(&i, &u, &s, &n, &f); err != nil {
			t.Errorf("scan: %v", err)
		}
		_ = rows.Close()

		if i != -5 || u != 1<<63 || s != "it's" || n.Valid || f != 1.5 {
			t.Errorf("got %d %d %q %v %v", i, u, s, n, f)
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

	c, err := gomysql.Connect(gomysql.Config{
		Network: "tcp", Address: addr, User: "b", Password: "b", Database: "gomysql", AllowPublicKeyRetrieval: true,
	})
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

// Collation is set by the handshake.
func TestConformanceCollation(t *testing.T) {
	t.Parallel()

	addr := os.Getenv("GOMYSQL_ADDR")
	if addr == "" {
		t.Skip("GOMYSQL_ADDR not set")
	}

	for _, collation := range []string{"utf8mb4_unicode_ci", "latin1_bin"} {
		c, err := gomysql.Connect(gomysql.Config{
			Network: "tcp", Address: addr, User: "b", Password: "b", Collation: collation,
			AllowPublicKeyRetrieval: true,
		})
		if err != nil {
			t.Fatal(err)
		}

		var got string
		if err := c.QueryRow("SELECT @@collation_connection").Scan(&got); err != nil || got != collation {
			t.Errorf("collation_connection %q, %v; want %q", got, err, collation)
		}

		_ = c.Close()
	}
}

// testTLSConfig accepts the self-signed certificates of test servers.
func testTLSConfig() *tls.Config {
	// MySQL 5.7 with OpenSSL 1.0.2 has no ECDHE, only the RSA key exchange,
	// which Go leaves out of its default cipher suites since Go 1.22.
	suites := make([]uint16, 0, len(tls.CipherSuites())+2)
	suites = append(suites, tls.TLS_RSA_WITH_AES_128_GCM_SHA256, tls.TLS_RSA_WITH_AES_256_GCM_SHA384)

	for _, cs := range tls.CipherSuites() {
		suites = append(suites, cs.ID)
	}

	return &tls.Config{
		InsecureSkipVerify: true, //nolint:gosec // self-signed test server
		CipherSuites:       suites,
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

// Compressed protocol: small results (sent uncompressed below 50 bytes) and
// results large enough to be compressed.
func TestConformanceCompress(t *testing.T) {
	t.Parallel()

	for _, n := range []int{1, 1000, 100_000, 3 << 20, 20 << 20} {
		t.Run(strconv.Itoa(n), func(t *testing.T) {
			t.Parallel()
			knownBug(t)

			addr := os.Getenv("GOMYSQL_ADDR")
			if addr == "" {
				t.Skip("GOMYSQL_ADDR not set")
			}

			c, err := gomysql.Connect(gomysql.Config{
				Network: "tcp", Address: addr, User: "b", Password: "b", Database: "gomysql", Compress: true,
				AllowPublicKeyRetrieval: true,
			})
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = c.Close() }()

			var maxPacket int
			if err := c.QueryRow("SELECT @@max_allowed_packet").Scan(&maxPacket); err != nil {
				t.Fatal(err)
			}

			if n > maxPacket-1024 {
				t.Skipf("max_allowed_packet is %d", maxPacket)
			}

			for range 2 {
				compressRoundTrip(t, c, n)
			}
		})
	}
}

// compressRoundTrip sends and receives n bytes, then pings.
func compressRoundTrip(t *testing.T, c *gomysql.Conn, n int) {
	t.Helper()

	var s string
	if err := c.QueryRow("SELECT REPEAT('x', ?)", n).Scan(&s); err != nil {
		t.Fatal(err)
	}

	if len(s) != n {
		t.Fatalf("got %d bytes, want %d", len(s), n)
	}

	var length int
	if err := c.QueryRow("SELECT LENGTH('" + strings.Repeat("y", n) + "')").Scan(&length); err != nil {
		t.Fatal(err)
	}

	if length != n {
		t.Fatalf("server got %d bytes, want %d", length, n)
	}

	if err := c.Ping(); err != nil {
		t.Fatalf("ping: %v", err)
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

// After wait_timeout the server closes the connection: MySQL 8.0.24+ sends
// error 4031 first, older servers just close. Both are ErrBadConn.
func TestConformanceWaitTimeout(t *testing.T) {
	t.Parallel()

	c := confConn(t, "b", "b")

	if _, err := c.Exec("SET SESSION wait_timeout = 1"); err != nil {
		t.Fatal(err)
	}

	time.Sleep(2500 * time.Millisecond)

	err := c.Ping()
	if err == nil {
		t.Fatal("Ping after wait_timeout succeeded")
	}

	var e *gomysql.Error
	if errors.As(err, &e) && e.Number != 4031 {
		t.Errorf("got server error %v, want 4031 or a network error", err)
	}

	// Either way the connection is closed.
	if !errors.Is(err, gomysql.ErrBadConn) {
		t.Errorf("got %v, want ErrBadConn", err)
	}

	t.Logf("Ping after wait_timeout: %v", err)
}

// TLS with the server's self-signed certificate.
func TestConformanceTLS(t *testing.T) {
	t.Parallel()

	addr := os.Getenv("GOMYSQL_ADDR")
	if addr == "" {
		t.Skip("GOMYSQL_ADDR not set")
	}

	// have_ssl is gone in MySQL 8.0.26+, where TLS is always built in.
	name, haveSSL := "", "YES"
	if err := confConn(t, "b", "b").QueryRow("SHOW VARIABLES LIKE 'have_ssl'").Scan(&name, &haveSSL); err != nil &&
		!errors.Is(err, sql.ErrNoRows) {
		t.Fatal(err)
	}

	if haveSSL != "YES" {
		t.Skipf("the server has no TLS (have_ssl=%s)", haveSSL)
	}

	c, err := gomysql.Connect(gomysql.Config{
		Network: "tcp", Address: addr, User: "b", Password: "b", Database: "gomysql",
		TLS: testTLSConfig(),
	})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = c.Close() }()

	var cipher string
	if err := c.QueryRow("SHOW SESSION STATUS LIKE 'Ssl_cipher'").Scan(new(string), &cipher); err != nil {
		t.Fatal(err)
	}

	if cipher == "" {
		t.Error("the session is not encrypted")
	}

	compressRoundTrip(t, c, 100_000)
}

// Compressed requests whose packet count differs from their frame count
// (a payload just under 16 MB is one packet but two frames), and
// incompressible payloads of 16 MB and more, whose compressed frame would
// be larger than the plain one.
func TestConformanceCompressEdges(t *testing.T) {
	t.Parallel()

	const wrapper = len("SELECT LENGTH('')") + 1 // and the command byte

	sizes := map[string]int{
		"one-packet-two-frames": 0xffffff - 2 - wrapper,
		"exactly-one-packet":    0xffffff - wrapper,
		"incompressible-16MB":   -(16 << 20),
		"incompressible-20MB":   -(20 << 20),
	}

	for name, n := range sizes {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			c := confCompressConn(t, max(n, -n)+1024)

			if n > 0 {
				compressRoundTrip(t, c, n)

				return
			}

			blob := make([]byte, -n)
			_, _ = rand.Read(blob)

			stmt, err := c.Prepare("SELECT LENGTH(?)")
			if err != nil {
				t.Fatal(err)
			}

			rows, err := stmt.Query(blob)
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = rows.Close() }()

			var length int64
			if !rows.Next() {
				t.Fatalf("no row: %v", rows.Err())
			}

			if err := rows.Scan(&length); err != nil {
				t.Fatal(err)
			}

			if length != int64(-n) {
				t.Errorf("server got %d bytes, want %d", length, -n)
			}
		})
	}
}

// confCompressConn opens a compressed connection, skipping the test when
// the server does not accept packets of need bytes.
func confCompressConn(t *testing.T, need int) *gomysql.Conn {
	t.Helper()

	addr := os.Getenv("GOMYSQL_ADDR")
	if addr == "" {
		t.Skip("GOMYSQL_ADDR not set")
	}

	c, err := gomysql.Connect(gomysql.Config{
		Network: "tcp", Address: addr, User: "b", Password: "b", Database: "gomysql", Compress: true,
		AllowPublicKeyRetrieval: true,
	})
	if err != nil {
		t.Fatal(err)
	}

	t.Cleanup(func() { _ = c.Close() })

	var maxPacket int
	if err := c.QueryRow("SELECT @@max_allowed_packet").Scan(&maxPacket); err != nil {
		t.Fatal(err)
	}

	if need > maxPacket {
		t.Skipf("max_allowed_packet is %d", maxPacket)
	}

	return c
}

// Prepare of the same text returns the cached statement; beyond the cache
// the least recently used are deallocated on the server and prepared again
// when executed.
func TestConformanceStmtCache(t *testing.T) {
	t.Parallel()

	c := confConn(t, "b", "b")

	first, err := c.Prepare("SELECT ?")
	if err != nil {
		t.Fatal(err)
	}

	if again, err := c.Prepare("SELECT ?"); err != nil || again != first {
		t.Fatalf("Prepare of the same text: %p, %v; want %p", again, err, first)
	}

	const n = 300 // more than a connection's share of max_prepared_stmt_count

	for i := range n {
		st, err := c.Prepare(fmt.Sprintf("SELECT ? + %d", i))
		if err != nil {
			t.Fatal(err)
		}

		var got int
		if err := st.QueryRow(1).Scan(&got); err != nil || got != i+1 {
			t.Fatalf("statement %d: %d, %v", i, got, err)
		}
	}

	var got int
	if err := first.QueryRow(7).Scan(&got); err != nil || got != 7 {
		t.Fatalf("the evicted first statement: %d, %v", got, err)
	}

	var (
		name   string
		closed int
	)

	if err := c.QueryRow("SHOW SESSION STATUS LIKE 'Com_stmt_close'").Scan(&name, &closed); err != nil {
		t.Fatal(err)
	}

	if closed < 100 {
		t.Errorf("Com_stmt_close = %d: the evicted statements were not deallocated", closed)
	}

	t.Logf("Com_stmt_close = %d", closed)
}

// A cached statement outlives its table: binary data stays whole after
// the table is recreated twice with other column types (MySQL 8 converted
// a MYSQL_TYPE_STRING parameter in the character set of the old column).
func TestConformanceStmtRecreatedTable(t *testing.T) {
	t.Parallel()

	c := confConn(t, "b", "b")
	defer func() { _, _ = c.Exec("DROP TABLE IF EXISTS stmt_recreated") }()

	var st *gomysql.Stmt

	for _, step := range []struct {
		typ string
		arg any
	}{{"BIGINT", 0}, {"VARCHAR(64)", "x"}, {"VARBINARY(8)", []byte{0, 0xff, 'a'}}} {
		mustExec(t, c, "DROP TABLE IF EXISTS stmt_recreated")
		mustExec(t, c, "CREATE TABLE stmt_recreated (id INT PRIMARY KEY, v "+step.typ+") DEFAULT CHARSET=utf8mb4")

		var err error
		if st, err = c.Prepare("INSERT INTO stmt_recreated VALUES (1, ?)"); err != nil {
			t.Fatal(err)
		}

		if _, err := st.Exec(step.arg); err != nil {
			t.Fatalf("%s: %v", step.typ, err)
		}
	}

	var got []byte
	if err := c.QueryRow("SELECT v FROM stmt_recreated").Scan(&got); err != nil || string(got) != "\x00\xffa" {
		t.Errorf("stored %q, %v; want %q", got, err, "\x00\xffa")
	}
}

// A DOUBLE after a FLOAT in one row is a float64: the decoder cleared the
// float32 flag only for FLOAT, so the DOUBLE came out as a float32.
func TestConformanceFloatThenDouble(t *testing.T) {
	t.Parallel()

	c := confConn(t, "b", "b")
	mustExec(t, c, "CREATE TEMPORARY TABLE float_double (f FLOAT, d DOUBLE)")
	mustExec(t, c, "INSERT INTO float_double VALUES (1.5, 1e300)")

	for _, m := range confModes {
		rows, err := m.query(c, "SELECT f, d FROM float_double")
		if err != nil {
			t.Fatalf("%s: %v", m.name, err)
		}

		var f, d any
		if !rows.Next() || rows.Scan(&f, &d) != nil {
			t.Fatalf("%s: no row", m.name)
		}

		_ = rows.Close()

		if _, ok := f.(float32); !ok || d != any(1e300) {
			t.Errorf("%s: FLOAT %T %v, DOUBLE %T %v", m.name, f, f, d, d)
		}
	}
}

// MySQL 8.0.22 and later keep the type a parameter had in an earlier
// execution of a statement and convert later arguments to it: a string
// after an integer came back as 0, anything after a DATETIME as a zero or
// garbage date, a large integer after a DOUBLE rounded. The client
// prepares the statement again when the class of a type changes, also for
// another Prepare of the same text, which the cache shares: every pair of
// classes gives what a new statement gives.
func TestConformanceStmtTypeChanges(t *testing.T) {
	t.Parallel()

	c := confConn(t, "b", "b")

	args := []any{
		int64(5), uint64(1 << 63), int64(math.MaxInt64 - 1), 1.5, float32(2.5), "abc", "12",
		[]byte("xyz"), true, time.Date(2024, 1, 2, 3, 4, 5, 0, time.UTC), nil,
	}

	fresh := 0

	scan := func(st *gomysql.Stmt, arg any) string {
		var got any
		if err := st.QueryRow(arg).Scan(&got); err != nil {
			return "error " + err.Error()
		}

		if b, ok := got.([]byte); ok {
			got = string(b)
		}

		return fmt.Sprintf("%T %v", got, got)
	}

	for _, first := range args {
		for _, second := range args {
			shared, err := c.Prepare("SELECT ?")
			if err != nil {
				t.Fatal(err)
			}

			_ = scan(shared, first)
			got := scan(shared, second)

			fresh++

			once, err := c.Prepare(fmt.Sprintf("SELECT ? /* %d */", fresh))
			if err != nil {
				t.Fatal(err)
			}

			if want := scan(once, second); got != want {
				t.Errorf("%T %v after %T %v: %s, want %s", second, second, first, first, got, want)
			}
		}
	}
}

// A command that does not start keeps the types the server knows: the
// next execution still sees the change.
func TestConformanceStmtTypeChangeNotSent(t *testing.T) {
	t.Parallel()

	c := confConn(t, "b", "b")

	st, err := c.Prepare("SELECT ? /* not sent */")
	if err != nil {
		t.Fatal(err)
	}

	var n int64
	if err := st.QueryRow(int64(5)).Scan(&n); err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	if _, err := st.ExecContext(ctx, "abc"); !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled: %v", err)
	}

	var s string
	if err := st.QueryRow("abc").Scan(&s); err != nil || s != "abc" {
		t.Errorf("after a canceled execution: %q, %v", s, err)
	}
}

// Stored procedures that return rows (CALL) need CLIENT_MULTI_RESULTS: the
// rows of the first result set are read, the rest and the status of the
// CALL are drained, and the connection stays in sync.
func TestConformanceCall(t *testing.T) {
	t.Parallel()

	c := confConn(t, "root", "root")

	for _, q := range []string{
		"DROP PROCEDURE IF EXISTS call_rows", "DROP PROCEDURE IF EXISTS call_two", "DROP PROCEDURE IF EXISTS call_none",
		"CREATE PROCEDURE call_rows(IN n INT) BEGIN SELECT n AS a UNION SELECT n + 1; END",
		"CREATE PROCEDURE call_two() BEGIN SELECT 1; SELECT 2, 3; END",
		"CREATE PROCEDURE call_none() BEGIN DO 1; END",
	} {
		mustExec(t, c, q)
	}

	defer func() {
		for _, p := range []string{"call_rows", "call_two", "call_none"} {
			_, _ = c.Exec("DROP PROCEDURE IF EXISTS " + p)
		}
	}()

	for _, m := range confModes {
		rows, err := m.query(c, "CALL call_rows(?)", 5)
		if err != nil {
			t.Fatalf("%s: %v", m.name, err)
		}

		var got []int

		for rows.Next() {
			var v int
			if err := rows.Scan(&v); err != nil {
				t.Fatal(err)
			}

			got = append(got, v)
		}

		if err := rows.Close(); err != nil || len(got) != 2 || got[0] != 5 || got[1] != 6 {
			t.Errorf("%s: CALL rows %v, %v", m.name, got, err)
		}
	}

	var v int
	if err := c.QueryRow("CALL call_two()").Scan(&v); err != nil || v != 1 {
		t.Errorf("QueryRow of two result sets: %d, %v", v, err)
	}

	for _, q := range []string{"CALL call_two()", "CALL call_none()", "CALL call_rows(1)"} {
		if _, err := c.Exec(q); err != nil {
			t.Errorf("Exec %s: %v", q, err)
		}
	}

	if err := c.Ping(); err != nil {
		t.Errorf("out of sync after CALL: %v", err)
	}
}
