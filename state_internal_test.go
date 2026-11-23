package gomysql

import (
	"bytes"
	"context"
	"database/sql"
	"errors"
	"io"
	"strconv"
	"strings"
	"testing"
	"testing/iotest"
	"time"
)

// stateServer answers COM_QUERY by its text: "rows" with three rows,
// "ok", "err" with a server error, "badseq" with a wrong sequence id,
// "hang" not at all; COM_PING with OK.
func stateServer(t *testing.T) Config {
	t.Helper()

	return newFakeServer(t, true, func(c *fakeConn, cmd byte, payload []byte) error {
		switch cmd {
		case comPing:
			return c.ok()
		case comStmtPrepare:
			return c.prepareOK(1, nil)
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

// Rows of a connection before Reconnect are refused, not read from the
// new one; its statements are prepared again.
func TestConnStale(t *testing.T) {
	t.Parallel()

	conn, err := Connect(stateServer(t))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = conn.Close() }()

	stmt, err := conn.Prepare("stmt")
	if err != nil {
		t.Fatal(err)
	}

	rows, err := conn.Query("rows")
	if err != nil {
		t.Fatal(err)
	}

	if err := conn.Reconnect(); err != nil {
		t.Fatal(err)
	}

	if rows.Next() || !errors.Is(rows.Err(), sql.ErrConnDone) {
		t.Errorf("old rows: %v", rows.Err())
	}

	if err := rows.Close(); !errors.Is(err, sql.ErrConnDone) {
		t.Errorf("old rows Close: %v", err)
	}

	if _, err := stmt.Exec(); err != nil {
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

func TestSessionCommands(t *testing.T) {
	t.Parallel()

	var sent []string

	cfg := newFakeServer(t, true, func(c *fakeConn, cmd byte, payload []byte) error {
		sent = append(sent, string(append([]byte{cmd}, payload...)))
		if string(payload) == "err" {
			return c.errPacket(1064, "42000", "syntax")
		}

		return c.ok()
	}).config()
	cfg.InitCommands = []string{"SET a", "SET b"}

	conn, err := Connect(cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = conn.Close() }()

	session := conn.session

	if e := conn.ResetConnection(); e != nil {
		t.Fatal(e)
	}

	want := []string{
		"\x03SET a", "\x03SET b",
		"\x1f", "\x03SET NAMES utf8mb4 COLLATE utf8mb4_general_ci", "\x03SET a", "\x03SET b",
	}
	if strings.Join(sent, "|") != strings.Join(want, "|") || conn.session != session+1 {
		t.Errorf("sent %q, session %d; want %q", sent, conn.session-session, want)
	}

	// A failing init command fails the connection.
	cfg.InitCommands = []string{"err"}
	if _, e := Connect(cfg); e == nil || !strings.Contains(e.Error(), `init command "err"`) {
		t.Errorf("failing init command: %v", e)
	}
}

func TestChangeUser(t *testing.T) {
	t.Parallel()

	var packets [][]byte

	cfg := newFakeServer(t, true, func(c *fakeConn, cmd byte, payload []byte) error {
		if cmd == comChangeUser {
			packets = append(packets, bytes.Clone(payload))

			if bytes.HasPrefix(payload, []byte("bad\x00")) {
				return c.errPacket(1045, "28000", "Access denied")
			}
		}

		return c.ok()
	}).config()
	cfg.InitCommands = []string{"SET a"}

	conn, err := Connect(cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = conn.Close() }()

	if e := conn.ChangeUser("other", "pw", "db2"); e != nil {
		t.Fatal(e)
	}

	// user NUL, a 20-byte scramble, database NUL, collation (2), plugin NUL
	p := packets[0]
	if !bytes.HasPrefix(p, []byte("other\x00\x14")) ||
		!bytes.Contains(p, []byte("db2\x00\x2d\x00mysql_native_password\x00")) {
		t.Errorf("COM_CHANGE_USER %q", p)
	}

	if conn.config.User != "other" || conn.config.Database != "db2" {
		t.Errorf("config %s %s", conn.config.User, conn.config.Database)
	}

	if e := conn.ChangeUser("bad", "pw", ""); !errors.Is(e, &Error{Number: 1045}) {
		t.Errorf("refused: %v", e)
	}

	if e := conn.Ping(); !errors.Is(e, ErrBadConn) {
		t.Errorf("Ping after a refused ChangeUser: %v", e)
	}

	if conn.config.User != "other" {
		t.Errorf("config after a refusal: %s", conn.config.User)
	}
}

func TestIsolationLevel(t *testing.T) {
	t.Parallel()

	for level, want := range map[sql.IsolationLevel]string{
		sql.LevelReadUncommitted: "READ UNCOMMITTED",
		sql.LevelReadCommitted:   "READ COMMITTED",
		sql.LevelRepeatableRead:  "REPEATABLE READ",
		sql.LevelSerializable:    "SERIALIZABLE",
		sql.LevelSnapshot:        "",
		sql.LevelLinearizable:    "",
	} {
		if got, err := isolationLevel(level); got != want || (err != nil) != (want == "") {
			t.Errorf("%v: %q, %v", level, got, err)
		}
	}
}

func TestTxMethods(t *testing.T) {
	t.Parallel()

	var sent []string

	conn, err := Connect(newFakeServer(t, true, func(c *fakeConn, cmd byte, payload []byte) error {
		sent = append(sent, string(payload))

		if cmd == comStmtPrepare {
			return c.prepareOK(1, nil)
		}

		if cmd == comQuery && string(payload) == "rows" {
			c.seq = 1
			out := c.eof(c.resultSet(nil, colString, []byte{1, '1'}), true)
			_, err := c.conn.Write(out)

			return err
		}

		return c.ok()
	}).config())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = conn.Close() }()

	ctx := context.Background()

	tx, err := conn.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelReadCommitted, ReadOnly: true})
	if err != nil {
		t.Fatal(err)
	}

	if _, e := tx.ExecContext(ctx, "x"); e != nil {
		t.Error(e)
	}

	var s string
	if e := tx.QueryRowContext(ctx, "rows").Scan(&s); e != nil || s != "1" {
		t.Errorf("QueryRowContext: %q, %v", s, e)
	}

	rows, err := tx.QueryContext(ctx, "rows")
	if err != nil || rows.Close() != nil {
		t.Errorf("QueryContext: %v", err)
	}

	if _, e := tx.PrepareContext(ctx, "p"); e != nil {
		t.Error(e)
	}

	if e := tx.Commit(); e != nil {
		t.Error(e)
	}

	if sent[0] != "SET TRANSACTION ISOLATION LEVEL READ COMMITTED" || sent[1] != "START TRANSACTION READ ONLY" {
		t.Errorf("sent %q", sent)
	}

	if _, e := conn.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelSnapshot}); e == nil {
		t.Error("snapshot: no error")
	}
}

// A reset whose SET NAMES or init commands fail leaves a session that is
// not the configured one: the connection is broken.
func TestResetConnectionFails(t *testing.T) {
	t.Parallel()

	for _, failing := range []string{"SET NAMES", "SET a"} {
		resets := 0

		cfg := newFakeServer(t, true, func(c *fakeConn, cmd byte, payload []byte) error {
			if cmd == comResetConnection {
				resets++
			}

			if resets > 0 && strings.HasPrefix(string(payload), failing) {
				return c.errPacket(1064, "42000", "syntax")
			}

			return c.ok()
		}).config()
		cfg.InitCommands = []string{"SET a"}

		conn, err := Connect(cfg)
		if err != nil {
			t.Fatal(err)
		}

		if e := conn.ResetConnection(); !errors.Is(e, &Error{Number: 1064}) {
			t.Errorf("%s fails: %v", failing, e)
		}

		if e := conn.Ping(); !errors.Is(e, ErrBadConn) {
			t.Errorf("%s fails, then Ping: %v", failing, e)
		}

		_ = conn.Close()
	}
}

// limitsResult answers limitsQuery with max_allowed_packet and defaults.
func (c *fakeConn) limitsResult(maxAllowedPacket int) error {
	cols := make([]fakeColumn, 6)
	for i := range cols {
		cols[i] = fakeColumn{"v", myTypeLongLong, 0}
	}

	var row []byte
	for _, v := range []string{strconv.Itoa(maxAllowedPacket), "16382", "151", "0", "28800", "0"} {
		row = appendLenEncString(row, v)
	}

	c.seq = 1
	_, err := c.conn.Write(c.eof(c.resultSet(nil, cols, row), true))

	return err
}

// loadServer answers LOAD statements with a request for a file, counts
// the bytes of the file and reports them as affected rows (mod 251, a
// one-byte length-encoded integer); the largest packet goes to *largest.
func loadServer(t *testing.T, maxAllowedPacket int, received, largest *int) Config {
	t.Helper()

	return newFakeServer(t, true, func(c *fakeConn, _ byte, payload []byte) error {
		if string(payload) == limitsQuery {
			return c.limitsResult(maxAllowedPacket)
		}

		if !strings.HasPrefix(string(payload), "LOAD") {
			return c.ok()
		}

		c.seq = 1
		if err := c.write(append([]byte{iLocalInfile}, "file"...)); err != nil {
			return err
		}

		for {
			p, err := c.read()
			if err != nil {
				return err
			}

			if len(p) == 0 {
				break
			}

			*received += len(p)
			*largest = max(*largest, len(p))
		}

		return c.write([]byte{iOK, byte(*received % 251), 0, 2, 0, 0, 0}) //nolint:gosec // G115: below 251
	}).config()
}

func TestLoadData(t *testing.T) {
	t.Parallel()

	var received, largest int

	conn, err := Connect(loadServer(t, 64<<20, &received, &largest))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = conn.Close() }()

	data := strings.Repeat("x", 3*loadDataChunk+5)

	res, err := conn.LoadData("LOAD", strings.NewReader(data))
	if err != nil || received != len(data) {
		t.Fatalf("%d of %d bytes, %v", received, len(data), err)
	}

	if n, _ := res.RowsAffected(); n != int64(len(data)%251) || largest != loadDataChunk {
		t.Errorf("affected %d, largest packet %d", n, largest)
	}

	// Without LoadData: an empty file, an error, the connection usable.
	received = 0

	if _, e := conn.Exec("LOAD"); !errors.Is(e, errLocalInfile) || received != 0 {
		t.Errorf("Exec of LOAD: %v, %d bytes", e, received)
	}

	if _, e := conn.LoadData("SELECT", strings.NewReader("x")); !errors.Is(e, errNoFile.error) {
		t.Errorf("no file asked for: %v", e)
	}

	if e := conn.Ping(); e != nil {
		t.Fatalf("Ping: %v", e)
	}

	// A failing reader closes the connection.
	errRead := errors.New("read failed")
	r := io.MultiReader(strings.NewReader("abc"), iotest.ErrReader(errRead))

	if _, e := conn.LoadData("LOAD", r); !errors.Is(e, errRead) || !errors.Is(e, ErrBadConn) {
		t.Errorf("failing reader: %v", e)
	}

	if e := conn.Ping(); !errors.Is(e, ErrBadConn) {
		t.Errorf("Ping after a failing reader: %v", e)
	}
}

// Packets of LoadData are below the server's max_allowed_packet.
func TestLoadDataPacketSize(t *testing.T) {
	t.Parallel()

	var received, largest int

	conn, err := Connect(loadServer(t, 2048, &received, &largest))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = conn.Close() }()

	if _, e := conn.LoadData("LOAD", strings.NewReader(strings.Repeat("x", 10000))); e != nil || received != 10000 {
		t.Fatalf("%d bytes, %v", received, e)
	}

	if largest != 2047 {
		t.Errorf("largest packet %d, want 2047", largest)
	}
}

// A server that refuses a packet sends ERR before it closes the connection:
// that error is returned, not the failed write.
func TestLoadDataServerError(t *testing.T) {
	t.Parallel()

	cfg := newFakeServer(t, true, func(c *fakeConn, _ byte, payload []byte) error {
		if string(payload) == limitsQuery {
			return c.limitsResult(64 << 20)
		}

		c.seq = 1
		if err := c.write(append([]byte{iLocalInfile}, "file"...)); err != nil {
			return err
		}

		if _, err := c.read(); err != nil {
			return err
		}

		_ = c.errPacket(1153, "08S01", "Got a packet bigger than 'max_allowed_packet' bytes")

		return io.EOF // closes the connection
	}).config()

	conn, err := Connect(cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = conn.Close() }()

	// Enough data that a write fails after the server is gone.
	_, err = conn.LoadData("LOAD", strings.NewReader(strings.Repeat("x", 64*loadDataChunk)))
	if !errors.Is(err, &Error{Number: 1153}) || !errors.Is(err, ErrBadConn) {
		t.Errorf("refused packet: %v", err)
	}
}

func TestClosesConnection(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		number uint16
		state  string
		closes bool
	}{
		{1153, "08S01", true},  // ER_NET_PACKET_TOO_LARGE
		{1053, "08S01", true},  // ER_SERVER_SHUTDOWN
		{1047, "08S01", false}, // ER_UNKNOWN_COM_ERROR
		{1064, "42000", false},
		{1317, "70100", false}, // ER_QUERY_INTERRUPTED
	} {
		e := &Error{Number: tc.number}
		copy(e.SQLState[:], tc.state)

		if got := closesConnection(e); got != tc.closes {
			t.Errorf("%d %s: %v", tc.number, tc.state, got)
		}
	}
}
