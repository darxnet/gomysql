package gomysql

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
	d := decoder{buf: buf}
	d.skip(1)
	id := d.uint32()
	columns := d.uint16()
	params := d.uint16()

	if err := d.err("COM_STMT_PREPARE_OK"); err != nil {
		return nil, err
	}

	stmt := &Stmt{
		conn:    conn,
		query:   query,
		id:      id,
		columns: make([]ColumnDefinition, columns),
		params:  make([]ColumnDefinition, params),
	}

	if err := conn.readColumns(stmt.params); err != nil {
		return nil, err
	}

	if err := conn.readColumns(stmt.columns); err != nil {
		return nil, err
	}

	return stmt, nil
}
