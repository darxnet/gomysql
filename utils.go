package gomysql

import (
	"encoding/binary"
	"errors"
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
		return le.Uint64(b[1:9]), 9 //nolint:gosec // G602: bounds checks are ROADMAP phase 4
	}

	return uint64(b[0]), 1
}

// https://dev.mysql.com/doc/internals/en/com-query-response.html#packet-ProtocolText::ResultsetRow
func isNUll(b byte) bool {
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
	end := pos + int(n) //nolint:gosec // G115: lengths come from the server; bounds checks are ROADMAP phase 4

	return buf[pos:end], end
}

// parse reads Protocol::ColumnDefinition41 from buf. Name is copied; the
// other names alias buf.
func (column *ColumnDefinition) parse(buf []byte) {
	var pos, n int

	names := []*[]byte{
		&column.Catalog, &column.Schema, &column.Table,
		&column.OrgTable, &column.Name, &column.OrgName,
	}

	for _, field := range names {
		*field, n = lenDecString(buf[pos:])
		pos += n
	}

	column.Name = append([]byte(nil), column.Name...)

	pos++ // length of the fixed-length fields, always 0x0c

	column.CharSet = buf[pos] // character set (2 bytes); the low byte is enough for the known ones
	pos += 2
	column.Len = binary.LittleEndian.Uint32(buf[pos:])
	pos += 4
	column.Type = buf[pos]
	pos++
	column.Flags = binary.LittleEndian.Uint16(buf[pos:])
	pos += 2
	column.Decimals = buf[pos]
}

var errExpectedEOF = errors.New("protocol error: expected EOF_Packet")

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

		columns[i].parse(buf)
	}

	if conn.capFlags&clientDeprecateEOF != 0 {
		return nil
	}

	buf, err := conn.readPacket()
	if err != nil {
		return err
	}

	if buf[0] != iEOF {
		return errExpectedEOF
	}

	return nil
}
