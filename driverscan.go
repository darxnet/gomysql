package gomysql

import (
	"bytes"
	"database/sql"
	"database/sql/driver"
	"math"
	"strconv"
	"time"
)

// scanColumn scans column i of the current row into dest for sqldriver,
// for the destinations where gomysql converts exactly as database/sql;
// for the others it returns the driver value for sql.ConvertAssign.
func (rows *Rows) scanColumn(i int, dest any) (driver.Value, bool, error) {
	if i == 0 || rows.cur.i != i {
		c, err := rows.startRow()
		if err != nil {
			return nil, false, err
		}

		var skip value
		for c.i < i {
			if err := rows.nextValue(&c, &skip); err != nil {
				return nil, false, err
			}
		}

		rows.cur = c
	}

	v := value{loc: rows.loc}
	if err := rows.nextValue(&rows.cur, &v); err != nil {
		return nil, false, err
	}

	col := &rows.columns[i]
	if assignDirect(dest, &v, col) {
		return nil, true, nil
	}

	if col.Type == myTypeBit && v.kind == kindBytes {
		v.bitToUint(nil)
	}

	return v.refDriverValue(col), false, nil
}

// assignDirect stores v into dest if database/sql would store the same
// from refDriverValue without an error.
func assignDirect(dest any, v *value, col *columnDefinition) bool {
	switch v.kind {
	case kindInt:
		return assignInt(dest, v.i, true)
	case kindUint:
		return assignInt(dest, v.i, false)
	case kindFloat:
		switch d := dest.(type) {
		case *float64:
			if !v.f32 {
				*d = v.f
				return true
			}
		case *float32:
			if v.f32 {
				*d = float32(v.f)
				return true
			}
		}
	case kindTime:
		if d, ok := dest.(*time.Time); ok {
			*d = v.t.time(v.loc)
			return true
		}
	case kindBytes:
		return assignText(dest, v, col)
	}

	return false
}

// assignText is assignDirect of a value in the text protocol, or bytes
// of the binary one.
func assignText(dest any, v *value, col *columnDefinition) bool {
	switch col.Type {
	case myTypeTiny, myTypeShort, myTypeInt24, myTypeLong, myTypeYear:
		n, err := parseInt64(v.b)
		return err == nil && assignInt(dest, uint64(n), true) //nolint:gosec // G115: int64 bits
	case myTypeLongLong:
		if col.Flags&flagUnsigned != 0 {
			n, err := parseUint64(v.b)
			return err == nil && assignInt(dest, n, false)
		}

		n, err := parseInt64(v.b)

		return err == nil && assignInt(dest, uint64(n), true) //nolint:gosec // G115: int64 bits
	case myTypeFloat, myTypeDouble, myTypeBit:
		return false
	}

	if isDateTime(col) {
		if d, ok := dest.(*time.Time); ok {
			if t, ok := parseDateTime(v.b); ok {
				*d = t.time(v.loc)
				return true
			}
		}

		return false
	}

	switch d := dest.(type) {
	case *sql.RawBytes:
		*d = v.b
	case *string:
		*d = string(v.b)
	case *[]byte:
		*d = bytes.Clone(v.b)
	default:
		return false
	}

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
	if neg || u > hi {
		return false
	}

	*d = T(u)

	return true
}
