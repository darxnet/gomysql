package gomysql

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
