package gomysql

import (
	"context"
	"database/sql"
	"fmt"
)

// Tx is a transaction: the commands of the connection between Begin and
// Commit or Rollback.
type Tx struct {
	conn *Conn
}

// Begin starts a transaction (START TRANSACTION).
func (conn *Conn) Begin() (*Tx, error) {
	return conn.BeginTx(context.Background(), nil)
}

// BeginTx starts a transaction within ctx, with the isolation level and
// read-only mode of opts, if not nil. As in database/sql, ctx applies
// until Commit or Rollback: when it is done, the socket is closed, so the
// server rolls the transaction back at once, and the connection is broken
// until Reconnect; its commands fail with the error of ctx and ErrBadConn.
func (conn *Conn) BeginTx(ctx context.Context, opts *sql.TxOptions) (*Tx, error) {
	start := "START TRANSACTION"

	if opts != nil {
		if opts.Isolation != sql.LevelDefault {
			level, err := isolationLevel(opts.Isolation)
			if err != nil {
				return nil, err
			}

			if _, err := conn.ExecContext(ctx, "SET TRANSACTION ISOLATION LEVEL "+level); err != nil {
				return nil, err
			}
		}

		if opts.ReadOnly {
			start += " READ ONLY"
		}
	}

	if _, err := conn.ExecContext(ctx, start); err != nil {
		return nil, err
	}

	conn.endTx()

	if ctx.Done() != nil {
		nc := conn.conn
		conn.txCtx = ctx
		conn.txStop = context.AfterFunc(ctx, func() { _ = nc.Close() })
	}

	return &Tx{conn: conn}, nil
}

// endTx stops watching the context of the transaction. It reports false if
// the context closed the socket.
func (conn *Conn) endTx() bool {
	if conn.txCtx == nil {
		return true
	}

	stopped := conn.txStop()
	conn.txCtx, conn.txStop = nil, nil

	return stopped
}

// isolationLevel returns the SQL of the levels MySQL has.
func isolationLevel(level sql.IsolationLevel) (string, error) {
	//exhaustive:ignore
	switch level { // the others are unsupported
	case sql.LevelReadUncommitted:
		return "READ UNCOMMITTED", nil
	case sql.LevelReadCommitted:
		return "READ COMMITTED", nil
	case sql.LevelRepeatableRead:
		return "REPEATABLE READ", nil
	case sql.LevelSerializable:
		return "SERIALIZABLE", nil
	}

	return "", fmt.Errorf("gomysql: isolation level %v is not supported", level)
}

// Commit commits the transaction.
func (tx *Tx) Commit() error {
	return tx.end("COMMIT")
}

// Rollback rolls the transaction back.
func (tx *Tx) Rollback() error {
	return tx.end("ROLLBACK")
}

// end commits or rolls back, unless the context of the transaction closed
// the socket.
func (tx *Tx) end(statement string) error {
	conn := tx.conn
	ctx := conn.txCtx

	if !conn.endTx() {
		err := fmt.Errorf("%w: %w", context.Cause(ctx), ErrBadConn)
		conn.breakWith(err)

		return err
	}

	_, err := conn.Exec(statement)

	return err
}

// Exec is Conn.Exec in the transaction.
func (tx *Tx) Exec(query string, args ...any) (*Result, error) {
	return tx.conn.Exec(query, args...)
}

// ExecContext is Conn.ExecContext in the transaction.
func (tx *Tx) ExecContext(ctx context.Context, query string, args ...any) (*Result, error) {
	return tx.conn.ExecContext(ctx, query, args...)
}

// Prepare is Conn.Prepare in the transaction.
func (tx *Tx) Prepare(query string) (*Stmt, error) {
	return tx.conn.Prepare(query)
}

// PrepareContext is Conn.PrepareContext in the transaction.
func (tx *Tx) PrepareContext(ctx context.Context, query string) (*Stmt, error) {
	return tx.conn.PrepareContext(ctx, query)
}

// Query is Conn.Query in the transaction.
func (tx *Tx) Query(query string, args ...any) (*Rows, error) {
	return tx.conn.Query(query, args...)
}

// QueryContext is Conn.QueryContext in the transaction.
func (tx *Tx) QueryContext(ctx context.Context, query string, args ...any) (*Rows, error) {
	return tx.conn.QueryContext(ctx, query, args...)
}

// QueryRow is Conn.QueryRow in the transaction.
func (tx *Tx) QueryRow(query string, args ...any) *Row {
	return tx.conn.QueryRow(query, args...)
}

// QueryRowContext is Conn.QueryRowContext in the transaction.
func (tx *Tx) QueryRowContext(ctx context.Context, query string, args ...any) *Row {
	return tx.conn.QueryRowContext(ctx, query, args...)
}
