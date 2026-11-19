package gomysql

const comInitDB byte = 0x02

func (conn *Conn) UseDB(schema string) (err error) {
	err = writeCommand(conn, comInitDB, schema)
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
