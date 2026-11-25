package gomysql

import (
	"bytes"
	"compress/zlib"
	"errors"
	"io"
)

// compression is the state of the compressed protocol (CLIENT_COMPRESS):
// packets travel inside frames with a 7-byte header (compressed length,
// frame sequence id, uncompressed length, 0 when the payload is sent as
// is). One sequence id counts the frames of both directions.
//
// https://dev.mysql.com/doc/dev/mysql-server/latest/page_protocol_basic_compression.html
type compression struct {
	zw  *zlib.Writer
	out appendWriter // reused output buffer; a field, so zlib does not move it to the heap
	seq byte
}

// startCompression switches both directions to the compressed protocol,
// after the authentication succeeded.
func (conn *Conn) startCompression() error {
	conn.compress = &compression{zw: zlib.NewWriter(nil)}

	return conn.reader.switchTo(&compressedReader{
		src:      newPacketReader(conn.conn, readerBufferSize),
		compress: conn.compress,
	})
}

// writeCompressed sends the packets in plain as frames of at most
// maxPayloadSize uncompressed bytes. A frame is sent as is when it is under
// writerMinCompressSize bytes or does not get smaller. zlib writes straight
// into the output buffer after a reserved header, which is filled in once
// the length is known.
func (conn *Conn) writeCompressed(plain []byte) error {
	c := conn.compress
	w := &c.out
	w.b = w.b[:0]

	for {
		chunk := plain[:min(len(plain), maxPayloadSize)]
		plain = plain[len(chunk):]

		start := len(w.b)
		w.b = append(w.b, make([]byte, headerCompressSize)...)
		plainLen := 0

		if len(chunk) >= writerMinCompressSize {
			if err := c.deflate(chunk); err != nil {
				return err
			}

			plainLen = len(chunk)
		}

		// Incompressible data grows; sending it as is also keeps the
		// compressed length below 1<<24.
		if plainLen == 0 || len(w.b)-start-headerCompressSize >= len(chunk) {
			w.b = append(w.b[:start+headerCompressSize], chunk...)
			plainLen = 0
		}

		h := w.b[start : start+headerCompressSize]
		putUint24(h, len(w.b)-start-headerCompressSize)
		h[3] = c.seq
		putUint24(h[4:], plainLen)
		c.seq++

		if len(plain) == 0 {
			break
		}
	}

	_, err := conn.conn.Write(w.b)

	if cap(w.b) > writeBufferKeep {
		w.b = nil
	}

	// The server answers with packet sequence ids that continue from the
	// frame sequence id (net_flush in MySQL and MariaDB).
	conn.sequence = c.seq

	return err
}

func (c *compression) deflate(chunk []byte) error {
	c.zw.Reset(&c.out)

	if _, err := c.zw.Write(chunk); err != nil {
		return err
	}

	return c.zw.Close()
}

// appendWriter is an io.Writer that appends to b.
type appendWriter struct {
	b []byte
}

func (w *appendWriter) Write(p []byte) (int, error) {
	w.b = append(w.b, p...)

	return len(p), nil
}

// zlibReader is what zlib.NewReader returns.
type zlibReader interface {
	io.ReadCloser
	zlib.Resetter
}

// compressedReader turns the frames back into the plain packet stream.
type compressedReader struct {
	zr       zlibReader
	compress *compression
	out      []byte // decoded data not yet read is out[pos:]
	dec      []byte // reused buffer for decompressed frames
	br       bytes.Reader
	src      packetReader
	pos      int
	probe    [1]byte // checks that a frame ends where its header says
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
	if cap(c.dec) > writeBufferKeep {
		c.dec = nil // the previous frame was large and is used up
	}

	h, err := c.src.next(headerCompressSize)
	if err != nil {
		return err
	}

	compressedLen, plainLen := uint24(h), uint24(h[4:])

	// Not checked, as in MySQL, MariaDB and go-sql-driver: the next frame
	// we send continues from the last one received.
	c.compress.seq = h[3] + 1

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

	return c.inflate(payload, plainLen)
}

// inflate decodes a frame into dec and checks that it decodes to exactly
// plainLen bytes, which also verifies the zlib checksum.
func (c *compressedReader) inflate(payload []byte, plainLen int) error {
	c.br.Reset(payload)

	var err error
	if c.zr == nil {
		var r io.ReadCloser

		r, err = zlib.NewReader(&c.br)
		if err == nil {
			c.zr = r.(zlibReader) //nolint:forcetypeassert // documented: the reader implements Resetter
		}
	} else {
		err = c.zr.Reset(&c.br, nil)
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

	if n, err := c.zr.Read(c.probe[:]); n != 0 || !errors.Is(err, io.EOF) {
		return malformed("compressed frame longer than its header says, or a bad checksum")
	}

	return nil
}

// putUint24 writes n as a 3-byte little-endian integer into b[:3].
func putUint24(b []byte, n int) {
	b[0], b[1], b[2] = byte(n), byte(n>>8), byte(n>>16) //nolint:gosec // G115: packet sizes are below 1<<24
}
