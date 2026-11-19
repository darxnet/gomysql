package gomysql

const comStatistics byte = 0x08

// Statistics returns a human readable string of some internal status vars.
//
// The statistics are refreshed at the time of executing this command.
// If the returned string is of zero length an error message is returned by mysql_stat
// to the conn application instead of the actual empty statistics string.
func (conn *Conn) Statistics() (s string, err error) {
	err = writeCommand(conn, comStatistics, "")
	if err != nil {
		return s, err
	}

	buf, err := conn.readPacket()
	if err != nil {
		return s, err
	}

	switch buf[0] {
	case iERR:
		err = conn.processERRPacket(buf)
		return s, err

	default:
		s = string(buf)
		return s, err
	}
}
