package gomysql

import (
	"bytes"
	"errors"
	"io"
	"testing"
	"testing/iotest"
)

func TestPacketReader(t *testing.T) {
	t.Parallel()

	data := []byte("abcdefghijklmnopqrstuvwxyz")

	// One byte per Read, buffer of 8: refills, moves and large reads.
	p := newPacketReader(iotest.OneByteReader(bytes.NewReader(data)), 8)

	var got []byte

	for _, n := range []int{3, 5, 8, 10} {
		b, err := p.next(n)
		if err != nil {
			t.Fatal(err)
		}

		if n <= 8 && cap(b) != n {
			t.Errorf("next(%d): cap %d, want %d so that append cannot overwrite the next data", n, cap(b), n)
		}

		got = append(got, b...)
	}

	if !bytes.Equal(got, data) {
		t.Errorf("got %q, want %q", got, data)
	}

	if _, err := p.next(1); !errors.Is(err, io.EOF) {
		t.Errorf("at the end: %v, want io.EOF", err)
	}
}

func TestPacketReaderTruncated(t *testing.T) {
	t.Parallel()

	for _, n := range []int{4, 20} { // buffered and large reads
		p := newPacketReader(bytes.NewReader([]byte("abc")), 8)
		if _, err := p.next(n); !errors.Is(err, io.ErrUnexpectedEOF) {
			t.Errorf("next(%d) of 3 bytes: %v, want io.ErrUnexpectedEOF", n, err)
		}
	}
}

func TestAppendPackets(t *testing.T) {
	t.Parallel()

	for _, size := range []int{0, 1, maxPayloadSize - 1, maxPayloadSize, maxPayloadSize + 1, 2 * maxPayloadSize} {
		arg := bytes.Repeat([]byte{'a'}, max(size-1, 0))

		var seq byte

		var stream []byte
		if size == 0 {
			stream = appendPackets(nil, &seq, nil, arg)
		} else {
			stream = appendPackets(nil, &seq, []byte{comQuery}, arg)
		}

		conn := &Conn{reader: newPacketReader(bytes.NewReader(stream), 64)}

		payload, err := conn.readPacket()
		if size == 0 {
			if !errors.Is(err, ErrBadConn) {
				t.Errorf("empty payload: %v", err)
			}

			continue
		}

		if err != nil || len(payload) != size || payload[0] != comQuery {
			t.Errorf("size %d: got %d bytes, err %v", size, len(payload), err)
		}

		if want := size/maxPayloadSize + 1; int(seq) != want || int(conn.sequence) != want {
			t.Errorf("size %d: %d packets written, %d read, want %d", size, seq, conn.sequence, want)
		}
	}
}
