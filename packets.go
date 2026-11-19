package gomysql

import (
	"encoding/binary"
)

// https://dev.mysql.com/doc/dev/mysql-server/latest/page_protocol_basic_eof_packet.html
func (conn *Conn) readUntilEOF() (err error) {
	for {
		buf, err := conn.readPacket()
		if err != nil {
			return err
		}

		switch {
		case isEOF(buf): // EOF, or OK with the EOF header under CLIENT_DEPRECATE_EOF
			conn.processEOFPacket(buf)
			return nil
		case buf[0] == iERR:
			return conn.processERRPacket(buf)
		case buf[0] == iOK:
			// do nothing, just read unprocessed packets
		default:
			return errUnexpectedPacketIdentity(buf[0])
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
			conn.processOKPacket(buf)
		case iERR:
			return conn.processERRPacket(buf)
		case iLocalInfile:
			return errLocalInfile
		default:
			columns, _ := lenDecInt(buf)
			for range columns {
				if _, err = conn.readPacket(); err != nil {
					return err
				}
			}

			if conn.capFlags&clientDeprecateEOF == 0 {
				buf, err = conn.readPacket()
				if err != nil {
					return err
				}
				if buf[0] != iEOF {
					return errUnexpectedPacketIdentity(buf[0])
				}
				conn.processEOFPacket(buf)
			}

			if err := conn.readUntilEOF(); err != nil {
				return err
			}
		}
	}

	return nil
}

// https://dev.mysql.com/doc/internals/en/packet-OK_Packet.html
// https://dev.mysql.com/doc/dev/mysql-server/latest/page_protocol_basic_ok_packet.html
func (conn *Conn) processOKPacket(buf []byte) {
	var pos, posN int

	pos += 1 // header: 0x00, or 0xfe under CLIENT_DEPRECATE_EOF

	// affected rows
	conn.rowsAffected, posN = lenDecInt(buf[pos:])
	pos += posN

	// last insert-id
	conn.lastInsertId, posN = lenDecInt(buf[pos:])
	pos += posN

	if len(buf) >= pos+4 {
		conn.statusFlags = uint16(buf[pos]) | uint16(buf[pos+1])<<8
	}
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

func (conn *Conn) processEOFPacket(buf []byte) {
	if len(buf) >= 5 {
		conn.statusFlags = uint16(buf[3]) | uint16(buf[4])<<8
	}
}
