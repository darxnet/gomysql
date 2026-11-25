package gomysql

import (
	"context"
	"time"
)

// serverLimits are the limits of the server and the account that the
// client follows, read once per session when first needed.
type serverLimits struct {
	maxAllowedPacket     int // the largest packet the server accepts is one byte less
	maxPreparedStmtCount int
	maxConnections       int
	maxUserConnections   int // of the account, 0 for no limit
	waitTimeout          time.Duration
	loaded               bool
}

// limitsQuery reads serverLimits. The session value of
// max_user_connections is the limit of the account if it has one.
const limitsQuery = "SELECT @@max_allowed_packet, @@max_prepared_stmt_count, @@max_connections, " +
	"@@max_user_connections, @@wait_timeout"

// serverLimits reads the limits once per session.
func (conn *Conn) serverLimits(ctx context.Context) (*serverLimits, error) {
	if conn.limits.loaded {
		return &conn.limits, nil
	}

	var (
		l           serverLimits
		waitTimeout int64
	)

	err := conn.QueryRowContext(ctx, limitsQuery).Scan(&l.maxAllowedPacket, &l.maxPreparedStmtCount,
		&l.maxConnections, &l.maxUserConnections, &waitTimeout)
	if err != nil {
		return nil, err
	}

	l.waitTimeout = time.Duration(waitTimeout) * time.Second
	l.loaded = true
	conn.limits = l

	return &conn.limits, nil
}
