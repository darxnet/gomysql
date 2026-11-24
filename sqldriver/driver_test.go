package sqldriver_test

import (
	"context"
	"database/sql"
	"errors"
	"math"
	"os"
	"reflect"
	"testing"
	"time"

	"github.com/darxnet/gomysql"
	"github.com/darxnet/gomysql/sqldriver"
)

func liveConfig(t *testing.T, user string) gomysql.Config {
	t.Helper()

	addr := os.Getenv("GOMYSQL_ADDR")
	if addr == "" {
		t.Skip("GOMYSQL_ADDR not set")
	}

	return gomysql.Config{
		Network: "tcp", Address: addr, User: user, Password: user, Database: "gomysql",
		AllowPublicKeyRetrieval: true,
	}
}

func openDB(t *testing.T) *sql.DB {
	t.Helper()

	db := sql.OpenDB(sqldriver.NewConnector(liveConfig(t, "b")))
	t.Cleanup(func() { _ = db.Close() })

	return db
}

func TestOpenDSN(t *testing.T) {
	t.Parallel()

	config := liveConfig(t, "b")

	db, err := sql.Open("gomysql", "b:b@tcp("+config.Address+")/gomysql?allowPublicKeyRetrieval=true&"+
		"time_zone=%27%2B02:00%27")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = db.Close() }()

	var zone string
	if err := db.QueryRow("SELECT @@session.time_zone").Scan(&zone); err != nil || zone != "+02:00" {
		t.Errorf("time_zone %q, %v", zone, err)
	}

	if _, err := sql.Open("gomysql", "/db?parseTime=true"); err == nil {
		t.Error("parseTime accepted")
	}
}

func TestArgsAndValues(t *testing.T) {
	t.Parallel()

	db := openDB(t)

	var (
		u       uint64
		name    string
		in      int
		null    sql.NullString
		when    time.Time
		anyInt  any
		anyText any
		anyJSON any
	)

	err := db.QueryRow(`SELECT ? + 0, ?, (SELECT COUNT(*) FROM (SELECT 1 a UNION SELECT 2 UNION SELECT 3) t
		WHERE a IN (?)), NULL, CAST('2026-11-24 10:11:12' AS DATETIME), 42, 'text', JSON_OBJECT('a', 1)`,
		uint64(math.MaxUint64), "it's", []int{1, 3}).
		Scan(&u, &name, &in, &null, &when, &anyInt, &anyText, &anyJSON)
	if err != nil {
		t.Fatal(err)
	}

	var named string
	if err := db.QueryRow("SELECT :a", sql.Named("a", "named")).Scan(&named); err != nil || named != "named" {
		t.Errorf("sql.Named: %q, %v", named, err)
	}

	switch {
	case u != math.MaxUint64 || name != "it's" || in != 2 || null.Valid:
		t.Errorf("%d %q %d %v", u, name, in, null)
	case !when.Equal(time.Date(2026, 11, 24, 10, 11, 12, 0, time.UTC)):
		t.Errorf("time %v", when)
	case reflect.TypeOf(anyInt) != reflect.TypeFor[int64]() || reflect.TypeOf(anyText) != reflect.TypeFor[string]():
		t.Errorf("any: %T %T, want int64 and string as the text protocol gives them too", anyInt, anyText)
	case reflect.TypeOf(anyJSON) != reflect.TypeFor[[]byte]() && reflect.TypeOf(anyJSON) != reflect.TypeFor[string]():
		// []byte from the JSON type of MySQL, string from MariaDB's LONGTEXT.
		t.Errorf("JSON as %T", anyJSON)
	}
}

func TestExecPrepareTx(t *testing.T) {
	t.Parallel()

	db := openDB(t)

	if _, err := db.Exec("CREATE TABLE IF NOT EXISTS sqldriver_t " +
		"(id INT AUTO_INCREMENT PRIMARY KEY, v VARCHAR(20))"); err != nil {
		t.Fatal(err)
	}

	t.Cleanup(func() { _, _ = db.Exec("DROP TABLE sqldriver_t") })

	res, err := db.Exec("INSERT INTO sqldriver_t (v) VALUES (?), (?)", "a", "b")
	if err != nil {
		t.Fatal(err)
	}

	if n, rerr := res.RowsAffected(); rerr != nil || n != 2 {
		t.Errorf("RowsAffected %d, %v", n, rerr)
	}

	st, err := db.Prepare("SELECT v FROM sqldriver_t WHERE id = ?")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = st.Close() }()

	id, _ := res.LastInsertId()

	var v string
	if serr := st.QueryRow(id).Scan(&v); serr != nil || v != "a" {
		t.Errorf("prepared: %q, %v", v, serr)
	}

	tx, err := db.BeginTx(context.Background(), &sql.TxOptions{Isolation: sql.LevelReadCommitted})
	if err != nil {
		t.Fatal(err)
	}

	if _, txErr := tx.Exec("DELETE FROM sqldriver_t"); txErr != nil {
		t.Fatal(txErr)
	}

	if rbErr := tx.Rollback(); rbErr != nil {
		t.Fatal(rbErr)
	}

	var n int
	if cErr := db.QueryRow("SELECT COUNT(*) FROM sqldriver_t").Scan(&n); cErr != nil || n < 2 {
		t.Errorf("after the rollback: %d, %v", n, cErr)
	}

	// A statement without rows through Query.
	rows, err := db.Query("DO 1")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = rows.Close() }()

	if rows.Next() || rows.Err() != nil {
		t.Errorf("Query without rows: %v", rows.Err())
	}
}

func TestColumnTypesAndRaw(t *testing.T) {
	t.Parallel()

	db := openDB(t)

	rows, err := db.Query("SELECT CAST(1.5 AS DECIMAL(5,2)) AS d, 'x' AS s")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = rows.Close() }()

	if rows.Err() != nil {
		t.Fatal(rows.Err())
	}

	types, err := rows.ColumnTypes()
	if err != nil {
		t.Fatal(err)
	}

	if p, s, ok := types[0].DecimalSize(); !ok || p != 5 || s != 2 || types[0].DatabaseTypeName() != "DECIMAL" {
		t.Errorf("DECIMAL: %d %d %v %s", p, s, ok, types[0].DatabaseTypeName())
	}

	conn, err := db.Conn(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = conn.Close() }()

	err = conn.Raw(func(dc any) error {
		c, ok := dc.(interface{ Conn() *gomysql.Conn })
		if !ok {
			return errors.New("no Conn method")
		}

		return c.Conn().Ping()
	})
	if err != nil {
		t.Errorf("Raw: %v", err)
	}
}

// A connection the server closed while idle in the pool is pinged before
// reuse and replaced: the statement runs on a new one.
func TestKilledIdleConn(t *testing.T) {
	t.Parallel()

	db := openDB(t)
	db.SetMaxOpenConns(1)

	var id int64
	if err := db.QueryRow("SELECT CONNECTION_ID()").Scan(&id); err != nil {
		t.Fatal(err)
	}

	admin, err := gomysql.Connect(liveConfig(t, "root"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = admin.Close() }()

	if _, err := admin.Exec("KILL ?", id); err != nil {
		t.Fatal(err)
	}

	time.Sleep(1100 * time.Millisecond)

	var other int64
	if err := db.QueryRow("SELECT CONNECTION_ID()").Scan(&other); err != nil || other == id {
		t.Errorf("after KILL: %d, %v", other, err)
	}
}
