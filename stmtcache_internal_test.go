package gomysql

import (
	"database/sql"
	"encoding/binary"
	"errors"
	"fmt"
	"slices"
	"sync"
	"testing"
)

// stmtServer prepares any text, under ids counted from 1; it answers 1243
// to an execute of an id in forget, once.
type stmtServer struct {
	mu     sync.Mutex
	nextID uint32
	forget map[uint32]bool
	log    []string // "prepare <text>", "execute <id>", "close <id>"
	max    int      // statements the server holds, as max_prepared_stmt_count; 0 for any
	live   map[uint32]bool
}

func (s *stmtServer) handle(c *fakeConn, cmd byte, payload []byte) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	switch cmd {
	case comStmtPrepare:
		s.log = append(s.log, "prepare "+string(payload))

		if s.max > 0 && len(s.live) >= s.max {
			return c.errPacket(erMaxPreparedStmtCountReached, "42000", "too many statements")
		}

		s.nextID++
		s.live[s.nextID] = true

		return c.prepareOK(s.nextID, nil)
	case comStmtExecute:
		id := binary.LittleEndian.Uint32(payload)
		s.log = append(s.log, fmt.Sprint("execute ", id))

		if s.forget[id] {
			delete(s.forget, id)

			return c.errPacket(erUnknownStmtHandler, "HY000", "Unknown prepared statement handler")
		}
	case comStmtClose:
		id := binary.LittleEndian.Uint32(payload)
		s.log = append(s.log, fmt.Sprint("close ", id))
		delete(s.live, id)

		return nil // no response
	}

	return c.ok()
}

// take returns the log since the last take, after a Ping, so that a
// COM_STMT_CLOSE, which has no response, is in it.
func (s *stmtServer) take(t *testing.T, conn *Conn) []string {
	t.Helper()

	if err := conn.Ping(); err != nil {
		t.Fatal(err)
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	log := s.log
	s.log = nil

	return log
}

func TestStmtCache(t *testing.T) {
	t.Parallel()

	srv := &stmtServer{forget: map[uint32]bool{}, live: map[uint32]bool{}}

	conn, err := Connect(newFakeServer(t, true, srv.handle).config())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = conn.Close() }()

	expect := func(what string, want ...string) {
		t.Helper()

		if got := srv.take(t, conn); !slices.Equal(got, want) {
			t.Errorf("%s: sent %q, want %q", what, got, want)
		}
	}

	a, err := conn.Prepare("a")
	if err != nil {
		t.Fatal(err)
	}

	again, err := conn.Prepare("a")
	if err != nil || again != a {
		t.Fatalf("Prepare of the same text: %p, %v; want %p", again, err, a)
	}

	if err := a.Close(); err != nil {
		t.Fatal(err)
	}

	if _, err := a.Exec(); err != nil {
		t.Fatal(err)
	}

	expect("Prepare twice, Close, Exec", "prepare a", "execute 1")

	// The server lost the statement: prepared again, executed once more.
	srv.mu.Lock()
	srv.forget[1] = true
	srv.mu.Unlock()

	if _, err := a.Exec(); err != nil {
		t.Fatalf("Exec after 1243: %v", err)
	}

	expect("Exec, 1243", "execute 1", "prepare a", "execute 2")

	srv.mu.Lock()
	srv.forget[2] = true
	srv.mu.Unlock()

	if err := a.QueryRow().Scan(); !errors.Is(err, sql.ErrNoRows) {
		t.Errorf("QueryRow after 1243: %v", err)
	}

	expect("Query, 1243", "execute 2", "prepare a", "execute 3")

	// Reconnect deallocates everything: prepared again when executed.
	if err := conn.Reconnect(); err != nil {
		t.Fatal(err)
	}

	if _, err := a.Exec(); err != nil {
		t.Fatalf("Exec after Reconnect: %v", err)
	}

	expect("Reconnect", "prepare a", "execute 4")

	// Beyond the limit the least recently used is deallocated.
	conn.stmtCache().limit = minStmtCache

	want := make([]string, 0, minStmtCache+1)

	for i := range minStmtCache {
		q := fmt.Sprint("q", i)
		if _, err := conn.Prepare(q); err != nil {
			t.Fatal(err)
		}

		want = append(want, "prepare "+q)
	}

	want = append(want, "close 4") // a, the least recently used

	expect("17 statements", want...)

	if _, err := conn.Prepare("q0"); err != nil {
		t.Fatal(err)
	}

	if _, err := a.Exec(); err != nil {
		t.Fatalf("Exec of an evicted statement: %v", err)
	}

	// q0 was used last: q1 is evicted, not q0.
	expect("evicted", "prepare a", "close 6", fmt.Sprint("execute ", minStmtCache+5))

	if got := len(conn.stmts.m); got != minStmtCache {
		t.Errorf("cached %d, want %d", got, minStmtCache)
	}

	// Statements keep their database: UseDatabase deallocates them all.
	if err := conn.UseDatabase("other"); err != nil {
		t.Fatal(err)
	}

	// From the least recently used: q2, ..., q0, a.
	got := srv.take(t, conn)
	if len(got) != minStmtCache || got[0] != "close 7" || got[len(got)-1] != fmt.Sprint("close ", minStmtCache+5) {
		t.Errorf("UseDatabase: sent %q, want %d closes from the least recently used", got, minStmtCache)
	}

	if _, err := a.Exec(); err != nil {
		t.Fatalf("Exec after UseDatabase: %v", err)
	}

	expect("after UseDatabase", "prepare a", fmt.Sprint("execute ", minStmtCache+6))
}

// Prepare of a cached text asks nothing and allocates nothing.
func TestStmtCacheAllocs(t *testing.T) { //nolint:paralleltest // AllocsPerRun counts process-wide
	srv := &stmtServer{forget: map[uint32]bool{}, live: map[uint32]bool{}}

	conn, err := Connect(newFakeServer(t, true, srv.handle).config())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = conn.Close() }()

	if _, err := conn.Prepare("a"); err != nil {
		t.Fatal(err)
	}

	if n := testing.AllocsPerRun(100, func() { _, _ = conn.Prepare("a") }); n != 0 {
		t.Errorf("Prepare of a cached text: %v allocations", n)
	}
}

// When the server holds max_prepared_stmt_count statements, half of the
// cache is deallocated, and the cache stays that small.
func TestStmtCacheServerFull(t *testing.T) {
	t.Parallel()

	srv := &stmtServer{forget: map[uint32]bool{}, live: map[uint32]bool{}, max: 4}

	conn, err := Connect(newFakeServer(t, true, srv.handle).config())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = conn.Close() }()

	for _, q := range []string{"a", "b", "c", "d", "e", "f"} {
		if _, err := conn.Prepare(q); err != nil {
			t.Fatalf("Prepare %s: %v", q, err)
		}
	}

	want := []string{
		"prepare a", "prepare b", "prepare c", "prepare d",
		"prepare e", "close 1", "close 2", "prepare e", "close 3", // 1461: a and b, then c beyond the new limit 2
		"prepare f", "close 4",
	}

	if got := srv.take(t, conn); !slices.Equal(got, want) {
		t.Errorf("sent %q, want %q", got, want)
	}

	if got := len(conn.stmts.m); got != 2 {
		t.Errorf("cached %d, want 2", got)
	}

	// Without statements of its own to deallocate the error stands.
	srv.mu.Lock()
	srv.max = 2
	srv.live[100], srv.live[101] = true, true
	srv.mu.Unlock()

	if err := conn.UseDatabase("x"); err != nil {
		t.Fatal(err)
	}

	var serverErr *Error
	if _, err := conn.Prepare("g"); !errors.As(err, &serverErr) || serverErr.Number != erMaxPreparedStmtCountReached {
		t.Errorf("Prepare with an empty cache: %v, want 1461", err)
	}
}
