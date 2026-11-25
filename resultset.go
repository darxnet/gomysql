package gomysql

import (
	"bytes"
)

// columnDefinition describes a column of a result set or a parameter of
// a prepared statement (Protocol::ColumnDefinition41). Of its names only
// Name is kept: catalog, schema, tables and the original name are not
// used.
type columnDefinition struct {
	Name      []byte
	Len       uint32
	Collation uint16 // collationBinary for binary strings
	Flags     uint16
	Type      byte
	Decimals  byte
}

// skipNames skips the names before Name: catalog, schema, table and the
// original table.
func skipNames(d *decoder) {
	for range 4 {
		d.lenEncString()
	}
}

// parse reads Protocol::ColumnDefinition41 from buf. Name is appended to
// *names, which keeps it after buf is reused by the next read: the names
// of a result set share one slice.
func (column *columnDefinition) parse(buf []byte, names *[]byte) error {
	d := decoder{buf: buf}
	skipNames(&d)
	name := d.lenEncString()
	d.lenEncString() // the original name
	column.parseFixed(&d)

	if err := d.err("column definition"); err != nil {
		return err
	}

	start := len(*names)
	*names = append(*names, name...)
	column.Name = (*names)[start:len(*names):len(*names)]

	return nil
}

// parseFixed reads the fixed-length fields that follow the names.
func (column *columnDefinition) parseFixed(d *decoder) {
	d.skip(1) // length of the fixed-length fields, always 0x0c

	column.Collation = d.uint16()

	column.Len = d.uint32()
	column.Type = d.uint8()
	column.Flags = d.uint16()
	column.Decimals = d.uint8()
}

// update reads Protocol::ColumnDefinition41 from buf into a column read
// before. Unless the name changed, only the fixed-length fields are set, so
// the name is not copied again.
func (column *columnDefinition) update(buf []byte) error {
	d := decoder{buf: buf}
	skipNames(&d)
	same := bytes.Equal(d.lenEncString(), column.Name)
	d.lenEncString()

	if !same || d.bad {
		var name []byte

		return column.parse(buf, &name)
	}

	column.parseFixed(&d)

	return d.err("column definition")
}

// readColumns reads a column definition into each element of columns,
// followed by an EOF packet unless CLIENT_DEPRECATE_EOF is on. An empty
// columns reads nothing.
func (conn *Conn) readColumns(columns []columnDefinition) error {
	if len(columns) == 0 {
		return nil
	}

	// One slice for the names, with room for 16 bytes each; a longer one
	// grows it, and the names before keep the old array.
	names := make([]byte, 0, 16*len(columns))

	for i := range columns {
		buf, err := conn.readPacket()
		if err != nil {
			return err
		}

		if err := columns[i].parse(buf, &names); err != nil {
			return err
		}
	}

	return conn.readColumnsEOF()
}

// skipColumns reads n column definitions without keeping them.
func (conn *Conn) skipColumns(n int) error {
	for range n {
		buf, err := conn.readPacket()
		if err != nil {
			return err
		}

		// MySQL 8.0 before 8.0.38 could send fewer definitions than the
		// column count (go-sql-driver #1786).
		if isEOF(buf) || buf[0] == iERR {
			return malformed("fewer column definitions than columns")
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
		return unexpectedPacket(buf[0])
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

// readResultHeader reads the first packet of the response to a command
// that may return rows: OK, ERR, or the column count of a result set,
// which it returns; the column definitions follow. Affected rows and last
// insert id are those of an OK packet, 0 for a result set.
//
// https://dev.mysql.com/doc/dev/mysql-server/latest/page_protocol_com_query_response.html
func (conn *Conn) readResultHeader() (columns int, err error) {
	conn.rowsAffected, conn.lastInsertID = 0, 0

	buf, err := conn.readPacket()
	if err != nil {
		return 0, err
	}

	switch buf[0] {
	case iOK:
		return 0, conn.processOKPacket(buf)
	case iERR:
		return 0, conn.processERRPacket(buf)
	case iLocalInfile:
		return 0, conn.sendFile()
	default:
		return columnCount(buf)
	}
}

// skipResult reads the rest of a result whose header said columns, and
// the results that follow it.
func (conn *Conn) skipResult(columns int) error {
	if err := conn.skipResultSet(columns); err != nil {
		return err
	}

	return conn.readMoreResults()
}

// readMoreResults drains all result sets that follow the current one.
func (conn *Conn) readMoreResults() error {
	for conn.statusFlags&serverStatusMoreResultsExists != 0 {
		n, err := conn.readResultHeader()
		if err != nil {
			return err
		}

		if err := conn.skipResultSet(n); err != nil {
			return err
		}
	}

	return nil
}

// skipResultSet reads the column definitions and rows of a result set
// whose header said columns; nothing for 0, an OK packet.
func (conn *Conn) skipResultSet(columns int) error {
	if columns == 0 {
		return nil
	}

	if err := conn.skipColumns(columns); err != nil {
		return err
	}

	return conn.readUntilEOF()
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
