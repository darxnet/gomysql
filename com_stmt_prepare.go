package gomysql

import (
	"encoding/binary"
)

const comStmtPrepare = 0x16

// Prepare prepares query on the server (COM_STMT_PREPARE).
//
// https://dev.mysql.com/doc/dev/mysql-server/latest/page_protocol_com_stmt_prepare.html
func (conn *Conn) Prepare(query string) (*Stmt, error) {
	if err := writeCommand(conn, comStmtPrepare, query); err != nil {
		return nil, err
	}

	buf, err := conn.readPacket()
	if err != nil {
		return nil, err
	}

	switch buf[0] {
	case iOK:
	case iERR:
		return nil, conn.processERRPacket(buf)
	default:
		return nil, errUnexpectedPacketIdentity(buf[0])
	}

	// COM_STMT_PREPARE_OK: 0x00, statement id (4), number of columns (2),
	// number of params (2), filler, warning count (2).
	le := binary.LittleEndian
	stmt := &Stmt{
		conn:    conn,
		query:   query,
		id:      le.Uint32(buf[1:]),
		columns: make([]ColumnDefinition, le.Uint16(buf[5:])),
		params:  make([]ColumnDefinition, le.Uint16(buf[7:])),
	}

	if err := conn.readColumns(stmt.params); err != nil {
		return nil, err
	}

	if err := conn.readColumns(stmt.columns); err != nil {
		return nil, err
	}

	return stmt, nil
}
