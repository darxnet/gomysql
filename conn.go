package gomysql

import (
	"bufio"
	"compress/zlib"
	"crypto/tls"
	"io"
	"net"
)

type Conn struct {
	readerCompressed        io.ReadCloser
	conn                    net.Conn
	errLast                 error
	writerCompressed        *zlib.Writer
	reader                  *bufio.Reader
	writer                  *bufio.Writer
	config                  Config
	server                  string
	buffer                  buffer
	bufferCompressed        buffer
	lastInsertId            uint64
	rowsAffected            uint64
	bufferCompressedDataLen int
	id                      uint32
	capFlags                uint32
	statusFlags             uint16
	charSet                 byte
	sequenceCompressed      byte
	isCompress              bool
	sequence                byte
}

func (conn *Conn) dial() error {
	var err error

	conn.conn, err = net.Dial(conn.config.Protocol, conn.config.Address)
	if err == nil {
		conn.reader = bufio.NewReaderSize(conn.conn, readerBufferSize)
		conn.writer = bufio.NewWriterSize(conn.conn, writerBufferSize)
	}

	return err
}

func (conn *Conn) getTlsConfig() *tls.Config {
	if conn.config.TlsConfig != nil {
		return conn.config.TlsConfig
	}

	return &tls.Config{
		ServerName: conn.config.Address,
	}
}

// Conn (re)establishes the connection: dial, handshake and authentication.
// On failure the socket is closed.
func (conn *Conn) Conn() error {
	conn.sequence = 0
	conn.sequenceCompressed = 0
	conn.capFlags = 0

	if err := conn.dial(); err != nil {
		return err
	}

	err := conn.connect()
	if err != nil {
		_ = conn.conn.Close()
	}

	return err
}

func (conn *Conn) connect() error {
	authPluginName, authPluginData, err := conn.handshake()
	if err != nil {
		return err
	}

	if err := conn.handshakeResponse41(authPluginName, authPluginData); err != nil {
		return err
	}

	return conn.handshakeAuth(authPluginName, authPluginData)
}
