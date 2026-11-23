package gomysql

import (
	"context"
	"errors"
	"io"
	"os"
	"runtime"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// poolServer answers pings and queries with OK; a ping fails, closing the
// connection, while kill is set.
type poolServer struct {
	pings atomic.Int32
	kill  atomic.Bool
	conns sync.Map // *fakeConn
}

func (s *poolServer) handle(c *fakeConn, cmd byte, payload []byte) error {
	s.conns.Store(c, true)

	switch {
	case cmd == comPing:
		s.pings.Add(1)

		if s.kill.Load() {
			return io.EOF // closes the connection
		}
	case cmd == comQuery && string(payload) == "rows":
		c.seq = 1
		_, err := c.conn.Write(c.eof(c.resultSet(nil, colString, []byte{1, '1'}), true))

		return err
	}

	return c.ok()
}

func newTestDB(t *testing.T) (*DB, *poolServer) {
	t.Helper()

	srv := &poolServer{}

	db, err := Open(newFakeServer(t, true, srv.handle).config())
	if err != nil {
		t.Fatal(err)
	}

	t.Cleanup(func() { _ = db.Close() })

	return db, srv
}

func mustConn(t *testing.T, db *DB) *Conn {
	t.Helper()

	conn, err := db.Conn(context.Background())
	if err != nil {
		t.Fatal(err)
	}

	return conn
}

func (db *DB) counts() (open, idle int) {
	db.mu.Lock()
	defer db.mu.Unlock()

	return db.open, len(db.idle)
}

// age makes the idle connections idle for d longer.
func (db *DB) age(d time.Duration) {
	db.mu.Lock()
	defer db.mu.Unlock()

	for _, conn := range db.idle {
		conn.idleSince = conn.idleSince.Add(-d)
	}
}

func TestOpenDefaults(t *testing.T) {
	t.Parallel()

	db, err := Open(Config{})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = db.Close() }()

	db.mu.Lock()
	defer db.mu.Unlock()

	if db.maxOpenLocked() != max(4, runtime.GOMAXPROCS(0)) || db.idleTimeLocked() != 0 ||
		db.maxLifetime != 30*time.Minute {
		t.Errorf("max open %d, idle time %v, lifetime %v", db.maxOpenLocked(), db.idleTimeLocked(), db.maxLifetime)
	}

	// The account and the server lower them.
	db.accountMax, db.waitIdle = 2, time.Minute
	db.maxOpen, db.maxIdleTime = 0, time.Hour

	if db.maxOpenLocked() != 2 || db.idleTimeLocked() != time.Minute {
		t.Errorf("limited: max open %d, idle time %v", db.maxOpenLocked(), db.idleTimeLocked())
	}
}

func TestPoolSetters(t *testing.T) {
	t.Parallel()

	db, _ := newTestDB(t)

	a, b, c := mustConn(t, db), mustConn(t, db), mustConn(t, db)
	_, _ = a.Close(), b.Close()

	// Lower: the oldest idle connection beyond the limit closes.
	db.SetMaxOpenConns(2)

	if open, idle := db.counts(); open != 2 || idle != 1 || a.state != stateClosed || b.state != stateIdle {
		t.Errorf("after SetMaxOpenConns(2): open %d, idle %d", open, idle)
	}

	// Idle connections close first; c, in use, then fits.
	db.SetMaxOpenConns(1)
	_ = c.Close()

	if open, idle := db.counts(); open != 1 || idle != 1 || b.state != stateClosed || c.state != stateIdle {
		t.Errorf("after SetMaxOpenConns(1): open %d, idle %d", open, idle)
	}

	// A connection returned beyond the limit closes.
	db.SetMaxOpenConns(2)
	c = mustConn(t, db)
	other := mustConn(t, db)
	db.SetMaxOpenConns(1) // below the connections in use
	_ = other.Close()

	if open, _ := db.counts(); open != 1 || other.state != stateClosed {
		t.Errorf("beyond the limit: open %d, state %d", open, other.state)
	}

	_ = c.Close()

	// Higher: a waiter gets a place.
	b = mustConn(t, db)
	db.SetMaxOpenConns(1)
	got := make(chan *Conn)

	go func() {
		conn, err := db.Conn(context.Background())
		if err != nil {
			t.Error(err)
		}

		got <- conn
	}()

	time.Sleep(10 * time.Millisecond)
	db.SetMaxOpenConns(0)

	if conn := <-got; conn == b {
		t.Error("the waiter got the connection in use")
	} else {
		_ = conn.Close()
	}

	_ = b.Close()

	// Fewer idle: the oldest close, and returned ones beyond.
	x, y := mustConn(t, db), mustConn(t, db)
	_ = x.Close()
	db.SetMaxIdleConns(1)
	_ = y.Close()

	if open, idle := db.counts(); open != 1 || idle != 1 || y.state != stateClosed {
		t.Errorf("after SetMaxIdleConns(1): open %d, idle %d", open, idle)
	}

	db.SetMaxIdleConns(0)

	if open, idle := db.counts(); open != 0 || idle != 0 || x.state != stateClosed {
		t.Errorf("after SetMaxIdleConns(0): open %d, idle %d", open, idle)
	}

	db.SetMaxIdleConns(10)
	_ = mustConn(t, db).Close()

	db.SetConnMaxIdleTime(time.Minute)
	db.age(time.Minute)
	db.SetConnMaxIdleTime(time.Minute) // closes now

	if open, idle := db.counts(); open != 0 || idle != 0 {
		t.Errorf("after SetConnMaxIdleTime: open %d, idle %d", open, idle)
	}
}

func TestPoolReuse(t *testing.T) {
	t.Parallel()

	db, srv := newTestDB(t)

	first := mustConn(t, db)
	if err := first.Close(); err != nil {
		t.Fatal(err)
	}

	if err := first.Close(); err != nil { // again: returned once
		t.Fatal(err)
	}

	if open, idle := db.counts(); open != 1 || idle != 1 {
		t.Fatalf("after Close: open %d, idle %d", open, idle)
	}

	// Idle less than a second: taken without a ping.
	again := mustConn(t, db)
	if again != first || srv.pings.Load() != 0 {
		t.Errorf("reuse: same %v, pings %d", again == first, srv.pings.Load())
	}

	_ = again.Close()

	// Idle longer: pinged.
	db.age(2 * time.Second)

	if again = mustConn(t, db); again != first || srv.pings.Load() != 1 {
		t.Errorf("after a pause: same %v, pings %d", again == first, srv.pings.Load())
	}

	_ = again.Close()

	// The server closed it meanwhile: replaced by a new connection.
	db.age(2 * time.Second)
	srv.kill.Store(true)

	again = mustConn(t, db)
	if again == first || first.state != stateClosed {
		t.Errorf("a dead connection was handed out")
	}

	if open, _ := db.counts(); open != 1 {
		t.Errorf("open %d after the replacement, want 1", open)
	}

	_ = again.Close()
}

func TestPoolWait(t *testing.T) {
	t.Parallel()

	db, _ := newTestDB(t)
	db.SetMaxOpenConns(1)

	first := mustConn(t, db)

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()

	if _, err := db.Conn(ctx); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("a full pool: %v, want a deadline", err)
	}

	got := make(chan *Conn)

	go func() {
		conn, err := db.Conn(context.Background())
		if err != nil {
			t.Error(err)
		}

		got <- conn
	}()

	time.Sleep(10 * time.Millisecond)
	_ = first.Close()

	conn := <-got
	if conn != first {
		t.Error("the waiter did not get the returned connection")
	}

	_ = conn.Close()
}

// Connections that cannot serve another user as they are close instead.
func TestPoolDiscard(t *testing.T) {
	t.Parallel()

	db, _ := newTestDB(t)

	for name, spoil := range map[string]func(*Conn){
		"rows": func(c *Conn) { _, _ = c.Query("rows") }, //nolint:gocritic // the rows stay open
		"transaction": func(c *Conn) {
			c.statusFlags |= serverStatusInTrans
		},
		"database": func(c *Conn) { _ = c.UseDatabase("other") },
		"user":     func(c *Conn) { c.config.User = "other" },
		"broken":   func(c *Conn) { c.breakWith(ErrBadConn) },
		"old":      func(c *Conn) { c.opened = c.opened.Add(-time.Hour) },
	} {
		conn := mustConn(t, db)
		spoil(conn)

		if err := conn.Close(); err != nil {
			t.Errorf("%s: Close: %v", name, err)
		}

		if open, idle := db.counts(); open != 0 || idle != 0 || conn.state != stateClosed {
			t.Errorf("%s: open %d, idle %d, state %d", name, open, idle, conn.state)
		}
	}
}

func TestPoolHijack(t *testing.T) {
	t.Parallel()

	db, _ := newTestDB(t)
	db.SetMaxOpenConns(1)

	own := mustConn(t, db)
	own.Hijack()

	other := mustConn(t, db) // the place is free
	_ = other.Close()

	if err := own.Close(); err != nil || own.state != stateClosed {
		t.Errorf("Close of a hijacked connection: %v, state %d", err, own.state)
	}

	if open, idle := db.counts(); open != 1 || idle != 1 {
		t.Errorf("open %d, idle %d", open, idle)
	}
}

func TestPoolCloseStale(t *testing.T) {
	t.Parallel()

	db, _ := newTestDB(t)
	db.SetConnMaxIdleTime(time.Minute)
	db.SetConnMaxLifetime(time.Hour)

	idle, old, fresh := mustConn(t, db), mustConn(t, db), mustConn(t, db)
	old.opened = old.opened.Add(-time.Hour + 30*time.Second)
	old.lifetimeShare = 1

	for _, conn := range []*Conn{idle, old, fresh} {
		_ = conn.Close()
	}

	db.mu.Lock()
	idle.idleSince = idle.idleSince.Add(-time.Minute)
	db.mu.Unlock()

	db.closeStale(time.Now().Add(31 * time.Second))

	if open, n := db.counts(); open != 1 || n != 1 || idle.state != stateClosed || old.state != stateClosed ||
		fresh.state != stateIdle {
		t.Errorf("open %d, idle %d; states idle %d, old %d, fresh %d", open, n, idle.state, old.state, fresh.state)
	}
}

func TestPoolLifetimeJitter(t *testing.T) {
	t.Parallel()

	db, _ := newTestDB(t)

	for range 20 {
		conn := mustConn(t, db)

		if conn.lifetimeShare > 1 || conn.lifetimeShare < 0.9 {
			t.Errorf("lifetime share %v, want 0.9..1", conn.lifetimeShare)
		}

		conn.Hijack()
		_ = conn.Close()
	}
}

func TestDBClose(t *testing.T) {
	t.Parallel()

	db, _ := newTestDB(t)
	db.SetMaxOpenConns(2)

	idle, used := mustConn(t, db), mustConn(t, db)

	waiting := make(chan error)

	go func() {
		_, err := db.Conn(context.Background())
		waiting <- err
	}()

	time.Sleep(10 * time.Millisecond)

	if err := db.Close(); err != nil {
		t.Fatal(err)
	}

	if err := <-waiting; !errors.Is(err, errDBClosed) {
		t.Errorf("a waiter: %v", err)
	}

	if _, err := db.Conn(context.Background()); !errors.Is(err, errDBClosed) {
		t.Errorf("Conn after Close: %v", err)
	}

	for _, conn := range []*Conn{idle, used} {
		if err := conn.Close(); err != nil || conn.state != stateClosed {
			t.Errorf("returned after Close: %v, state %d", err, conn.state)
		}
	}

	if open, _ := db.counts(); open != 0 {
		t.Errorf("open %d", open)
	}
}

func TestDBCloseIdle(t *testing.T) {
	t.Parallel()

	db, _ := newTestDB(t)

	idle := mustConn(t, db)
	_ = idle.Close()

	if err := db.Close(); err != nil {
		t.Fatal(err)
	}

	if open, n := db.counts(); open != 0 || n != 0 || idle.state != stateClosed {
		t.Errorf("open %d, idle %d, state %d", open, n, idle.state)
	}
}

// The account allows 7 connections (TestConformanceServerLimits): the
// pool keeps poolReserve of them free.
func TestPoolLimitsLive(t *testing.T) {
	t.Parallel()

	addr := os.Getenv("GOMYSQL_ADDR")
	if addr == "" {
		t.Skip("GOMYSQL_ADDR not set")
	}

	db, err := Open(Config{
		Network: "tcp", Address: addr, User: "lim", Password: "lim", AllowPublicKeyRetrieval: true,
		InitCommands: []string{"SET SESSION wait_timeout = 100"},
	})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = db.Close() }()

	db.SetMaxOpenConns(50)

	conn := mustConn(t, db)
	defer func() { _ = conn.Close() }()

	db.mu.Lock()
	maxOpen, idleTime := db.maxOpenLocked(), db.idleTimeLocked()
	db.mu.Unlock()

	// wait_timeout 100 s: max(100/2, 100 − 65) = 50 s.
	if maxOpen != 7-poolReserve || idleTime != 50*time.Second {
		t.Errorf("max open %d, idle time %v; want %d and 50s", maxOpen, idleTime, 7-poolReserve)
	}

	second := mustConn(t, db)
	defer func() { _ = second.Close() }()

	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()

	if _, err := db.Conn(ctx); !errors.Is(err, context.DeadlineExceeded) {
		t.Errorf("beyond the limit: %v", err)
	}
}

// Many goroutines share a few connections: every query runs, and no more
// than MaxConns are open.
func TestPoolConcurrentLive(t *testing.T) {
	t.Parallel()

	addr := os.Getenv("GOMYSQL_ADDR")
	if addr == "" {
		t.Skip("GOMYSQL_ADDR not set")
	}

	db, err := Open(Config{
		Network: "tcp", Address: addr, User: "b", Password: "b", AllowPublicKeyRetrieval: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = db.Close() }()

	db.SetMaxOpenConns(4)

	var wg sync.WaitGroup

	for g := range 50 {
		wg.Add(1)

		go func() {
			defer wg.Done()

			for i := range 20 {
				conn, err := db.Conn(context.Background())
				if err != nil {
					t.Error(err)

					return
				}

				var got int
				if err := conn.QueryRow("SELECT ?", g*100+i).Scan(&got); err != nil || got != g*100+i {
					t.Errorf("%d, %v; want %d", got, err, g*100+i)
				}

				if open, _ := db.counts(); open > 4 {
					t.Errorf("%d connections open", open)
				}

				_ = conn.Close()
			}
		}()
	}

	wg.Wait()
}
