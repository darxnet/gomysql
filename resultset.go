package gomysql

import (
	"bytes"
)

// ColumnDefinition describes a column of a result set or a parameter of
// a prepared statement (Protocol::ColumnDefinition41).
type ColumnDefinition struct {
	Catalog  []byte
	Schema   []byte
	Table    []byte
	OrgTable []byte
	Name     []byte
	OrgName  []byte

	CharSet  byte
	Len      uint32 // [4]byte
	Type     byte   // [1]byte
	Flags    uint16 // [2]byte
	Decimals byte   // [1]byte
}

// intLen is the size of an integer column in the binary protocol.
func (cd *ColumnDefinition) intLen() int {
	switch cd.Type {
	case myTypeLongLong:
		return 8
	case myTypeLong, myTypeInt24:
		return 4
	case myTypeShort, myTypeYear:
		return 2
	default: // myTypeTiny
		return 1
	}
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

// maxColumns bounds the column count of a result set; MySQL allows 4096
// columns, and COM_STMT_PREPARE_OK counts them in 2 bytes.
const maxColumns = 0xffff

// readUntilEOF skips rows, text or binary, up to the packet that ends
// them. A row never starts with 0xff, so that is an ERR packet.
func (conn *Conn) readUntilEOF() error {
	for {
		buf, err := conn.readPacket()
		if err != nil {
			return err
		}

		switch {
		case isEOF(buf): // EOF, or OK with the EOF header under CLIENT_DEPRECATE_EOF
			return conn.processEOFPacket(buf)
		case buf[0] == iERR:
			return conn.processERRPacket(buf)
		}
	}
}

// readMoreResults drains all result sets that follow the current one.
func (conn *Conn) readMoreResults() error {
	for conn.statusFlags&serverStatusMoreResultsExists != 0 {
		buf, err := conn.readPacket()
		if err != nil {
			return err
		}

		switch buf[0] {
		case iOK:
			if err := conn.processOKPacket(buf); err != nil {
				return err
			}
		case iERR:
			return conn.processERRPacket(buf)
		case iLocalInfile:
			return errLocalInfile
		default:
			if err := conn.skipColumns(buf); err != nil {
				return err
			}

			if err := conn.readUntilEOF(); err != nil {
				return err
			}
		}
	}

	return nil
}

// columnCount reads the column count that starts a result set.
func columnCount(buf []byte) (int, error) {
	d := decoder{buf: buf}
	n := d.lenEncInt()

	if d.bad || n == 0 || n > maxColumns {
		return 0, malformed("column count")
	}

	return int(n), nil
}
