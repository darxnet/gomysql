package gomysql

import (
	"errors"
	"net"
	"runtime"
	"testing"
)

func noCommands(c *fakeConn, _ byte, _ []byte) error {
	return c.errPacket(1047, "08S01", "unknown command")
}

// TestOpenMemory guards the per-connection buffers: the write buffer used to
// be 16 MB, allocated on every Open.
//
//nolint:paralleltest // MemStats counts process-wide allocations
func TestOpenMemory(t *testing.T) {
	srv := newFakeServer(t, true, noCommands)

	var before, after runtime.MemStats

	runtime.ReadMemStats(&before)

	conn, err := Open(srv.config())
	if err != nil {
		t.Fatal(err)
	}

	runtime.ReadMemStats(&after)

	_ = conn.Close()

	const limit = 256 << 10
	if n := after.TotalAlloc - before.TotalAlloc; n > limit {
		t.Errorf("Open allocated %d bytes, want at most %d", n, limit)
	}
}

func TestCloseHealthy(t *testing.T) {
	t.Parallel()

	conn, err := Open(newFakeServer(t, true, noCommands).config())
	if err != nil {
		t.Fatal(err)
	}

	if err := conn.Close(); err != nil {
		t.Errorf("Close on a healthy connection: %v", err)
	}
}

// TestReconnectClosesOldSocket checks that Conn closes the socket it
// replaces.
func TestReconnectClosesOldSocket(t *testing.T) {
	t.Parallel()

	srv := newFakeServer(t, true, noCommands)

	conn, err := Open(srv.config())
	if err != nil {
		t.Fatal(err)
	}

	old := conn.conn

	if err := conn.Conn(); err != nil {
		t.Fatal(err)
	}

	t.Cleanup(func() { _ = conn.Close() })

	if _, err := old.Write([]byte{0}); !errors.Is(err, net.ErrClosed) {
		t.Errorf("old socket: %v, want net.ErrClosed", err)
		_ = old.Close() // or the fake server waits for it forever
	}

	// noCommands answers every command with ERR 1047.
	if err := conn.Ping(); !errors.Is(err, &Error{Number: 1047}) {
		t.Errorf("Ping on the new socket: %v", err)
	}
}
