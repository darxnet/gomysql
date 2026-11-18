package gomysql

import (
	"errors"
	"io"
	"reflect"
	"time"
)

type RowsText struct {
	conn    *Conn
	errLast error

	columns []ColumnDefinition

	buf       []byte
	isStopped bool
}

func (rows *RowsText) Close() error {
	if rows.errLast != nil {
		return rows.errLast
	}
	if !rows.isStopped {
		rows.errLast = rows.conn.readUntilEOF()
		rows.isStopped = true
	}
	if rows.errLast == nil {
		rows.errLast = rows.conn.readMoreResults()
	}

	return rows.errLast
}

func (rows *RowsText) Err() error {
	if rows.errLast != nil && !errors.Is(rows.errLast, io.EOF) {
		return rows.errLast
	}

	return nil
}

func (rows *RowsText) Next() bool {
	if rows.isStopped {
		return false
	}

	buf, err := rows.conn.readPacket()
	if err != nil {
		rows.errLast = err
		rows.isStopped = true
		return false
	}

	switch buf[0] {
	case iEOF: // EOF, or OK with the EOF header under CLIENT_DEPRECATE_EOF
		rows.conn.processEOFPacket(buf)
		rows.isStopped = true
		return false
	case iERR:
		rows.errLast = rows.conn.processERRPacket(buf)
		rows.isStopped = true
		return false
	default:
		rows.buf = buf
		return true
	}
}

func (rows *RowsText) Scan(dest ...any) error {
	if len(rows.columns) != len(dest) {
		return errors.New("wrong dest count")
	}

	buf := rows.buf
	var pos int

	for i := range dest {
		if isNUll(buf[pos]) {
			continue
		}

		b, posN := lenDecString(buf[pos:])
		pos += posN

		switch d := dest[i].(type) {
		case nil:
			return errors.New("dest type error: nil")
		case *any:
			*d = string(b)
			continue
		case *string:
			*d = string(b)
			continue
		case *[]byte:
			if len(*d) < len(b) {
				*d = make([]byte, len(b))
			}
			copy(*d, b)
			continue
		case *time.Time:
			tmp, err := time.Parse(timeFormat, string(b))
			if err != nil {
				return err
			}
			*d = tmp
			continue
		case *int:
			*d = parseInt[int](b)
			continue
		case *int8:
			*d = parseInt[int8](b)
			continue
		case *int16:
			*d = parseInt[int16](b)
			continue
		case *int32:
			*d = parseInt[int32](b)
			continue
		case *int64:
			*d = parseInt[int64](b)
			continue
		case *uint:
			*d = parseUint[uint](b)
			continue
		case *uint8:
			*d = parseUint[uint8](b)
			continue
		case *uint16:
			*d = parseUint[uint16](b)
			continue
		case *uint32:
			*d = parseUint[uint32](b)
			continue
		case *uint64:
			*d = parseUint[uint64](b)
			continue
		default:
			return errScanUnsupportedDest(reflect.TypeOf(d).Name())
		}
	}

	return nil
}

type RowText struct {
	err  error
	rows *RowsText
}

func (r *RowText) Scan(dest ...any) error {
	if r.err != nil {
		if r.rows != nil {
			_ = r.rows.Close()
		}
		return r.err
	}

	err := r.rows.Scan(dest...)
	if err != nil {
		_ = r.rows.Close()
		return err
	}

	return r.rows.Close()
}
