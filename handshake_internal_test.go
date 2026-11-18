package gomysql

import (
	"errors"
	"io"
	"testing"
	"time"
)

// expectClosed fails unless the client closes the connection without sending
// anything more.
func expectClosed(t *testing.T, c *fakeConn) error {
	t.Helper()

	_ = c.conn.SetReadDeadline(time.Now().Add(2 * time.Second))

	p, err := c.read()

	switch {
	case err == nil:
		t.Errorf("client sent %q instead of closing", p)
	case !errors.Is(err, io.EOF):
		t.Errorf("client did not close the connection: %v", err)
	}

	return nil
}

func TestHandshakeRejectsOldServers(t *testing.T) {
	t.Parallel()

	cases := map[string][]byte{
		"protocol v9":     append([]byte{9}, "3.23.58\x00\x01\x00\x00\x00abcdefgh\x00"...),
		"no protocol 4.1": handshakeV10(fakeCapabilities&^clientProtocol41, "mysql_native_password"),
	}

	for name, hs := range cases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			cfg := rawServer(t, func(c *fakeConn) error {
				if err := c.write(hs); err != nil {
					return err
				}

				return expectClosed(t, c)
			})

			conn, err := Open(cfg)
			if !errors.Is(err, errServerTooOld) || conn != nil {
				t.Errorf("Open: %v, %v; want nil, %v", conn, err, errServerTooOld)
			}
		})
	}
}

func TestAuthRefusesOldPassword(t *testing.T) {
	t.Parallel()

	cases := map[string][]byte{
		"OldAuthSwitchRequest": {iEOF},
		"AuthSwitchRequest":    append([]byte{iEOF}, "mysql_old_password\x00abcdefgh\x00"...),
	}

	for name, sw := range cases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			cfg := rawServer(t, func(c *fakeConn) error {
				if err := c.write(handshakeV10(fakeCapabilities, "mysql_native_password")); err != nil {
					return err
				}

				if _, err := c.read(); err != nil {
					return err
				}

				if err := c.write(sw); err != nil {
					return err
				}

				return expectClosed(t, c)
			})

			conn, err := Open(cfg)
			if !errors.Is(err, errOldPassword) || conn != nil {
				t.Errorf("Open: %v, %v; want nil, %v", conn, err, errOldPassword)
			}
		})
	}
}

// An error sent instead of the handshake has no SQL state (MariaDB 1129).
func TestHandshakeError(t *testing.T) {
	t.Parallel()

	cfg := rawServer(t, func(c *fakeConn) error {
		return c.write(append([]byte{iERR, 0x69, 0x04}, "Host 'x' is blocked"...))
	})

	_, err := Open(cfg)
	if err == nil || err.Error() != "ERROR 1129 (): Host 'x' is blocked" {
		t.Errorf("Open: %v", err)
	}
}

// The plugin name and data must survive the next packet overwriting the
// read buffer: caching_sha2_password uses them after more round trips.
func TestHandshakeV10Copies(t *testing.T) {
	t.Parallel()

	buf := handshakeV10(fakeCapabilities, "caching_sha2_password")

	var conn Conn

	name, data, err := conn.handshakeV10(buf)
	if err != nil {
		t.Fatal(err)
	}

	clear(buf)

	if string(name) != "caching_sha2_password" || string(data) != "abcdefghijklmnopqrst\x00" {
		t.Errorf("after reuse of the buffer: name %q, data %q", name, data)
	}

	if conn.server != "5.7.35-fake" || conn.id != 1 || conn.capFlags != fakeCapabilities {
		t.Errorf("server %q, id %d, caps %#x", conn.server, conn.id, conn.capFlags)
	}
}

func TestHandshakeV10Malformed(t *testing.T) {
	t.Parallel()

	full := handshakeV10(fakeCapabilities, "mysql_native_password")

	for _, n := range []int{1, 5, 12, 20, 30, 40} {
		var conn Conn
		if _, _, err := conn.handshakeV10(full[:n]); !errors.Is(err, errMalformedHandshake) {
			t.Errorf("%d bytes: %v", n, err)
		}
	}
}
