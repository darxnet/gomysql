package gomysql

import (
	"bytes"
	"crypto/tls"
	"encoding/binary"
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

// handshakeV10 reads Protocol::HandshakeV10. The returned plugin name and
// data are copies: the read buffer is reused by the next packet.
//
// https://dev.mysql.com/doc/dev/mysql-server/latest/page_protocol_connection_phase_packets_protocol_handshake_v10.html
func (conn *Conn) handshakeV10(buf []byte) (authPluginName, authPluginData []byte, err error) {
	d := decoder{buf: buf}
	d.skip(1) // protocol version
	server := d.nulString()
	id := d.uint32()
	authPluginData = bytes.Clone(d.take(8)) // auth-plugin-data-part-1
	d.skip(1)                               // filler
	capFlags := uint32(d.uint16())          // capability flags, lower 2 bytes

	if d.bad {
		return nil, nil, errMalformedHandshake
	}

	conn.server, conn.id, conn.capFlags = string(server), id, capFlags

	if len(d.buf) == 0 {
		return nil, authPluginData, nil
	}

	d.skip(1) // character set
	d.skip(2) // status flags
	conn.capFlags |= uint32(d.uint16()) << 16
	authPluginDataLen := int(d.uint8())
	d.skip(10) // reserved

	if conn.capFlags&clientSecureConn != 0 {
		// auth-plugin-data-part-2: the rest of the data, at least 13 bytes
		// (a 12-byte nonce tail and a NUL)
		authPluginData = append(authPluginData, d.take(max(13, authPluginDataLen-8))...)
	}

	if conn.capFlags&clientPluginAuth != 0 {
		// auth-plugin name (string[NUL]); before MySQL 5.5.10 and 5.6.2 the
		// NUL may be missing (Bug#59453)
		name := d.rest()
		if end := bytes.IndexByte(name, 0x00); end >= 0 {
			name = name[:end]
		}

		authPluginName = bytes.Clone(name)
	}

	if d.bad {
		return nil, nil, errMalformedHandshake
	}

	return authPluginName, authPluginData, nil
}

// handshakeResponse41 sends Protocol::HandshakeResponse41, after an
// SSLRequest and the TLS handshake when Config.TLS is set.
func (conn *Conn) handshakeResponse41(authPluginName, authPluginData []byte) error {
	authResponse, err := conn.authResponse(authPluginName, authPluginData)
	if err != nil {
		return err
	}

	capFlags, err := conn.clientCaps(len(authResponse))
	if err != nil {
		return err
	}

	collation, err := conn.config.collationID()
	if err != nil {
		return err
	}

	// capability flags, max packet size, character set, 23 reserved bytes;
	// this much alone is the SSLRequest
	buf := make([]byte, 32, 128)
	binary.LittleEndian.PutUint32(buf, capFlags)
	binary.LittleEndian.PutUint32(buf[4:], maxPacketSize)
	buf[8] = collation

	if capFlags&clientSSL != 0 {
		if err := conn.upgradeTLS(buf); err != nil {
			return err
		}
	}

	buf = append(buf, conn.config.User...)
	buf = append(buf, 0x00)
	buf = appendAuthResponse(buf, authResponse)

	if capFlags&clientConnectWithDB != 0 {
		buf = append(buf, conn.config.Database...)
		buf = append(buf, 0x00)
	}

	buf = append(buf, authPluginName...)
	buf = append(buf, 0x00)

	if capFlags&clientConnectAttrs != 0 {
		buf = appendConnectAttrs(buf, conn.config.ConnectAttrs)
	}

	conn.capFlags = capFlags

	return conn.writePacket(buf)
}

// clientCaps returns the capability flags the client asks for, given those
// the server offers in conn.capFlags.
func (conn *Conn) clientCaps(authResponseLen int) (uint32, error) {
	server := conn.capFlags
	caps := clientProtocol41 | clientLongPassword | clientPluginAuth | clientSecureConn |
		server&clientDeprecateEOF

	if conn.config.Compress {
		if server&clientCompress == 0 {
			return 0, errServerDontSupportCompression
		}

		caps |= clientCompress
	}

	if conn.config.TLS != nil {
		if server&clientSSL == 0 {
			return 0, errServerDontSupportSSL
		}

		caps |= clientSSL
	}

	if conn.config.FoundRows {
		caps |= clientFoundRows
	}

	if conn.config.Database != "" {
		caps |= clientConnectWithDB
	}

	// Servers without CLIENT_CONNECT_ATTRS, such as MySQL 5.5 and some
	// proxies, would read the attributes as part of the next field.
	if len(conn.config.ConnectAttrs) > 0 {
		caps |= server & clientConnectAttrs
	}

	if authResponseLen > 255 {
		caps |= clientPluginAuthLenEncClientData
	}

	return caps, nil
}

// upgradeTLS sends the SSLRequest and switches the connection to TLS.
//
// https://dev.mysql.com/doc/dev/mysql-server/latest/page_protocol_connection_phase_packets_protocol_ssl_request.html
func (conn *Conn) upgradeTLS(sslRequest []byte) error {
	if err := conn.writePacket(sslRequest); err != nil {
		return err
	}

	tlsConn := tls.Client(conn.conn, conn.config.tlsConfig())
	if err := tlsConn.Handshake(); err != nil {
		return err
	}

	conn.conn = tlsConn

	return conn.reader.switchTo(tlsConn)
}

// appendAuthResponse appends the auth response after a length byte, or as
// a length-encoded string when it is longer than 255 bytes
// (CLIENT_PLUGIN_AUTH_LENENC_CLIENT_DATA).
func appendAuthResponse(dst, authResponse []byte) []byte {
	if len(authResponse) > 255 {
		return appendLenEncBytes(dst, authResponse)
	}

	dst = append(dst, byte(len(authResponse))) //nolint:gosec // G115: at most 255

	return append(dst, authResponse...)
}

// appendConnectAttrs appends the connection attributes as a length-encoded
// block of length-encoded key and value strings.
func appendConnectAttrs(dst []byte, attrs map[string]string) []byte {
	var kv []byte
	for key, val := range attrs {
		kv = appendLenEncString(kv, key)
		kv = appendLenEncString(kv, val)
	}

	dst = appendLenEncInt(dst, uint64(len(kv)))

	return append(dst, kv...)
}
