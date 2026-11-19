package gomysql

const comStatistics byte = 0x08

// Statistics returns the server's status line, such as "Uptime: 5
// Threads: 1 Questions: 2 ..." (COM_STATISTICS).
func (conn *Conn) Statistics() (string, error) {
	if err := writeCommand(conn, comStatistics, ""); err != nil {
		return "", err
	}

	buf, err := conn.readPacket()
	if err != nil {
		return "", err
	}

	if buf[0] == iERR {
		return "", conn.processERRPacket(buf)
	}

	return string(buf), nil
}
