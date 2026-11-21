package gomysql_test

import (
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"testing"

	"github.com/darxnet/gomysql"
)

// tsvRows returns n TSV rows: id, name.
func tsvRows(from, n int) string {
	var b strings.Builder
	for i := from; i < from+n; i++ {
		fmt.Fprintf(&b, "%d\tname %d ✓\n", i, i)
	}

	return b.String()
}

// failingReader returns data, then err.
type failingReader struct {
	r   io.Reader
	err error
}

func (f *failingReader) Read(p []byte) (int, error) {
	n, err := f.r.Read(p)
	if errors.Is(err, io.EOF) {
		return n, f.err
	}

	return n, err
}

// enableLocalInfile turns local_infile on as root, OFF by default since
// MySQL 8.0.
func enableLocalInfile(t *testing.T) {
	t.Helper()

	root, err := gomysql.Connect(gomysql.Config{
		Network: "tcp", Address: os.Getenv("GOMYSQL_ADDR"), User: "root", Password: "root",
		AllowPublicKeyRetrieval: true,
	})
	if err != nil {
		t.Skipf("no root account: %v", err)
	}
	defer func() { _ = root.Close() }()

	if _, err := root.Exec("SET GLOBAL local_infile = 1"); err != nil {
		t.Skipf("local_infile: %v", err)
	}
}

func TestConformanceLoadData(t *testing.T) {
	t.Parallel()

	c, tbl := confSetup(t)
	defer confRecover(t)

	enableLocalInfile(t)
	mustExec(t, c, "CREATE TABLE "+tbl+" (id INT PRIMARY KEY, name VARCHAR(50)) ENGINE=InnoDB CHARACTER SET utf8mb4")

	load := "LOAD DATA LOCAL INFILE 'rows' REPLACE INTO TABLE " + tbl + " CHARACTER SET utf8mb4 (id, name)"
	count := func() (n int) {
		if err := c.QueryRow("SELECT COUNT(*) FROM " + tbl).Scan(&n); err != nil {
			t.Fatal(err)
		}

		return n
	}

	// 100000 rows, several packets of 1 MB.
	res, err := c.LoadData(load, strings.NewReader(tsvRows(0, 100000)))
	if err != nil {
		t.Fatal(err)
	}

	if n, _ := res.RowsAffected(); n != 100000 || count() != 100000 {
		t.Errorf("affected %d, count %d", n, count())
	}

	var name string
	if e := c.QueryRow("SELECT name FROM " + tbl + " WHERE id = 99999").Scan(&name); e != nil || name != "name 99999 ✓" {
		t.Errorf("row 99999: %q, %v", name, e)
	}

	// An empty file.
	if _, e := c.LoadData(load, strings.NewReader("")); e != nil {
		t.Errorf("empty: %v", e)
	}

	// A failing reader: the connection is closed, the statement rolled
	// back.
	mustExec(t, c, "TRUNCATE TABLE "+tbl)

	errRead := errors.New("read failed")

	_, err = c.LoadData(load, &failingReader{strings.NewReader(tsvRows(0, 200000)), errRead})
	if !errors.Is(err, errRead) || !errors.Is(err, gomysql.ErrBadConn) {
		t.Fatalf("failing reader: %v", err)
	}

	if e := c.Reconnect(); e != nil {
		t.Fatal(e)
	}

	if n := count(); n != 0 {
		t.Errorf("%d rows of an aborted LOAD DATA", n)
	}

	// Not a LOAD DATA statement, and LOAD DATA outside LoadData: errors,
	// the connection usable.
	if _, e := c.LoadData("DO 1", strings.NewReader("x")); e == nil {
		t.Error("DO 1: no error")
	}

	if _, e := c.Exec(load); e == nil || !strings.Contains(e.Error(), "LoadData") {
		t.Errorf("Exec of LOAD DATA: %v", e)
	}

	if n := count(); n != 0 {
		t.Errorf("Exec of LOAD DATA loaded %d rows", n)
	}

	if e := c.Ping(); e != nil {
		t.Errorf("Ping: %v", e)
	}
}

func TestConformanceLoadDataCompressed(t *testing.T) {
	t.Parallel()

	_, tbl := confSetup(t)
	defer confRecover(t)

	enableLocalInfile(t)

	c, err := gomysql.Connect(gomysql.Config{
		Network: "tcp", Address: os.Getenv("GOMYSQL_ADDR"), User: "b", Password: "b", Database: "gomysql",
		AllowPublicKeyRetrieval: true, Compress: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = c.Close() }()

	mustExec(t, c, "CREATE TABLE "+tbl+" (id INT PRIMARY KEY, name VARCHAR(50)) CHARACTER SET utf8mb4")

	res, err := c.LoadData("LOAD DATA LOCAL INFILE 'x' INTO TABLE "+tbl+" CHARACTER SET utf8mb4 (id, name)",
		strings.NewReader(tsvRows(0, 50000)))
	if n, _ := res.RowsAffected(); err != nil || n != 50000 {
		t.Errorf("compressed: %d, %v", n, err)
	}
}
