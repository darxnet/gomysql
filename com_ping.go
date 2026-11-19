package gomysql

const comPing byte = 0x0e

func (conn *Conn) Ping() (err error) {
	err = writeCommand(conn, comPing, "")
	if err != nil {
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
		err = conn.processERRPacket(buf)
		return err

	default:
		err = errUnexpectedPacketIdentity(buf[0])
		return err
	}
}
