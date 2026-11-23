package gomysql

import (
	"context"
	"errors"
	"math"
	"math/rand/v2"
	"runtime"
	"sync"
	"time"
)

const (
	poolMinConns        = 4
	poolReserve         = 5 // connections of the account left to administrators and migrations
	poolMaxLifetime     = 30 * time.Minute
	poolLifetimeJitter  = 0.1         // a connection lives up to this share of the lifetime less
	poolPingAfter       = time.Second // idle longer: pinged when taken
	poolHealthCheckTime = time.Minute // closing of idle and old connections
	// The pool closes an idle connection this long before the server
	// would: the period of the health check and a margin.
	poolWaitTimeoutMargin = poolHealthCheckTime + 5*time.Second
)

// errDBClosed is the error of DB.Conn after DB.Close.
var errDBClosed = errors.New("gomysql: DB is closed")

// DB is a pool of connections of one Config, safe for concurrent use.
// Conn takes a connection; its Close returns it.
type DB struct {
	config Config

	mu      sync.Mutex
	idle    []*Conn // the most recently returned last
	open    int     // idle and in use
	waiters []chan struct{}
	closed  bool

	maxOpen     int           // of SetMaxOpenConns, 0 for no limit of its own
	maxIdle     int           // of SetMaxIdleConns, math.MaxInt by default
	maxIdleTime time.Duration // of SetConnMaxIdleTime, 0 for none
	maxLifetime time.Duration // of SetConnMaxLifetime, 0 for none

	// From the server, read on the first connection.
	limited    bool
	accountMax int           // the connection limit of the account less poolReserve, 0 if unknown
	waitIdle   time.Duration // idle time before wait_timeout, 0 if unknown

	stop chan struct{}
	done chan struct{}
}

// Open returns a pool of connections of config, as sql.Open: it connects
// when a connection is first needed.
//
// By default the pool keeps at most max(4, GOMAXPROCS) connections open
// (SetMaxOpenConns), all of them when idle (SetMaxIdleConns), closes a
// connection 30 minutes after it was opened (SetConnMaxLifetime) and an
// idle one a minute before the server would, after wait_timeout
// (SetConnMaxIdleTime; never if the server does not tell). It never opens more than the
// connection limit of the account (max_user_connections, else
// max_connections) less 5, left to administrators and migrations.
//
//nolint:gocritic // hugeParam: once per pool
func Open(config Config) (*DB, error) {
	db := &DB{
		config:      config,
		maxOpen:     max(poolMinConns, runtime.GOMAXPROCS(0)),
		maxIdle:     math.MaxInt,
		maxLifetime: poolMaxLifetime,
		stop:        make(chan struct{}),
		done:        make(chan struct{}),
	}

	go db.healthCheck()

	return db, nil
}

// SetMaxOpenConns sets the most connections open at once, idle and in
// use; n <= 0 sets no limit but that of the account.
func (db *DB) SetMaxOpenConns(n int) {
	db.mu.Lock()
	db.maxOpen = max(0, n)
	excess := db.trimLocked()
	db.mu.Unlock()

	closeAll(excess)
}

// SetMaxIdleConns sets the most idle connections kept; more close when
// returned. n <= 0 keeps none. By default the pool keeps all, unlike
// database/sql, whose 2 make connections open and close under load.
func (db *DB) SetMaxIdleConns(n int) {
	db.mu.Lock()
	db.maxIdle = max(0, n)
	excess := db.trimLocked()
	db.mu.Unlock()

	closeAll(excess)
}

// SetConnMaxIdleTime closes connections idle for d; d <= 0 closes them
// only shortly before wait_timeout of the server.
func (db *DB) SetConnMaxIdleTime(d time.Duration) {
	db.mu.Lock()
	db.maxIdleTime = max(0, d)
	db.mu.Unlock()

	db.closeStale(time.Now())
}

// SetConnMaxLifetime closes connections when returned or idle after d
// from their opening, each up to 10% earlier, so that connections opened
// together do not close together; d <= 0 keeps them. It makes the pool
// follow a failover, new replicas or a new address.
func (db *DB) SetConnMaxLifetime(d time.Duration) {
	db.mu.Lock()
	db.maxLifetime = max(0, d)
	db.mu.Unlock()

	db.closeStale(time.Now())
}

// maxOpenLocked is the effective limit of open connections.
func (db *DB) maxOpenLocked() int {
	n := db.maxOpen
	if db.accountMax > 0 && (n == 0 || n > db.accountMax) {
		n = db.accountMax
	}

	if n == 0 {
		return math.MaxInt
	}

	return n
}

// idleTimeLocked is the effective idle time, 0 for none.
func (db *DB) idleTimeLocked() time.Duration {
	d := db.maxIdleTime
	if db.waitIdle > 0 && (d == 0 || d > db.waitIdle) {
		d = db.waitIdle
	}

	return d
}

// expiredLocked reports whether conn has outlived its lifetime at now.
func (db *DB) expiredLocked(conn *Conn, now time.Time) bool {
	if db.maxLifetime == 0 {
		return false
	}

	lifetime := time.Duration(float64(db.maxLifetime) * conn.lifetimeShare)

	return now.Sub(conn.opened) >= lifetime
}

// Conn takes an idle connection, or connects if fewer than the limit are
// open, or waits for one within ctx. A connection idle for more than a
// second is pinged first; a dead one is replaced. Close returns the
// connection to the pool, Hijack takes it out.
//
// The connection is returned as it is: session variables, user variables
// and temporary tables pass to the next user. A connection that is
// broken, has open Rows, is in a transaction or was switched to another
// user or database is closed instead.
func (db *DB) Conn(ctx context.Context) (*Conn, error) {
	for {
		conn, wait, err := db.take()
		if err != nil {
			return nil, err
		}

		switch {
		case conn != nil:
			if time.Since(conn.idleSince) <= poolPingAfter {
				return conn, nil
			}

			if err := conn.PingContext(ctx); err != nil {
				db.discard(conn)

				if ctxErr := context.Cause(ctx); ctxErr != nil {
					return nil, ctxErr
				}

				continue
			}

			return conn, nil
		case wait == nil:
			return db.connect(ctx)
		}

		select {
		case <-wait:
		case <-ctx.Done():
			db.cancelWait(wait)

			return nil, context.Cause(ctx)
		}
	}
}

// take returns an idle connection, or nothing if a new one may be
// opened (counted already), or a channel to wait on.
func (db *DB) take() (conn *Conn, wait chan struct{}, err error) {
	db.mu.Lock()
	defer db.mu.Unlock()

	if db.closed {
		return nil, nil, errDBClosed
	}

	if n := len(db.idle); n > 0 {
		conn = db.idle[n-1]
		db.idle[n-1] = nil
		db.idle = db.idle[:n-1]
		conn.inPool = false

		return conn, nil, nil
	}

	if db.open < db.maxOpenLocked() {
		db.open++

		return nil, nil, nil
	}

	wait = make(chan struct{}, 1)
	db.waiters = append(db.waiters, wait)

	return nil, wait, nil
}

// connect opens a connection counted by take. The first one reads the
// limits of the server and the account.
func (db *DB) connect(ctx context.Context) (*Conn, error) {
	conn, err := ConnectContext(ctx, db.config)
	if err == nil && !db.isLimited() {
		err = db.limit(ctx, conn)
		if err != nil {
			_ = conn.Close()
		}
	}

	if err != nil {
		db.mu.Lock()
		db.open--
		db.wakeLocked()
		db.mu.Unlock()

		return nil, err
	}

	conn.opened = time.Now()
	conn.lifetimeShare = 1 - poolLifetimeJitter*rand.Float64() //nolint:gosec // G404: jitter
	conn.database = db.config.Database
	conn.pool = db

	return conn, nil
}

func (db *DB) isLimited() bool {
	db.mu.Lock()
	defer db.mu.Unlock()

	return db.limited
}

// limit reads the connection limit of the account and wait_timeout.
// Limits the server does not give, as a proxy may not, are left unknown;
// an error that breaks conn is returned.
func (db *DB) limit(ctx context.Context, conn *Conn) error {
	limits, err := conn.serverLimits(ctx)
	if err != nil && conn.state != stateIdle {
		return err
	}

	db.mu.Lock()
	defer db.mu.Unlock()

	db.limited = true

	if limits == nil {
		return nil
	}

	account := limits.maxUserConnections
	if account == 0 {
		account = limits.maxConnections
	}

	if account > 0 {
		db.accountMax = max(1, account-poolReserve)
	}

	if wait := limits.waitTimeout; wait > 0 {
		db.waitIdle = max(wait/2, wait-poolWaitTimeoutMargin)
	}

	return nil
}

// cancelWait gives up waiting on wait: a wakeup it got meanwhile passes to
// the next waiter.
func (db *DB) cancelWait(wait chan struct{}) {
	db.mu.Lock()
	defer db.mu.Unlock()

	for i, w := range db.waiters {
		if w == wait {
			db.waiters = append(db.waiters[:i], db.waiters[i+1:]...)

			return
		}
	}

	// Already woken.
	db.wakeLocked()
}

// wakeLocked wakes the longest waiter: an idle connection or a place for
// a new one is free.
func (db *DB) wakeLocked() {
	if len(db.waiters) == 0 {
		return
	}

	wait := db.waiters[0]
	db.waiters[0] = nil
	db.waiters = db.waiters[1:]
	wait <- struct{}{}
}

// trimLocked takes out the oldest idle connections beyond the limits, to
// be closed, and wakes waiters for places freed by a higher limit.
func (db *DB) trimLocked() []*Conn {
	limit := db.maxOpenLocked()

	n := min(len(db.idle), max(0, db.open-limit, len(db.idle)-db.maxIdle))
	excess := append([]*Conn(nil), db.idle[:n]...)
	db.idle = append(db.idle[:0], db.idle[n:]...)
	db.open -= n

	for range min(len(db.waiters), max(0, limit-db.open)) {
		db.wakeLocked()
	}

	return excess
}

// put returns conn to the pool, or closes it if it cannot serve another
// user as it is, is too old, is beyond the limits or the pool is closed.
func (db *DB) put(conn *Conn) error {
	if conn.inPool { // closed again
		return nil
	}

	reusable := conn.state == stateIdle && conn.rows == nil &&
		conn.statusFlags&serverStatusInTrans == 0 &&
		conn.config.User == db.config.User && conn.database == db.config.Database

	if reusable {
		db.mu.Lock()

		now := time.Now()
		if !db.closed && !db.expiredLocked(conn, now) && db.open <= db.maxOpenLocked() && len(db.idle) < db.maxIdle {
			conn.idleSince = now
			conn.inPool = true
			db.idle = append(db.idle, conn)
			db.wakeLocked()
			db.mu.Unlock()

			return nil
		}

		db.mu.Unlock()
	}

	db.discard(conn)

	return nil
}

// discard closes conn, taken from the pool, and frees its place.
func (db *DB) discard(conn *Conn) {
	conn.pool = nil
	_ = conn.Close()

	db.mu.Lock()
	db.open--
	db.wakeLocked()
	db.mu.Unlock()
}

// Hijack takes the connection out of its pool: its place is freed, and
// Close closes it. It does nothing to a connection of Connect.
func (conn *Conn) Hijack() {
	db := conn.pool
	if db == nil || conn.inPool {
		return
	}

	conn.pool = nil

	db.mu.Lock()
	db.open--
	db.wakeLocked()
	db.mu.Unlock()
}

// Close closes the idle connections and makes Conn fail; connections in
// use are closed when returned.
func (db *DB) Close() error {
	db.mu.Lock()
	if db.closed {
		db.mu.Unlock()

		return nil
	}

	db.closed = true
	idle := db.idle
	db.idle = nil
	db.open -= len(idle)

	for len(db.waiters) > 0 { // to fail
		db.wakeLocked()
	}
	db.mu.Unlock()

	close(db.stop)
	<-db.done

	closeAll(idle)

	return nil
}

// closeAll closes connections taken out of the pool.
func closeAll(conns []*Conn) {
	for _, conn := range conns {
		conn.pool, conn.inPool = nil, false
		_ = conn.Close()
	}
}

// healthCheck closes idle connections past their idle time or lifetime,
// once every poolHealthCheckTime, until Close.
func (db *DB) healthCheck() {
	defer close(db.done)

	ticker := time.NewTicker(poolHealthCheckTime)
	defer ticker.Stop()

	for {
		select {
		case <-db.stop:
			return
		case now := <-ticker.C:
			db.closeStale(now)
		}
	}
}

// closeStale closes the idle connections past their idle time or lifetime
// at now.
func (db *DB) closeStale(now time.Time) {
	db.mu.Lock()

	var stale []*Conn

	idleTime := db.idleTimeLocked()
	kept := db.idle[:0]

	for _, conn := range db.idle {
		if idleTime > 0 && now.Sub(conn.idleSince) >= idleTime || db.expiredLocked(conn, now) {
			stale = append(stale, conn)
		} else {
			kept = append(kept, conn)
		}
	}

	clear(db.idle[len(kept):])
	db.idle = kept
	db.open -= len(stale)

	for range stale {
		db.wakeLocked()
	}

	db.mu.Unlock()

	closeAll(stale)
}
