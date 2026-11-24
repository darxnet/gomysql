package gomysql

import (
	"bytes"
	"database/sql"
	"database/sql/driver"
	"errors"
	"math"
	"strconv"
	"time"
)

// errColumnOrder is the error of ScanColumn calls out of column order;
// database/sql makes them in order, from column 0.
var errColumnOrder = errors.New("gomysql: columns scanned out of order")

// scanColumn scans column i of the current row into dest for sqldriver,
// for the destinations where gomysql converts exactly as database/sql;
// for the others it returns the driver value for sql.ConvertAssign. The
// columns come in order: column 0 starts the row again.
func (rows *Rows) scanColumn(i int, dest any) (driver.Value, bool, error) {
	if i == 0 {
		c, err := rows.startRow()
		if err != nil {
			return nil, false, rows.badRow(err)
		}

		rows.cur = c
	} else if int(rows.cur.i) != i {
		return nil, false, errColumnOrder
	}

	v := value{loc: rows.loc}
	if err := rows.nextValue(&rows.cur, &v); err != nil {
		return nil, false, rows.badRow(err)
	}

	col := &rows.columns[i]
	v.typed(col)

	if assignDirect(dest, &v) || assignNull(dest, &v) {
		return nil, true, nil
	}

	return v.refDriverValue(col), false, nil
}

// typed gives bytes their value for sqldriver: BIT as uint64, numbers and
// dates sent as text parsed.
func (v *value) typed(col *columnDefinition) {
	if v.kind != kindBytes {
		return
	}

	if col.Type == myTypeBit {
		v.bitToUint(nil)
	} else {
		v.parseText(col)
	}
}

// assignDirect stores v, through typed, into dest if database/sql would
// store the same from refDriverValue without an error.
func assignDirect(dest any, v *value) bool {
	switch v.kind {
	case kindInt:
		return assignInt(dest, v.i, true)
	case kindUint:
		return assignInt(dest, v.i, false)
	case kindFloat:
		switch d := dest.(type) {
		case *float64:
			if !v.f32 && d != nil {
				*d = v.f
				return true
			}
		case *float32:
			if v.f32 && d != nil {
				*d = float32(v.f)
				return true
			}
		}
	case kindTime:
		if d, ok := dest.(*time.Time); ok && d != nil {
			*d = v.t.time(v.loc)
			return true
		}
	case kindBytes:
		return assignBytes(dest, v.b)
	}

	return false
}

// assignBytes is assignDirect of bytes: aliased into sql.RawBytes, copied
// into []byte and string, and parsed into floats as database/sql parses
// them (DECIMAL, text).
func assignBytes(dest any, b []byte) bool {
	switch d := dest.(type) {
	case *sql.RawBytes:
		if d != nil {
			*d = b
			return true
		}
	case *string:
		if d != nil {
			*d = string(b)
			return true
		}
	case *[]byte:
		if d != nil {
			*d = bytes.Clone(b)
			return true
		}
	case *float64:
		if f, err := strconv.ParseFloat(string(b), 64); err == nil && d != nil {
			*d = f
			return true
		}
	case *float32:
		if f, err := strconv.ParseFloat(string(b), 32); err == nil && d != nil {
			*d = float32(f)
			return true
		}
	}

	return false
}

// assignNull is assignDirect into the sql.Null types, as their Scan
// methods would store v.
func assignNull(dest any, v *value) bool {
	switch d := dest.(type) {
	case *sql.NullInt64:
		return d != nil && assignNullable(&d.Int64, &d.Valid, v)
	case *sql.NullString:
		return d != nil && assignNullable(&d.String, &d.Valid, v)
	case *sql.NullTime:
		return d != nil && assignNullable(&d.Time, &d.Valid, v)
	case *sql.NullFloat64:
		return d != nil && assignNullable(&d.Float64, &d.Valid, v)
	}

	return false
}

// assignNullable stores v into the fields of an sql.Null type: NULL as the
// zero value, not valid; else as assignDirect, valid.
func assignNullable[T any](field *T, valid *bool, v *value) bool {
	if v.kind == kindNull {
		var zero T

		*field, *valid = zero, false

		return true
	}

	if !assignDirect(field, v) {
		return false
	}

	*valid = true

	return true
}

// assignInt stores an integer, int64 bits if signed, into an integer or
// string dest, as database/sql would from int64 or uint64.
func assignInt(dest any, u uint64, signed bool) bool {
	neg := signed && int64(u) < 0 //nolint:gosec // G115: int64 bits

	switch d := dest.(type) {
	case *int64:
		return setInt(d, u, neg, math.MinInt64, math.MaxInt64)
	case *int:
		return setInt(d, u, neg, math.MinInt, math.MaxInt)
	case *int32:
		return setInt(d, u, neg, math.MinInt32, math.MaxInt32)
	case *int16:
		return setInt(d, u, neg, math.MinInt16, math.MaxInt16)
	case *int8:
		return setInt(d, u, neg, math.MinInt8, math.MaxInt8)
	case *uint64:
		return setUint(d, u, neg, math.MaxUint64)
	case *uint:
		return setUint(d, u, neg, math.MaxUint)
	case *uint32:
		return setUint(d, u, neg, math.MaxUint32)
	case *uint16:
		return setUint(d, u, neg, math.MaxUint16)
	case *uint8:
		return setUint(d, u, neg, math.MaxUint8)
	case *string:
		if d == nil {
			return false
		}

		if signed {
			*d = strconv.FormatInt(int64(u), 10) //nolint:gosec // G115: int64 bits
		} else {
			*d = strconv.FormatUint(u, 10)
		}

		return true
	}

	return false
}

func setInt[T signed](d *T, u uint64, neg bool, lo, hi int64) bool {
	if d == nil {
		return false
	}

	if neg {
		if int64(u) < lo { //nolint:gosec // G115: int64 bits
			return false
		}
	} else if u > uint64(hi) { //nolint:gosec // G115: hi is positive
		return false
	}

	*d = T(int64(u)) //nolint:gosec // G115: checked

	return true
}

func setUint[T unsigned](d *T, u uint64, neg bool, hi uint64) bool {
	if d == nil || neg || u > hi {
		return false
	}

	*d = T(u)

	return true
}
