package gomysql

import (
	"bytes"
	"strconv"
	"testing"
)

// Payload sizes around the 16 MB packet limit: a payload of exactly
// maxPayloadSize (or a multiple) is followed by an empty packet.
var largeSizes = []int{
	1000,
	maxPayloadSize - 1,
	maxPayloadSize,
	maxPayloadSize + 1,
	2 * maxPayloadSize,
	2*maxPayloadSize + 100,
}

// TestLargeResponse reads a row whose packet has each of largeSizes bytes.
func TestLargeResponse(t *testing.T) {
	t.Parallel()

	for _, size := range largeSizes {
		t.Run(strconv.Itoa(size), func(t *testing.T) {
			t.Parallel()

			// A text row with one column: a length-encoded string; the
			// 0xfd length prefix takes 4 bytes below 1<<24, 0xfe 9 above.
			prefix := 4
			if size-4 >= 1<<24 {
				prefix = 9
			}

			value := bytes.Repeat([]byte{'x'}, size-prefix)
			value[len(value)-1] = 'y'

			srv := newFakeServer(t, true, func(c *fakeConn, _ byte, _ []byte) error {
				c.seq = 1
				out := c.frame(nil, []byte{1})
				out = c.frame(out, fakeColumn{"v", myTypeBlob, 0}.definition())
				out = c.frame(out, appendLenEncBytes(nil, value))
				out = c.eof(out, true)

				_, err := c.conn.Write(out)

				return err
			})

			conn, err := Connect(srv.config())
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = conn.Close() }()

			var got []byte

			if err := conn.QueryRow("SELECT v").Scan(&got); err != nil {
				t.Fatal(err)
			}

			if !bytes.Equal(got, value) {
				t.Errorf("got %d bytes, want %d", len(got), len(value))
			}
		})
	}
}

// TestLargeRequest sends a command whose payload has each of largeSizes
// bytes.
func TestLargeRequest(t *testing.T) {
	t.Parallel()

	for _, size := range largeSizes {
		t.Run(strconv.Itoa(size), func(t *testing.T) {
			t.Parallel()

			query := bytes.Repeat([]byte{'q'}, size-1) // the command byte is the first
			query[len(query)-1] = 'z'

			var received int

			srv := newFakeServer(t, true, func(c *fakeConn, cmd byte, p []byte) error {
				if cmd == comQuery && bytes.Equal(p, query) {
					received = len(p) + 1
				}

				return c.ok()
			})

			conn, err := Connect(srv.config())
			if err != nil {
				t.Fatal(err)
			}

			if _, err := conn.Exec(string(query)); err != nil {
				t.Fatal(err)
			}

			_ = conn.Close()

			if received != size {
				t.Errorf("server received %d bytes, want %d", received, size)
			}
		})
	}
}
