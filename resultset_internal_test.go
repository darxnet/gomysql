package gomysql

import (
	"bytes"
	"errors"
	"testing"
)

func TestReadMoreResults(t *testing.T) {
	t.Parallel()

	stream := appendPacket(nil, 0, []byte{iEOF, 0, 0, byte(serverStatusMoreResultsExists), 0})
	stream = appendPacket(stream, 1, []byte{iOK, 0, 0, 0, 0, 0, 0})

	conn := &Conn{
		capFlags: clientProtocol41,
		reader:   newPacketReader(bytes.NewReader(stream), 64),
	}
	if err := conn.readUntilEOF(); err != nil {
		t.Fatal(err)
	}
	if err := conn.readMoreResults(); err != nil {
		t.Fatal(err)
	}
	if conn.statusFlags&serverStatusMoreResultsExists != 0 {
		t.Fatal("final result still has SERVER_MORE_RESULTS_EXISTS")
	}
}

func appendPacket(dst []byte, sequence byte, payload []byte) []byte {
	dst = append(appendUint24(dst, len(payload)), sequence)
	return append(dst, payload...)
}

func TestProcessResultStatusFlags(t *testing.T) {
	t.Parallel()

	conn := &Conn{capFlags: clientProtocol41}
	if err := conn.processOKPacket([]byte{iOK, 0, 0, byte(serverStatusMoreResultsExists), 0, 0, 0}); err != nil {
		t.Fatal(err)
	}
	if conn.statusFlags&serverStatusMoreResultsExists == 0 {
		t.Fatal("OK packet did not set SERVER_MORE_RESULTS_EXISTS")
	}

	if err := conn.processEOFPacket([]byte{iEOF, 0, 0, 0, 0}); err != nil {
		t.Fatal(err)
	}
	if conn.statusFlags&serverStatusMoreResultsExists != 0 {
		t.Fatal("EOF packet did not clear SERVER_MORE_RESULTS_EXISTS")
	}
}

// TestExecSkipsTextRows checks that Exec reads past the rows of a SELECT,
// whatever byte they start with, and returns an ERR that ends them.
func TestExecSkipsTextRows(t *testing.T) {
	t.Parallel()

	for _, deprecateEOF := range []bool{false, true} {
		srv := newFakeServer(t, deprecateEOF, func(c *fakeConn, cmd byte, query []byte) error {
			if cmd == comPing {
				return c.ok()
			}

			out := c.frame(nil, []byte{1})
			out = c.frame(out, fakeColumn{name: "s", typ: myTypeVarString}.definition())
			out = c.eof(out, false)

			for _, row := range [][]byte{{3, 'a', 'b', 'c'}, {0}, {0xfb}, {0xfc, 1, 0, 'x'}} {
				out = c.frame(out, row)
			}

			if _, err := c.conn.Write(out); err != nil {
				return err
			}

			if string(query) == "fail" {
				return c.errPacket(1317, "70100", "Query execution was interrupted")
			}

			_, err := c.conn.Write(c.eof(nil, true))

			return err
		})

		conn, err := Connect(srv.config())
		if err != nil {
			t.Fatal(err)
		}

		if _, err := conn.Exec("select"); err != nil {
			t.Errorf("deprecateEOF %v: Exec: %v", deprecateEOF, err)
		}

		if _, err := conn.Exec("fail"); !errors.Is(err, &Error{Number: 1317}) {
			t.Errorf("deprecateEOF %v: Exec with ERR among rows: %v", deprecateEOF, err)
		}

		if err := conn.Ping(); err != nil {
			t.Errorf("deprecateEOF %v: Ping after Exec: %v", deprecateEOF, err)
		}

		_ = conn.Close()
	}
}
