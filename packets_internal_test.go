package gomysql

import (
	"bytes"
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
	conn.processOKPacket([]byte{iOK, 0, 0, byte(serverStatusMoreResultsExists), 0, 0, 0})
	if conn.statusFlags&serverStatusMoreResultsExists == 0 {
		t.Fatal("OK packet did not set SERVER_MORE_RESULTS_EXISTS")
	}

	conn.processEOFPacket([]byte{iEOF, 0, 0, 0, 0})
	if conn.statusFlags&serverStatusMoreResultsExists != 0 {
		t.Fatal("EOF packet did not clear SERVER_MORE_RESULTS_EXISTS")
	}
}
