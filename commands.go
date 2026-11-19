package gomysql

// ColumnDefinition describes a column of a result set or a parameter of
// a prepared statement (Protocol::ColumnDefinition41).
type ColumnDefinition struct {
	Catalog  []byte
	Schema   []byte
	Table    []byte
	OrgTable []byte
	Name     []byte
	OrgName  []byte

	CharSet  byte
	Len      uint32 // [4]byte
	Type     byte   // [1]byte
	Flags    uint16 // [2]byte
	Decimals byte   // [1]byte
}

// intLen is the size of an integer column in the binary protocol.
func (cd *ColumnDefinition) intLen() int {
	switch cd.Type {
	case myTypeLongLong:
		return 8
	case myTypeLong, myTypeInt24:
		return 4
	case myTypeShort, myTypeYear:
		return 2
	default: // myTypeTiny
		return 1
	}
}
