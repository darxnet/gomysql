package gomysql_test

import (
	"context"
	"database/sql"
	"errors"
	"testing"
	"time"

	"github.com/darxnet/gomysql"
)

// A deadline or a cancel interrupts a statement the server is still
// running; the connection is broken until Reconnect.
func TestConformanceContext(t *testing.T) {
	t.Parallel()

	c := confConn(t, "b", "b")
	defer confRecover(t)

	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()

	start := time.Now()

	rows, err := c.QueryContext(ctx, "SELECT SLEEP(5)")
	if err == nil {
		_ = rows.Close()
	}

	if took := time.Since(start); !errors.Is(err, context.DeadlineExceeded) || !errors.Is(err, gomysql.ErrBadConn) ||
		took > 2*time.Second {
		t.Errorf("SLEEP with a deadline: %v after %v", err, time.Since(start))
	}

	if err := c.Ping(); !errors.Is(err, gomysql.ErrBadConn) {
		t.Errorf("Ping after: %v, want ErrBadConn", err)
	}

	if err := c.Reconnect(); err != nil {
		t.Fatal(err)
	}

	ctx, cancel = context.WithCancel(context.Background())
	time.AfterFunc(100*time.Millisecond, cancel)

	if _, err := c.ExecContext(ctx, "DO SLEEP(5)"); !errors.Is(err, context.Canceled) {
		t.Errorf("canceled: %v", err)
	}

	if err := c.Reconnect(); err != nil {
		t.Fatal(err)
	}

	// A context that is not done changes nothing.
	ctx, cancel = context.WithTimeout(context.Background(), time.Minute)
	defer cancel()

	var n int
	if err := c.QueryRowContext(ctx, "SELECT 1").Scan(&n); err != nil || n != 1 {
		t.Errorf("SELECT 1: %d, %v", n, err)
	}

	if err := c.PingContext(ctx); err != nil {
		t.Errorf("Ping: %v", err)
	}
}

func TestConformanceBusy(t *testing.T) {
	t.Parallel()

	c := confConn(t, "b", "b")
	defer confRecover(t)

	rows, err := c.Query("SELECT 1 UNION ALL SELECT 2")
	if err != nil {
		t.Fatal(err)
	}

	if _, err := c.Exec("DO 1"); !errors.Is(err, gomysql.ErrBusy) {
		t.Errorf("Exec with open Rows: %v", err)
	}

	if err := rows.Close(); err != nil {
		t.Fatal(err)
	}

	if _, err := c.Exec("DO 1"); err != nil {
		t.Errorf("Exec after Close: %v", err)
	}

	if err := c.Close(); err != nil {
		t.Fatal(err)
	}

	if _, err := c.Exec("DO 1"); !errors.Is(err, sql.ErrConnDone) {
		t.Errorf("Exec after Close: %v", err)
	}
}

func TestConformanceBeginTx(t *testing.T) {
	t.Parallel()

	c, tbl := confSetup(t)
	defer confRecover(t)

	mustExec(t, c, "CREATE TABLE "+tbl+" (id INT)")

	ctx := context.Background()

	tx, err := c.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		t.Fatal(err)
	}

	// ER_CANT_EXECUTE_IN_READ_ONLY_TRANSACTION
	if _, e := tx.ExecContext(ctx, "INSERT INTO "+tbl+" VALUES (1)"); !errors.Is(e, &gomysql.Error{Number: 1792}) {
		t.Errorf("INSERT in a read-only transaction: %v", e)
	}

	if e := tx.Rollback(); e != nil {
		t.Fatal(e)
	}

	tx, err = c.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelSerializable})
	if err != nil {
		t.Fatal(err)
	}

	if _, err := tx.ExecContext(ctx, "INSERT INTO "+tbl+" VALUES (1)"); err != nil {
		t.Error(err)
	}

	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}

	if _, err := c.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelSnapshot}); err == nil {
		t.Error("snapshot isolation: no error")
	}
}

// A transaction whose context is canceled is rolled back by the server at
// once: its locks are free.
func TestConformanceTxContext(t *testing.T) {
	t.Parallel()

	c, tbl := confSetup(t)
	defer confRecover(t)

	mustExec(t, c, "CREATE TABLE "+tbl+" (id INT PRIMARY KEY, v INT) ENGINE=InnoDB")
	mustExec(t, c, "INSERT INTO "+tbl+" VALUES (1, 0)")

	ctx, cancel := context.WithCancel(context.Background())

	tx, err := c.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}

	if _, e := tx.Exec("UPDATE " + tbl + " SET v = 1 WHERE id = 1"); e != nil {
		t.Fatal(e)
	}

	cancel()

	other := confConn(t, "b", "b")
	mustExec(t, other, "SET SESSION innodb_lock_wait_timeout = 5")

	start := time.Now()

	if _, e := other.Exec("UPDATE " + tbl + " SET v = 2 WHERE id = 1"); e != nil || time.Since(start) > 2*time.Second {
		t.Errorf("UPDATE after cancel: %v after %v", e, time.Since(start))
	}

	var v int
	if e := other.QueryRow("SELECT v FROM " + tbl + " WHERE id = 1").Scan(&v); e != nil || v != 2 {
		t.Errorf("v = %d, %v; want 2: the canceled transaction rolled back", v, e)
	}

	if e := tx.Commit(); !errors.Is(e, context.Canceled) {
		t.Errorf("Commit after cancel: %v", e)
	}
}
