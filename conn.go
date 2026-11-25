package gomysql

import (
	"context"
	"io"
	"net"
	"sync"
	"time"
)

// Conn is a connection to the server. It is not safe for concurrent use.
type Conn struct {
	conn         net.Conn
	compress     *compression // nil unless the compressed protocol is on
	config       Config
	server       string
	reader       packetReader
	wbuf         []byte // reused write buffer
	qbuf         []byte // reused buffer of interpolated queries
	lastInsertID uint64
	rowsAffected uint64
	id           uint32
	capFlags     uint32
	statusFlags  uint16
	sequence     byte

	session uint64 // counts sessions, for statements prepared in one

	infile      io.Reader // of LoadData, nil when sent
	infileChunk int       // the size of its packets

	limits serverLimits // of the session
	stmts  stmtCache    // the statements prepared in the session

	database string // the default database, as far as known

	// Of a connection of a DB.
	pool          *DB
	idleSince     time.Time // when returned
	opened        time.Time
	lifetimeShare float64 // of the lifetime of the pool this connection lives
	inPool        bool    // idle in the pool

	authPlugin string // of the handshake
	nonce      []byte // of the handshake

	state uint8
	cause error // of stateBroken
	rows  *Rows // open, the server still sending them

	// The context of the command, watched by an AfterFunc.
	ctx       context.Context //nolint:containedctx // of the running command only
	stopWatch func() bool
	watching  sync.WaitGroup

	// The context of the transaction, if BeginTx had one: when done, the
	// socket is closed, which rolls the transaction back.
	txCtx  context.Context //nolint:containedctx // of the running transaction only
	txStop func() bool
}

func (conn *Conn) dial(ctx context.Context) error {
	nc, err := conn.dialContext(ctx)
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

// Reconnect (re)establishes the connection: dial, handshake and
// authentication. The previous socket, if any, is closed first, and Rows
// and statements of it are unusable. On failure the new socket is closed
// too, and the connection is broken until Reconnect succeeds.
func (conn *Conn) Reconnect() error {
	return conn.ReconnectContext(context.Background())
}

// ReconnectContext is Reconnect within ctx.
func (conn *Conn) ReconnectContext(ctx context.Context) error {
	if conn.conn != nil {
		_ = conn.conn.Close()
	}

	conn.sequence = 0
	conn.capFlags = 0
	conn.dropRows()
	conn.unwatch()
	conn.newSession()
	conn.state, conn.cause = stateIdle, nil

	if err := conn.dial(ctx); err != nil {
		conn.breakWith(err)

		return err
	}

	err := conn.watch(ctx)
	if err == nil {
		err = conn.connect()
		if err == nil {
			err = conn.initSession()
		}

		err = conn.end(err)
	}

	conn.database = conn.config.Database

	// Even a refusal of the server leaves no connection.
	if err != nil {
		conn.breakWith(err)
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

	// For ChangeUser, which answers the same nonce.
	conn.authPlugin, conn.nonce = string(authPluginName), authPluginData

	return conn.authenticate(string(authPluginName), authPluginData)
}

// Connect connects and authenticates. On failure it returns a nil Conn and
// leaves no socket open.
func Connect(config Config) (*Conn, error) { //nolint:gocritic // hugeParam: once per connection
	return ConnectContext(context.Background(), config)
}

// ConnectContext is Connect within ctx: dialing, the handshake and
// authentication.
//
//nolint:gocritic // hugeParam: once per connection
func ConnectContext(ctx context.Context, config Config) (*Conn, error) {
	conn := &Conn{config: config}

	if err := conn.ReconnectContext(ctx); err != nil {
		return nil, err
	}

	return conn, nil
}
