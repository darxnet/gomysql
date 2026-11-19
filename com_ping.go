package gomysql

const comPing byte = 0x0e

// Ping checks that the connection is alive (COM_PING).
func (conn *Conn) Ping() error {
	return simpleCommand(conn, comPing, "")
}

// simpleCommand sends a command that the server answers with OK or ERR.
func simpleCommand(conn *Conn, com byte, arg string) error {
	if err := writeCommand(conn, com, arg); err != nil {
		return err
	}

	buf, err := conn.readPacket()
	if err != nil {
		return err
	}

	switch buf[0] {
	case iOK:
		return conn.processOKPacket(buf)
	case iERR:
		return conn.processERRPacket(buf)
	default:
		return errUnexpectedPacketIdentity(buf[0])
	}
}
