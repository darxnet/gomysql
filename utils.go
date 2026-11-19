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

// https://dev.mysql.com/doc/internals/en/com-query-response.html#packet-ProtocolText::ResultsetRow
func isNull(b byte) bool {
	return b == 0xfb
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

// lenDecString reads Protocol::LengthEncodedString; it returns the string
// (aliasing buf) and the number of bytes it took.
func lenDecString(buf []byte) (s []byte, size int) {
	n, pos := lenDecInt(buf)
	end := pos + int(n) //nolint:gosec // G115: rows are bounds-checked by the row decoder of ROADMAP phase 6

	return buf[pos:end], end
}

// parse reads Protocol::ColumnDefinition41 from buf. The names are copied
// into one new slice, since buf is reused by the next read.
func (column *ColumnDefinition) parse(buf []byte) error {
	d := decoder{buf: bytes.Clone(buf)}
	column.Catalog = d.lenEncString()
	column.Schema = d.lenEncString()
	column.Table = d.lenEncString()
	column.OrgTable = d.lenEncString()
	column.Name = d.lenEncString()
	column.OrgName = d.lenEncString()
	d.skip(1) // length of the fixed-length fields, always 0x0c

	// The character set has 2 bytes; the low one is enough for the known
	// ones.
	column.CharSet = d.uint8()
	d.skip(1)

	column.Len = d.uint32()
	column.Type = d.uint8()
	column.Flags = d.uint16()
	column.Decimals = d.uint8()

	return d.err("column definition")
}

// readColumns reads a column definition into each element of columns,
// followed by an EOF packet unless CLIENT_DEPRECATE_EOF is on. An empty
// columns reads nothing.
func (conn *Conn) readColumns(columns []ColumnDefinition) error {
	if len(columns) == 0 {
		return nil
	}

	for i := range columns {
		buf, err := conn.readPacket()
		if err != nil {
			return err
		}

		if err := columns[i].parse(buf); err != nil {
			return err
		}
	}

	return conn.readColumnsEOF()
}

// skipColumns reads the column definitions of a result set that starts
// with buf, the column count, without keeping them.
func (conn *Conn) skipColumns(buf []byte) error {
	n, err := columnCount(buf)
	if err != nil {
		return err
	}

	for range n {
		if _, err := conn.readPacket(); err != nil {
			return err
		}
	}

	return conn.readColumnsEOF()
}

// readColumnsEOF reads the EOF packet after column definitions, which
// CLIENT_DEPRECATE_EOF leaves out.
func (conn *Conn) readColumnsEOF() error {
	if conn.capFlags&clientDeprecateEOF != 0 {
		return nil
	}

	buf, err := conn.readPacket()
	if err != nil {
		return err
	}

	if !isEOF(buf) {
		return errUnexpectedPacketIdentity(buf[0])
	}

	return nil
}
