package gomysql

import (
	"bytes"
	"crypto/tls"
	"encoding/binary"
	"errors"
)

// handshakeV10 reads Protocol::HandshakeV10. The returned plugin name and
// data are copies: the read buffer is reused by the next packet.
//
// https://dev.mysql.com/doc/dev/mysql-server/latest/page_protocol_connection_phase_packets_protocol_handshake_v10.html
func (conn *Conn) handshakeV10(buf []byte) (authPluginName, authPluginData []byte, err error) {
	// protocol version, server version (string[NUL])
	end := bytes.IndexByte(buf, 0x00)
	if end < 0 || len(buf) < end+16 {
		return nil, nil, errMalformedHandshake
	}

	conn.server = string(buf[1:end])
	buf = buf[end+1:]

	// connection id (4), auth-plugin-data-part-1 (8), filler (1),
	// capability flags, lower 2 bytes
	le := binary.LittleEndian
	conn.id = le.Uint32(buf)
	authPluginData = append([]byte(nil), buf[4:12]...)
	conn.capFlags |= uint32(le.Uint16(buf[13:]))
	buf = buf[15:]

	if len(buf) == 0 {
		return nil, authPluginData, nil
	}

	// character set (1), status flags (2), capability flags, upper 2 bytes,
	// length of auth-plugin-data (1), reserved (10)
	if len(buf) < 16 {
		return nil, nil, errMalformedHandshake
	}

	conn.charSet = buf[0]
	conn.capFlags |= uint32(le.Uint16(buf[3:])) << 16
	authPluginDataLen := int(buf[5])
	buf = buf[16:]

	if conn.capFlags&clientSecureConn != 0 {
		// auth-plugin-data-part-2: the rest of the data, at least 13 bytes
		// (a 12-byte nonce tail and a NUL)
		n := max(13, authPluginDataLen-8)
		if len(buf) < n {
			return nil, nil, errMalformedHandshake
		}

		authPluginData = append(authPluginData, buf[:n]...)
		buf = buf[n:]
	}

	if conn.capFlags&clientPluginAuth != 0 {
		// auth-plugin name (string[NUL]); before MySQL 5.5.10 and 5.6.2 the
		// NUL may be missing (Bug#59453)
		if end := bytes.IndexByte(buf, 0x00); end >= 0 {
			buf = buf[:end]
		}

		authPluginName = append([]byte(nil), buf...)
	}

	return authPluginName, authPluginData, nil
}

// handshakeResponse41 sends Protocol::HandshakeResponse41, after an
// SSLRequest and the TLS handshake when Config.IsSSL is set.
func (conn *Conn) handshakeResponse41(authPluginName, authPluginData []byte) (err error) {
	capFlags := clientProtocol41 | clientLongPassword

	if conn.config.IsCompress {
		if conn.capFlags&clientCompress == 0 {
			err = errors.New("gomysql: server does not support compression")
			_ = conn.conn.Close()
			return err
		}
		capFlags |= clientCompress
	}

	if conn.config.IsClientFoundRows {
		capFlags |= clientFoundRows
	}

	if conn.capFlags&clientDeprecateEOF != 0 {
		capFlags |= clientDeprecateEOF
	}

	if conn.config.Charset != 0 {
		conn.charSet = conn.config.Charset
	} else {
		conn.charSet = CharSet_utf8mb4_general_ci
	}

	// capability flags (4, set below), max packet size (4), character set,
	// 23 reserved bytes
	buf := make([]byte, 32, 128)
	binary.LittleEndian.PutUint32(buf[4:], maxPacketSize)
	buf[8] = conn.charSet

	// https://dev.mysql.com/doc/internals/en/connection-phase-packets.html#packet-Protocol::SSLRequest
	if conn.config.IsSSL {
		if conn.capFlags&clientSSL == 0 {
			err = errServerDontSupportSSL
			_ = conn.conn.Close()
			return err
		}
		capFlags |= clientSSL
		binary.LittleEndian.PutUint32(buf, capFlags)

		err = conn.writePacket(buf)
		if err != nil {
			_ = conn.conn.Close()
			return err
		}
		tlsConn := tls.Client(conn.conn, conn.getTlsConfig())
		err = tlsConn.Handshake()
		if err != nil {
			_ = conn.conn.Close()
			return err
		}
		conn.conn = tlsConn
		err = conn.reader.switchTo(tlsConn)
		if err != nil {
			return err
		}
	}

	// username
	buf = append(buf, conn.config.User...)
	buf = append(buf, 0x00)

	// auth-response
	capFlags |= clientPluginAuth | clientSecureConn
	authResponse, err := conn.getAuthResponse(authPluginName, authPluginData)
	if err != nil {
		_ = conn.conn.Close()
		return err
	}

	if len(authResponse) > 255 {
		capFlags |= clientPluginAuthLenEncClientData
	}

	// clientSecureConn is always set above: a length byte, or a
	// length-encoded integer for a longer response
	if len(authResponse) > 255 {
		buf = appendLenEncBytes(buf, authResponse)
	} else {
		buf = append(buf, byte(len(authResponse))) //nolint:gosec // G115: at most 255
		buf = append(buf, authResponse...)
	}

	// database
	if conn.config.DbName != "" {
		capFlags |= clientConnectWithDB
		buf = append(buf, conn.config.DbName...)
		buf = append(buf, 0x00)
	}

	// auth plugin name
	buf = append(buf, authPluginName...)
	buf = append(buf, 0x00)

	// attrs
	if len(conn.config.Attr) > 0 {
		capFlags |= clientConnectAttrs
		var keyVal []byte
		for key, val := range conn.config.Attr {
			keyVal = appendLenEncString(keyVal, key)
			keyVal = appendLenEncString(keyVal, val)
		}
		buf = appendLenEncInt(buf, uint64(len(keyVal)))
		buf = append(buf, keyVal...)
	}

	binary.LittleEndian.PutUint32(buf, capFlags)
	conn.capFlags = capFlags

	err = conn.writePacket(buf)
	if err != nil {
		_ = conn.conn.Close()
	}

	return err
}
