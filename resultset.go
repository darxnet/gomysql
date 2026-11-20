package gomysql

import (
	"bytes"
)

// columnDefinition describes a column of a result set or a parameter of
// a prepared statement (Protocol::ColumnDefinition41).
type columnDefinition struct {
	Catalog  []byte
	Schema   []byte
	Table    []byte
	OrgTable []byte
	Name     []byte
	OrgName  []byte

	Collation uint16 // collationBinary for binary strings
	Len       uint32
	Type      byte
	Flags     uint16
	Decimals  byte
}

// parse reads Protocol::ColumnDefinition41 from buf. The names are copied
// into one new slice, since buf is reused by the next read.
func (column *columnDefinition) parse(buf []byte) error {
	d := decoder{buf: bytes.Clone(buf)}
	column.Catalog = d.lenEncString()
	column.Schema = d.lenEncString()
	column.Table = d.lenEncString()
	column.OrgTable = d.lenEncString()
	column.Name = d.lenEncString()
	column.OrgName = d.lenEncString()
	column.parseFixed(&d)

	return d.err("column definition")
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
// before. Unless a name changed, only the fixed-length fields are set, so
// the names are not copied again.
func (column *columnDefinition) update(buf []byte) error {
	d := decoder{buf: buf}

	same := bytes.Equal(d.lenEncString(), column.Catalog) &&
		bytes.Equal(d.lenEncString(), column.Schema) &&
		bytes.Equal(d.lenEncString(), column.Table) &&
		bytes.Equal(d.lenEncString(), column.OrgTable) &&
		bytes.Equal(d.lenEncString(), column.Name) &&
		bytes.Equal(d.lenEncString(), column.OrgName)

	if !same || d.bad {
		return column.parse(buf)
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
		// Not reached: the client does not set CLIENT_LOCAL_FILES.
		return 0, errLocalInfile
	default:
		return columnCount(buf)
	}
}

// skipResult reads the rest of a result whose header said columns, and
// the results that follow it.
func (conn *Conn) skipResult(columns int) error {
	if columns > 0 {
		if err := conn.skipColumns(columns); err != nil {
			return err
		}

		if err := conn.readUntilEOF(); err != nil {
			return err
		}
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

		if n > 0 {
			if err := conn.skipColumns(n); err != nil {
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
