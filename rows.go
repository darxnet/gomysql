package gomysql

import (
	"database/sql"
	"database/sql/driver"
	"errors"
	"fmt"
	"math"
	"reflect"
	"time"
)

// Rows is the result of a query: of Conn.Query in the text protocol, of
// Stmt.Query in the binary protocol. It is used like *sql.Rows: Next
// before every Scan, Err after the loop, and Close to free the connection
// for the next command. Until the rows are read to the end or closed, other
// commands of the connection fail with ErrBusy.
type Rows struct {
	conn    *Conn
	columns []columnDefinition
	loc     *time.Location // Config.Location
	buf     []byte         // the current row
	err     error
	done    bool // the rows are read up to the end, or failed
	closed  bool
	binary  bool
	// TIME values of the binary protocol of this row, as text: each its
	// own bytes, for sql.RawBytes. Valid until Next.
	text []byte

	// ScanStruct: the type of the last struct, the field of each column in
	// it, and whether none is behind a pointer; the destinations for Scan,
	// pointers into the struct destOf points to.
	structType reflect.Type
	plan       [][]int
	direct     bool
	dest       []any
	destOf     any

	// sqldriver: the destinations of driverValues, into the dest database/sql
	// passes, which starts at driverOf.
	driverDests []any
	driverOf    *driver.Value
}

// errRowsGone is the error of Rows whose connection was closed or
// reconnected before they were read.
var errRowsGone = fmt.Errorf("%w: the connection of the Rows was closed or reconnected", sql.ErrConnDone)

// openRows makes rows the open Rows of the connection, until they are
// read or closed; Rows without a row and without more results are done at
// once.
func (conn *Conn) openRows(rows *Rows) *Rows {
	conn.rows = rows

	if rows.done && conn.statusFlags&serverStatusMoreResultsExists == 0 {
		rows.err = rows.release(nil)
	}

	return rows
}

// dropRows ends the open Rows, if any, without reading them: the
// connection is closed or replaced.
func (conn *Conn) dropRows() {
	if rows := conn.rows; rows != nil {
		rows.err, rows.buf, rows.done = errRowsGone, nil, true
		conn.rows = nil
	}
}

// release ends the command of the rows: the connection is idle again, or
// broken by err.
func (rows *Rows) release(err error) error {
	rows.conn.rows = nil

	return rows.conn.end(err)
}

// Next reads the next row for Scan. It returns false at the end of the rows
// or on an error, which Err returns.
func (rows *Rows) Next() bool {
	if rows.done {
		return false
	}

	buf, err := rows.conn.readPacket()

	switch {
	case err != nil:
		rows.err = err
	case isEOF(buf): // EOF, or OK with the EOF header under CLIENT_DEPRECATE_EOF
		rows.err = rows.conn.processEOFPacket(buf)
	case buf[0] == iERR:
		rows.err = rows.conn.processERRPacket(buf)
	default:
		rows.buf, rows.text = buf, rows.text[:0]

		return true
	}

	rows.end()

	return false
}

// end ends the rows at their last packet or an error. More results are
// read by Close.
func (rows *Rows) end() {
	rows.buf, rows.done = nil, true

	if rows.err != nil || rows.conn.statusFlags&serverStatusMoreResultsExists == 0 {
		rows.err = rows.release(rows.err)
	}
}

// Err returns the error that ended the rows, if any.
func (rows *Rows) Err() error {
	return rows.err
}

// Close reads the rows left and the results that follow, so that the
// connection can run the next command. It returns the error that ended
// the rows, if any.
func (rows *Rows) Close() error {
	if rows.closed {
		return rows.err
	}

	rows.closed = true
	rows.releaseDest()

	if rows.conn.rows != rows { // read to the end, or the connection is gone
		rows.buf, rows.done = nil, true

		return rows.err
	}

	if !rows.done {
		rows.buf, rows.done = nil, true
		rows.err = rows.conn.readUntilEOF()
	}

	if rows.err == nil {
		rows.err = rows.conn.readMoreResults()
	}

	rows.err = rows.release(rows.err)

	return rows.err
}

// Scan copies the columns of the current row into dest, with the
// conversions of database/sql: a pointer to a string, []byte,
// sql.RawBytes, any, bool, integer, float, time.Time or time.Duration, an
// sql.Scanner such as sql.NullString, a pointer to a pointer (nil for
// NULL), or a pointer to a type based on one of these.
//
// NULL goes only into []byte, sql.RawBytes, any, a pointer to a pointer
// and an sql.Scanner; into anything else it is an error. DATE, DATETIME
// and TIMESTAMP values scanned into time.Time, any or an sql.Scanner are
// time.Time in Config.Location; the zero date is the zero time.Time.
// Scanned into a string they keep the server's format. TIME values go
// into time.Duration, and so do integers, as nanoseconds; BIT values (up
// to 64 bits) go into integers and bool. JSON and text values go into a pointer to a struct, map, array or
// slice through json.Unmarshal.
//
// An any or an sql.Scanner gets the same in both protocols: int64 for
// integers but BIGINT UNSIGNED and BIT, which are uint64; float32 for
// FLOAT; float64 for DOUBLE; time.Time; json.RawMessage for JSON (MariaDB
// sends JSON as LONGTEXT, a string); string for text; and []byte for
// binary strings, DECIMAL, TIME and the rest.
//
// []byte gets a copy; sql.RawBytes points into the row and is valid until
// the next call to Next, Scan or Close.
func (rows *Rows) Scan(dest ...any) error {
	if rows.buf == nil {
		if rows.closed {
			return errors.New("gomysql: Rows are closed")
		}

		return errors.New("gomysql: Scan called without calling Next")
	}

	if len(dest) != len(rows.columns) {
		return fmt.Errorf("gomysql: expected %d destination arguments in Scan, not %d", len(rows.columns), len(dest))
	}

	if rows.binary {
		return rows.scanBinary(dest)
	}

	return rows.scanText(dest)
}

// scanText reads a row of the text protocol: a length-encoded string, or
// 0xfb for NULL, per column.
//
// https://dev.mysql.com/doc/dev/mysql-server/latest/page_protocol_com_query_response_text_resultset_row.html
//
//nolint:gocognit // the fast path of assign is inline: a call per column costs 20%
func (rows *Rows) scanText(dest []any) error {
	buf := rows.buf
	v := value{loc: rows.loc}

	for i := range dest {
		col := &rows.columns[i]

		// A length below 251 takes one byte, the common case; 0xfb is NULL.
		switch {
		case len(buf) > 0 && buf[0] < 0xfb:
			n := int(buf[0]) + 1
			if len(buf) < n {
				return malformed("text row")
			}

			v.kind, v.b, buf = kindBytes, buf[1:n:n], buf[n:]
		case len(buf) > 0 && buf[0] == 0xfb:
			v.kind, buf = kindNull, buf[1:]
		default:
			d := decoder{buf: buf}
			v.kind, v.b = kindBytes, d.lenEncString()

			if d.bad {
				return malformed("text row")
			}

			buf = d.buf
		}

		// The fast path of assign, inline: strings and integers. BIT values
		// are bytes, not digits.
		if v.kind == kindBytes && col.Type != myTypeBit {
			switch p := dest[i].(type) {
			case *string:
				*p = string(v.b)
				continue
			case *int64:
				if n, err := parseInt64(v.b); err == nil {
					*p = n
					continue
				}
			case *uint64:
				if n, err := parseUint64(v.b); err == nil {
					*p = n
					continue
				}
			case *uint32:
				if n, err := parseUint64(v.b); err == nil && n <= math.MaxUint32 {
					*p = uint32(n)
					continue
				}
			case *uint16:
				if n, err := parseUint64(v.b); err == nil && n <= math.MaxUint16 {
					*p = uint16(n)
					continue
				}
			}
		}

		if err := assign(dest[i], &v, col); err != nil {
			return scanError(i, col, dest[i], err)
		}
	}

	return nil
}

// scanBinary reads a row of the binary protocol: a header, a NULL bitmap
// with an offset of 2 bits, and the values that are not NULL, encoded by
// column type.
//
// https://dev.mysql.com/doc/dev/mysql-server/latest/page_protocol_binary_resultset.html
//
//nolint:gocognit // the fast path of assign is inline: a call per column costs 20%
func (rows *Rows) scanBinary(dest []any) error {
	nullsLen := (len(dest) + 7 + 2) / 8
	if len(rows.buf) < 1+nullsLen || rows.buf[0] != iOK {
		return malformed("binary row")
	}

	nulls := rows.buf[1 : 1+nullsLen]

	// buf is a local slice, not a decoder, so that it stays in registers on
	// the integer path; the other types go through a decoder.
	buf := rows.buf[1+nullsLen:]

	v := value{loc: rows.loc}

	for i := range dest {
		col := &rows.columns[i]

		if bit := i + 2; nulls[bit/8]&(1<<(bit%8)) != 0 {
			v.kind = kindNull
		} else if n := intSize(col.Type); n > 0 {
			// Integers are most of bulk reads: into an integer type they
			// skip value and assign.
			if len(buf) < n {
				return malformed("binary row")
			}

			u, signed := binaryInt(buf, n, col.Flags&flagUnsigned == 0)
			buf = buf[n:]

			negative := signed && int64(u) < 0 //nolint:gosec // G115: int64 bits

			switch p := dest[i].(type) {
			case *uint64:
				if !negative {
					*p = u
					continue
				}
			case *int64:
				if negative || u <= math.MaxInt64 {
					*p = int64(u) //nolint:gosec // G115: checked
					continue
				}
			case *uint32:
				if !negative && u <= math.MaxUint32 {
					*p = uint32(u)
					continue
				}
			case *uint16:
				if !negative && u <= math.MaxUint16 {
					*p = uint16(u)
					continue
				}
			}

			v.setInt(u, signed)
		} else {
			d := decoder{buf: buf}
			v.readBinary(&d, col, &rows.text)

			if d.bad {
				return malformed("binary row")
			}

			buf = d.buf
		}

		if err := assign(dest[i], &v, col); err != nil {
			return scanError(i, col, dest[i], err)
		}
	}

	return nil
}

// Row is the result of QueryRow.
type Row struct {
	err  error
	rows *Rows
}

// Err returns the error of the query, if any, without scanning the row,
// like sql.Row.Err.
func (r *Row) Err() error {
	return r.err
}

// Scan copies the columns of the row into dest, like Rows.Scan, and closes
// the rows. Without a row it returns sql.ErrNoRows.
func (r *Row) Scan(dest ...any) error {
	return r.scan(func(rows *Rows) error { return rows.Scan(dest...) })
}

// scan runs scan on the row and closes the rows.
func (r *Row) scan(scan func(*Rows) error) error {
	if r.err != nil {
		return r.err
	}

	defer func() { _ = r.rows.Close() }()

	if !r.rows.Next() {
		if err := r.rows.Err(); err != nil {
			return err
		}

		return sql.ErrNoRows
	}

	if err := scan(r.rows); err != nil {
		return err
	}

	return r.rows.Close()
}
