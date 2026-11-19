package gomysql

import (
	"encoding/binary"
	"errors"
	"math"
	"reflect"
	"time"
)

const comStmtExecute byte = 0x17

// Query executes the prepared statement with args (COM_STMT_EXECUTE) and
// returns its rows.
//
// https://dev.mysql.com/doc/dev/mysql-server/latest/page_protocol_com_stmt_execute.html
func (stmt *Stmt) Query(args ...any) (rows *RowsBinary, err error) {
	if len(stmt.params) != len(args) {
		err = errors.New("wrong args count")
		return rows, err
	}

	// COM_STMT_EXECUTE: command, stmt-id, flags (no cursor), iteration-count 1
	buf := make([]byte, 0, 16)
	buf = append(buf, comStmtExecute)
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

			types, values, err = appendParam(types, values, arg)
			if err != nil {
				return rows, err
			}
		}

		buf = append(buf, types...)
		buf = append(buf, values...)
	}

	stmt.conn.sequence = 0
	stmt.conn.sequenceCompressed = 0
	err = stmt.conn.writePacket(buf)
	if err != nil {
		_ = stmt.conn.conn.Close()
		return rows, err
	}

	buf, err = stmt.conn.readPacket()
	if err != nil {
		return rows, err
	}

	switch buf[0] {
	case iERR:
		err = stmt.conn.processERRPacket(buf)
		return rows, err

	case iOK:
		stmt.conn.processOKPacket(buf)
		return rows, err

	default:
		rows = new(RowsBinary)
		rows.stmt = stmt

		// number of columns in the resultSet
		num, _ := lenDecInt(buf)

		// we have all columns definitions in stmt, so skip it...
		for range num {
			_, err = stmt.conn.readPacket()
			if err != nil {
				return rows, err
			}
		}
		if stmt.conn.capFlags&clientDeprecateEOF == 0 {
			buf, err = stmt.conn.readPacket()
			if err != nil {
				return rows, err
			}
			if buf[0] != iEOF {
				err = errExpectedEOF
				return rows, err
			}
		}

		return rows, err
	}
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
