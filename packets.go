package gomysql

import (
	"encoding/binary"
)

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

// columnCount reads the column count that starts a result set.
func columnCount(buf []byte) (int, error) {
	d := decoder{buf: buf}
	n := d.lenEncInt()

	if d.bad || n == 0 || n > maxColumns {
		return 0, malformed("column count")
	}

	return int(n), nil
}
