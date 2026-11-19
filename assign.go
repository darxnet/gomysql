package gomysql

import (
	"bytes"
	"database/sql"
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
)

func scanError(i int, col *ColumnDefinition, dest any, err error) error {
	return fmt.Errorf("gomysql: Scan error on column index %d, name %q, into %T: %w", i, col.Name, dest, err)
}

// assign stores v into dest with the conversions of database/sql.
//
//nolint:gocyclo,cyclop // one case per destination type
func assign(dest any, v *value, col *ColumnDefinition) error {
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
func assignReflect(dest any, v *value, col *ColumnDefinition) error {
	rv := reflect.ValueOf(dest)
	if rv.Kind() != reflect.Pointer || rv.IsNil() {
		return errScanUnsupported
	}

	e := rv.Elem()

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

func (v *value) time(col *ColumnDefinition) (time.Time, error) {
	switch v.kind {
	case kindTime:
		return v.t.time(), nil
	case kindBytes:
		if t, ok := parseDateTime(v.b); ok && isDateTime(col) {
			return t.time(), nil
		}

		return time.Time{}, errScanConvert
	case kindNull:
		return time.Time{}, errScanNull
	default:
		return time.Time{}, errScanConvert
	}
}

// bytes returns a copy of v as text; nil for NULL.
func (v *value) bytes(col *ColumnDefinition) []byte {
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
func (v *value) appendText(dst []byte, col *ColumnDefinition) []byte {
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

// driverValue is what go-sql-driver with parseTime passes to an
// sql.Scanner or stores into an any: nil, int64, float32 for FLOAT,
// float64, []byte, or time.Time for DATE, DATETIME and TIMESTAMP. In the
// binary protocol unsigned integers above MaxInt64 are []byte; in the text
// one BIGINT UNSIGNED is uint64.
func (v *value) driverValue(col *ColumnDefinition) any {
	switch v.kind {
	case kindInt:
		return int64(v.i) //nolint:gosec // G115: int64 bits
	case kindUint:
		if v.i > math.MaxInt64 {
			return strconv.AppendUint(nil, v.i, 10)
		}

		return int64(v.i)
	case kindFloat:
		if v.f32 {
			return float32(v.f)
		}

		return v.f
	case kindTime:
		return v.t.time()
	case kindBytes:
		return textDriverValue(v.b, col)
	default:
		return nil
	}
}

// textDriverValue converts a text value as go-sql-driver does: integer
// columns to int64, BIGINT UNSIGNED to uint64, FLOAT to float32, DOUBLE to
// float64, dates to time.Time, the rest to a copy of the bytes.
func textDriverValue(b []byte, col *ColumnDefinition) any {
	switch col.Type {
	case myTypeLongLong:
		if col.Flags&flagUnsigned != 0 {
			if n, err := parseUint64(b); err == nil {
				return n
			}

			break
		}

		fallthrough
	case myTypeTiny, myTypeShort, myTypeInt24, myTypeLong, myTypeYear:
		if n, err := parseInt64(b); err == nil {
			return n
		}
	case myTypeFloat:
		if f, err := strconv.ParseFloat(string(b), 32); err == nil {
			return float32(f)
		}
	case myTypeDouble:
		if f, err := strconv.ParseFloat(string(b), 64); err == nil {
			return f
		}
	default:
		if isDateTime(col) {
			if t, ok := parseDateTime(b); ok {
				return t.time()
			}
		}
	}

	return bytes.Clone(b)
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
