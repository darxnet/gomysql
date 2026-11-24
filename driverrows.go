package gomysql

import (
	"database/sql/driver"
	"errors"

	"github.com/darxnet/gomysql/internal/sqlhook"
)

//nolint:gochecknoinits // gives sqldriver the unexported parts without exporting them
func init() {
	sqlhook.Values = func(rows any, dest []driver.Value) error {
		return rows.(*Rows).driverValues(dest) //nolint:forcetypeassert // sqldriver passes its *Rows
	}

	sqlhook.ScanColumn = func(rows any, i int, dest any) (driver.Value, bool, error) {
		return rows.(*Rows).scanColumn(i, dest) //nolint:forcetypeassert // sqldriver passes its *Rows
	}

	sqlhook.Broken = func(conn any) bool {
		state := conn.(*Conn).state //nolint:forcetypeassert // sqldriver passes its *Conn

		return state == stateBroken || state == stateClosed
	}

	sqlhook.RefusedReadOnly = func(err error) bool {
		var serverErr *Error

		return errors.As(err, &serverErr) && readOnly(serverErr)
	}
}

// rowCursor walks the columns of the current row.
type rowCursor struct {
	buf   []byte
	nulls []byte // binary protocol
	i     int
}

// startRow returns a cursor at the first column of the current row.
func (rows *Rows) startRow() (rowCursor, error) {
	if !rows.binary {
		return rowCursor{buf: rows.buf}, nil
	}

	nullsLen := (len(rows.columns) + 7 + 2) / 8
	if len(rows.buf) < 1+nullsLen || rows.buf[0] != iOK {
		return rowCursor{}, malformed("binary row")
	}

	return rowCursor{buf: rows.buf[1+nullsLen:], nulls: rows.buf[1 : 1+nullsLen]}, nil
}

// nextValue decodes the column of c into v and moves c to the next one.
func (rows *Rows) nextValue(c *rowCursor, v *value) error {
	col := &rows.columns[c.i]
	buf := c.buf

	if rows.binary {
		bit := c.i + 2
		c.i++

		switch {
		case c.nulls[bit/8]&(1<<(bit%8)) != 0:
			v.kind = kindNull
		case intSize(col.Type) > 0:
			n := intSize(col.Type)
			if len(buf) < n {
				return malformed("binary row")
			}

			v.setInt(binaryInt(buf, n, col.Flags&flagUnsigned == 0))
			c.buf = buf[n:]
		default:
			d := decoder{buf: buf}
			v.readBinary(&d, col, &rows.text)

			if d.bad {
				return malformed("binary row")
			}

			c.buf = d.buf
		}

		return nil
	}

	c.i++

	switch {
	case len(buf) > 0 && buf[0] < 0xfb:
		n := int(buf[0]) + 1
		if len(buf) < n {
			return malformed("text row")
		}

		v.kind, v.b, c.buf = kindBytes, buf[1:n:n], buf[n:]
	case len(buf) > 0 && buf[0] == 0xfb:
		v.kind, c.buf = kindNull, buf[1:]
	default:
		d := decoder{buf: buf}
		v.kind, v.b = kindBytes, d.lenEncString()

		if d.bad {
			return malformed("text row")
		}

		c.buf = d.buf
	}

	return nil
}

// malformed breaks the connection after err, a row that does not decode:
// sqldriver then drops it, as it would after a failed Next.
func (rows *Rows) malformed(err error) error {
	rows.conn.breakWith(err)

	return err
}

// driverValues scans the current row into dest for sqldriver: the values
// of refDriverValue through typed.
func (rows *Rows) driverValues(dest []driver.Value) error {
	c, err := rows.startRow()
	if err != nil {
		return rows.malformed(err)
	}

	v := value{loc: rows.loc}

	for i := range dest {
		if err := rows.nextValue(&c, &v); err != nil {
			return rows.malformed(err)
		}

		col := &rows.columns[i]
		v.typed(col)
		dest[i] = v.refDriverValue(col)
	}

	return nil
}
