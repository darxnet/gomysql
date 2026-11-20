package gomysql

import (
	"net"
)

type Conn struct {
	conn         net.Conn
	compress     *compression // nil unless the compressed protocol is on
	config       Config
	server       string
	reader       packetReader
	wbuf         []byte // reused write buffer
	lastInsertID uint64
	rowsAffected uint64
	id           uint32
	capFlags     uint32
	statusFlags  uint16
	sequence     byte
}

func (conn *Conn) dial() error {
	nc, err := net.Dial(conn.config.Network, conn.config.Address)
	if err != nil {
		return err
	}

	conn.conn = nc
	conn.compress = nil

	if conn.reader.buf == nil {
		conn.reader = newPacketReader(nc, readerBufferSize)
	} else {
		conn.reader.reset(nc)
	}

	return nil
}

// Reconnect (re)establishes the connection: dial, handshake and authentication.
// The previous socket, if any, is closed first. On failure the new socket
// is closed too.
func (conn *Conn) Reconnect() error {
	if conn.conn != nil {
		_ = conn.conn.Close()
	}

	conn.sequence = 0
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

// connect runs the connection phase on a new socket. It never closes the
// socket: Conn does that on failure.
func (conn *Conn) connect() error {
	authPluginName, authPluginData, err := conn.handshake()
	if err != nil {
		return err
	}

	return conn.authenticate(string(authPluginName), authPluginData)
}

// Connect connects and authenticates. On failure it returns a nil Conn and
// leaves no socket open.
func Connect(config Config) (*Conn, error) { //nolint:gocritic // hugeParam: once per connection
	conn := &Conn{config: config}

	if err := conn.Reconnect(); err != nil {
		return nil, err
	}

	return conn, nil
}
