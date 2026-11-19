package gomysql

import (
	"errors"
	"reflect"
	"time"
)

// RowsText is the result of Conn.Query, read in the text protocol.
type RowsText struct {
	resultRows
}

// Next reads the next row for Scan. It returns false at the end of the rows
// or on an error, which Err returns.
func (rows *RowsText) Next() bool { return rows.next() }

// Err returns the error that ended the rows, if any.
func (rows *RowsText) Err() error { return rows.err }

// Close reads the rows left and the results that follow, so that the
// connection can run the next command. It returns the error that ended
// the rows, if any.
func (rows *RowsText) Close() error { return rows.close() }

func (rows *RowsText) Scan(dest ...any) error {
	if len(rows.columns) != len(dest) {
		return errors.New("wrong dest count")
	}

	buf := rows.buf
	var pos int

	for i := range dest {
		if isNull(buf[pos]) {
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
