package gomysql

import (
	"encoding/binary"
	"math"
	"strconv"
	"time"
)

// flagUnsigned is UNSIGNED_FLAG of a column definition.
const flagUnsigned uint16 = 0x20

// valueKind says which field of value holds the value. An alias, not an
// enum: every switch on it handles the other kinds in its default case.
type valueKind = uint8

const (
	kindNull  valueKind = iota
	kindBytes           // text values, and strings, decimals, blobs in binary
	kindInt             // binary integers, signed
	kindUint            // binary integers, unsigned
	kindFloat           // binary FLOAT and DOUBLE
	kindTime            // binary DATE, DATETIME and TIMESTAMP
)

// value is one column of a row, decoded just enough to assign it.
type value struct {
	b    []byte // kindBytes, aliasing the row
	i    uint64 // kindInt as int64 bits, kindUint
	f    float64
	t    dateTime
	loc  *time.Location // of DATE, DATETIME and TIMESTAMP values
	kind valueKind
	f32  bool // a FLOAT column
}

// dateTime holds the fields of DATE, DATETIME and TIMESTAMP values, which
// can be zero dates that time.Time cannot represent.
type dateTime struct {
	micro  uint32
	year   uint16
	month  uint8
	day    uint8
	hour   uint8
	minute uint8
	second uint8
}

// readBinary decodes a value of the binary protocol that is not NULL.
// TIME values are formatted as text, appended to *text, so that they stay
// valid as long as the row: sql.RawBytes of two TIME columns must not
// share a buffer.
//
// https://dev.mysql.com/doc/dev/mysql-server/latest/page_protocol_binary_resultset.html
func (v *value) readBinary(d *decoder, col *columnDefinition, text *[]byte) {
	switch col.Type {
	case myTypeTiny, myTypeShort, myTypeYear, myTypeInt24, myTypeLong, myTypeLongLong:
		v.readBinaryInt(d, col)
	case myTypeFloat:
		v.kind, v.f, v.f32 = kindFloat, float64(math.Float32frombits(d.uint32())), true
	case myTypeDouble:
		v.kind, v.f = kindFloat, math.Float64frombits(d.uint64())
	case myTypeDate, myTypeNewDate, myTypeDatetime, myTypeTimestamp, myTypeDatetime2, myTypeTimestamp2:
		v.kind, v.t = kindTime, readBinaryDateTime(d)
	case myTypeTime, myTypeTime2:
		start := len(*text)
		*text = appendBinaryTime(*text, d, col.Decimals)
		v.kind, v.b = kindBytes, (*text)[start:len(*text):len(*text)]
	default:
		v.kind, v.b = kindBytes, d.lenEncString()
	}
}

// intSize is the size of an integer column in the binary protocol, 0 for
// other types.
func intSize(colType byte) int {
	switch colType {
	case myTypeTiny:
		return 1
	case myTypeShort, myTypeYear:
		return 2
	case myTypeInt24, myTypeLong:
		return 4
	case myTypeLongLong:
		return 8
	}

	return 0
}

func (v *value) setInt(u uint64, signed bool) {
	if signed {
		v.kind, v.i = kindInt, u
	} else {
		v.kind, v.i = kindUint, u
	}
}

// readBinaryInt reads an integer column.
func (v *value) readBinaryInt(d *decoder, col *columnDefinition) {
	n := intSize(col.Type)

	b := d.take(n)
	if b == nil {
		return
	}

	v.setInt(binaryInt(b, n, col.Flags&flagUnsigned == 0))
}

// binaryInt decodes an integer of n = 1, 2, 4 or 8 bytes from b: the bits
// of the sign-extended int64 if signed, else the value.
func binaryInt(b []byte, n int, signed bool) (uint64, bool) {
	var u uint64

	switch n {
	case 1:
		u = uint64(b[0])
	case 2:
		u = uint64(binary.LittleEndian.Uint16(b))
	case 4:
		u = uint64(binary.LittleEndian.Uint32(b))
	default:
		u = binary.LittleEndian.Uint64(b)
	}

	if signed {
		shift := uint(64 - 8*n)              //nolint:gosec // G115: n <= 8
		u = uint64(int64(u<<shift) >> shift) //nolint:gosec // G115: sign extension
	}

	return u, signed
}

// readBinaryDateTime reads a binary DATE, DATETIME or TIMESTAMP: a length
// of 0, 4, 7 or 11 bytes, then the fields that are not zero.
func readBinaryDateTime(d *decoder) (t dateTime) {
	n := d.uint8()

	if n >= 4 {
		t.year, t.month, t.day = d.uint16(), d.uint8(), d.uint8()
	}

	if n >= 7 {
		t.hour, t.minute, t.second = d.uint8(), d.uint8(), d.uint8()
	}

	if n >= 11 {
		t.micro = d.uint32()
	}

	if n != 0 && n != 4 && n != 7 && n != 11 {
		d.bad = true
	}

	return t
}

// appendBinaryTime formats a binary TIME (length 0, 8 or 12: sign, days,
// hours, minutes, seconds, microseconds) like the text protocol does:
// [-]HH:MM:SS with as many fractional digits as the column has.
func appendBinaryTime(dst []byte, d *decoder, decimals byte) []byte {
	n := d.uint8()

	var (
		negative             bool
		days, micro          uint32
		hour, minute, second uint8
	)

	if n >= 8 {
		negative = d.uint8() == 1
		days, hour, minute, second = d.uint32(), d.uint8(), d.uint8(), d.uint8()
	}

	if n >= 12 {
		micro = d.uint32()
	}

	if n != 0 && n != 8 && n != 12 {
		d.bad = true
	}

	if negative {
		dst = append(dst, '-')
	}

	hours := uint64(days)*24 + uint64(hour)
	if hours < 10 {
		dst = append(dst, '0')
	}

	dst = strconv.AppendUint(dst, hours, 10)
	dst = append(dst, ':')
	dst = append2(dst, minute)
	dst = append(dst, ':')
	dst = append2(dst, second)

	return appendFraction(dst, micro, decimals)
}

// appendText formats a DATE, DATETIME or TIMESTAMP like the text protocol.
func (t *dateTime) appendText(dst []byte, col *columnDefinition) []byte {
	dst = append2(dst, uint8(t.year/100)) //nolint:gosec // G115: year < 10000
	dst = append2(dst, uint8(t.year%100)) //nolint:gosec // G115: < 100
	dst = append(dst, '-')
	dst = append2(dst, t.month)
	dst = append(dst, '-')
	dst = append2(dst, t.day)

	if col.Type == myTypeDate || col.Type == myTypeNewDate {
		return dst
	}

	dst = append(dst, ' ')
	dst = append2(dst, t.hour)
	dst = append(dst, ':')
	dst = append2(dst, t.minute)
	dst = append(dst, ':')
	dst = append2(dst, t.second)

	return appendFraction(dst, t.micro, col.Decimals)
}

// inLocation returns t in loc, UTC if nil: time.Time arguments are sent
// as the wall clock in Config.Location.
func inLocation(t time.Time, loc *time.Location) time.Time {
	if loc == nil {
		loc = time.UTC
	}

	return t.In(loc)
}

// time returns t in loc; the zero date is the zero time.Time.
func (t *dateTime) time(loc *time.Location) time.Time {
	if t.year == 0 && t.month == 0 && t.day == 0 {
		return time.Time{}
	}

	if loc == nil {
		loc = time.UTC
	}

	return time.Date(int(t.year), time.Month(t.month), int(t.day),
		int(t.hour), int(t.minute), int(t.second), int(t.micro)*1000, loc)
}

// parseDateTime parses a text DATE, DATETIME or TIMESTAMP:
// YYYY-MM-DD[ HH:MM:SS[.fraction]].
func parseDateTime(b []byte) (t dateTime, ok bool) {
	digits := func(b []byte) (int, bool) {
		n := 0

		for _, c := range b {
			if c < '0' || c > '9' {
				return 0, false
			}

			n = n*10 + int(c-'0')
		}

		return n, true
	}

	if len(b) != 10 && len(b) != 19 && (len(b) < 21 || len(b) > 26 || b[19] != '.') {
		return t, false
	}

	year, ok1 := digits(b[0:4])
	month, ok2 := digits(b[5:7])
	day, ok3 := digits(b[8:10])

	if !ok1 || !ok2 || !ok3 || b[4] != '-' || b[7] != '-' {
		return t, false
	}

	t.year, t.month, t.day = uint16(year), uint8(month), uint8(day) //nolint:gosec // G115: at most 4 and 2 digits

	if len(b) == 10 {
		return t, true
	}

	hour, ok1 := digits(b[11:13])
	minute, ok2 := digits(b[14:16])
	second, ok3 := digits(b[17:19])

	if !ok1 || !ok2 || !ok3 || b[10] != ' ' || b[13] != ':' || b[16] != ':' {
		return t, false
	}

	t.hour, t.minute, t.second = uint8(hour), uint8(minute), uint8(second) //nolint:gosec // G115: 2 digits

	if len(b) > 19 {
		frac := b[20:]

		micro, ok := digits(frac)
		if !ok {
			return t, false
		}

		for range 6 - len(frac) {
			micro *= 10
		}

		t.micro = uint32(micro) //nolint:gosec // G115: at most 6 digits
	}

	return t, true
}

// append2 appends n < 100 as two digits.
func append2(dst []byte, n uint8) []byte {
	return append(dst, '0'+n/10, '0'+n%10)
}

// appendFraction appends the first decimals of 6 fractional digits of
// micro, if decimals is 1 to 6.
func appendFraction(dst []byte, micro uint32, decimals byte) []byte {
	if decimals == 0 || decimals > 6 {
		return dst
	}

	var digits [6]byte
	for i := 5; i >= 0; i-- {
		digits[i] = '0' + byte(micro%10)
		micro /= 10
	}

	dst = append(dst, '.')

	return append(dst, digits[:decimals]...)
}

// isText reports whether col holds text: JSON, which MySQL sends with the
// binary collation but is always utf8mb4, or a string type with a
// collation other than binary.
func isText(col *columnDefinition) bool {
	switch col.Type {
	case myTypeJSON:
		return true
	case myTypeVarChar, myTypeVarString, myTypeString, myTypeEnum, myTypeSet,
		myTypeTinyBlob, myTypeMediumBlob, myTypeLongBlob, myTypeBlob:
		return col.Collation != collationBinary
	}

	return false
}

// isDateTime reports whether col holds DATE, DATETIME or TIMESTAMP values.
func isDateTime(col *columnDefinition) bool {
	switch col.Type {
	case myTypeDate, myTypeNewDate, myTypeDatetime, myTypeTimestamp, myTypeDatetime2, myTypeTimestamp2:
		return true
	}

	return false
}
