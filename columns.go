package gomysql

import (
	"database/sql"
	"errors"
	"reflect"
	"time"
)

// Flags of a column definition besides flagUnsigned.
const (
	flagNotNull uint16 = 0x0001
	flagEnum    uint16 = 0x0100
	flagSet     uint16 = 0x0800
)

var errRowsClosed = errors.New("gomysql: Rows are closed")

// Columns returns the names of the columns, like sql.Rows.Columns.
func (rows *Rows) Columns() ([]string, error) {
	if rows.closed {
		return nil, errRowsClosed
	}

	names := make([]string, len(rows.columns))
	for i := range rows.columns {
		names[i] = string(rows.columns[i].Name)
	}

	return names, nil
}

// ColumnTypes describes the columns, like sql.Rows.ColumnTypes.
func (rows *Rows) ColumnTypes() ([]*ColumnType, error) {
	if rows.closed {
		return nil, errRowsClosed
	}

	types := make([]*ColumnType, len(rows.columns))
	for i := range rows.columns {
		types[i] = &ColumnType{name: string(rows.columns[i].Name), col: rows.columns[i]}
	}

	return types, nil
}

// ColumnType describes a column, with the methods of sql.ColumnType.
type ColumnType struct {
	name string
	col  columnDefinition
}

// Name returns the name or alias of the column.
func (ct *ColumnType) Name() string {
	return ct.name
}

// DatabaseTypeName returns the MySQL name of the type, such as "INT",
// "INT UNSIGNED", "VARCHAR", "VARBINARY", "TEXT", "BLOB" or "DECIMAL",
// without lengths; "" if unknown.
func (ct *ColumnType) DatabaseTypeName() string {
	col := &ct.col

	switch col.Type {
	case myTypeTiny, myTypeShort, myTypeInt24, myTypeLong, myTypeLongLong:
		name := intTypeNames[col.Type]
		if col.Flags&flagUnsigned != 0 {
			return name + " UNSIGNED"
		}

		return name
	case myTypeString:
		switch {
		case col.Flags&flagEnum != 0:
			return "ENUM"
		case col.Flags&flagSet != 0:
			return "SET"
		}
	}

	if names, ok := stringTypeNames[col.Type]; ok {
		if col.Collation == collationBinary {
			return names[1]
		}

		return names[0]
	}

	return typeNames[col.Type]
}

var intTypeNames = map[byte]string{
	myTypeTiny:     "TINYINT",
	myTypeShort:    "SMALLINT",
	myTypeInt24:    "MEDIUMINT",
	myTypeLong:     "INT",
	myTypeLongLong: "BIGINT",
}

// stringTypeNames are the names of string types: as text, and as binary.
var stringTypeNames = map[byte][2]string{
	myTypeString:     {"CHAR", "BINARY"},
	myTypeVarChar:    {"VARCHAR", "VARBINARY"},
	myTypeVarString:  {"VARCHAR", "VARBINARY"},
	myTypeTinyBlob:   {"TINYTEXT", "TINYBLOB"},
	myTypeBlob:       {"TEXT", "BLOB"},
	myTypeMediumBlob: {"MEDIUMTEXT", "MEDIUMBLOB"},
	myTypeLongBlob:   {"LONGTEXT", "LONGBLOB"},
}

var typeNames = map[byte]string{
	myTypeDecimal:    "DECIMAL",
	myTypeNewDecimal: "DECIMAL",
	myTypeFloat:      "FLOAT",
	myTypeDouble:     "DOUBLE",
	myTypeNull:       "NULL",
	myTypeTimestamp:  "TIMESTAMP",
	myTypeTimestamp2: "TIMESTAMP",
	myTypeDate:       "DATE",
	myTypeNewDate:    "DATE",
	myTypeTime:       "TIME",
	myTypeTime2:      "TIME",
	myTypeDatetime:   "DATETIME",
	myTypeDatetime2:  "DATETIME",
	myTypeYear:       "YEAR",
	myTypeBit:        "BIT",
	myTypeEnum:       "ENUM",
	myTypeSet:        "SET",
	myTypeJSON:       "JSON",
	myTypeGeometry:   "GEOMETRY",
	myTypeVector:     "VECTOR",
}

// Length returns the maximum length in bytes of string and binary types.
func (ct *ColumnType) Length() (length int64, ok bool) {
	if _, ok := stringTypeNames[ct.col.Type]; ok || ct.col.Type == myTypeJSON {
		return int64(ct.col.Len), true
	}

	return 0, false
}

// DecimalSize returns the precision and scale of DECIMAL columns.
func (ct *ColumnType) DecimalSize() (precision, scale int64, ok bool) {
	col := &ct.col
	if col.Type != myTypeNewDecimal && col.Type != myTypeDecimal {
		return 0, 0, false
	}

	// The length counts the sign of signed columns and the decimal point.
	precision = int64(col.Len)
	if col.Flags&flagUnsigned == 0 {
		precision--
	}

	if col.Decimals > 0 {
		precision--
	}

	return precision, int64(col.Decimals), true
}

// Nullable reports whether the column can be NULL.
func (ct *ColumnType) Nullable() (nullable, ok bool) {
	return ct.col.Flags&flagNotNull == 0, true
}

// ScanType returns a type that Scan fills without loss: an integer type of
// the size of the column, float32, float64, string for text and DECIMAL,
// []byte for binary strings, time.Time, time.Duration for TIME, uint64 for
// BIT. Columns that can be NULL get sql.Null of the type, but []byte,
// where nil is NULL.
func (ct *ColumnType) ScanType() reflect.Type {
	col := &ct.col
	unsigned := col.Flags&flagUnsigned != 0

	var t reflect.Type

	switch col.Type {
	case myTypeTiny:
		t = pick[int8, uint8](unsigned)
	case myTypeShort:
		t = pick[int16, uint16](unsigned)
	case myTypeYear:
		t = reflect.TypeFor[uint16]()
	case myTypeInt24, myTypeLong:
		t = pick[int32, uint32](unsigned)
	case myTypeLongLong:
		t = pick[int64, uint64](unsigned)
	case myTypeFloat:
		t = reflect.TypeFor[float32]()
	case myTypeDouble:
		t = reflect.TypeFor[float64]()
	case myTypeDecimal, myTypeNewDecimal:
		t = reflect.TypeFor[string]()
	case myTypeTime, myTypeTime2:
		t = reflect.TypeFor[time.Duration]()
	case myTypeBit:
		t = reflect.TypeFor[uint64]()
	case myTypeNull:
		return reflect.TypeFor[any]()
	default:
		switch {
		case isDateTime(col):
			t = reflect.TypeFor[time.Time]()
		case isText(col):
			t = reflect.TypeFor[string]()
		default:
			return reflect.TypeFor[[]byte]()
		}
	}

	if col.Flags&flagNotNull != 0 {
		return t
	}

	return nullTypes[t]
}

func pick[S, U any](unsigned bool) reflect.Type {
	if unsigned {
		return reflect.TypeFor[U]()
	}

	return reflect.TypeFor[S]()
}

// nullTypes maps the types of ScanType to their sql.Null.
var nullTypes = map[reflect.Type]reflect.Type{
	reflect.TypeFor[int8]():          reflect.TypeFor[sql.Null[int8]](),
	reflect.TypeFor[uint8]():         reflect.TypeFor[sql.Null[uint8]](),
	reflect.TypeFor[int16]():         reflect.TypeFor[sql.Null[int16]](),
	reflect.TypeFor[uint16]():        reflect.TypeFor[sql.Null[uint16]](),
	reflect.TypeFor[int32]():         reflect.TypeFor[sql.Null[int32]](),
	reflect.TypeFor[uint32]():        reflect.TypeFor[sql.Null[uint32]](),
	reflect.TypeFor[int64]():         reflect.TypeFor[sql.Null[int64]](),
	reflect.TypeFor[uint64]():        reflect.TypeFor[sql.Null[uint64]](),
	reflect.TypeFor[float32]():       reflect.TypeFor[sql.Null[float32]](),
	reflect.TypeFor[float64]():       reflect.TypeFor[sql.Null[float64]](),
	reflect.TypeFor[string]():        reflect.TypeFor[sql.Null[string]](),
	reflect.TypeFor[time.Time]():     reflect.TypeFor[sql.Null[time.Time]](),
	reflect.TypeFor[time.Duration](): reflect.TypeFor[sql.Null[time.Duration]](),
}
