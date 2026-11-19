package gomysql

import (
	"bytes"
	"encoding/binary"
)

// decoder reads the fields of a packet payload in order. A read past the
// end returns zero values and marks the decoder bad, so a parser reads all
// its fields and checks err once at the end.
type decoder struct {
	buf []byte
	bad bool
}

// take returns the next n bytes, or nil past the end.
func (d *decoder) take(n int) []byte {
	if n < 0 || n > len(d.buf) {
		d.bad = true
		d.buf = nil

		return nil
	}

	b := d.buf[:n:n]
	d.buf = d.buf[n:]

	return b
}

func (d *decoder) skip(n int) {
	d.take(n)
}

func (d *decoder) uint8() byte {
	if b := d.take(1); b != nil {
		return b[0]
	}

	return 0
}

func (d *decoder) uint16() uint16 {
	if b := d.take(2); b != nil {
		return binary.LittleEndian.Uint16(b)
	}

	return 0
}

func (d *decoder) uint32() uint32 {
	if b := d.take(4); b != nil {
		return binary.LittleEndian.Uint32(b)
	}

	return 0
}

// lenEncInt reads Protocol::LengthEncodedInteger.
//
// https://dev.mysql.com/doc/dev/mysql-server/latest/page_protocol_basic_dt_integers.html
func (d *decoder) lenEncInt() uint64 {
	switch first := d.uint8(); first {
	case 0xfc:
		return uint64(d.uint16())
	case 0xfd:
		return uint64(d.uint16()) | uint64(d.uint8())<<16
	case 0xfe:
		if b := d.take(8); b != nil {
			return binary.LittleEndian.Uint64(b)
		}

		return 0
	case 0xfb, 0xff: // NULL in a row, and an ERR packet header
		d.bad = true
		return 0
	default:
		return uint64(first)
	}
}

// lenEncString reads Protocol::LengthEncodedString; the result aliases the
// payload.
func (d *decoder) lenEncString() []byte {
	n := d.lenEncInt()
	if n > uint64(len(d.buf)) {
		d.bad = true
		d.buf = nil

		return nil
	}

	return d.take(int(n)) //nolint:gosec // G115: n <= len(d.buf)
}

// nulString reads Protocol::NullTerminatedString without the NUL; the
// result aliases the payload.
func (d *decoder) nulString() []byte {
	end := bytes.IndexByte(d.buf, 0x00)
	if end < 0 {
		d.bad = true
		d.buf = nil

		return nil
	}

	s := d.take(end)
	d.skip(1)

	return s
}

// rest returns the unread bytes.
func (d *decoder) rest() []byte {
	return d.take(len(d.buf))
}

// err returns a malformed packet error naming what was parsed, if a read
// went past the end.
func (d *decoder) err(what string) error {
	if d.bad {
		return malformed(what)
	}

	return nil
}
