package gomysql

import (
	"context"
	"database/sql"
	"errors"
	"testing"
	"time"
)

// stateServer answers COM_QUERY by its text: "rows" with three rows,
// "ok", "err" with a server error, "badseq" with a wrong sequence id,
// "hang" not at all; COM_PING with OK.
func stateServer(t *testing.T) Config {
	t.Helper()

	return newFakeServer(t, true, func(c *fakeConn, cmd byte, payload []byte) error {
		if cmd == comPing {
			return c.ok()
		}

		switch string(payload) {
		case "rows":
			c.seq = 1
			out := c.resultSet(nil, colString, []byte{1, '1'}, []byte{1, '2'}, []byte{1, '3'})
			out = c.eof(out, true)
			_, err := c.conn.Write(out)

			return err
		case "err":
			return c.errPacket(1064, "42000", "syntax")
		case "badseq":
			c.seq = 7

			return c.ok()
		case "hang":
			_, err := c.read() // until the client goes

			return err
		}

		return c.ok()
	}).config()
}

func TestConnBusy(t *testing.T) {
	t.Parallel()

	conn, err := Connect(stateServer(t))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = conn.Close() }()

	rows, err := conn.Query("rows")
	if err != nil {
		t.Fatal(err)
	}

	if !rows.Next() {
		t.Fatal(rows.Err())
	}

	for name, call := range map[string]func() error{
		"Exec":    func() error { _, e := conn.Exec("ok"); return e },
		"Query":   func() error { r, e := conn.Query("rows"); _ = r; return e }, //nolint:sqlclosecheck // fails
		"Ping":    conn.Ping,
		"Prepare": func() error { _, e := conn.Prepare("x"); return e },
	} {
		if e := call(); !errors.Is(e, ErrBusy) {
			t.Errorf("%s with open Rows: %v, want ErrBusy", name, e)
		}
	}

	// The rows were not disturbed.
	var n int
	for rows.Next() {
		n++
	}

	if n != 2 || rows.Err() != nil {
		t.Errorf("%d more rows, %v", n, rows.Err())
	}

	// Read to the end: the connection is idle without Close.
	if e := conn.Ping(); e != nil {
		t.Errorf("Ping after the rows: %v", e)
	}

	// Closed early: idle again.
	rows, err = conn.Query("rows")
	if err != nil || rows.Close() != nil {
		t.Fatal(err)
	}

	if _, err := conn.Exec("ok"); err != nil {
		t.Errorf("Exec after Close: %v", err)
	}
}

func TestConnBroken(t *testing.T) {
	t.Parallel()

	conn, err := Connect(stateServer(t))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = conn.Close() }()

	// A server error leaves the connection usable.
	var serverErr *Error
	if _, err := conn.Exec("err"); !errors.As(err, &serverErr) {
		t.Fatalf("err: %v", err)
	}

	if e := conn.Ping(); e != nil {
		t.Fatalf("Ping after a server error: %v", e)
	}

	if _, err := conn.Exec("badseq"); !errors.Is(err, ErrBadConn) {
		t.Fatalf("badseq: %v, want ErrBadConn", err)
	}

	// Broken: nothing more is sent, the cause is kept.
	if err := conn.Ping(); !errors.Is(err, ErrBadConn) || !errors.Is(err, conn.cause) {
		t.Errorf("Ping on a broken connection: %v", err)
	}

	if err := conn.Reconnect(); err != nil {
		t.Fatal(err)
	}

	if e := conn.Ping(); e != nil {
		t.Errorf("Ping after Reconnect: %v", e)
	}

	if err := conn.Close(); err != nil {
		t.Errorf("Close: %v", err)
	}

	if err := conn.Ping(); !errors.Is(err, sql.ErrConnDone) {
		t.Errorf("Ping after Close: %v, want sql.ErrConnDone", err)
	}

	if err := conn.Close(); err != nil {
		t.Errorf("second Close: %v", err)
	}
}

// Rows and statements of a connection before Reconnect are refused, not
// read from the new one.
func TestConnStale(t *testing.T) {
	t.Parallel()

	conn, err := Connect(stateServer(t))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = conn.Close() }()

	rows, err := conn.Query("rows")
	if err != nil {
		t.Fatal(err)
	}

	stmt := &Stmt{conn: conn, session: conn.session}

	if err := conn.Reconnect(); err != nil {
		t.Fatal(err)
	}

	if rows.Next() || !errors.Is(rows.Err(), sql.ErrConnDone) {
		t.Errorf("old rows: %v", rows.Err())
	}

	if err := rows.Close(); !errors.Is(err, sql.ErrConnDone) {
		t.Errorf("old rows Close: %v", err)
	}

	if _, err := stmt.Exec(); !errors.Is(err, errStaleStmt) {
		t.Errorf("old statement: %v", err)
	}

	if err := stmt.Close(); err != nil {
		t.Errorf("old statement Close: %v", err)
	}

	if e := conn.Ping(); e != nil {
		t.Errorf("Ping: %v", e)
	}
}

func TestConnContext(t *testing.T) {
	t.Parallel()

	cfg := stateServer(t)

	conn, err := Connect(cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = conn.Close() }()

	// Done before: nothing sent, the connection idle.
	canceled, cancel := context.WithCancel(context.Background())
	cancel()

	if _, e := conn.ExecContext(canceled, "ok"); !errors.Is(e, context.Canceled) || errors.Is(e, ErrBadConn) {
		t.Errorf("canceled before: %v", e)
	}

	if e := conn.Ping(); e != nil {
		t.Fatalf("Ping: %v", e)
	}

	// A deadline interrupts a statement that does not end.
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()

	start := time.Now()

	_, err = conn.ExecContext(ctx, "hang")
	if !errors.Is(err, context.DeadlineExceeded) || !errors.Is(err, ErrBadConn) || time.Since(start) > 2*time.Second {
		t.Errorf("hang with a deadline: %v after %v", err, time.Since(start))
	}

	if err := conn.Ping(); !errors.Is(err, ErrBadConn) {
		t.Errorf("Ping after the deadline: %v, want ErrBadConn", err)
	}

	// Canceling interrupts too.
	if err := conn.Reconnect(); err != nil {
		t.Fatal(err)
	}

	ctx, cancel = context.WithCancel(context.Background())
	time.AfterFunc(50*time.Millisecond, cancel)

	if _, err := conn.ExecContext(ctx, "hang"); !errors.Is(err, context.Canceled) {
		t.Errorf("hang canceled: %v", err)
	}

	// A context that ends after the statement leaves no deadline behind.
	if err := conn.Reconnect(); err != nil {
		t.Fatal(err)
	}

	ctx, cancel = context.WithTimeout(context.Background(), 30*time.Millisecond)
	if _, err := conn.ExecContext(ctx, "ok"); err != nil {
		t.Fatal(err)
	}

	cancel()
	time.Sleep(50 * time.Millisecond)

	if e := conn.Ping(); e != nil {
		t.Errorf("Ping after the context: %v", e)
	}
}

// The context of a query applies until its rows are read.
func TestConnContextRows(t *testing.T) {
	t.Parallel()

	conn, err := Connect(stateServer(t))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = conn.Close() }()

	ctx, cancel := context.WithCancel(context.Background())

	rows, err := conn.QueryContext(ctx, "rows")
	if err != nil || !rows.Next() {
		t.Fatal(err)
	}

	cancel()

	// The rows are buffered, so the cancel may come late; then the next
	// command is refused: the connection is broken.
	for rows.Next() { //nolint:revive // drain
	}

	if rows.Err() != nil && !errors.Is(rows.Err(), context.Canceled) {
		t.Errorf("rows after cancel: %v", rows.Err())
	}

	if rows.Err() == nil {
		if err := conn.Ping(); err != nil {
			t.Errorf("Ping: %v", err)
		}
	}
}

// Cancellation racing the end of a statement: either the statement
// succeeds and the connection stays usable, or it fails with the context's
// error and the connection is broken.
func TestConnContextRace(t *testing.T) {
	t.Parallel()

	conn, err := Connect(stateServer(t))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = conn.Close() }()

	for i := range 300 {
		ctx, cancel := context.WithCancel(context.Background())

		go cancel()

		_, err := conn.ExecContext(ctx, "ok")

		switch {
		case err == nil:
			if e := conn.Ping(); e != nil {
				t.Fatalf("%d: Ping after a statement that succeeded: %v", i, e)
			}
		case errors.Is(err, context.Canceled):
			if errors.Is(err, ErrBadConn) {
				if e := conn.Reconnect(); e != nil {
					t.Fatal(e)
				}
			}
		default:
			t.Fatalf("%d: %v", i, err)
		}
	}
}

func TestConnectContext(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	if _, err := ConnectContext(ctx, stateServer(t)); !errors.Is(err, context.Canceled) {
		t.Errorf("canceled: %v", err)
	}

	// A server that never sends the handshake.
	cfg := rawServer(t, func(c *fakeConn) error {
		_, err := c.read()

		return err
	})

	ctx, cancel = context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()

	if _, err := ConnectContext(ctx, cfg); !errors.Is(err, context.DeadlineExceeded) {
		t.Errorf("no handshake: %v", err)
	}
}

// The context of a transaction closes the socket when done: the commands
// of the transaction then fail with its error.
func TestTxContext(t *testing.T) {
	t.Parallel()

	conn, err := Connect(stateServer(t))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = conn.Close() }()

	// Committed before the context ends: nothing happens after.
	ctx, cancel := context.WithCancel(context.Background())

	tx, err := conn.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}

	if e := tx.Commit(); e != nil {
		t.Fatal(e)
	}

	cancel()

	if e := conn.Ping(); e != nil {
		t.Fatalf("Ping after Commit and cancel: %v", e)
	}

	// Canceled during the transaction.
	ctx, cancel = context.WithCancel(context.Background())

	if tx, err = conn.BeginTx(ctx, nil); err != nil {
		t.Fatal(err)
	}

	cancel()

	if _, e := tx.Exec("ok"); !errors.Is(e, context.Canceled) || !errors.Is(e, ErrBadConn) {
		t.Errorf("Exec after cancel: %v", e)
	}

	if e := tx.Commit(); !errors.Is(e, context.Canceled) || !errors.Is(e, ErrBadConn) {
		t.Errorf("Commit after cancel: %v", e)
	}

	// Canceled while a statement of it runs.
	if e := conn.Reconnect(); e != nil {
		t.Fatal(e)
	}

	ctx, cancel = context.WithCancel(context.Background())

	if tx, err = conn.BeginTx(ctx, nil); err != nil {
		t.Fatal(err)
	}

	time.AfterFunc(50*time.Millisecond, cancel)

	if _, e := tx.Exec("hang"); !errors.Is(e, context.Canceled) || !errors.Is(e, ErrBadConn) {
		t.Errorf("hang canceled: %v", e)
	}

	if e := tx.Rollback(); !errors.Is(e, context.Canceled) {
		t.Errorf("Rollback after cancel: %v", e)
	}

	// Reconnect forgets the transaction.
	if e := conn.Reconnect(); e != nil {
		t.Fatal(e)
	}

	if e := conn.Ping(); e != nil {
		t.Errorf("Ping after Reconnect: %v", e)
	}
}
