package gomysql

import (
	"bytes"
	"database/sql"
	"encoding/binary"
	"errors"
	"math"
	"reflect"
	"testing"
	"time"
)

type (
	testInt    int64
	testString string
	testBytes  []byte
)

// cell is one column of a test row: its definition and its encoding, nil
// for NULL.
type cell struct {
	col columnDefinition
	enc []byte
}

func col(colType byte, flags uint16, decimals byte) columnDefinition {
	return columnDefinition{Name: []byte("c"), Type: colType, Flags: flags, Decimals: decimals}
}

func le16(v uint16) []byte { return binary.LittleEndian.AppendUint16(nil, v) }
func le32(v uint32) []byte { return binary.LittleEndian.AppendUint32(nil, v) }
func le64(v uint64) []byte { return binary.LittleEndian.AppendUint64(nil, v) }

// binaryRows returns Rows positioned on a binary row of the given cells.
func binaryRows(cells ...cell) *Rows {
	rows := &Rows{binary: true}
	nulls := make([]byte, (len(cells)+7+2)/8)

	var values []byte

	for i := range cells {
		c := &cells[i]
		rows.columns = append(rows.columns, c.col)

		if c.enc == nil {
			nulls[(i+2)/8] |= 1 << ((i + 2) % 8)
		}

		values = append(values, c.enc...)
	}

	rows.buf = append(append([]byte{iOK}, nulls...), values...)

	return rows
}

// textRows returns Rows positioned on a text row; a nil value is NULL.
func textRows(c *columnDefinition, values ...[]byte) *Rows {
	rows := &Rows{}

	for _, v := range values {
		rows.columns = append(rows.columns, *c)

		if v == nil {
			rows.buf = append(rows.buf, 0xfb)
		} else {
			rows.buf = appendLenEncBytes(rows.buf, v)
		}
	}

	return rows
}

var (
	unsignedFlag = flagUnsigned

	// TIME -1 day 02:03:04.5 and DATETIME 2026-11-18 13:31:33.120000.
	timeMinus26 = append([]byte{12, 1}, append(le32(1), append([]byte{2, 3, 4}, le32(500000)...)...)...)
	dateTime3   = append([]byte{11}, append(le16(2026), 11, 18, 13, 31, 33, 0xc0, 0xd4, 0x01, 0x00)...)
)

// scanCase scans one cell into a new destination of want's type and
// expects want, or an error if want is an error.
type scanCase struct {
	name string
	cell cell
	want any
}

func runScanCases(t *testing.T, rowsFor func(c cell) *Rows, cases []scanCase) {
	t.Helper()

	for i := range cases {
		c := &cases[i]
		e, isErr := c.want.(errDest)

		var dest reflect.Value

		switch {
		case isErr:
			dest = reflect.New(reflect.TypeOf(e.dest))
		case c.want == nil:
			dest = reflect.New(reflect.TypeFor[any]())
		default:
			dest = reflect.New(reflect.TypeOf(c.want))
		}

		err := rowsFor(c.cell).Scan(dest.Interface())

		switch {
		case isErr && !errors.Is(err, e.err):
			t.Errorf("%s: %v, want %v", c.name, err, e.err)
		case !isErr && err != nil:
			t.Errorf("%s: %v", c.name, err)
		case !isErr && !reflect.DeepEqual(dest.Elem().Interface(), c.want):
			t.Errorf("%s: %#v, want %#v", c.name, dest.Elem().Interface(), c.want)
		}
	}
}

// errDest pairs an expected error with the destination type to scan into.
type errDest struct {
	err  error
	dest any
}

func fail(err error, dest any) errDest { return errDest{err, dest} }

func TestScanBinary(t *testing.T) {
	t.Parallel()

	tiny := col(myTypeTiny, 0, 0)
	utiny := col(myTypeTiny, unsignedFlag, 0)
	ubig := col(myTypeLongLong, unsignedFlag, 0)
	str := col(myTypeVarString, 0, 0)
	dt3 := col(myTypeDatetime, 0, 3)
	tm1 := col(myTypeTime, 0, 1)
	null := cell{col(myTypeLong, 0, 0), nil}
	when := time.Date(2026, 11, 18, 13, 31, 33, 120e6, time.UTC)

	runScanCases(t, func(c cell) *Rows { return binaryRows(c) }, []scanCase{
		{"tiny -1 int64", cell{tiny, []byte{0xff}}, int64(-1)},
		{"tiny -1 int8", cell{tiny, []byte{0xff}}, int8(-1)},
		{"tiny -1 any", cell{tiny, []byte{0xff}}, any(int64(-1))},
		{"tiny -1 string", cell{tiny, []byte{0xff}}, "-1"},
		{"tiny -1 uint64", cell{tiny, []byte{0xff}}, fail(errScanRange, uint64(0))},
		{"tiny -1 bool", cell{tiny, []byte{0xff}}, fail(errScanRange, false)},
		{"tiny 1 bool", cell{tiny, []byte{1}}, true},
		{"utiny 255 uint8", cell{utiny, []byte{0xff}}, uint8(255)},
		{"utiny 255 int8", cell{utiny, []byte{0xff}}, fail(errScanRange, int8(0))},
		{"short -2", cell{col(myTypeShort, 0, 0), le16(0xfffe)}, int16(-2)},
		{"long -3 int", cell{col(myTypeLong, 0, 0), le32(0xfffffffd)}, -3},
		{"long named", cell{col(myTypeLong, 0, 0), le32(7)}, testInt(7)},
		{"ubig max uint64", cell{ubig, le64(math.MaxUint64)}, uint64(math.MaxUint64)},
		{"ubig max int64", cell{ubig, le64(math.MaxUint64)}, fail(errScanRange, int64(0))},
		{"ubig max any", cell{ubig, le64(math.MaxUint64)}, any([]byte("18446744073709551615"))},
		{"ubig 5 uint32", cell{ubig, le64(5)}, uint32(5)},
		{"float", cell{col(myTypeFloat, 0, 0), le32(math.Float32bits(3.25))}, float32(3.25)},
		{"float any", cell{col(myTypeFloat, 0, 0), le32(math.Float32bits(3.25))}, any(float32(3.25))},
		{"float string", cell{col(myTypeFloat, 0, 0), le32(math.Float32bits(0.1))}, "0.1"},
		{"float int", cell{col(myTypeFloat, 0, 0), le32(math.Float32bits(3.25))}, fail(errScanRange, int64(0))},
		{"double 2 int", cell{col(myTypeDouble, 0, 0), le64(math.Float64bits(2))}, int64(2)},
		{"datetime(3) string", cell{dt3, dateTime3}, "2026-11-18 13:31:33.120"},
		{"datetime(3) time", cell{dt3, dateTime3}, when},
		{"datetime(3) any", cell{dt3, dateTime3}, any(when)},
		{"datetime NullTime", cell{dt3, dateTime3}, sql.NullTime{Time: when, Valid: true}},
		{"datetime int", cell{dt3, dateTime3}, fail(errScanConvert, int64(0))},
		{"zero datetime string", cell{col(myTypeDatetime, 0, 0), []byte{0}}, "0000-00-00 00:00:00"},
		{"zero datetime time", cell{col(myTypeDatetime, 0, 0), []byte{0}}, time.Time{}},
		{"date", cell{col(myTypeDate, 0, 0), append([]byte{4}, append(le16(2026), 11, 18)...)}, "2026-11-18"},
		{"time -26:03:04.5", cell{tm1, timeMinus26}, "-26:03:04.5"},
		{"time zero", cell{col(myTypeTime, 0, 0), []byte{0}}, "00:00:00"},
		{"string bytes", cell{str, []byte{3, 'a', 'b', 'c'}}, []byte("abc")},
		{"string named bytes", cell{str, []byte{3, 'a', 'b', 'c'}}, testBytes("abc")},
		{"string named string", cell{str, []byte{3, 'a', 'b', 'c'}}, testString("abc")},
		{"string RawBytes", cell{str, []byte{3, 'a', 'b', 'c'}}, sql.RawBytes("abc")},
		{"string 42 int", cell{str, []byte{2, '4', '2'}}, 42},
		{"empty bytes", cell{str, []byte{0}}, []byte{}},
		{"null int", null, fail(errScanNull, 0)},
		{"null string", null, fail(errScanNull, "")},
		{"null *int", null, (*int)(nil)},
		{"null any", null, any(nil)},
		{"null []byte", null, []byte(nil)},
		{"null RawBytes", null, sql.RawBytes(nil)},
		{"null NullInt64", null, sql.NullInt64{}},
		{"*int64", cell{col(myTypeLong, 0, 0), le32(9)}, ptr(int64(9))},
		{"unsupported", cell{col(myTypeLong, 0, 0), le32(9)}, fail(errScanUnsupported, struct{}{})},
	})
}

func ptr[T any](v T) *T { return &v }

func TestScanText(t *testing.T) {
	t.Parallel()

	when := time.Date(2026, 11, 18, 13, 31, 33, 120e6, time.UTC)
	long := bytes.Repeat([]byte("x"), 300)

	runScanCases(t, func(c cell) *Rows { return textRows(&c.col, c.enc) }, []scanCase{
		{"-5 int64", cell{col(myTypeLong, 0, 0), []byte("-5")}, int64(-5)},
		{"-5 uint64", cell{col(myTypeLong, 0, 0), []byte("-5")}, fail(errScanSyntax, uint64(0))},
		{"min int64", cell{col(myTypeLongLong, 0, 0), []byte("-9223372036854775808")}, int64(math.MinInt64)},
		{"overflow int64", cell{col(myTypeLongLong, 0, 0), []byte("9223372036854775808")}, fail(errScanRange, int64(0))},
		{"max uint64", cell{col(myTypeLongLong, 0, 0), []byte("18446744073709551615")}, uint64(math.MaxUint64)},
		{"overflow uint64", cell{col(myTypeLongLong, 0, 0), []byte("18446744073709551616")}, fail(errScanRange, uint64(0))},
		{"300 uint8", cell{col(myTypeLong, 0, 0), []byte("300")}, fail(errScanRange, uint8(0))},
		{"abc int", cell{col(myTypeVarString, 0, 0), []byte("abc")}, fail(errScanSyntax, 0)},
		{"1.5 int", cell{col(myTypeNewDecimal, 0, 0), []byte("1.5")}, fail(errScanSyntax, 0)},
		{"1.5 float64", cell{col(myTypeNewDecimal, 0, 0), []byte("1.5")}, 1.5},
		{"true bool", cell{col(myTypeVarString, 0, 0), []byte("true")}, true},
		{"2 bool", cell{col(myTypeTiny, 0, 0), []byte("2")}, fail(errScanSyntax, false)},
		{"int any", cell{col(myTypeLong, 0, 0), []byte("7")}, any(int64(7))},
		{"ubig any", cell{col(myTypeLongLong, flagUnsigned, 0), []byte("7")}, any(uint64(7))},
		{"float any", cell{col(myTypeFloat, 0, 0), []byte("0.1")}, any(float32(0.1))},
		{"string any", cell{col(myTypeVarString, 0, 0), []byte("x")}, any([]byte("x"))},
		{"date time", cell{col(myTypeDate, 0, 0), []byte("2026-11-18")}, time.Date(2026, 11, 18, 0, 0, 0, 0, time.UTC)},
		{"datetime(2) any", cell{col(myTypeDatetime, 0, 2), []byte("2026-11-18 13:31:33.12")}, any(when)},
		{"zero date time", cell{col(myTypeDatetime, 0, 0), []byte("0000-00-00 00:00:00")}, time.Time{}},
		{"bad date", cell{col(myTypeDatetime, 0, 0), []byte("2026-11-18T13:31:33")}, fail(errScanConvert, time.Time{})},
		{"varchar date", cell{col(myTypeVarString, 0, 0), []byte("2026-11-18")}, fail(errScanConvert, time.Time{})},
		{"null int", cell{col(myTypeLong, 0, 0), nil}, fail(errScanNull, 0)},
		{"null **", cell{col(myTypeLong, 0, 0), nil}, (*string)(nil)},
		{"long string", cell{col(myTypeVarString, 0, 0), long}, string(long)},
	})
}

func TestScanErrors(t *testing.T) {
	t.Parallel()

	rows := textRows(ptr(col(myTypeLong, 0, 0)), []byte("1"), []byte("2"))

	var a, b int
	if err := rows.Scan(&a); err == nil {
		t.Error("Scan with too few destinations: no error")
	}

	err := rows.Scan(&a, &b)
	if err != nil || a != 1 || b != 2 {
		t.Errorf("Scan: %v, %d %d", err, a, b)
	}

	rows.buf = nil
	if err = rows.Scan(&a, &b); err == nil {
		t.Error("Scan without a row: no error")
	}

	var s string
	err = textRows(ptr(col(myTypeLong, 0, 0)), nil).Scan(&s)

	if !errors.Is(err, errScanNull) || err.Error() !=
		`gomysql: Scan error on column index 0, name "c", into *string: `+errScanNull.Error() {
		t.Errorf("error message: %v", err)
	}
}

// TestScanTruncated checks that every truncation of a row is ErrBadConn,
// not a panic or a wrong value.
func TestScanTruncated(t *testing.T) {
	t.Parallel()

	binRows := binaryRows(
		cell{col(myTypeLong, 0, 0), le32(1)},
		cell{col(myTypeVarString, 0, 0), []byte{3, 'a', 'b', 'c'}},
		cell{col(myTypeDatetime, 0, 3), dateTime3},
		cell{col(myTypeTime, 0, 0), append([]byte{8, 0}, append(le32(0), 1, 2, 3)...)},
	)
	txtRows := textRows(ptr(col(myTypeVarString, 0, 0)), []byte("1"), bytes.Repeat([]byte("y"), 300))

	for _, rows := range []*Rows{binRows, txtRows} {
		full := rows.buf

		for n := range full {
			rows.buf = full[:n:n]

			dest := make([]any, len(rows.columns))
			for i := range dest {
				dest[i] = new(any)
			}

			if err := rows.Scan(dest...); !errors.Is(err, ErrBadConn) {
				t.Errorf("binary %v, %d of %d bytes: %v, want ErrBadConn", rows.binary, n, len(full), err)
			}
		}
	}
}

func FuzzScan(f *testing.F) {
	f.Add([]byte{0, 0, 1, 0, 0, 0, 3, 'a', 'b', 'c'}, true)
	f.Add([]byte{1, '1', 0xfb, 0xfc, 1, 0, 'x'}, false)

	columns := []columnDefinition{
		col(myTypeLong, 0, 0), col(myTypeVarString, 0, 0), col(myTypeDatetime, 0, 6), col(myTypeTime, 0, 3),
	}

	f.Fuzz(func(t *testing.T, buf []byte, binary bool) {
		if len(buf) == 0 {
			return
		}

		rows := &Rows{binary: binary, columns: columns, buf: buf}

		var (
			n    int64
			s    string
			when time.Time
			a    any
		)

		_ = rows.Scan(&n, &s, &when, &a)
		_ = rows.Scan(&a, &a, &a, &a)
	})
}

func scanBenchRows(binaryProtocol bool) *Rows {
	cols := make([]columnDefinition, len(replayColumns))
	for i, c := range replayColumns {
		if err := cols[i].parse(c.definition()); err != nil {
			panic(err)
		}
	}

	if binaryProtocol {
		return &Rows{binary: true, columns: cols, buf: replayRow(12345, []byte{iOK, 0})}
	}

	var buf []byte
	for _, v := range []string{"345", "32791823459", "15"} {
		buf = appendLenEncString(buf, v)
	}

	return &Rows{columns: cols, buf: buf}
}

func benchmarkScan(b *testing.B, binaryProtocol bool) {
	b.Helper()

	rows := scanBenchRows(binaryProtocol)

	var (
		aud  uint32
		user uint64
		keep uint16
	)

	b.ReportAllocs()

	for b.Loop() {
		if err := rows.Scan(&aud, &user, &keep); err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkScanBinary(b *testing.B) { benchmarkScan(b, true) }
func BenchmarkScanText(b *testing.B)   { benchmarkScan(b, false) }
