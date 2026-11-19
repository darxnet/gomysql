package gomysql

import (
	"database/sql"
	"errors"
	"reflect"
	"time"
)

// RowsBinary is the result of Stmt.Query, read in the binary protocol.
type RowsBinary struct {
	resultRows
}

// Next reads the next row for Scan. It returns false at the end of the rows
// or on an error, which Err returns.
func (rows *RowsBinary) Next() bool { return rows.next() }

// Err returns the error that ended the rows, if any.
func (rows *RowsBinary) Err() error { return rows.err }

// Close reads the rows left and the results that follow, so that the
// connection can run the next command. It returns the error that ended
// the rows, if any.
func (rows *RowsBinary) Close() error { return rows.close() }

// Scan copies the columns of the current row into dest.
//
//nolint:gocognit,gocyclo,maintidx,dupl,gosec // replaced by a single decode layer in ROADMAP phase 6
func (rows *RowsBinary) Scan(dest ...any) error {
	if len(rows.columns) != len(dest) {
		return errors.New("wrong dest count")
	}

	var pos int

	if rows.buf[0] != iOK {
		return errUnexpectedPacketIdentity(rows.buf[0])
	}
	pos += 1

	offset := (len(dest) + 7 + 2) / 8
	bufNUL := rows.buf[pos : pos+offset]
	pos += offset

	// https://dev.mysql.com/doc/internals/en/binary-protocol-value.html
	for i := range dest {
		nullByte := (i + 2) / 8
		nullBit := uint((i + 2) % 8)
		if bufNUL[nullByte]&(1<<nullBit) != 0 {
			continue
		}

		switch d := dest[i].(type) {
		case nil:
			return errors.New("dest type error: nil")
		case *any:
			switch rows.columns[i].Type {
			case myTypeString,
				myTypeVarChar, myTypeVarString,
				myTypeEnum, myTypeSet,
				myTypeLongBlob, myTypeMediumBlob, myTypeBlob, myTypeTinyBlob,
				myTypeGeometry, myTypeBit,
				myTypeDecimal, myTypeNewDecimal:
				var tmp []byte
				tmp, posN := lenDecString(rows.buf[pos:])
				pos += posN
				*d = string(tmp)
				continue
			case myTypeTiny:
				*d = int64(rows.buf[pos])
				pos += 1
				continue
			case myTypeShort:
				*d = int64(rows.buf[pos]) | int64(rows.buf[pos+1])<<8
				pos += 2
				continue
			case myTypeInt24, myTypeLong:
				*d = int64(rows.buf[pos]) | int64(rows.buf[pos+1])<<8 |
					int64(rows.buf[pos+2])<<16 | int64(rows.buf[pos+3])<<24
				pos += 4
				continue
			case myTypeLongLong:
				*d = int64(rows.buf[pos]) | int64(rows.buf[pos+1])<<8 |
					int64(rows.buf[pos+2])<<16 | int64(rows.buf[pos+3])<<24 |
					int64(rows.buf[pos+4])<<32 | int64(rows.buf[pos+5])<<40 |
					int64(rows.buf[pos+6])<<48 | int64(rows.buf[pos+7])<<56
				pos += 8
				continue
			}
		case *string:
			switch rows.columns[i].Type {
			case myTypeString,
				myTypeVarChar, myTypeVarString,
				myTypeEnum, myTypeSet,
				myTypeLongBlob, myTypeMediumBlob, myTypeBlob, myTypeTinyBlob,
				myTypeGeometry, myTypeBit,
				myTypeDecimal, myTypeNewDecimal:
				var tmp []byte
				tmp, posN := lenDecString(rows.buf[pos:])
				pos += posN
				*d = string(tmp)
				continue
			}
		case *[]byte:
			switch rows.columns[i].Type {
			case myTypeString,
				myTypeVarChar, myTypeVarString,
				myTypeEnum, myTypeSet,
				myTypeLongBlob, myTypeMediumBlob, myTypeBlob, myTypeTinyBlob,
				myTypeGeometry, myTypeBit,
				myTypeDecimal, myTypeNewDecimal:
				tmp, posN := lenDecString(rows.buf[pos:])
				pos += posN
				*d = append(*d, tmp...)
				continue
			}
		case *sql.RawBytes:
			switch rows.columns[i].Type {
			case myTypeString,
				myTypeVarChar, myTypeVarString,
				myTypeEnum, myTypeSet,
				myTypeLongBlob, myTypeMediumBlob, myTypeBlob, myTypeTinyBlob,
				myTypeGeometry, myTypeBit,
				myTypeDecimal, myTypeNewDecimal:
				tmp, posN := lenDecString(rows.buf[pos:])
				pos += posN
				*d = tmp
				continue
			}
		case *time.Time:
			switch rows.columns[i].Type {
			case myTypeString,
				myTypeVarChar, myTypeVarString,
				myTypeEnum, myTypeSet,
				myTypeLongBlob, myTypeMediumBlob, myTypeBlob, myTypeTinyBlob,
				myTypeGeometry, myTypeBit,
				myTypeDecimal, myTypeNewDecimal:
				var tmp []byte
				tmp, posN := lenDecString(rows.buf[pos:])
				pos += posN
				//
				tmp2, err := time.Parse(timeFormat, string(tmp))
				if err != nil {
					return err
				}
				*d = tmp2
				continue
			default:
				return errScanUnsupportedPair(rows.columns[i].Type, reflect.TypeOf(dest[i]).Name())
			}
		case *int:
			switch rows.columns[i].Type {
			case myTypeString,
				myTypeVarChar, myTypeVarString,
				myTypeEnum, myTypeSet,
				myTypeLongBlob, myTypeMediumBlob, myTypeBlob, myTypeTinyBlob,
				myTypeGeometry, myTypeBit,
				myTypeDecimal, myTypeNewDecimal:
				var tmp []byte
				tmp, posN := lenDecString(rows.buf[pos:])
				pos += posN
				*d = parseInt[int](tmp)
				continue
			case myTypeTiny:
				*d = int(rows.buf[pos])
				pos += 1
				continue
			case myTypeShort:
				*d = int(rows.buf[pos]) | int(rows.buf[pos+1])<<8
				pos += 2
				continue
			case myTypeInt24, myTypeLong:
				*d = int(rows.buf[pos]) | int(rows.buf[pos+1])<<8 |
					int(rows.buf[pos+2])<<16 | int(rows.buf[pos+3])<<24
				pos += 4
				continue
			case myTypeLongLong:
				if isFewer64bit {
					return errScanDestFewerThanSource(string(rows.columns[i].Name))
				}
				*d = int(rows.buf[pos]) | int(rows.buf[pos+1])<<8 |
					int(rows.buf[pos+2])<<16 | int(rows.buf[pos+3])<<24 |
					int(rows.buf[pos+4])<<32 | int(rows.buf[pos+5])<<40 |
					int(rows.buf[pos+6])<<48 | int(rows.buf[pos+7])<<56
				pos += 8
				continue
			}
		case *int8:
			switch rows.columns[i].Type {
			case myTypeString,
				myTypeVarChar, myTypeVarString,
				myTypeEnum, myTypeSet,
				myTypeLongBlob, myTypeMediumBlob, myTypeBlob, myTypeTinyBlob,
				myTypeGeometry, myTypeBit,
				myTypeDecimal, myTypeNewDecimal:
				var tmp []byte
				tmp, posN := lenDecString(rows.buf[pos:])
				pos += posN
				*d = parseInt[int8](tmp)
				continue
			case myTypeTiny:
				*d = int8(rows.buf[pos])
				pos += 1
				continue
			case myTypeShort, myTypeInt24, myTypeLong, myTypeLongLong:
				return errScanDestFewerThanSource(string(rows.columns[i].Name))
			}
		case *int16:
			switch rows.columns[i].Type {
			case myTypeString,
				myTypeVarChar, myTypeVarString,
				myTypeEnum, myTypeSet,
				myTypeLongBlob, myTypeMediumBlob, myTypeBlob, myTypeTinyBlob,
				myTypeGeometry, myTypeBit,
				myTypeDecimal, myTypeNewDecimal:
				var tmp []byte
				tmp, posN := lenDecString(rows.buf[pos:])
				pos += posN
				*d = parseInt[int16](tmp)
				continue
			case myTypeTiny:
				*d = int16(rows.buf[pos])
				pos += 1
				continue
			case myTypeShort:
				*d = int16(rows.buf[pos]) | int16(rows.buf[pos+1])<<8
				pos += 2
				continue
			case myTypeInt24, myTypeLong, myTypeLongLong:
				return errScanDestFewerThanSource(string(rows.columns[i].Name))
			}
		case *int32:
			switch rows.columns[i].Type {
			case myTypeString,
				myTypeVarChar, myTypeVarString,
				myTypeEnum, myTypeSet,
				myTypeLongBlob, myTypeMediumBlob, myTypeBlob, myTypeTinyBlob,
				myTypeGeometry, myTypeBit,
				myTypeDecimal, myTypeNewDecimal:
				var tmp []byte
				tmp, posN := lenDecString(rows.buf[pos:])
				pos += posN
				*d = parseInt[int32](tmp)
				continue
			case myTypeTiny:
				*d = int32(rows.buf[pos])
				pos += 1
				continue
			case myTypeShort:
				*d = int32(rows.buf[pos]) | int32(rows.buf[pos+1])<<8
				pos += 2
				continue
			case myTypeInt24, myTypeLong:
				*d = int32(rows.buf[pos]) | int32(rows.buf[pos+1])<<8 |
					int32(rows.buf[pos+2])<<16 | int32(rows.buf[pos+3])<<24
				pos += 4
				continue
			case myTypeLongLong:
				return errScanDestFewerThanSource(string(rows.columns[i].Name))
			}
		case *int64:
			switch rows.columns[i].Type {
			case myTypeString,
				myTypeVarChar, myTypeVarString,
				myTypeEnum, myTypeSet,
				myTypeLongBlob, myTypeMediumBlob, myTypeBlob, myTypeTinyBlob,
				myTypeGeometry, myTypeBit,
				myTypeDecimal, myTypeNewDecimal:
				var tmp []byte
				tmp, posN := lenDecString(rows.buf[pos:])
				pos += posN
				*d = parseInt[int64](tmp)
				continue
			case myTypeTiny:
				*d = int64(rows.buf[pos])
				pos += 1
				continue
			case myTypeShort:
				*d = int64(rows.buf[pos]) | int64(rows.buf[pos+1])<<8
				pos += 2
				continue
			case myTypeInt24, myTypeLong:
				*d = int64(rows.buf[pos]) | int64(rows.buf[pos+1])<<8 |
					int64(rows.buf[pos+2])<<16 | int64(rows.buf[pos+3])<<24
				pos += 4
				continue
			case myTypeLongLong:
				*d = int64(rows.buf[pos]) | int64(rows.buf[pos+1])<<8 |
					int64(rows.buf[pos+2])<<16 | int64(rows.buf[pos+3])<<24 |
					int64(rows.buf[pos+4])<<32 | int64(rows.buf[pos+5])<<40 |
					int64(rows.buf[pos+6])<<48 | int64(rows.buf[pos+7])<<56
				pos += 8
				continue
			}
		case *uint:
			switch rows.columns[i].Type {
			case myTypeString,
				myTypeVarChar, myTypeVarString,
				myTypeEnum, myTypeSet,
				myTypeLongBlob, myTypeMediumBlob, myTypeBlob, myTypeTinyBlob,
				myTypeGeometry, myTypeBit,
				myTypeDecimal, myTypeNewDecimal:
				var tmp []byte
				tmp, posN := lenDecString(rows.buf[pos:])
				pos += posN
				*d = parseUint[uint](tmp)
				continue
			case myTypeTiny:
				*d = uint(rows.buf[pos])
				pos += 1
				continue
			case myTypeShort:
				*d = uint(rows.buf[pos]) | uint(rows.buf[pos+1])<<8
				pos += 2
				continue
			case myTypeInt24, myTypeLong:
				*d = uint(rows.buf[pos]) | uint(rows.buf[pos+1])<<8 |
					uint(rows.buf[pos+2])<<16 | uint(rows.buf[pos+3])<<24
				pos += 4
				continue
			case myTypeLongLong:
				if isFewer64bit {
					return errScanDestFewerThanSource(string(rows.columns[i].Name))
				}
				*d = uint(rows.buf[pos]) | uint(rows.buf[pos+1])<<8 |
					uint(rows.buf[pos+2])<<16 | uint(rows.buf[pos+3])<<24 |
					uint(rows.buf[pos+4])<<32 | uint(rows.buf[pos+5])<<40 |
					uint(rows.buf[pos+6])<<48 | uint(rows.buf[pos+7])<<56
				pos += 8
				continue
			}
		case *uint8:
			switch rows.columns[i].Type {
			case myTypeString,
				myTypeVarChar, myTypeVarString,
				myTypeEnum, myTypeSet,
				myTypeLongBlob, myTypeMediumBlob, myTypeBlob, myTypeTinyBlob,
				myTypeGeometry, myTypeBit,
				myTypeDecimal, myTypeNewDecimal:
				var tmp []byte
				tmp, posN := lenDecString(rows.buf[pos:])
				pos += posN
				*d = parseUint[uint8](tmp)
				continue
			case myTypeTiny:
				*d = rows.buf[pos]
				pos += 1
				continue
			case myTypeShort, myTypeInt24, myTypeLong, myTypeLongLong:
				return errScanDestFewerThanSource(string(rows.columns[i].Name))
			}
		case *uint16:
			switch rows.columns[i].Type {
			case myTypeString,
				myTypeVarChar, myTypeVarString,
				myTypeEnum, myTypeSet,
				myTypeLongBlob, myTypeMediumBlob, myTypeBlob, myTypeTinyBlob,
				myTypeGeometry, myTypeBit,
				myTypeDecimal, myTypeNewDecimal:
				var tmp []byte
				tmp, posN := lenDecString(rows.buf[pos:])
				pos += posN
				*d = parseUint[uint16](tmp)
				continue
			case myTypeTiny:
				*d = uint16(rows.buf[pos])
				pos += 1
				continue
			case myTypeShort:
				*d = uint16(rows.buf[pos]) | uint16(rows.buf[pos+1])<<8
				pos += 2
				continue
			case myTypeInt24, myTypeLong, myTypeLongLong:
				return errScanDestFewerThanSource(string(rows.columns[i].Name))
			}
		case *uint32:
			switch rows.columns[i].Type {
			case myTypeString,
				myTypeVarChar, myTypeVarString,
				myTypeEnum, myTypeSet,
				myTypeLongBlob, myTypeMediumBlob, myTypeBlob, myTypeTinyBlob,
				myTypeGeometry, myTypeBit,
				myTypeDecimal, myTypeNewDecimal:
				var tmp []byte
				tmp, posN := lenDecString(rows.buf[pos:])
				pos += posN
				*d = parseUint[uint32](tmp)
				continue
			case myTypeTiny:
				*d = uint32(rows.buf[pos])
				pos += 1
				continue
			case myTypeShort:
				*d = uint32(rows.buf[pos]) | uint32(rows.buf[pos+1])<<8
				pos += 2
				continue
			case myTypeInt24, myTypeLong:
				*d = uint32(rows.buf[pos]) | uint32(rows.buf[pos+1])<<8 |
					uint32(rows.buf[pos+2])<<16 | uint32(rows.buf[pos+3])<<24
				pos += 4
				continue
			case myTypeLongLong:
				return errScanDestFewerThanSource(string(rows.columns[i].Name))
			}
		case *uint64:
			switch rows.columns[i].Type {
			case myTypeString,
				myTypeVarChar, myTypeVarString,
				myTypeEnum, myTypeSet,
				myTypeLongBlob, myTypeMediumBlob, myTypeBlob, myTypeTinyBlob,
				myTypeGeometry, myTypeBit,
				myTypeDecimal, myTypeNewDecimal:
				var tmp []byte
				tmp, posN := lenDecString(rows.buf[pos:])
				pos += posN
				*d = parseUint[uint64](tmp)
				continue
			case myTypeTiny:
				*d = uint64(rows.buf[pos])
				pos += 1
				continue
			case myTypeShort:
				*d = uint64(rows.buf[pos]) | uint64(rows.buf[pos+1])<<8
				pos += 2
				continue
			case myTypeInt24, myTypeLong:
				*d = uint64(rows.buf[pos]) | uint64(rows.buf[pos+1])<<8 |
					uint64(rows.buf[pos+2])<<16 | uint64(rows.buf[pos+3])<<24
				pos += 4
				continue
			case myTypeLongLong:
				*d = uint64(rows.buf[pos]) | uint64(rows.buf[pos+1])<<8 |
					uint64(rows.buf[pos+2])<<16 | uint64(rows.buf[pos+3])<<24 |
					uint64(rows.buf[pos+4])<<32 | uint64(rows.buf[pos+5])<<40 |
					uint64(rows.buf[pos+6])<<48 | uint64(rows.buf[pos+7])<<56
				pos += 8
				continue
			}
		}

		refDest := reflect.ValueOf(dest[i])

		if refDest.IsNil() {
			continue
		}

		if refDest.Kind() != reflect.Pointer {
			return errors.New("destination not a pointer")
		}
		refDest = reflect.Indirect(refDest)

		var refSrc reflect.Value
		switch rows.columns[i].Type {
		case myTypeString,
			myTypeVarChar, myTypeVarString,
			myTypeEnum, myTypeSet,
			myTypeLongBlob, myTypeMediumBlob, myTypeBlob, myTypeTinyBlob,
			myTypeGeometry, myTypeBit,
			myTypeDecimal, myTypeNewDecimal:
			var tmp []byte
			tmp, posN := lenDecString(rows.buf[pos:])
			pos += posN
			refSrc = reflect.ValueOf(string(tmp))
		case myTypeTiny, myTypeShort, myTypeInt24, myTypeLong, myTypeLongLong:
			var tmp uint64
			posN := rows.columns[i].intLen()
			for i := range posN {
				tmp |= uint64(rows.buf[pos+i]) << uint(8*i)
			}
			pos += posN
			refSrc = reflect.ValueOf(tmp)
		default:
			return errScanUnsupportedPair(rows.columns[i].Type, reflect.TypeOf(dest[i]).Name())
		}

		if refSrc.Type().ConvertibleTo(refDest.Type()) {
			refDest.Set(refSrc.Convert(refDest.Type()))
			continue
		}

		return errScanUnsupportedPair(rows.columns[i].Type, reflect.TypeOf(dest[i]).Name())
	}

	return nil
}
