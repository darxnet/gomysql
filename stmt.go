package gomysql

import (
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"math"
	"reflect"
	"time"
)

const comStmtClose byte = 0x19

// Stmt is a prepared statement of a connection.
type Stmt struct {
	conn       *Conn
	prev, next *Stmt  // in the cache, by use
	session    uint64 // of conn when prepared, 0 when no longer cached
	query      string
	sent       string // query with :name placeholders as ?
	columns    []columnDefinition
	params     []columnDefinition
	names      []string // of the :name placeholders
	id         uint32
	backslash  bool // whether backslashes escaped in sent

	// numeric[i]: the last argument of parameter i that was not NULL was a
	// number, the type MySQL 8 keeps for it (see retype).
	numeric []bool
}

// Close does nothing: the statement stays in the cache of the connection,
// shared by every Prepare of its text, and is deallocated when the least
// recently used of more than the cache holds, or with the connection.
func (stmt *Stmt) Close() error {
	return nil
}

const comStmtPrepare = 0x16

// Prepare prepares query on the server (COM_STMT_PREPARE). Its
// placeholders are ? or :name, not both; a :name placeholder takes its
// argument by name, as in Conn.Query.
//
// The connection caches its statements: Prepare of the same text returns
// the same Stmt without asking the server. The cache holds a share of
// max_prepared_stmt_count per connection, at least 64 statements, and
// deallocates the least recently used beyond. If the server already holds
// max_prepared_stmt_count statements (error 1461), the cache deallocates
// its older half, stays that small and prepares again. A statement
// deallocated so, or by Reconnect, ResetConnection or ChangeUser, or
// unknown to the server (error 1243, as after a failover), is prepared
// again when executed.
//
// https://dev.mysql.com/doc/dev/mysql-server/latest/page_protocol_com_stmt_prepare.html
func (conn *Conn) Prepare(query string) (*Stmt, error) {
	return conn.PrepareContext(context.Background(), query)
}

// PrepareContext is Prepare within ctx.
func (conn *Conn) PrepareContext(ctx context.Context, query string) (*Stmt, error) {
	backslash := conn.statusFlags&serverStatusNoBackslashEscapes == 0

	if conn.state == stateIdle {
		if stmt := conn.stmtCache().get(query, backslash); stmt != nil {
			if err := context.Cause(ctx); err != nil {
				return nil, err
			}

			return stmt, nil
		}

		if err := conn.growStmtCache(ctx); err != nil {
			return nil, err
		}
	}

	sent, names, err := rewriteNamed(query, backslash)
	if err != nil {
		return nil, err
	}

	if berr := conn.begin(ctx); berr != nil {
		return nil, berr
	}

	stmt := &Stmt{conn: conn, query: query, sent: sent, names: names, backslash: backslash}
	if err := conn.end(conn.prepare(stmt)); err != nil {
		return nil, err
	}

	return stmt, nil
}

// prepare prepares stmt within the command and caches it. If the server
// holds max_prepared_stmt_count statements of all connections (1461), half
// of the cache is deallocated, the cache shrinks to the rest, and stmt is
// prepared once more.
func (conn *Conn) prepare(stmt *Stmt) error {
	err := conn.prepareStmt(stmt)

	var serverErr *Error
	if errors.As(err, &serverErr) && serverErr.Number == erMaxPreparedStmtCountReached && conn.stmts.tail != nil {
		if shrinkErr := conn.shrinkStmtCache(); shrinkErr != nil {
			return shrinkErr
		}

		err = conn.prepareStmt(stmt)
	}

	return err
}

func (conn *Conn) prepareStmt(stmt *Stmt) error {
	err := writeCommand(conn, comStmtPrepare, stmt.sent)
	if err != nil {
		return err
	}

	buf, err := conn.readPacket()
	if err != nil {
		return err
	}

	switch buf[0] {
	case iOK:
	case iERR:
		return conn.processERRPacket(buf)
	default:
		return unexpectedPacket(buf[0])
	}

	// COM_STMT_PREPARE_OK: 0x00, statement id (4), number of columns (2),
	// number of params (2), filler, warning count (2).
	d := decoder{buf: buf}
	d.skip(1)
	id := d.uint32()
	columns := d.uint16()
	params := d.uint16()

	if err := d.err("COM_STMT_PREPARE_OK"); err != nil {
		return err
	}

	stmt.id = id
	stmt.columns = make([]columnDefinition, columns)
	stmt.params = make([]columnDefinition, params)

	if err := conn.readColumns(stmt.params); err != nil {
		return err
	}

	if err := conn.readColumns(stmt.columns); err != nil {
		return err
	}

	stmt.session = conn.session

	return conn.addStmt(stmt)
}

const comStmtExecute byte = 0x17

// Query executes the prepared statement with args (COM_STMT_EXECUTE) and
// returns its rows. A statement without a result set has no rows.
//
// https://dev.mysql.com/doc/dev/mysql-server/latest/page_protocol_com_stmt_execute.html
func (stmt *Stmt) Query(args ...any) (*Rows, error) {
	return stmt.QueryContext(context.Background(), args...)
}

// QueryContext is Query within ctx, which applies until the Rows are read
// or closed, as in Conn.QueryContext.
func (stmt *Stmt) QueryContext(ctx context.Context, args ...any) (*Rows, error) {
	conn := stmt.conn

	if err := stmt.execute(ctx, args); err != nil {
		return nil, err
	}

	n, err := conn.readResultHeader()
	if err != nil && stmt.unknownStmt(err) {
		if retryErr := stmt.execute(ctx, args); retryErr != nil {
			return nil, retryErr
		}

		n, err = conn.readResultHeader()
	}

	if err != nil {
		return nil, conn.end(err)
	}

	if err := stmt.readColumns(n); err != nil {
		return nil, conn.end(err)
	}

	rows := &Rows{conn: conn, done: n == 0, columns: stmt.columns, loc: conn.config.Location, binary: true}

	return conn.openRows(rows), nil
}

// QueryRow executes the prepared statement like Query and returns the
// first row; Row.Scan reads it and closes the rows.
func (stmt *Stmt) QueryRow(args ...any) *Row {
	return stmt.QueryRowContext(context.Background(), args...)
}

// QueryRowContext is QueryRow within ctx.
func (stmt *Stmt) QueryRowContext(ctx context.Context, args ...any) *Row {
	rows, err := stmt.QueryContext(ctx, args...)

	return &Row{rows: rows, err: err}
}

// Exec executes the prepared statement with args and returns its result.
// Rows it returns are skipped.
func (stmt *Stmt) Exec(args ...any) (*Result, error) {
	return stmt.ExecContext(context.Background(), args...)
}

// ExecContext is Exec within ctx.
func (stmt *Stmt) ExecContext(ctx context.Context, args ...any) (*Result, error) {
	if err := stmt.execute(ctx, args); err != nil {
		return nil, err
	}

	conn := stmt.conn

	result, err := conn.readResult()
	if err != nil && stmt.unknownStmt(err) {
		if retryErr := stmt.execute(ctx, args); retryErr != nil {
			return nil, retryErr
		}

		result, err = conn.readResult()
	}

	return result, conn.end(err)
}

// execute starts the command and sends COM_STMT_EXECUTE, preparing the
// statement again if it is no longer cached; on success the command runs
// until end.
func (stmt *Stmt) execute(ctx context.Context, args []any) error {
	var scratch [64]byte

	buf, retype, err := stmt.executePacket(scratch[:0], args)
	if err != nil {
		return err
	}

	conn := stmt.conn

	if stmt.session != conn.session && conn.state == stateIdle {
		if err := conn.growStmtCache(ctx); err != nil {
			return err
		}
	}

	if err := conn.begin(ctx); err != nil {
		return err
	}

	if retype && stmt.session == conn.session {
		if err := conn.evictStmt(stmt); err != nil {
			return conn.end(err)
		}
	}

	if stmt.session != conn.session {
		if err := conn.prepare(stmt); err != nil {
			return conn.end(err)
		}

		binary.LittleEndian.PutUint32(buf, stmt.id)
	}

	if err := writeCommand(conn, comStmtExecute, buf); err != nil {
		return conn.end(err)
	}

	return nil
}

// executePacket appends COM_STMT_EXECUTE after the command byte to buf;
// retype says the statement must be prepared again first.
func (stmt *Stmt) executePacket(buf []byte, args []any) (packet []byte, retype bool, err error) {
	args, err = stmt.bind(args)
	if err != nil {
		return nil, false, err
	}

	if len(stmt.params) != len(args) {
		return nil, false, errArgCount(len(stmt.params), len(args))
	}

	// COM_STMT_EXECUTE after the command byte: stmt-id, flags (no cursor),
	// iteration-count 1
	buf = binary.LittleEndian.AppendUint32(buf, stmt.id)
	buf = append(buf, 0x00)
	buf = binary.LittleEndian.AppendUint32(buf, 1)

	if len(args) > 0 {
		// NULL bitmap, then new-params-bound-flag, types and values
		posNull := len(buf)
		buf = append(buf, make([]byte, (len(args)+7)/8)...)
		buf = append(buf, 0x01)

		types := make([]byte, 0, len(args)*2)

		var values []byte

		for i, arg := range args {
			if _, ok := expandable(arg); ok {
				return nil, false, fmt.Errorf("argument %d: %w", i, errSliceInStmt)
			}

			v, err := convertArg(arg)
			if err != nil {
				return nil, false, fmt.Errorf("argument %d: %w", i, err)
			}

			if v == nil {
				buf[posNull+i/8] |= 1 << (i % 8)
			}

			types, values, err = appendParam(types, values, v, stmt.conn.config.Location)
			if err != nil {
				return nil, false, err
			}
		}

		retype = stmt.retype(types)
		buf = append(buf, types...)
		buf = append(buf, values...)
	}

	return buf, retype, nil
}

// bind returns the arguments of the placeholders in order.
func (stmt *Stmt) bind(args []any) ([]any, error) {
	var b binder
	if err := b.init(args); err != nil {
		return nil, err
	}

	if stmt.names == nil {
		if b.isNamed() {
			return nil, errNeedPositional
		}

		return args, nil
	}

	bound := make([]any, len(stmt.names))
	for i, name := range stmt.names {
		var err error
		if bound[i], err = b.named(name); err != nil {
			return nil, err
		}
	}

	return bound, b.done(stmt.query, true)
}

// readColumns reads the column definitions of an EXECUTE result into
// stmt.columns. They can differ from those of PREPARE: the type of SELECT ?
// is known only with the argument. A definition with the same names only
// updates the fixed-length fields, without allocating.
func (stmt *Stmt) readColumns(n int) error {
	if n == 0 {
		return nil
	}

	if n != len(stmt.columns) {
		stmt.columns = make([]columnDefinition, n)

		return stmt.conn.readColumns(stmt.columns)
	}

	for i := range stmt.columns {
		buf, err := stmt.conn.readPacket()
		if err != nil {
			return err
		}

		if err := stmt.columns[i].update(buf); err != nil {
			return err
		}
	}

	return stmt.conn.readColumnsEOF()
}

// retype records the types of the arguments, pairs of type and flags, and
// reports whether a string follows a number for some parameter. MySQL 8
// keeps the type of a parameter from the first execution of a statement
// and turns a later string into that number: "abc" comes back as 0. The
// statement is then prepared again. NULL keeps the type; other changes,
// such as a number after a string or an int after a float, convert
// correctly.
func (stmt *Stmt) retype(types []byte) bool {
	if stmt.numeric == nil {
		stmt.numeric = make([]bool, len(types)/2)
	}

	retype := false

	for i := range stmt.numeric {
		switch types[2*i] {
		case myTypeNull:
		case myTypeString, myTypeBlob:
			retype = retype || stmt.numeric[i]
			stmt.numeric[i] = false
		case myTypeDatetime:
			stmt.numeric[i] = false
		default:
			stmt.numeric[i] = true
		}
	}

	return retype
}

const unsignedParam byte = 0x80

// appendParam appends the type (type byte, unsigned flag) and the binary
// value of arg.
func appendParam(types, values []byte, arg any, loc *time.Location) (newTypes, newValues []byte, err error) {
	le := binary.LittleEndian

	switch v := arg.(type) {
	case nil:
		return append(types, myTypeNull, 0), values, nil
	case string:
		return append(types, myTypeString, 0), appendLenEncString(values, v), nil
	case []byte:
		// A binary string, as _binary'...' in text: MySQL 8 converts a
		// MYSQL_TYPE_STRING parameter of a statement reprepared after its
		// table was recreated in the character set of the old column,
		// truncating binary data at the first invalid byte.
		return append(types, myTypeBlob, 0), appendLenEncBytes(values, v), nil
	case time.Time:
		return append(types, myTypeDatetime, 0), appendDatetime(values, inLocation(v, loc)), nil
	case bool:
		b := byte(0)
		if v {
			b = 1
		}

		return append(types, myTypeTiny, 0), append(values, b), nil
	case int:
		return append(types, myTypeLongLong, 0), appendSigned(values, int64(v), 8), nil
	case int8:
		return append(types, myTypeTiny, 0), appendSigned(values, int64(v), 1), nil
	case int16:
		return append(types, myTypeShort, 0), appendSigned(values, int64(v), 2), nil
	case int32:
		return append(types, myTypeLong, 0), appendSigned(values, int64(v), 4), nil
	case int64:
		return append(types, myTypeLongLong, 0), appendSigned(values, v, 8), nil
	case uint:
		return append(types, myTypeLongLong, unsignedParam), le.AppendUint64(values, uint64(v)), nil
	case uint8:
		return append(types, myTypeTiny, unsignedParam), append(values, v), nil
	case uint16:
		return append(types, myTypeShort, unsignedParam), le.AppendUint16(values, v), nil
	case uint32:
		return append(types, myTypeLong, unsignedParam), le.AppendUint32(values, v), nil
	case uint64:
		return append(types, myTypeLongLong, unsignedParam), le.AppendUint64(values, v), nil
	case float32:
		return append(types, myTypeFloat, 0), le.AppendUint32(values, math.Float32bits(v)), nil
	case float64:
		return append(types, myTypeDouble, 0), le.AppendUint64(values, math.Float64bits(v)), nil
	default:
		return types, values, unsupportedArg(reflect.TypeOf(v).Name())
	}
}

// appendSigned appends the low size bytes of v in two's complement, the
// wire format of signed integers.
func appendSigned(dst []byte, v int64, size int) []byte {
	var b [8]byte

	binary.LittleEndian.PutUint64(b[:], uint64(v)) //nolint:gosec // G115: two's complement reinterpretation is intended

	return append(dst, b[:size]...)
}

// appendDatetime appends a MYSQL_TYPE_DATETIME value in its shortest form:
// 0 (zero time), 4 (date), 7 (date and time) or 11 bytes (with microseconds).
func appendDatetime(dst []byte, t time.Time) []byte {
	if t.IsZero() {
		return append(dst, 0)
	}

	year, month, day := t.Date()
	hour, minute, second := t.Clock()
	micro := t.Nanosecond() / 1e3

	size := byte(11)

	switch {
	case micro != 0:
	case hour == 0 && minute == 0 && second == 0:
		size = 4
	default:
		size = 7
	}

	dst = append(dst, size)
	dst = binary.LittleEndian.AppendUint16(dst, uint16(year)) //nolint:gosec // G115: MySQL years are 0..9999
	dst = append(dst, byte(month), byte(day))                 //nolint:gosec // G115: 1..12, 1..31
	if size == 4 {
		return dst
	}

	dst = append(dst, byte(hour), byte(minute), byte(second)) //nolint:gosec // G115: clock values fit a byte
	if size == 7 {
		return dst
	}

	return binary.LittleEndian.AppendUint32(dst, uint32(micro)) //nolint:gosec // G115: 0..999999
}
