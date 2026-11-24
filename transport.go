package gomysql

import (
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"os"
	"time"
)

// packetReader reads a byte stream through one buffer. Data that fits in
// the buffer is returned as a slice of it, without copying; the slice is
// valid until the next call.
type packetReader struct {
	rd   io.Reader
	buf  []byte // buffered data is buf[r:w]
	r, w int
}

func newPacketReader(rd io.Reader, size int) packetReader {
	return packetReader{rd: rd, buf: make([]byte, size)}
}

// reset switches to rd and drops buffered data.
func (p *packetReader) reset(rd io.Reader) {
	p.rd = rd
	p.r, p.w = 0, 0
}

// switchTo is reset for a protocol switch (TLS, compression), where the
// server sends nothing until the client speaks: buffered data means the
// stream is out of sync.
func (p *packetReader) switchTo(rd io.Reader) error {
	if p.r != p.w {
		return outOfSync("data before a protocol switch")
	}

	p.reset(rd)

	return nil
}

// next returns the next n bytes. A slice of the buffer has its capacity
// cut to n, so appending to it cannot overwrite the data that follows.
func (p *packetReader) next(n int) ([]byte, error) {
	if n > len(p.buf) {
		return p.nextLarge(n)
	}

	if err := p.fill(n); err != nil {
		return nil, err
	}

	b := p.buf[p.r : p.r+n : p.r+n]
	p.r += n

	return b, nil
}

// fill makes at least n bytes (n <= len(buf)) available in buf[r:w].
func (p *packetReader) fill(n int) error {
	if p.w-p.r >= n {
		return nil
	}

	if p.r > 0 {
		p.w = copy(p.buf, p.buf[p.r:p.w])
		p.r = 0
	}

	for p.w < n {
		m, err := p.rd.Read(p.buf[p.w:])
		p.w += m

		if p.w >= n {
			return nil
		}

		if err != nil {
			if errors.Is(err, io.EOF) && p.w > 0 {
				return io.ErrUnexpectedEOF
			}

			return err
		}
	}

	return nil
}

// nextLarge returns n bytes, more than the buffer holds, in a new slice.
func (p *packetReader) nextLarge(n int) ([]byte, error) {
	b := make([]byte, n)
	k := copy(b, p.buf[p.r:p.w])
	p.r, p.w = 0, 0

	if _, err := io.ReadFull(p.rd, b[k:]); err != nil {
		if errors.Is(err, io.EOF) {
			err = io.ErrUnexpectedEOF
		}

		return nil, err
	}

	return b, nil
}

// appendUint24 appends n as a 3-byte little-endian integer, the size field
// of packet headers.
func appendUint24(dst []byte, n int) []byte {
	dst = binary.LittleEndian.AppendUint32(dst, uint32(n)) //nolint:gosec // G115: packet sizes are below 1<<24

	return dst[:len(dst)-1]
}

// uint24 reads a 3-byte little-endian integer.
func uint24(b []byte) int {
	return int(b[0]) | int(b[1])<<8 | int(b[2])<<16
}

// readPacket returns the payload of the next packet, joining packets split
// at maxPayloadSize. It is valid until the next read.
//
// https://dev.mysql.com/doc/dev/mysql-server/latest/page_protocol_basic_packets.html
func (conn *Conn) readPacket() ([]byte, error) {
	var joined []byte

	for {
		h, err := conn.reader.next(headerSize)
		if err != nil {
			return nil, err
		}

		n, seq := uint24(h), h[3]

		// Under compression the server syncs the packet sequence id to the
		// frame sequence id on every flush, and neither MySQL nor MariaDB
		// checks it there (go-sql-driver does the same).
		if seq != conn.sequence && conn.compress == nil {
			return nil, conn.sequenceMismatch(seq, n)
		}

		conn.sequence = seq + 1

		payload, err := conn.reader.next(n)
		if err != nil {
			return nil, err
		}

		if n < maxPayloadSize {
			switch {
			case joined != nil:
				return append(joined, payload...), nil
			case n == 0:
				return nil, errPacketPayloadEmpty
			default:
				return payload, nil
			}
		}

		// A full packet is larger than the read buffer, so it is a new
		// slice from nextLarge and can be kept without a copy.
		if joined == nil {
			joined = payload
		} else {
			joined = append(joined, payload...)
		}
	}
}

// sequenceMismatch reads the payload of a packet with an unexpected
// sequence id: an ERR packet sent by the server on its own, such as 4031
// ER_CLIENT_INTERACTION_TIMEOUT after wait_timeout (MySQL 8.0.24+, seq 0),
// is returned as *Error, with ErrBadConn, rather than as a sequence
// mismatch. Either way the connection is unusable.
func (conn *Conn) sequenceMismatch(received byte, payloadLen int) error {
	errSeq := badSequence(conn.sequence, received)
	conn.breakWith(errSeq)

	if payloadLen == 0 || payloadLen >= maxPayloadSize {
		return errSeq
	}

	buf, err := conn.reader.next(payloadLen)
	if err != nil || buf[0] != iERR {
		return errSeq
	}

	return fmt.Errorf("%w: %w", conn.processERRPacket(buf), ErrBadConn)
}

// writeBufferKeep is the largest write buffer kept between writes.
const writeBufferKeep = 64 << 10

// appendPackets appends payload, made of an optional command byte and arg,
// as packets of at most maxPayloadSize bytes; a payload that fills the last
// packet is followed by an empty one.
func appendPackets[T string | []byte](dst []byte, seq *byte, com []byte, arg T) []byte {
	for {
		n := min(len(com)+len(arg), maxPayloadSize)
		dst = append(appendUint24(dst, n), *seq)
		*seq++

		dst = append(dst, com...)
		dst = append(dst, arg[:n-len(com)]...)
		arg = arg[n-len(com):]
		com = nil

		if n < maxPayloadSize {
			return dst
		}
	}
}

// writeCommand starts a command: it resets the sequence ids and sends com
// followed by arg.
func writeCommand[T string | []byte](conn *Conn, com byte, arg T) error {
	conn.sequence = 0
	if conn.compress != nil {
		conn.compress.seq = 0
	}

	return writePackets(conn, []byte{com}, arg)
}

// writePacket sends payload as the next packet of the current exchange.
func (conn *Conn) writePacket(payload []byte) error {
	return writePackets(conn, nil, payload)
}

func writePackets[T string | []byte](conn *Conn, com []byte, arg T) error {
	b := appendPackets(conn.wbuf[:0], &conn.sequence, com, arg)

	if cap(b) <= writeBufferKeep {
		conn.wbuf = b[:0]
	}

	var err error
	if conn.compress != nil {
		err = conn.writeCompressed(b)
	} else {
		_, err = conn.conn.Write(b)
	}

	if err != nil {
		return conn.errorAfterWrite(err)
	}

	return nil
}

// errorAfterWriteWait is how long errorAfterWrite waits for the error. The
// server sends it before it closes the connection, so it has arrived when
// the write fails: 4-65 µs on local servers.
const errorAfterWriteWait = 100 * time.Millisecond

// errorAfterWrite returns the error the server sent before it closed the
// connection, such as 1153 ER_NET_PACKET_TOO_LARGE for a packet larger
// than max_allowed_packet, or else err. Rarely the reset of the
// connection overtakes the error, and the kernel drops it: then err. Not after a deadline or a canceled
// context, which must not wait.
func (conn *Conn) errorAfterWrite(err error) error {
	if errors.Is(err, os.ErrDeadlineExceeded) || conn.ctx != nil && conn.ctx.Err() != nil {
		return err
	}

	_ = conn.conn.SetReadDeadline(time.Now().Add(errorAfterWriteWait))

	buf, rerr := conn.reader.next(headerSize)
	if rerr != nil {
		return err
	}

	payload, rerr := conn.reader.next(uint24(buf))
	if rerr != nil || len(payload) == 0 || payload[0] != iERR {
		return err
	}

	conn.breakWith(err)

	return fmt.Errorf("%w: %w", conn.processERRPacket(payload), ErrBadConn)
}
