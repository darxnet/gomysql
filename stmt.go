package gomysql

import (
	"encoding/binary"
	"errors"
	"math"
	"reflect"
	"time"
)

const comStmtClose byte = 0x19

type Stmt struct {
	conn    *Conn
	query   string
	columns []ColumnDefinition
	params  []ColumnDefinition
	id      uint32
}

func (stmt *Stmt) Close() error {
	return writeCommand(stmt.conn, comStmtClose, binary.LittleEndian.AppendUint32(nil, stmt.id))
}

const comStmtPrepare = 0x16

// Prepare prepares query on the server (COM_STMT_PREPARE).
//
// https://dev.mysql.com/doc/dev/mysql-server/latest/page_protocol_com_stmt_prepare.html
func (conn *Conn) Prepare(query string) (*Stmt, error) {
	if err := writeCommand(conn, comStmtPrepare, query); err != nil {
		return nil, err
	}

	buf, err := conn.readPacket()
	if err != nil {
		return nil, err
	}

	switch buf[0] {
	case iOK:
	case iERR:
		return nil, conn.processERRPacket(buf)
	default:
		return nil, errUnexpectedPacketIdentity(buf[0])
	}

	// COM_STMT_PREPARE_OK: 0x00, statement id (4), number of columns (2),
	// number of params (2), filler, warning count (2).
	d := decoder{buf: buf}
	d.skip(1)
	id := d.uint32()
	columns := d.uint16()
	params := d.uint16()

	if err := d.err("COM_STMT_PREPARE_OK"); err != nil {
		return nil, err
	}

	stmt := &Stmt{
		conn:    conn,
		query:   query,
		id:      id,
		columns: make([]ColumnDefinition, columns),
		params:  make([]ColumnDefinition, params),
	}

	if err := conn.readColumns(stmt.params); err != nil {
		return nil, err
	}

	if err := conn.readColumns(stmt.columns); err != nil {
		return nil, err
	}

	return stmt, nil
}

const comStmtExecute byte = 0x17

// Query executes the prepared statement with args (COM_STMT_EXECUTE) and
// returns its rows. A statement without a result set has no rows.
//
// https://dev.mysql.com/doc/dev/mysql-server/latest/page_protocol_com_stmt_execute.html
func (stmt *Stmt) Query(args ...any) (*Rows, error) {
	if err := stmt.execute(args); err != nil {
		return nil, err
	}

	n, err := stmt.conn.readResultHeader()
	if err != nil {
		return nil, err
	}

	if err := stmt.readColumns(n); err != nil {
		return nil, err
	}

	return &Rows{conn: stmt.conn, done: n == 0, columns: stmt.columns, binary: true}, nil
}

// Exec executes the prepared statement with args and returns its result.
// Rows it returns are skipped.
func (stmt *Stmt) Exec(args ...any) (*Result, error) {
	if err := stmt.execute(args); err != nil {
		return nil, err
	}

	return stmt.conn.readResult()
}

// execute sends COM_STMT_EXECUTE.
func (stmt *Stmt) execute(args []any) error {
	if len(stmt.params) != len(args) {
		return errors.New("wrong args count")
	}

	// COM_STMT_EXECUTE after the command byte: stmt-id, flags (no cursor),
	// iteration-count 1
	buf := make([]byte, 0, 16)
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
			if arg == nil {
				buf[posNull+i/8] |= 1 << (i % 8)
			}

			var err error

			types, values, err = appendParam(types, values, arg)
			if err != nil {
				return err
			}
		}

		buf = append(buf, types...)
		buf = append(buf, values...)
	}

	return writeCommand(stmt.conn, comStmtExecute, buf)
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
		stmt.columns = make([]ColumnDefinition, n)

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

const unsignedParam byte = 0x80

// appendParam appends the type (type byte, unsigned flag) and the binary
// value of arg.
func appendParam(types, values []byte, arg any) (newTypes, newValues []byte, err error) {
	le := binary.LittleEndian

	switch v := arg.(type) {
	case nil:
		return append(types, myTypeNull, 0), values, nil
	case string:
		return append(types, myTypeString, 0), appendLenEncString(values, v), nil
	case []byte:
		return append(types, myTypeString, 0), appendLenEncBytes(values, v), nil
	case time.Time:
		return append(types, myTypeDatetime, 0), appendDatetime(values, v), nil
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
		return types, values, errExecuteUnsupportedArg(reflect.TypeOf(v).Name())
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
