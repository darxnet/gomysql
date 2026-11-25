package gomysql

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"
)

// ErrBusy means the Rows of an earlier query are not closed: the server is
// still sending them. Nothing was sent; close the Rows and try again.
var ErrBusy = errors.New("gomysql: connection busy: the Rows of the previous query are not closed")

// inSyncError is an error of the client after which the protocol is in sync:
// it does not break the connection.
type inSyncError struct{ error }

func (e inSyncError) Unwrap() error { return e.error }

// The states of a connection besides idle: an open Rows is Conn.rows.
const (
	stateIdle   uint8 = iota
	stateBroken       // an I/O error, a malformed packet or a canceled context: only Reconnect and Close
	stateClosed
)

// aLongTimeAgo is a deadline in the past, which interrupts I/O at once.
var aLongTimeAgo = time.Unix(1, 0)

// begin starts a command: an error, without sending anything, if the
// connection is not idle or ctx is done. Otherwise ctx is watched until
// end: its deadline is the deadline of the socket, and canceling it
// interrupts I/O.
func (conn *Conn) begin(ctx context.Context) error {
	switch {
	case conn.state == stateClosed:
		return sql.ErrConnDone
	case conn.state == stateBroken:
		return fmt.Errorf("%w: unusable after an earlier error: %w", ErrBadConn, conn.cause)
	case conn.txCtx != nil && conn.txCtx.Err() != nil:
		// The socket is closed, or about to be.
		err := fmt.Errorf("%w: %w", contextErr(conn.txCtx), ErrBadConn) //nolint:contextcheck // of the transaction
		conn.breakWith(err)

		return err
	case conn.rows != nil:
		return ErrBusy
	}

	return conn.watch(ctx)
}

// end ends a command, returning err, which breaks the connection if it is
// not an error of the server, as fail does.
func (conn *Conn) end(err error) error {
	err = conn.fail(err)
	conn.unwatch()

	return err
}

// fail marks the connection broken after err, unless err is nil or an
// error of the server (*Error), which leaves the protocol in sync, but for
// connection exceptions and, with Config.RejectReadOnly, read-only
// refusals. After a canceled or expired context it returns the context's
// error, with ErrBadConn.
func (conn *Conn) fail(err error) error {
	if err == nil {
		return nil
	}

	var (
		serverErr *Error
		clean     inSyncError
	)

	switch {
	case errors.Is(err, ErrBadConn):
		conn.breakWith(err)
	case errors.As(err, &serverErr):
		if closesConnection(serverErr) || conn.config.RejectReadOnly && readOnly(serverErr) {
			err = fmt.Errorf("%w: %w", err, ErrBadConn)
			conn.breakWith(err)
		}
	case !errors.As(err, &clean):
		// I/O errors: the connection is unusable, which ErrBadConn says.
		err = fmt.Errorf("%w: %w", err, ErrBadConn)
		conn.breakWith(err)
	}

	if conn.state == stateBroken {
		if ctxErr := conn.contextErr(); ctxErr != nil {
			return fmt.Errorf("%w: %w", ctxErr, ErrBadConn)
		}
	}

	return err
}

// contextErr is the error of the context of the command or of the
// transaction, if one is done.
func (conn *Conn) contextErr() error {
	if conn.ctx != nil {
		if err := contextErr(conn.ctx); err != nil {
			return err
		}
	}

	if conn.txCtx != nil {
		return contextErr(conn.txCtx)
	}

	return nil
}

// contextErr is the error of ctx, also when its deadline, the deadline of
// the socket, has passed before its timer fired.
func contextErr(ctx context.Context) error {
	if err := context.Cause(ctx); err != nil {
		return err
	}

	if deadline, ok := ctx.Deadline(); ok && !time.Now().Before(deadline) {
		return context.DeadlineExceeded
	}

	return nil
}

// closesConnection reports whether the server closes the connection after
// err: an error of SQLSTATE class 08, connection exception, such as 1153
// ER_NET_PACKET_TOO_LARGE, but 1047 ER_UNKNOWN_COM_ERROR, after which it
// keeps it open (as go-sql-driver decides since #1852). Errors the server
// sends on its own before closing, such as 4031 after wait_timeout, come
// out of sequence: sequenceMismatch breaks the connection.
func closesConnection(err *Error) bool {
	return err.SQLState[0] == '0' && err.SQLState[1] == '8' && err.Number != 1047
}

// readOnly reports whether the server refused a statement because it is
// read-only: 1836 ER_READ_ONLY_MODE, or 1290 ER_OPTION_PREVENTS_STATEMENT
// naming --read-only or --super-read-only (it names other options too,
// such as --secure-file-priv). The option names stay as they are in the
// messages of every language. 1792, a write in a READ ONLY transaction,
// is the client's own doing.
func readOnly(err *Error) bool {
	if err.Number == 1836 {
		return true
	}

	return err.Number == 1290 &&
		(strings.Contains(err.Message, "--read-only") || strings.Contains(err.Message, "--super-read-only"))
}

// breakWith marks the connection broken by err, the first cause kept.
func (conn *Conn) breakWith(err error) {
	if conn.state == stateIdle {
		conn.state, conn.cause = stateBroken, err
	}
}

// watch makes ctx the deadline of the socket and lets its cancellation
// interrupt I/O, until unwatch. A context that is never done costs
// nothing.
func (conn *Conn) watch(ctx context.Context) error {
	if ctx == nil || ctx.Done() == nil {
		return nil
	}

	if err := context.Cause(ctx); err != nil {
		return err
	}

	nc := conn.conn
	conn.ctx = ctx

	if deadline, ok := ctx.Deadline(); ok {
		_ = nc.SetDeadline(deadline)
	}

	conn.watching.Add(1)
	conn.stopWatch = context.AfterFunc(ctx, func() {
		defer conn.watching.Done()

		_ = nc.SetDeadline(aLongTimeAgo)
	})

	return nil
}

// unwatch stops watching the context of the command. If it was canceled
// meanwhile, it waits for the deadline set then and clears it.
func (conn *Conn) unwatch() {
	if conn.ctx == nil {
		return
	}

	if conn.stopWatch() {
		conn.watching.Done()
	} else {
		conn.watching.Wait()
	}

	_ = conn.conn.SetDeadline(time.Time{})
	conn.ctx, conn.stopWatch = nil, nil
}
