package gomysql

import "context"

const comQuery byte = 0x03

// Query runs query in the text protocol and returns its rows. A statement
// without a result set has no rows.
//
// The placeholders of query are replaced by args as SQL literals, on the
// client. They are ? for positional arguments or :name for named ones,
// not both: sql.Named arguments, or one struct (fields found as by
// Rows.ScanStruct, so :user_id finds UserID) or map with string keys. A
// placeholder in a string, a quoted identifier or a comment is not one,
// as for the server in a prepared statement, nor is a colon after an
// identifier or a colon (lbl:LOOP, @a:=1). A query without args is sent
// as it is.
//
// Arguments are converted as in database/sql: a driver.Valuer gives its
// value, a pointer what it points to (nil is NULL), a type based on a
// basic type that type; time.Time is written in Config.Location. A slice
// other than []byte becomes a list: IN (?) with []int{1, 2} is IN (1, 2);
// an empty one is an error. Strings are escaped for NO_BACKSLASH_ESCAPES
// when the server reports it; collations whose multibyte characters can
// contain a backslash (big5, cp932, gbk, gb18030, sjis) cannot have string
// arguments: use Prepare.
func (conn *Conn) Query(query string, args ...any) (*Rows, error) {
	return conn.QueryContext(context.Background(), query, args...)
}

// QueryContext is Query within ctx, which applies until the Rows are
// read or closed: canceling it interrupts the query and breaks the
// connection.
func (conn *Conn) QueryContext(ctx context.Context, query string, args ...any) (*Rows, error) {
	if err := conn.sendQuery(ctx, query, args); err != nil {
		return nil, err
	}

	n, err := conn.readResultHeader()
	if err != nil {
		return nil, conn.end(err)
	}

	// A statement without a result set, such as DO 1, has no rows.
	rows := &Rows{conn: conn, done: n == 0, columns: make([]columnDefinition, n), loc: conn.config.Location}

	if err := conn.readColumns(rows.columns); err != nil {
		return nil, conn.end(err)
	}

	return conn.openRows(rows), nil
}

// sendQuery starts a command and sends query with args interpolated
// (COM_QUERY); on success the command runs until end.
func (conn *Conn) sendQuery(ctx context.Context, query string, args []any) error {
	if len(args) > 0 {
		var err error

		query, err = conn.interpolate(query, args)
		if err != nil {
			return err
		}
	}

	if err := conn.begin(ctx); err != nil {
		return err
	}

	if err := writeCommand(conn, comQuery, query); err != nil {
		return conn.end(err)
	}

	return nil
}

// QueryRow runs query like Query and returns the first row; Row.Scan
// reads it and closes the rows.
func (conn *Conn) QueryRow(query string, args ...any) *Row {
	return conn.QueryRowContext(context.Background(), query, args...)
}

// QueryRowContext is QueryRow within ctx.
func (conn *Conn) QueryRowContext(ctx context.Context, query string, args ...any) *Row {
	rows, err := conn.QueryContext(ctx, query, args...)

	return &Row{rows: rows, err: err}
}

// Result is the outcome of Exec.
type Result struct {
	lastInsertID uint64
	rowsAffected uint64
}

// LastInsertId returns the AUTO_INCREMENT value of the last inserted row.
func (r *Result) LastInsertId() (int64, error) {
	return int64(r.lastInsertID), nil //nolint:gosec // G115: database/sql's Result uses int64
}

// RowsAffected returns the number of rows changed, or matched with
// Config.FoundRows.
func (r *Result) RowsAffected() (int64, error) {
	return int64(r.rowsAffected), nil //nolint:gosec // G115: database/sql's Result uses int64
}

// Exec runs query, with args interpolated as by Query, and returns its
// result. Rows it returns are skipped.
func (conn *Conn) Exec(query string, args ...any) (*Result, error) {
	return conn.ExecContext(context.Background(), query, args...)
}

// ExecContext is Exec within ctx: canceling it interrupts the statement
// and breaks the connection.
func (conn *Conn) ExecContext(ctx context.Context, query string, args ...any) (*Result, error) {
	if err := conn.sendQuery(ctx, query, args); err != nil {
		return nil, err
	}

	result, err := conn.readResult()

	return result, conn.end(err)
}

// readResult reads the response to a command run for its result: the
// counts of its first OK packet. Rows and further results are skipped.
func (conn *Conn) readResult() (*Result, error) {
	n, err := conn.readResultHeader()
	if err != nil {
		return nil, err
	}

	result := &Result{lastInsertID: conn.lastInsertID, rowsAffected: conn.rowsAffected}

	if err := conn.skipResult(n); err != nil {
		return nil, err
	}

	return result, nil
}
