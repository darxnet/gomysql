package gomysql

import (
	"bytes"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"reflect"
	"strconv"
	"time"
)

var (
	errScanNull        = errors.New("converting NULL is unsupported; scan into a pointer, an sql.Null* type or any")
	errScanSyntax      = errors.New("invalid syntax")
	errScanRange       = errors.New("value out of range")
	errScanConvert     = errors.New("cannot convert the column type")
	errScanUnsupported = errors.New("unsupported destination type")
	errScanTime        = errors.New("TIME goes into a string or []byte, not time.Duration")
)

func scanError(i int, col *columnDefinition, dest any, err error) error {
	return fmt.Errorf("gomysql: Scan error on column index %d, name %q, into %T: %w", i, col.Name, dest, err)
}

// assign stores v into dest with the conversions of database/sql.
//
//nolint:gocyclo,cyclop // one case per destination type
func assign(dest any, v *value, col *columnDefinition) error {
	if col.Type == myTypeBit && v.kind == kindBytes {
		v.bitToUint(dest)
	}

	switch d := dest.(type) {
	case *string:
		switch v.kind {
		case kindBytes:
			*d = string(v.b)
		case kindNull:
			return errScanNull
		default:
			var buf [32]byte
			*d = string(v.appendText(buf[:0], col))
		}
	case *[]byte:
		*d = v.bytes(col)
	case *sql.RawBytes:
		switch v.kind {
		case kindNull:
			*d = nil
		case kindBytes:
			*d = v.b
		default:
			*d = v.appendText((*d)[:0], col)
		}
	case *any:
		*d = v.driverValue(col)
	case *bool:
		return setBool(d, v)
	case *int:
		return setSigned(d, v)
	case *int8:
		return setSigned(d, v)
	case *int16:
		return setSigned(d, v)
	case *int32:
		return setSigned(d, v)
	case *int64:
		return setSigned(d, v)
	case *uint:
		return setUnsigned(d, v)
	case *uint8:
		return setUnsigned(d, v)
	case *uint16:
		return setUnsigned(d, v)
	case *uint32:
		return setUnsigned(d, v)
	case *uint64:
		return setUnsigned(d, v)
	case *float64:
		f, err := v.float(64)
		if err != nil {
			return err
		}

		*d = f
	case *float32:
		f, err := v.float(32)
		if err != nil {
			return err
		}

		*d = float32(f)
	case *time.Duration:
		if col.Type == myTypeTime || col.Type == myTypeTime2 {
			return errScanTime
		}

		return setSigned(d, v)
	case *time.Time:
		t, err := v.time(col)
		if err != nil {
			return err
		}

		*d = t
	case sql.Scanner:
		return d.Scan(v.driverValue(col))
	default:
		return assignReflect(dest, v, col)
	}

	return nil
}

// assignReflect handles pointers to pointers and to types based on the
// basic ones, such as type ID int64.
func assignReflect(dest any, v *value, col *columnDefinition) error {
	rv := reflect.ValueOf(dest)
	if rv.Kind() != reflect.Pointer || rv.IsNil() {
		return errScanUnsupported
	}

	e := rv.Elem()

	if isJSONDest(e.Type(), col) {
		if v.kind == kindNull {
			return errScanNull
		}

		if err := json.Unmarshal(v.b, dest); err != nil {
			return fmt.Errorf("%w: %w", errScanSyntax, err)
		}

		return nil
	}

	// The other kinds are unsupported.
	//
	//exhaustive:ignore
	switch e.Kind() {
	case reflect.Pointer:
		if v.kind == kindNull {
			e.SetZero()

			return nil
		}

		p := reflect.New(e.Type().Elem())
		if err := assign(p.Interface(), v, col); err != nil {
			return err
		}

		e.Set(p)
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		n, err := v.int64()
		if err != nil {
			return err
		}

		if e.OverflowInt(n) {
			return errScanRange
		}

		e.SetInt(n)
	case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64:
		n, err := v.uint64()
		if err != nil {
			return err
		}

		if e.OverflowUint(n) {
			return errScanRange
		}

		e.SetUint(n)
	case reflect.Float32, reflect.Float64:
		f, err := v.float(e.Type().Bits())
		if err != nil {
			return err
		}

		e.SetFloat(f)
	case reflect.Bool:
		var b bool
		if err := setBool(&b, v); err != nil {
			return err
		}

		e.SetBool(b)
	case reflect.String:
		var s string
		if err := assign(&s, v, col); err != nil {
			return err
		}

		e.SetString(s)
	case reflect.Slice:
		if !reflect.TypeFor[[]byte]().ConvertibleTo(e.Type()) {
			return errScanUnsupported
		}

		e.Set(reflect.ValueOf(v.bytes(col)).Convert(e.Type()))
	default:
		return errScanUnsupported
	}

	return nil
}

// isJSONDest reports whether a destination of type t is filled by
// json.Unmarshal: a struct, map, array or slice other than bytes, for JSON
// and text columns. MariaDB sends JSON as LONGTEXT.
func isJSONDest(t reflect.Type, col *columnDefinition) bool {
	//exhaustive:ignore
	switch t.Kind() {
	case reflect.Struct, reflect.Map:
		return isText(col)
	case reflect.Array, reflect.Slice:
		return t.Elem().Kind() != reflect.Uint8 && isText(col)
	}

	return false
}

type signed interface {
	~int | ~int8 | ~int16 | ~int32 | ~int64
}

type unsigned interface {
	~uint | ~uint8 | ~uint16 | ~uint32 | ~uint64
}

func setSigned[T signed](d *T, v *value) error {
	n, err := v.int64()
	if err != nil {
		return err
	}

	if int64(T(n)) != n {
		return errScanRange
	}

	*d = T(n)

	return nil
}

func setUnsigned[T unsigned](d *T, v *value) error {
	n, err := v.uint64()
	if err != nil {
		return err
	}

	if uint64(T(n)) != n {
		return errScanRange
	}

	*d = T(n)

	return nil
}

// bitToUint makes a BIT value, big-endian bytes of up to 64 bits, an
// unsigned integer, but for strings and bytes, which get the bytes as the
// server sends them.
func (v *value) bitToUint(dest any) {
	switch dest.(type) {
	case *string, *[]byte, *sql.RawBytes:
		return
	}

	if len(v.b) > 8 {
		return
	}

	var u uint64
	for _, c := range v.b {
		u = u<<8 | uint64(c)
	}

	v.kind, v.i = kindUint, u
}

// setBool accepts 0 and 1, and the strings of strconv.ParseBool.
func setBool(d *bool, v *value) error {
	switch v.kind {
	case kindInt, kindUint:
		if v.i > 1 {
			return errScanRange
		}

		*d = v.i == 1
	case kindBytes:
		b, err := strconv.ParseBool(string(v.b))
		if err != nil {
			return errScanSyntax
		}

		*d = b
	case kindNull:
		return errScanNull
	default:
		return errScanConvert
	}

	return nil
}

func (v *value) int64() (int64, error) {
	switch v.kind {
	case kindInt:
		return int64(v.i), nil //nolint:gosec // G115: int64 bits
	case kindUint:
		if v.i > math.MaxInt64 {
			return 0, errScanRange
		}

		return int64(v.i), nil
	case kindBytes:
		return parseInt64(v.b)
	case kindFloat:
		if v.f != math.Trunc(v.f) || v.f < math.MinInt64 || v.f >= math.MaxInt64 {
			return 0, errScanRange
		}

		return int64(v.f), nil
	case kindNull:
		return 0, errScanNull
	default:
		return 0, errScanConvert
	}
}

func (v *value) uint64() (uint64, error) {
	switch v.kind {
	case kindUint:
		return v.i, nil
	case kindInt:
		if int64(v.i) < 0 { //nolint:gosec // G115: int64 bits
			return 0, errScanRange
		}

		return v.i, nil
	case kindBytes:
		return parseUint64(v.b)
	case kindFloat:
		if v.f != math.Trunc(v.f) || v.f < 0 || v.f >= math.MaxUint64 {
			return 0, errScanRange
		}

		return uint64(v.f), nil
	case kindNull:
		return 0, errScanNull
	default:
		return 0, errScanConvert
	}
}

func (v *value) float(bits int) (float64, error) {
	var f float64

	switch v.kind {
	case kindFloat:
		f = v.f
	case kindInt:
		f = float64(int64(v.i)) //nolint:gosec // G115: int64 bits
	case kindUint:
		f = float64(v.i)
	case kindBytes:
		var err error

		f, err = strconv.ParseFloat(string(v.b), bits)
		if err != nil {
			if errors.Is(err, strconv.ErrRange) {
				return 0, errScanRange
			}

			return 0, errScanSyntax
		}
	case kindNull:
		return 0, errScanNull
	default:
		return 0, errScanConvert
	}

	if bits == 32 && math.Abs(f) > math.MaxFloat32 && !math.IsInf(f, 0) {
		return 0, errScanRange
	}

	return f, nil
}

func (v *value) time(col *columnDefinition) (time.Time, error) {
	switch v.kind {
	case kindTime:
		return v.t.time(v.loc), nil
	case kindBytes:
		if t, ok := parseDateTime(v.b); ok && isDateTime(col) {
			return t.time(v.loc), nil
		}

		return time.Time{}, errScanConvert
	case kindNull:
		return time.Time{}, errScanNull
	default:
		return time.Time{}, errScanConvert
	}
}

// bytes returns a copy of v as text; nil for NULL.
func (v *value) bytes(col *columnDefinition) []byte {
	switch v.kind {
	case kindNull:
		return nil
	case kindBytes:
		return append([]byte{}, v.b...)
	default:
		return v.appendText(nil, col)
	}
}

// appendText formats v like the text protocol would send it.
func (v *value) appendText(dst []byte, col *columnDefinition) []byte {
	switch v.kind {
	case kindBytes:
		return append(dst, v.b...)
	case kindInt:
		return strconv.AppendInt(dst, int64(v.i), 10) //nolint:gosec // G115: int64 bits
	case kindUint:
		return strconv.AppendUint(dst, v.i, 10)
	case kindFloat:
		bits := 64
		if v.f32 {
			bits = 32
		}

		return strconv.AppendFloat(dst, v.f, 'g', -1, bits)
	case kindTime:
		return v.t.appendText(dst, col)
	default:
		return dst
	}
}

// driverValue is what an sql.Scanner gets and an any holds, the same in
// both protocols: nil; int64 for integers but BIGINT UNSIGNED and BIT,
// which are uint64; float32 for FLOAT; float64 for DOUBLE; time.Time for
// DATE, DATETIME and TIMESTAMP; json.RawMessage for JSON; string for text;
// and a copy of the bytes for binary strings, DECIMAL, TIME and the rest.
func (v *value) driverValue(col *columnDefinition) any {
	switch v.kind {
	case kindInt:
		return int64(v.i) //nolint:gosec // G115: int64 bits
	case kindUint:
		if col.Type == myTypeLongLong || col.Type == myTypeBit {
			return v.i
		}

		return int64(v.i) //nolint:gosec // G115: below 1<<32
	case kindFloat:
		if v.f32 {
			return float32(v.f)
		}

		return v.f
	case kindTime:
		return v.t.time(v.loc)
	case kindBytes:
		return v.textDriverValue(col)
	default:
		return nil
	}
}

// refDriverValue is driverValue for sqldriver, of a value through
// parseText: the bytes of text, JSON, binary strings and the rest point
// into the row, as database/sql copies them where it needs.
func (v *value) refDriverValue(col *columnDefinition) any {
	if v.kind == kindBytes {
		return v.b
	}

	return v.driverValue(col)
}

// textDriverValue is driverValue of a value sent as text: in the text
// protocol, all of them.
func (v *value) textDriverValue(col *columnDefinition) any {
	if t := *v; t.parseText(col) {
		return t.driverValue(col)
	}

	switch {
	case col.Type == myTypeJSON:
		return json.RawMessage(bytes.Clone(v.b))
	case isText(col):
		return string(v.b)
	}

	return bytes.Clone(v.b)
}

// parseText turns a number, date or time sent as text into its kind;
// false for the other columns, and for text that does not parse.
func (v *value) parseText(col *columnDefinition) bool {
	b := v.b

	switch col.Type {
	case myTypeLongLong:
		if col.Flags&flagUnsigned != 0 {
			n, err := parseUint64(b)
			if err != nil {
				return false
			}

			v.kind, v.i = kindUint, n

			return true
		}

		fallthrough
	case myTypeTiny, myTypeShort, myTypeInt24, myTypeLong, myTypeYear:
		n, err := parseInt64(b)
		if err != nil {
			return false
		}

		v.kind, v.i = kindInt, uint64(n) //nolint:gosec // G115: int64 bits

		return true
	case myTypeFloat, myTypeDouble:
		bits := 64
		if col.Type == myTypeFloat {
			bits = 32
		}

		f, err := strconv.ParseFloat(string(b), bits)
		if err != nil {
			return false
		}

		v.kind, v.f, v.f32 = kindFloat, f, bits == 32

		return true
	}

	if isDateTime(col) {
		if t, ok := parseDateTime(b); ok {
			v.kind, v.t = kindTime, t

			return true
		}
	}

	return false
}

// parseUint64 parses decimal digits, refusing anything else and overflow.
func parseUint64(b []byte) (uint64, error) {
	if len(b) == 0 {
		return 0, errScanSyntax
	}

	var n uint64

	// Up to 19 digits cannot overflow; only longer numbers are checked.
	for _, c := range b[:min(len(b), 19)] {
		c -= '0'
		if c > 9 {
			return 0, errScanSyntax
		}

		n = n*10 + uint64(c)
	}

	for _, c := range b[min(len(b), 19):] {
		c -= '0'
		if c > 9 {
			return 0, errScanSyntax
		}

		if n > (math.MaxUint64-uint64(c))/10 {
			return 0, errScanRange
		}

		n = n*10 + uint64(c)
	}

	return n, nil
}

// parseInt64 is parseUint64 with an optional sign.
func parseInt64(b []byte) (int64, error) {
	negative := len(b) > 0 && b[0] == '-'
	if negative || len(b) > 0 && b[0] == '+' {
		b = b[1:]
	}

	u, err := parseUint64(b)
	if err != nil {
		return 0, err
	}

	if negative {
		if u > 1<<63 {
			return 0, errScanRange
		}

		return int64(-u), nil //nolint:gosec // G115: two's complement, -(1<<63) included
	}

	if u > math.MaxInt64 {
		return 0, errScanRange
	}

	return int64(u), nil
}
