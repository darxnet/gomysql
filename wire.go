package gomysql

import (
	"bytes"
	"encoding/binary"
)

// appendLenEncInt appends Protocol::LengthEncodedInteger.
//
// https://dev.mysql.com/doc/dev/mysql-server/latest/page_protocol_basic_dt_integers.html
func appendLenEncInt(dst []byte, n uint64) []byte {
	le := binary.LittleEndian

	switch {
	case n < 251:
		return append(dst, byte(n))
	case n < 1<<16:
		return le.AppendUint16(append(dst, 0xfc), uint16(n)) //nolint:gosec // G115: n < 1<<16
	case n < 1<<24:
		return append(le.AppendUint16(append(dst, 0xfd), uint16(n)), byte(n>>16)) //nolint:gosec // G115: n < 1<<24
	default:
		return le.AppendUint64(append(dst, 0xfe), n)
	}
}

// lenDecInt reads Protocol::LengthEncodedInteger and returns it with the
// number of bytes it took.
func lenDecInt(b []byte) (n uint64, size int) {
	le := binary.LittleEndian

	switch b[0] {
	case 0xfc:
		return uint64(le.Uint16(b[1:3])), 3
	case 0xfd:
		return uint64(le.Uint16(b[1:3])) | uint64(b[3])<<16, 4
	case 0xfe:
		return le.Uint64(b[1:9]), 9 //nolint:gosec // G602: rows are bounds-checked by the row decoder of ROADMAP phase 6
	}

	return uint64(b[0]), 1
}

// isEOF reports whether buf ends rows or column definitions: an EOF packet,
// or an OK packet with the EOF header under CLIENT_DEPRECATE_EOF. A text
// row that starts with 0xfe (a string of 16 MB or more) is longer.
func isEOF(buf []byte) bool {
	return buf[0] == iEOF && len(buf) < maxPayloadSize
}

// appendLenEncString appends Protocol::LengthEncodedString.
func appendLenEncString(dst []byte, value string) []byte {
	dst = appendLenEncInt(dst, uint64(len(value)))
	return append(dst, value...)
}

func appendLenEncBytes(dst, value []byte) []byte {
	dst = appendLenEncInt(dst, uint64(len(value)))
	return append(dst, value...)
}

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

func (d *decoder) uint64() uint64 {
	if b := d.take(8); b != nil {
		return binary.LittleEndian.Uint64(b)
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
		return d.uint64()
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

// processOKPacket reads an OK packet, or an OK packet with the EOF header
// that ends rows under CLIENT_DEPRECATE_EOF.
//
// https://dev.mysql.com/doc/dev/mysql-server/latest/page_protocol_basic_ok_packet.html
func (conn *Conn) processOKPacket(buf []byte) error {
	d := decoder{buf: buf}
	d.skip(1) // header: 0x00, or 0xfe
	rowsAffected := d.lenEncInt()
	lastInsertID := d.lenEncInt()
	statusFlags := d.uint16()

	if err := d.err("OK packet"); err != nil {
		return err
	}

	conn.rowsAffected, conn.lastInsertId, conn.statusFlags = rowsAffected, lastInsertID, statusFlags

	return nil
}

// https://dev.mysql.com/doc/internals/en/packet-ERR_Packet.html
// https://dev.mysql.com/doc/dev/mysql-server/latest/page_protocol_basic_err_packet.html
// https://dev.mysql.com/doc/dev/mysql-server/latest/page_protocol_basic_expired_passwords.html not implemented
func (conn *Conn) processERRPacket(buf []byte) error {
	if len(buf) < 3 {
		return malformed("ERR packet")
	}

	e := &Error{Number: binary.LittleEndian.Uint16(buf[1:])}
	buf = buf[3:]

	// sql_state_marker '#' and the 5-byte SQL state; absent in errors sent
	// before the handshake, such as 1129 "Host blocked".
	if len(buf) >= 6 && buf[0] == '#' {
		copy(e.SQLState[:], buf[1:6])
		buf = buf[6:]
	}

	e.Message = string(buf)

	return e
}

// processEOFPacket reads the packet that ends column definitions or rows:
// an EOF packet, or under CLIENT_DEPRECATE_EOF an OK packet with the EOF
// header, whose status flags follow two length-encoded integers. Only the
// status flags are kept: they tell whether more results follow.
//
// https://dev.mysql.com/doc/dev/mysql-server/latest/page_protocol_basic_eof_packet.html
func (conn *Conn) processEOFPacket(buf []byte) error {
	d := decoder{buf: buf}
	d.skip(1) // header 0xfe

	if conn.capFlags&clientDeprecateEOF != 0 {
		d.lenEncInt() // affected rows
		d.lenEncInt() // last insert id
	} else {
		d.skip(2) // warnings
	}

	statusFlags := d.uint16()

	if err := d.err("EOF packet"); err != nil {
		return err
	}

	conn.statusFlags = statusFlags

	return nil
}
