package gomysql

import (
	"bytes"
	"database/sql"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"time"
)

const comQuery byte = 0x03

func (conn *Conn) Query(query string, args ...any) (rows *RowsText, err error) {
	if len(args) > 0 {
		query, err = encodeQuery(query, args...)
		if err != nil {
			return rows, err
		}
	}

	err = conn.writePacketComStr(comQuery, query)
	if err != nil {
		return rows, err
	}

	buf, err := conn.readPacket()
	if err != nil {
		return rows, err
	}

	switch buf[0] {
	case iERR:
		err = conn.processERRPacket(buf)
		return rows, err

	case iOK:
		rows = &RowsText{conn: conn, isStopped: true}
		conn.processOKPacket(buf)
		return rows, err

	case iLocalInfile:
		err = errLocalInfile
		return rows, err

	// ProtocolText::ResultSet
	default:
		rows = new(RowsText)
		rows.conn = conn

		// column count, then the column definitions; the rows follow
		num, _ := lenDecInt(buf)
		rows.columns = make([]ColumnDefinition, num)
		err = conn.readColumns(rows.columns)

		return rows, err
	}
}

func (conn *Conn) QueryRow(query string, args ...any) (row *RowText) {
	row = new(RowText)

	row.rows, row.err = conn.Query(query, args...)
	if row.err != nil {
		return row
	}

	if !row.rows.Next() {
		if row.err = row.rows.Err(); row.err != nil {
			return row
		}
		row.err = sql.ErrNoRows
		return row
	}

	return row
}

type Result struct {
	errLast      error
	lastInsertId uint64
	rowsAffected uint64
}

func (r *Result) LastInsertId() (int64, error) {
	return int64(r.lastInsertId), r.errLast //nolint:gosec // G115: database/sql's Result uses int64
}

func (r *Result) RowsAffected() (int64, error) {
	return int64(r.rowsAffected), r.errLast //nolint:gosec // G115: database/sql's Result uses int64
}

func (conn *Conn) Exec(query string, args ...any) (result *Result, err error) {
	if len(args) > 0 {
		query, err = encodeQuery(query, args...)
		if err != nil {
			return result, err
		}
	}

	err = conn.writePacketComStr(comQuery, query)
	if err != nil {
		return result, err
	}

	buf, err := conn.readPacket()
	if err != nil {
		return result, err
	}

	result = new(Result)

	switch buf[0] {
	case iERR:
		err = conn.processERRPacket(buf)
		result.errLast = err
		return result, err

	case iOK:
		conn.processOKPacket(buf)
		result.rowsAffected = conn.rowsAffected
		result.lastInsertId = conn.lastInsertId
		return result, err

	case iLocalInfile:
		err = errLocalInfile
		return result, err

	// ProtocolText::ResultSet
	default:
		// number of columns in the resultSet
		num, _ := lenDecInt(buf)

		// the columns definition
		for range num {
			_, err = conn.readPacket()
			if err != nil {
				result.errLast = err
				return result, err
			}
		}

		if conn.capFlags&clientDeprecateEOF == 0 {
			buf, err = conn.readPacket()
			if err != nil {
				return result, err
			}
			if buf[0] != iEOF {
				err = errUnexpectedPacketIdentity(buf[0])
				return result, err
			}
		}

		// the rows
		for {
			buf, err = conn.readPacket()
			if err != nil {
				conn.errLast = err
				return result, err
			}

			switch buf[0] {
			case iEOF: // EOF, or OK with the EOF header under CLIENT_DEPRECATE_EOF
				return result, err
			case iERR:
				conn.errLast = conn.processERRPacket(buf)
				return result, err
			default:
				continue
			}
		}
	}
}

func encodeQuery(query string, args ...any) (s string, err error) {
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
			s = strings.Replace(s, "?", quoteString(a.Format(timeFormat)), 1)
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
