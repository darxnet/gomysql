package gomysql

import (
	"errors"
	"fmt"
	"testing"
)

func TestErrorFormat(t *testing.T) {
	t.Parallel()

	e := &Error{
		Number:   1062,
		SQLState: [5]byte{'2', '3', '0', '0', '0'},
		Message:  "Duplicate entry '1' for key 'PRIMARY'",
	}
	if got := e.Error(); got != "Error 1062 (23000): Duplicate entry '1' for key 'PRIMARY'" {
		t.Errorf("Error() = %q", got)
	}

	wrapped := fmt.Errorf("insert: %w", e)
	if !errors.Is(wrapped, &Error{Number: 1062}) {
		t.Error("errors.Is does not match by Number")
	}

	if errors.Is(wrapped, &Error{Number: 1045}) {
		t.Error("errors.Is matches another Number")
	}
}

func TestProcessERRPacket(t *testing.T) {
	t.Parallel()

	var conn Conn

	err := conn.processERRPacket(append([]byte{iERR, 0x15, 0x04, '#', '2', '8', '0', '0', '0'}, "Access denied"...))

	var e *Error
	if !errors.As(err, &e) || e.Number != 1045 || string(e.SQLState[:]) != "28000" || e.Message != "Access denied" {
		t.Errorf("got %#v", err)
	}

	if err := conn.processERRPacket([]byte{iERR, 0x15}); !errors.Is(err, ErrMalformedPacket) {
		t.Errorf("short ERR packet: %v", err)
	}
}

// A reply with an unexpected sequence id is ErrOutOfSync.
func TestOutOfSync(t *testing.T) {
	t.Parallel()

	srv := newFakeServer(t, true, func(c *fakeConn, _ byte, _ []byte) error {
		c.seq = 5

		return c.ok()
	})

	conn, err := Open(srv.config())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = conn.Close() }()

	if err := conn.Ping(); !errors.Is(err, ErrOutOfSync) {
		t.Errorf("Ping: %v, want %v", err, ErrOutOfSync)
	}
}
