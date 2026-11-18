package gomysql

/*
https://dev.mysql.com/doc/internals/en/text-protocol.html
https://dev.mysql.com/doc/dev/mysql-server/latest/page_protocol_command_phase_utility.html
14.6.1 COM_SLEEP
14.6.2 COM_QUIT
14.6.3 COM_INIT_DB
14.6.4 COM_QUERY
14.6.5 COM_FIELD_LIST
14.6.6 COM_CREATE_DB
14.6.7 COM_DROP_DB
14.6.8 COM_REFRESH
14.6.9 COM_SHUTDOWN
14.6.10 COM_STATISTICS
14.6.11 COM_PROCESS_INFO
14.6.12 COM_CONNECT
14.6.13 COM_PROCESS_KILL
14.6.14 COM_DEBUG
14.6.15 COM_PING
14.6.16 COM_TIME
14.6.17 COM_DELAYED_INSERT
14.6.18 COM_CHANGE_USER
14.6.19 COM_RESET_CONNECTION
14.6.20 COM_DAEMON
*/

// ColumnDefinition

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
