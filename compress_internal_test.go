package gomysql

import (
	"bytes"
	"compress/zlib"
	"crypto/rand"
	"errors"
	"io"
	"net"
	"strconv"
	"testing"
)

// captureConn is a net.Conn that keeps what is written to it.
type captureConn struct {
	net.Conn

	buf bytes.Buffer
}

func (c *captureConn) Write(p []byte) (int, error) { return c.buf.Write(p) }

// frames splits a compressed stream into frame headers, checking that each
// frame holds what its header says.
func frames(t *testing.T, stream []byte) (headers [][headerCompressSize]byte) {
	t.Helper()

	for len(stream) > 0 {
		if len(stream) < headerCompressSize {
			t.Fatalf("%d bytes after the last frame", len(stream))
		}

		var h [headerCompressSize]byte
		copy(h[:], stream)
		headers = append(headers, h)

		n := uint24(h[:])
		if n > len(stream)-headerCompressSize {
			t.Fatalf("frame %d: length %d, %d bytes left", len(headers), n, len(stream)-headerCompressSize)
		}

		if plainLen := uint24(h[4:]); plainLen > maxPayloadSize {
			t.Fatalf("frame %d: uncompressed length %d", len(headers), plainLen)
		}

		stream = stream[headerCompressSize+n:]
	}

	return headers
}

// TestCompressRoundTrip writes a command through writeCompressed and reads
// it back through compressedReader, at the sizes where frames and packets
// stop lining up.
func TestCompressRoundTrip(t *testing.T) {
	t.Parallel()

	random := make([]byte, 20<<20)
	_, _ = rand.Read(random)

	compressible := bytes.Repeat([]byte("gomysql "), 20<<20/8)

	cases := map[string][]byte{
		"tiny":                      []byte("SELECT 1"),
		"small":                     compressible[:1000],
		"incompressible":            random[:1000],
		"one packet, two frames":    compressible[:maxPayloadSize-3], // with its header
		"exactly one packet":        compressible[:maxPayloadSize-1], // and an empty one
		"incompressible 16MB":       random[:maxPayloadSize],
		"incompressible 20MB":       random,
		"compressible 20MB":         compressible,
		"incompressible tail frame": append(append([]byte(nil), compressible[:maxPayloadSize]...), random[:100]...),
	}

	for name, arg := range cases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			out := &captureConn{}
			client := &Conn{conn: out, compress: &compression{zw: zlib.NewWriter(nil)}}

			if err := writeCommand(client, comQuery, arg); err != nil {
				t.Fatal(err)
			}

			headers := frames(t, out.buf.Bytes())
			for i, h := range headers {
				if int(h[3]) != i%256 {
					t.Fatalf("frame %d has sequence id %d", i, h[3])
				}
			}

			if int(client.sequence) != len(headers)%256 {
				t.Errorf("packet sequence %d after %d frames, want them in step", client.sequence, len(headers))
			}

			server := &Conn{compress: &compression{}}
			server.reader = newPacketReader(&compressedReader{
				src:      newPacketReader(&out.buf, readerBufferSize),
				compress: server.compress,
			}, readerBufferSize)

			got, err := server.readPacket()
			if err != nil {
				t.Fatal(err)
			}

			if got[0] != comQuery || !bytes.Equal(got[1:], arg) {
				t.Fatalf("read back %d bytes, want %d", len(got)-1, len(arg))
			}

			if _, err := server.readPacket(); !errors.Is(err, io.EOF) {
				t.Errorf("after the command: %v, want io.EOF", err)
			}
		})
	}
}

// TestCompressedReaderRejectsBadFrames checks that a frame whose data does
// not match its header is ErrBadConn.
func TestCompressedReaderRejectsBadFrames(t *testing.T) {
	t.Parallel()

	out := &captureConn{}
	client := &Conn{conn: out, compress: &compression{zw: zlib.NewWriter(nil)}}

	if err := writeCommand(client, comQuery, bytes.Repeat([]byte("x"), 1000)); err != nil {
		t.Fatal(err)
	}

	good := out.buf.Bytes()

	corrupt := map[string]func(b []byte){
		"checksum":       func(b []byte) { b[len(b)-1] ^= 0xff },
		"length too big": func(b []byte) { putUint24(b[4:], uint24(b[4:])+1) },
		"length too low": func(b []byte) { putUint24(b[4:], uint24(b[4:])-1) },
		"not zlib":       func(b []byte) { b[headerCompressSize] ^= 0xff },
	}

	for name, change := range corrupt {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			b := bytes.Clone(good)
			change(b)

			r := &compressedReader{src: newPacketReader(bytes.NewReader(b), readerBufferSize), compress: &compression{}}

			_, err := io.ReadAll(r)
			if !errors.Is(err, ErrBadConn) {
				t.Errorf("%v, want ErrBadConn", err)
			}
		})
	}
}

func BenchmarkWriteCompressed(b *testing.B) {
	for _, n := range []int{100, 64 << 10} {
		b.Run(strconv.Itoa(n), func(b *testing.B) {
			arg := bytes.Repeat([]byte("gomysql "), n/8)
			conn := &Conn{conn: &captureConn{}, compress: &compression{zw: zlib.NewWriter(nil)}}

			b.ReportAllocs()

			for b.Loop() {
				conn.conn.(*captureConn).buf.Reset() //nolint:forcetypeassert // set above
				if err := writeCommand(conn, comQuery, arg); err != nil {
					b.Fatal(err)
				}
			}
		})
	}
}
