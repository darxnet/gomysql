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
		return unexpectedPacket(buf[0])
	}
}

const comInitDB byte = 0x02

// UseDatabase makes name the default database (COM_INIT_DB).
func (conn *Conn) UseDatabase(name string) error {
	return simpleCommand(conn, comInitDB, name)
}

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

const comQuit byte = 0x01

// Close sends COM_QUIT and closes the connection. The server answers
// COM_QUIT by closing the socket, so there is no response to read.
func (conn *Conn) Close() error {
	err := writeCommand(conn, comQuit, "")

	if errClose := conn.conn.Close(); err == nil {
		err = errClose
	}

	return err
}
