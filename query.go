package gomysql

import (
	"bytes"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"time"
)

const comQuery byte = 0x03

// Query runs query, with args interpolated, in the text protocol and
// returns its rows. A statement without a result set has no rows.
func (conn *Conn) Query(query string, args ...any) (*Rows, error) {
	if len(args) > 0 {
		var err error

		query, err = encodeQuery(conn.config.Location, query, args...)
		if err != nil {
			return nil, err
		}
	}

	if err := writeCommand(conn, comQuery, query); err != nil {
		return nil, err
	}

	n, err := conn.readResultHeader()
	if err != nil {
		return nil, err
	}

	// A statement without a result set, such as DO 1, has no rows.
	rows := &Rows{conn: conn, done: n == 0, columns: make([]columnDefinition, n), loc: conn.config.Location}

	if err := conn.readColumns(rows.columns); err != nil {
		return nil, err
	}

	return rows, nil
}

// QueryRow runs query like Query and returns the first row; Row.Scan
// reads it and closes the rows.
func (conn *Conn) QueryRow(query string, args ...any) *Row {
	rows, err := conn.Query(query, args...)

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

// Exec runs query, with args interpolated, and returns its result. Rows it
// returns are skipped.
func (conn *Conn) Exec(query string, args ...any) (*Result, error) {
	if len(args) > 0 {
		var err error

		query, err = encodeQuery(conn.config.Location, query, args...)
		if err != nil {
			return nil, err
		}
	}

	if err := writeCommand(conn, comQuery, query); err != nil {
		return nil, err
	}

	return conn.readResult()
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

func encodeQuery(loc *time.Location, query string, args ...any) (s string, err error) {
	if strings.Count(query, "?") != len(args) {
		return "", errors.New("wrong args count")
	}

	s = query
	for i := range args {
		switch a := args[i].(type) {
		case nil:
			s = strings.Replace(s, "?", "NULL", 1)
			continue
		case string:
			s = strings.Replace(s, "?", quoteString(encodeQueryString(a)), 1)
			continue
		case []byte:
			s = strings.Replace(s, "?", quoteString(encodeQueryString(string(a))), 1)
			continue
		case time.Time:
			s = strings.Replace(s, "?", quoteString(inLocation(a, loc).Format(timeFormat)), 1)
			continue
		case int, int8, int16, int32, int64, uint, uint8, uint16, uint32, uint64:
			s = strings.Replace(s, "?", fmt.Sprintf("%d", a), 1)
			continue
		case float32, float64:
			s = strings.Replace(s, "?", fmt.Sprintf("%f", a), 1)
			continue
		}

		if v, ok := args[i].(fmt.Stringer); ok {
			s = strings.Replace(s, "?", encodeQueryString(v.String()), 1)
			continue
		}

		refArg := reflect.ValueOf(args[i])

		if refArg.Kind() == reflect.Pointer {
			refArg = reflect.Indirect(refArg)
		}

		dest := ""
		refDest := reflect.ValueOf(&dest).Elem()
		if refArg.Type().ConvertibleTo(refDest.Type()) {
			refDest.Set(refArg.Convert(refDest.Type()))
			s = strings.Replace(s, "?", dest, 1)
			continue
		}

		return "", errors.New("non basic type in query args dont implement fmt.Stringer interface")
	}

	return s, err
}

func encodeQueryString(s string) string {
	// https://dev.mysql.com/doc/refman/5.7/en/string-literals.html
	buf := bytes.NewBuffer(nil)
	buf.WriteByte('\'')
	for i := range len(s) {
		switch s[i] {
		case 0:
			buf.WriteString(`\0`)
		case '\'':
			buf.WriteString(`\'`)
		case '"':
			buf.WriteString(`\"`)
		case '\b':
			buf.WriteString(`\b`)
		case '\n':
			buf.WriteString(`\n`)
		case '\r':
			buf.WriteString(`\r`)
		case '\t':
			buf.WriteString(`\t`)
		case 26:
			buf.WriteString(`\Z`)
		case '\\':
			buf.WriteString(`\\`)
		default:
			buf.WriteByte(s[i])
		}
	}
	buf.WriteByte('\'')
	return buf.String()
}

func quoteString(s string) string {
	return "`" + s + "`"
}
