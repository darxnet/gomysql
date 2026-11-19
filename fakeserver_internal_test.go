package gomysql

import (
	"bufio"
	"encoding/binary"
	"errors"
	"io"
	"net"
	"sync"
	"testing"
)

// fakeServer speaks enough of the server side of the protocol to test the
// client without MySQL: a v10 handshake that accepts any password, then each
// command goes to handle. It listens on loopback TCP, so tests use the public
// Open.
type fakeServer struct {
	ln     net.Listener
	handle fakeHandler
	wg     sync.WaitGroup

	// deprecateEOF offers CLIENT_DEPRECATE_EOF (MySQL 5.7.5+).
	deprecateEOF bool
}

type fakeHandler func(c *fakeConn, cmd byte, payload []byte) error

func newFakeServer(tb testing.TB, deprecateEOF bool, handle fakeHandler) *fakeServer {
	tb.Helper()

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		tb.Fatal(err)
	}

	s := &fakeServer{ln: ln, handle: handle, deprecateEOF: deprecateEOF}

	s.wg.Add(1)

	go s.serve(tb)

	tb.Cleanup(func() {
		_ = ln.Close()
		s.wg.Wait()
	})

	return s
}

func (s *fakeServer) config() Config {
	return Config{Protocol: "tcp", Address: s.ln.Addr().String(), User: "u", Password: "p", DbName: "db"}
}

func (s *fakeServer) serve(tb testing.TB) {
	tb.Helper()

	defer s.wg.Done()

	for {
		nc, err := s.ln.Accept()
		if err != nil {
			return
		}

		s.wg.Add(1)

		go func() {
			defer s.wg.Done()
			defer func() { _ = nc.Close() }()

			c := &fakeConn{conn: nc, r: bufio.NewReader(nc), deprecateEOF: s.deprecateEOF}
			if err := c.session(s.handle); err != nil && !errors.Is(err, io.EOF) && !errors.Is(err, net.ErrClosed) {
				tb.Errorf("fake server: %v", err)
			}
		}()
	}
}

type fakeConn struct {
	conn         net.Conn
	r            *bufio.Reader
	buf          []byte // reused payload buffer: the read path does not allocate
	seq          byte
	deprecateEOF bool
}

const fakeCapabilities = clientLongPassword | clientConnectWithDB | clientProtocol41 |
	clientSecureConn | clientPluginAuth | clientTransactions

func (c *fakeConn) session(handle fakeHandler) error {
	caps := fakeCapabilities
	if c.deprecateEOF {
		caps |= clientDeprecateEOF
	}

	hs := handshakeV10(caps, "mysql_native_password")

	if err := c.write(hs); err != nil {
		return err
	}

	if _, err := c.read(); err != nil { // HandshakeResponse41, any password
		return err
	}

	if err := c.ok(); err != nil {
		return err
	}

	for {
		c.seq = 0

		p, err := c.read()
		if err != nil {
			return err
		}

		if p[0] == comQuit {
			return nil
		}

		if err := handle(c, p[0], p[1:]); err != nil {
			return err
		}
	}
}

// handshakeV10 builds Protocol::HandshakeV10 with a 20-byte nonce.
func handshakeV10(caps uint32, plugin string) []byte {
	hs := []byte{serverProtocolV10}
	hs = append(hs, "5.7.35-fake\x00"...)
	hs = binary.LittleEndian.AppendUint32(hs, 1)                // connection id
	hs = append(hs, "abcdefgh"...)                              // auth-plugin-data-part-1
	hs = append(hs, 0)                                          // filler
	hs = binary.LittleEndian.AppendUint16(hs, uint16(caps))     //nolint:gosec // capability flags, lower
	hs = append(hs, CharSet_utf8mb4_bin)                        // character set
	hs = binary.LittleEndian.AppendUint16(hs, 2)                // status: autocommit
	hs = binary.LittleEndian.AppendUint16(hs, uint16(caps>>16)) // capability flags, upper
	hs = append(hs, 21)                                         // auth-plugin-data length
	hs = append(hs, make([]byte, 10)...)                        // reserved
	hs = append(hs, "ijklmnopqrst\x00"...)                      // auth-plugin-data-part-2

	return append(hs, plugin+"\x00"...)
}

// rawServer accepts one connection and runs script on it, for tests of the
// connection phase; the script writes the handshake itself.
func rawServer(tb testing.TB, script func(c *fakeConn) error) Config {
	tb.Helper()

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		tb.Fatal(err)
	}

	done := make(chan struct{})

	go func() {
		defer close(done)

		nc, err := ln.Accept()
		if err != nil {
			return
		}
		defer func() { _ = nc.Close() }()

		if err := script(&fakeConn{conn: nc, r: bufio.NewReader(nc)}); err != nil && !errors.Is(err, io.EOF) {
			tb.Errorf("raw server: %v", err)
		}
	}()

	tb.Cleanup(func() {
		_ = ln.Close()
		<-done
	})

	return Config{Protocol: "tcp", Address: ln.Addr().String(), User: "u", Password: "p", DbName: "db"}
}

// read returns the next payload, joining packets split at 16 MB; it is
// valid until the next read.
func (c *fakeConn) read() ([]byte, error) {
	c.buf = c.buf[:0]

	for {
		var h [4]byte
		if _, err := io.ReadFull(c.r, h[:]); err != nil {
			return nil, err
		}

		n := int(h[0]) | int(h[1])<<8 | int(h[2])<<16
		c.seq = h[3] + 1

		start := len(c.buf)
		c.buf = append(c.buf, make([]byte, n)...)

		if _, err := io.ReadFull(c.r, c.buf[start:]); err != nil {
			return nil, err
		}

		if n < maxPayloadSize {
			return c.buf, nil
		}
	}
}

func (c *fakeConn) write(payload []byte) error {
	_, err := c.conn.Write(c.frame(nil, payload))
	return err
}

// frame appends payload to dst as packets, splitting at 16 MB, and advances
// the sequence id.
func (c *fakeConn) frame(dst, payload []byte) []byte {
	for {
		n := min(len(payload), maxPayloadSize)
		dst = append(appendUint24(dst, n), c.seq)
		dst = append(dst, payload[:n]...)
		c.seq++
		payload = payload[n:]

		if n < maxPayloadSize {
			return dst
		}
	}
}

func (c *fakeConn) ok() error {
	return c.write([]byte{iOK, 0, 0, 2, 0, 0, 0})
}

// eof is the end of column definitions or rows: an EOF packet, or with
// CLIENT_DEPRECATE_EOF an OK packet with the EOF header (rows only).
func (c *fakeConn) eof(dst []byte, rows bool) []byte {
	if c.deprecateEOF {
		if !rows {
			return dst
		}

		return c.frame(dst, []byte{iEOF, 0, 0, 2, 0, 0, 0})
	}

	return c.frame(dst, []byte{iEOF, 0, 0, 2, 0})
}

func (c *fakeConn) errPacket(code uint16, state, msg string) error {
	p := []byte{iERR}
	p = binary.LittleEndian.AppendUint16(p, code)
	p = append(p, '#')
	p = append(p, state...)
	p = append(p, msg...)

	return c.write(p)
}

type fakeColumn struct {
	name  string
	typ   byte
	flags uint16
}

// definition encodes Protocol::ColumnDefinition41.
func (col fakeColumn) definition() []byte {
	var p []byte
	for _, s := range []string{"def", "db", "t", "t", col.name, col.name} {
		p = appendLenEncString(p, s)
	}

	p = append(p, 0x0c)
	p = binary.LittleEndian.AppendUint16(p, 63) // binary
	p = binary.LittleEndian.AppendUint32(p, 20)
	p = append(p, col.typ)
	p = binary.LittleEndian.AppendUint16(p, col.flags)
	p = append(p, 0, 0, 0) // decimals, filler

	return p
}

// prepareOK answers COM_STMT_PREPARE for a statement without parameters.
func (c *fakeConn) prepareOK(id uint32, cols []fakeColumn) error {
	p := []byte{iOK}
	p = binary.LittleEndian.AppendUint32(p, id)
	p = binary.LittleEndian.AppendUint16(p, uint16(len(cols))) //nolint:gosec // test data
	p = append(p, 0, 0, 0, 0, 0)                               // params, filler, warnings

	out := c.frame(nil, p)
	for _, col := range cols {
		out = c.frame(out, col.definition())
	}

	if len(cols) > 0 {
		out = c.eof(out, false)
	}

	_, err := c.conn.Write(out)

	return err
}

// binaryResult builds the whole COM_STMT_EXECUTE response: column count,
// definitions, rows encoded by row(i, dst) and the terminator. The sequence
// ids start at 1, so it can be built once and replayed for every execute.
func (c *fakeConn) binaryResult(cols []fakeColumn, n int, row func(i int, dst []byte) []byte) []byte {
	c.seq = 1
	out := c.frame(nil, appendLenEncInt(nil, uint64(len(cols))))

	for _, col := range cols {
		out = c.frame(out, col.definition())
	}

	out = c.eof(out, false)

	bitmap := (len(cols) + 7 + 2) / 8

	var p []byte
	for i := range n {
		p = append(p[:0], iOK)
		p = append(p, make([]byte, bitmap)...)
		p = row(i, p)
		out = c.frame(out, p)
	}

	return c.eof(out, true)
}
