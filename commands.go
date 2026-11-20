package gomysql

import "context"

const comPing byte = 0x0e

// Ping checks that the connection is alive (COM_PING).
func (conn *Conn) Ping() error {
	return conn.PingContext(context.Background())
}

// PingContext is Ping within ctx.
func (conn *Conn) PingContext(ctx context.Context) error {
	return simpleCommand(ctx, conn, comPing, "")
}

// simpleCommand sends a command that the server answers with OK or ERR.
func simpleCommand(ctx context.Context, conn *Conn, com byte, arg string) error {
	if err := conn.begin(ctx); err != nil {
		return err
	}

	return conn.end(conn.simpleCommand(com, arg))
}

func (conn *Conn) simpleCommand(com byte, arg string) error {
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
	return simpleCommand(context.Background(), conn, comInitDB, name)
}

const comStatistics byte = 0x08

// Statistics returns the server's status line, such as "Uptime: 5
// Threads: 1 Questions: 2 ..." (COM_STATISTICS).
func (conn *Conn) Statistics() (string, error) {
	if err := conn.begin(context.Background()); err != nil {
		return "", err
	}

	s, err := conn.statistics()

	return s, conn.end(err)
}

func (conn *Conn) statistics() (string, error) {
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

// Close sends COM_QUIT, if the connection is idle, and closes it. The
// server answers COM_QUIT by closing the socket, so there is no response
// to read. Closing a closed connection does nothing.
func (conn *Conn) Close() error {
	if conn.state == stateClosed {
		return nil
	}

	var err error
	if conn.state == stateIdle && conn.rows == nil {
		err = writeCommand(conn, comQuit, "")
	}

	if errClose := conn.conn.Close(); err == nil {
		err = errClose
	}

	conn.dropRows()
	conn.unwatch()
	conn.endTx()
	conn.state = stateClosed

	return err
}
