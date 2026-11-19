package gomysql

import (
	"bytes"
	"compress/zlib"
	"errors"
	"io"
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

// reset switches to rd (after the TLS handshake or when compression starts)
// and drops buffered data, of which there is none at those points.
func (p *packetReader) reset(rd io.Reader) {
	p.rd = rd
	p.r, p.w = 0, 0
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

// compressedReader turns the compressed protocol back into the plain packet
// stream: each frame has a 7-byte header (compressed length, compressed
// sequence id, uncompressed length; 0 means the payload is not compressed).
//
// https://dev.mysql.com/doc/dev/mysql-server/latest/page_protocol_basic_compression.html
type compressedReader struct {
	zr  io.ReadCloser
	seq *byte
	out []byte // decoded data not yet read is out[pos:]
	dec []byte // reused buffer for decompressed frames
	br  bytes.Reader
	src packetReader
	pos int
}

func (c *compressedReader) Read(p []byte) (int, error) {
	for c.pos == len(c.out) {
		if err := c.readFrame(); err != nil {
			return 0, err
		}
	}

	n := copy(p, c.out[c.pos:])
	c.pos += n

	return n, nil
}

func (c *compressedReader) readFrame() error {
	h, err := c.src.next(headerCompressSize)
	if err != nil {
		return err
	}

	compressedLen := uint24(h)
	seq := h[3]
	plainLen := uint24(h[4:])

	if seq != *c.seq {
		return errPacketSequenceMismatch(*c.seq, seq)
	}

	*c.seq++

	payload, err := c.src.next(compressedLen)
	if err != nil {
		return err
	}

	c.pos = 0

	if plainLen == 0 {
		// payload stays valid: src is read again only when out is used up
		c.out = payload

		return nil
	}

	c.br.Reset(payload)

	if c.zr == nil {
		c.zr, err = zlib.NewReader(&c.br)
	} else {
		err = c.zr.(zlib.Resetter).Reset(&c.br, nil) //nolint:forcetypeassert // zlib readers implement Resetter
	}

	if err != nil {
		return malformed("compressed frame: " + err.Error())
	}

	if cap(c.dec) < plainLen {
		c.dec = make([]byte, plainLen)
	}

	c.out = c.dec[:plainLen]

	if _, err := io.ReadFull(c.zr, c.out); err != nil {
		return malformed("compressed frame: " + err.Error())
	}

	return nil
}

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
		if seq != conn.sequence {
			return nil, conn.sequenceMismatch(seq, n)
		}

		conn.sequence++

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

		joined = append(joined, payload...)
	}
}

// sequenceMismatch reads the payload of a packet with an unexpected
// sequence id: an ERR packet sent by the server on its own, such as 4031
// ER_CLIENT_INTERACTION_TIMEOUT after wait_timeout (MySQL 8.0.24+, seq 0),
// is returned as *Error rather than as a sequence mismatch. Either way the
// connection is unusable.
func (conn *Conn) sequenceMismatch(received byte, payloadLen int) error {
	errSeq := errPacketSequenceMismatch(conn.sequence, received)

	if payloadLen == 0 || payloadLen >= maxPayloadSize {
		return errSeq
	}

	buf, err := conn.reader.next(payloadLen)
	if err != nil || buf[0] != iERR {
		return errSeq
	}

	return conn.processERRPacket(buf)
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
	conn.sequenceCompressed = 0

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

	if conn.isCompress {
		return conn.writeCompressed(b)
	}

	_, err := conn.conn.Write(b)

	return err
}

// writeCompressed sends the packets in plain as compressed frames of at most
// maxPayloadSize uncompressed bytes. Frames under writerMinCompressSize
// bytes are sent uncompressed.
func (conn *Conn) writeCompressed(plain []byte) error {
	var out bytes.Buffer

	for {
		chunk := plain[:min(len(plain), maxPayloadSize)]
		plain = plain[len(chunk):]

		if len(chunk) < writerMinCompressSize {
			out.Write(appendUint24(nil, len(chunk)))
			out.WriteByte(conn.sequenceCompressed)
			out.Write([]byte{0, 0, 0})
			out.Write(chunk)
		} else {
			conn.zbuf.Reset()
			conn.zw.Reset(&conn.zbuf)

			if _, err := conn.zw.Write(chunk); err != nil {
				return err
			}

			if err := conn.zw.Close(); err != nil {
				return err
			}

			out.Write(appendUint24(nil, conn.zbuf.Len()))
			out.WriteByte(conn.sequenceCompressed)
			out.Write(appendUint24(nil, len(chunk)))
			out.Write(conn.zbuf.Bytes())
		}

		conn.sequenceCompressed++

		if len(plain) == 0 {
			break
		}
	}

	_, err := conn.conn.Write(out.Bytes())

	return err
}

// startCompression switches both directions to the compressed protocol,
// after the authentication succeeded.
func (conn *Conn) startCompression() {
	conn.isCompress = true
	conn.zw = zlib.NewWriter(nil)
	conn.reader.reset(&compressedReader{
		src: newPacketReader(conn.conn, readerBufferSize),
		seq: &conn.sequenceCompressed,
	})
}
