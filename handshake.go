package gomysql

import (
	"fmt"
)

// handshake reads Protocol::HandshakeV10. Servers older than MySQL 4.1
// (protocol v9, or v10 without CLIENT_PROTOCOL_41) are refused.
func (conn *Conn) handshake() (authPluginName, authPluginData []byte, err error) {
	buf, err := conn.readPacket()
	if err != nil {
		return nil, nil, err
	}

	switch buf[0] {
	case iERR:
		return nil, nil, conn.processERRPacket(buf)

	case serverProtocolV10:
		authPluginName, authPluginData, err = conn.handshakeV10(buf)
		if err == nil && conn.capFlags&clientProtocol41 == 0 {
			err = errServerTooOld
		}

		return authPluginName, authPluginData, err
	}

	return nil, nil, fmt.Errorf("%w: protocol version %d", errServerTooOld, buf[0])
}
