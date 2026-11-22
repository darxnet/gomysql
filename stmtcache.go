package gomysql

import (
	"context"
	"encoding/binary"
	"errors"
)

// minStmtCache is the least size of the cache of prepared statements, and
// its size until the limits of the server are read.
const minStmtCache = 64

// erUnknownStmtHandler, 1243, is the error of a statement the server does
// not know, such as after a failover of Aurora (go-sql-driver #1555).
const erUnknownStmtHandler = 1243

// erMaxPreparedStmtCountReached, 1461, is the error of a prepare when the
// server holds max_prepared_stmt_count statements of all connections.
const erMaxPreparedStmtCountReached = 1461

// stmtCache holds the statements prepared in a session by their text, the
// least recently used deallocated first. A statement is in it while its
// session is the session of the connection.
type stmtCache struct {
	m          map[string]*Stmt
	head, tail *Stmt // the most and the least recently used
	session    uint64
	limit      int // 0 until the limits of the server are read: minStmtCache
}

// stmtCache returns the cache of the session, emptied when the session
// has changed: the server has deallocated its statements.
func (conn *Conn) stmtCache() *stmtCache {
	c := &conn.stmts
	if c.session != conn.session {
		clear(c.m)
		c.head, c.tail, c.session, c.limit = nil, nil, conn.session, 0
	}

	return c
}

// get returns the statement of query prepared in the same escaping mode, if
// cached, and makes it the most recently used.
func (c *stmtCache) get(query string, backslash bool) *Stmt {
	stmt := c.m[query]
	if stmt == nil || stmt.backslash != backslash {
		return nil
	}

	if c.head != stmt {
		c.unlink(stmt)
		c.pushFront(stmt)
	}

	return stmt
}

// growStmtCache reads the limits of the server when the cache is about to
// outgrow minStmtCache, before the command that prepares: a share of
// max_prepared_stmt_count, the limit of the whole server, per connection.
func (conn *Conn) growStmtCache(ctx context.Context) error {
	c := conn.stmtCache()
	if c.limit != 0 || len(c.m) < minStmtCache {
		return nil
	}

	limits, err := conn.serverLimits(ctx)
	if err != nil {
		return err
	}

	c.limit = max(minStmtCache, limits.maxPreparedStmtCount/max(1, limits.maxConnections))

	return nil
}

// add caches stmt, just prepared, within the command: the statement it
// replaces and the least recently used beyond the limit are deallocated
// (COM_STMT_CLOSE, which has no response).
func (conn *Conn) addStmt(stmt *Stmt) error {
	c := conn.stmtCache()
	if c.m == nil {
		c.m = make(map[string]*Stmt)
	}

	if old := c.m[stmt.query]; old != nil {
		if err := conn.evictStmt(old); err != nil {
			return err
		}
	}

	limit := c.limit
	if limit == 0 {
		limit = minStmtCache
	}

	for len(c.m) >= limit {
		if err := conn.evictStmt(c.tail); err != nil {
			return err
		}
	}

	c.m[stmt.query] = stmt
	c.pushFront(stmt)

	return nil
}

// evictStmt removes stmt from the cache and deallocates it on the server.
func (conn *Conn) evictStmt(stmt *Stmt) error {
	conn.stmts.remove(stmt)

	return writeCommand(conn, comStmtClose, binary.LittleEndian.AppendUint32(nil, stmt.id))
}

// shrinkStmtCache deallocates the least recently used half of the cache,
// at least one statement, and limits the cache to the rest for the session.
func (conn *Conn) shrinkStmtCache() error {
	c := conn.stmtCache()
	keep := len(c.m) / 2

	for len(c.m) > keep {
		if err := conn.evictStmt(c.tail); err != nil {
			return err
		}
	}

	c.limit = max(1, keep)

	return nil
}

// evictStmts deallocates every cached statement, within the command.
func (conn *Conn) evictStmts() error {
	c := conn.stmtCache()
	for c.tail != nil {
		if err := conn.evictStmt(c.tail); err != nil {
			return err
		}
	}

	return nil
}

// remove takes stmt out of the cache: it is prepared again when executed.
func (c *stmtCache) remove(stmt *Stmt) {
	if stmt.session == c.session && c.m[stmt.query] == stmt {
		delete(c.m, stmt.query)
		c.unlink(stmt)
	}

	stmt.session = 0
}

func (c *stmtCache) pushFront(stmt *Stmt) {
	stmt.prev, stmt.next = nil, c.head
	if c.head != nil {
		c.head.prev = stmt
	} else {
		c.tail = stmt
	}

	c.head = stmt
}

func (c *stmtCache) unlink(stmt *Stmt) {
	if stmt.prev != nil {
		stmt.prev.next = stmt.next
	} else {
		c.head = stmt.next
	}

	if stmt.next != nil {
		stmt.next.prev = stmt.prev
	} else {
		c.tail = stmt.prev
	}

	stmt.prev, stmt.next = nil, nil
}

// unknownStmt reports whether err, of an execution of stmt, says the server
// does not know the statement. Then the command ends, in sync, and stmt is
// prepared again when executed.
func (stmt *Stmt) unknownStmt(err error) bool {
	var serverErr *Error
	if !errors.As(err, &serverErr) || serverErr.Number != erUnknownStmtHandler {
		return false
	}

	conn := stmt.conn
	_ = conn.end(err)
	conn.stmtCache().remove(stmt)

	return true
}
