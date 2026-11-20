package gomysql

import (
	"database/sql"
	"database/sql/driver"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"reflect"
	"strconv"
	"strings"
	"time"
)

var valuerType = reflect.TypeFor[driver.Valuer]()

// convertArg turns an argument into one of the types the protocols send:
// nil, an integer, a float, bool, string, []byte or time.Time. As in
// database/sql, a driver.Valuer gives its value, a pointer the value it
// points to (nil is NULL), and a type based on a basic type is converted
// to it. json.RawMessage is a string.
func convertArg(arg any) (any, error) {
	switch b := arg.(type) {
	case []byte:
		if b == nil {
			return nil, nil //nolint:nilnil // NULL
		}
	case json.RawMessage:
		// Text: MySQL refuses JSON from a binary string.
		if b == nil {
			return nil, nil //nolint:nilnil // NULL
		}

		return string(b), nil
	}

	switch v := arg.(type) {
	case sql.NamedArg:
		return nil, fmt.Errorf("gomysql: sql.Named %q among positional arguments", v.Name)
	case nil, int, int8, int16, int32, int64, uint, uint8, uint16, uint32, uint64,
		float32, float64, bool, string, []byte, time.Time:
		return arg, nil
	}

	rv := reflect.ValueOf(arg)

	if valuer, ok := arg.(driver.Valuer); ok {
		// A nil pointer to a type with a value receiver is NULL, as in
		// database/sql: calling Value would panic.
		if rv.Kind() == reflect.Pointer && rv.IsNil() && rv.Type().Elem().Implements(valuerType) {
			return nil, nil //nolint:nilnil // NULL
		}

		v, err := valuer.Value()
		if err != nil {
			return nil, err
		}

		if _, ok := v.(driver.Valuer); ok {
			return nil, fmt.Errorf("gomysql: Value of %T returned a driver.Valuer", arg)
		}

		return convertArg(v)
	}

	// The other kinds are unsupported.
	//
	//exhaustive:ignore
	switch rv.Kind() {
	case reflect.Pointer:
		if rv.IsNil() {
			return nil, nil //nolint:nilnil // NULL
		}

		return convertArg(rv.Elem().Interface())
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		return rv.Int(), nil
	case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64:
		return rv.Uint(), nil
	case reflect.Float32, reflect.Float64:
		return rv.Float(), nil
	case reflect.Bool:
		return rv.Bool(), nil
	case reflect.String:
		return rv.String(), nil
	case reflect.Slice:
		if rv.Type().Elem().Kind() == reflect.Uint8 {
			return rv.Bytes(), nil
		}
	}

	return nil, fmt.Errorf("gomysql: unsupported argument type %T", arg)
}

func errArgCount(placeholders, args int) error {
	return fmt.Errorf("gomysql: %d placeholders in the query, %d arguments", placeholders, args)
}

// interpolate replaces the placeholders of query with args as SQL
// literals, for the text protocol. A ? or :name is a placeholder where the
// server would see one in a prepared statement: not in a string, a quoted
// identifier or a comment. A slice argument becomes a list.
func (conn *Conn) interpolate(query string, args []any) (string, error) {
	var b binder
	if err := b.init(args); err != nil {
		return "", err
	}

	var err error

	e := escaper{
		loc:         conn.config.Location,
		noBackslash: conn.statusFlags&serverStatusNoBackslashEscapes != 0,
		unsafe:      unsafeCollation(conn.config.Collation),
		collation:   conn.config.Collation,
	}

	buf := make([]byte, 0, len(query)+16*len(args))

	for i := 0; i < len(query); {
		// Copy what can start neither a placeholder nor what skipNonCode
		// skips at once.
		j := i
		for j < len(query) && !special[query[j]] {
			j++
		}

		if j > i {
			buf = append(buf, query[i:j]...)
			i = j

			continue
		}

		end := skipNonCode(query, i, !e.noBackslash)
		if end > i {
			buf = append(buf, query[i:end]...)
			i = end

			continue
		}

		var arg any

		switch end = namedEnd(query, i); {
		case query[i] == '?':
			var (
				n  int
				ok bool
			)

			if n, ok, err = b.positional(); err != nil {
				return "", err
			} else if !ok {
				return "", errArgCount(countPlaceholders(query, !e.noBackslash), len(args))
			}

			arg = args[n]

			end = i + 1
		case end > i:
			if arg, err = b.named(query[i+1 : end]); err != nil {
				return "", err
			}
		default:
			buf = append(buf, query[i])
			i++

			continue
		}

		if buf, err = e.appendArg(buf, arg); err != nil {
			if query[i] == '?' {
				return "", fmt.Errorf("argument %d: %w", b.next-1, err)
			}

			return "", fmt.Errorf("%s: %w", query[i:end], err)
		}

		i = end
	}

	if err := b.done(query, !e.noBackslash); err != nil {
		return "", err
	}

	return string(buf), nil
}

// special marks the bytes that start a placeholder, a string, a quoted
// identifier or a comment.
var special = [256]bool{'?': true, ':': true, '\'': true, '"': true, '`': true, '#': true, '-': true, '/': true}

// skipNonCode returns the end of the string, quoted identifier or comment
// that starts at query[i], or i if none starts there. Versioned comments
// /*!...*/ are code to MySQL.
func skipNonCode(query string, i int, backslash bool) int {
	switch c := query[i]; {
	case c == '\'' || c == '"':
		return skipQuoted(query, i, backslash)
	case c == '`':
		return skipQuoted(query, i, false)
	case c == '#':
		return skipLine(query, i)
	case c == '-' && strings.HasPrefix(query[i:], "--") && (len(query) == i+2 || query[i+2] <= ' ' || query[i+2] == 0x7f):
		return skipLine(query, i)
	case c == '/' && strings.HasPrefix(query[i:], "/*") && !strings.HasPrefix(query[i:], "/*!"):
		if end := strings.Index(query[i+2:], "*/"); end >= 0 {
			return i + 2 + end + 2
		}

		return len(query)
	}

	return i
}

// skipQuoted returns the end of the quoted string at query[i]: a doubled
// quote is a quote, and so is one after a backslash if backslash.
func skipQuoted(query string, i int, backslash bool) int {
	q := query[i]

	for j := i + 1; j < len(query); j++ {
		switch query[j] {
		case '\\':
			if backslash {
				j++
			}
		case q:
			if j+1 < len(query) && query[j+1] == q {
				j++

				continue
			}

			return j + 1
		}
	}

	return len(query)
}

func skipLine(query string, i int) int {
	if end := strings.IndexByte(query[i:], '\n'); end >= 0 {
		return i + end + 1
	}

	return len(query)
}

// countPlaceholders counts the placeholders of query, for errors.
func countPlaceholders(query string, backslash bool) int {
	n := 0

	for i := 0; i < len(query); {
		if end := skipNonCode(query, i, backslash); end > i {
			i = end

			continue
		}

		if query[i] == '?' {
			n++
		}

		i++
	}

	return n
}

// unsafeCollation reports whether a collation's character set has
// multibyte characters whose second byte can be a backslash or a quote,
// which escaping with a backslash cannot handle.
func unsafeCollation(name string) bool {
	for _, cs := range []string{"big5_", "cp932_", "gbk_", "gb18030_", "sjis_"} {
		if strings.HasPrefix(strings.ToLower(name), cs) {
			return true
		}
	}

	return false
}

// escaper writes arguments as SQL literals.
type escaper struct {
	loc         *time.Location
	noBackslash bool // NO_BACKSLASH_ESCAPES: only quotes are escaped, by doubling them
	unsafe      bool // strings cannot be escaped in the collation
	collation   string
}

// appendArg appends arg as an SQL literal, and a slice as a list of them.
func (e *escaper) appendArg(buf []byte, arg any) ([]byte, error) {
	list, ok := expandable(arg)
	if !ok {
		return e.appendConverted(buf, arg)
	}

	if list.Len() == 0 {
		return buf, errEmptySlice
	}

	for i := range list.Len() {
		if i > 0 {
			buf = append(buf, ", "...)
		}

		var err error
		if buf, err = e.appendConverted(buf, list.Index(i).Interface()); err != nil {
			return buf, fmt.Errorf("element %d: %w", i, err)
		}
	}

	return buf, nil
}

func (e *escaper) appendConverted(buf []byte, arg any) ([]byte, error) {
	v, err := convertArg(arg)
	if err != nil {
		return buf, err
	}

	switch v.(type) {
	case string, []byte:
		if e.unsafe {
			return buf, fmt.Errorf("gomysql: strings cannot be escaped safely in the collation %s; "+
				"use Prepare", e.collation)
		}
	}

	return e.appendLiteral(buf, v)
}

var errNonFinite = errors.New("gomysql: MySQL has no NaN or infinite floats")

// appendLiteral appends arg, a value of convertArg, as an SQL literal.
func (e *escaper) appendLiteral(buf []byte, arg any) ([]byte, error) {
	switch v := arg.(type) {
	case nil:
		return append(buf, "NULL"...), nil
	case int:
		return strconv.AppendInt(buf, int64(v), 10), nil
	case int8:
		return strconv.AppendInt(buf, int64(v), 10), nil
	case int16:
		return strconv.AppendInt(buf, int64(v), 10), nil
	case int32:
		return strconv.AppendInt(buf, int64(v), 10), nil
	case int64:
		return strconv.AppendInt(buf, v, 10), nil
	case uint:
		return strconv.AppendUint(buf, uint64(v), 10), nil
	case uint8:
		return strconv.AppendUint(buf, uint64(v), 10), nil
	case uint16:
		return strconv.AppendUint(buf, uint64(v), 10), nil
	case uint32:
		return strconv.AppendUint(buf, uint64(v), 10), nil
	case uint64:
		return strconv.AppendUint(buf, v, 10), nil
	case float32:
		return appendFloat(buf, float64(v), 32)
	case float64:
		return appendFloat(buf, v, 64)
	case bool:
		if v {
			return append(buf, '1'), nil
		}

		return append(buf, '0'), nil
	case string:
		return appendQuoted(buf, v, e.noBackslash), nil
	case []byte:
		if v == nil {
			return append(buf, "NULL"...), nil
		}

		return appendQuoted(append(buf, "_binary"...), v, e.noBackslash), nil
	case time.Time:
		return appendTimeLiteral(buf, inLocation(v, e.loc)), nil
	}

	return buf, fmt.Errorf("gomysql: unsupported argument type %T", arg)
}

func appendFloat(buf []byte, f float64, bits int) ([]byte, error) {
	if math.IsNaN(f) || math.IsInf(f, 0) {
		return buf, errNonFinite
	}

	return strconv.AppendFloat(buf, f, 'g', -1, bits), nil
}

// appendQuoted appends s quoted, escaping what the server would read as
// the end of the string or as an escape.
func appendQuoted[T string | []byte](buf []byte, s T, noBackslash bool) []byte {
	buf = append(buf, '\'')

	for i := range len(s) {
		c := s[i]

		if noBackslash {
			if c == '\'' {
				buf = append(buf, '\'')
			}

			buf = append(buf, c)

			continue
		}

		switch c {
		case 0:
			buf = append(buf, '\\', '0')
		case '\n':
			buf = append(buf, '\\', 'n')
		case '\r':
			buf = append(buf, '\\', 'r')
		case 0x1a:
			buf = append(buf, '\\', 'Z')
		case '\'', '"', '\\':
			buf = append(buf, '\\', c)
		default:
			buf = append(buf, c)
		}
	}

	return append(buf, '\'')
}

// appendTimeLiteral appends t's wall clock as 'YYYY-MM-DD HH:MM:SS' with
// microseconds if any; the zero time is the zero date.
func appendTimeLiteral(buf []byte, t time.Time) []byte {
	if t.IsZero() {
		return append(buf, "'0000-00-00'"...)
	}

	buf = append(buf, '\'')
	buf = t.AppendFormat(buf, "2006-01-02 15:04:05")

	if micro := t.Nanosecond() / 1000; micro != 0 {
		buf = appendFraction(buf, uint32(micro), 6) //nolint:gosec // G115: < 1e6
	}

	return append(buf, '\'')
}
