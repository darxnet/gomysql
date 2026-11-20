package gomysql

const (
	headerSize     = 4
	maxPacketSize  = 1 << 24
	maxPayloadSize = maxPacketSize - 1

	headerCompressSize = 7
)

const (
	// readerBufferSize is the read buffer of a connection; a packet that
	// does not fit is read into a new slice.
	readerBufferSize = 64 << 10

	// writerMinCompressSize is the smallest chunk worth compressing.
	writerMinCompressSize = 50
)

// https://dev.mysql.com/doc/internals/en/generic-response-packets.html
const (
	iOK  byte = 0x00
	iEOF byte = 0xfe
	iERR byte = 0xff

	iLocalInfile byte = 0xfb
)

// https://dev.mysql.com/doc/internals/en/connection-phase-packets.html#packet-Protocol::Handshake
const (
	serverProtocolV10 byte = 10
)

// https://dev.mysql.com/doc/internals/en/capability-flags.html#packet-Protocol::CapabilityFlags
const (
	clientLongPassword uint32 = 1 << iota
	clientFoundRows
	clientLongFlag
	clientConnectWithDB
	clientNoSchema
	clientCompress
	clientODBC
	clientLocalFiles
	clientIgnoreSpace
	clientProtocol41
	clientInteractive
	clientSSL
	clientIgnoreSIGPIPE
	clientTransactions
	clientReserved
	clientSecureConn
	clientMultiStatements
	clientMultiResults
	clientPSMultiResults
	clientPluginAuth
	clientConnectAttrs
	clientPluginAuthLenEncClientData
	clientCanHandleExpiredPasswords
	clientSessionTrack
	clientDeprecateEOF
)

// https://dev.mysql.com/doc/internals/en/status-flags.html
// https://dev.mysql.com/doc/dev/mysql-server/latest/mysql__com_8h.html#a1d854e841086925be1883e4d7b4e8cad
const (
	serverStatusInTrans uint16 = 1 << iota
	serverStatusAutocommit
	_ // Not in docs
	serverStatusMoreResultsExists
	serverStatusNoGoodIndexUsed
	serverStatusNoIndexUsed
	serverStatusCursorExists
	serverStatusLastRowSent
	serverStatusDbDropped
	serverStatusNoBackslashEscapes
	serverStatusMetadataChanged
	serverStatusQueryWasSlow
	serverStatusPsOutParams
	serverStatusInTransReadonly
	serverStatusSessionStateChanged
)

// https://dev.mysql.com/doc/internals/en/com-query-response.html#column-type
const (
	myTypeDecimal    byte = 0x00
	myTypeTiny       byte = 0x01
	myTypeShort      byte = 0x02
	myTypeLong       byte = 0x03
	myTypeFloat      byte = 0x04
	myTypeDouble     byte = 0x05
	myTypeNull       byte = 0x06
	myTypeTimestamp  byte = 0x07
	myTypeLongLong   byte = 0x08
	myTypeInt24      byte = 0x09
	myTypeDate       byte = 0x0a
	myTypeTime       byte = 0x0b
	myTypeDatetime   byte = 0x0c
	myTypeYear       byte = 0x0d
	myTypeNewDate    byte = 0x0e
	myTypeVarChar    byte = 0x0f
	myTypeBit        byte = 0x10
	myTypeTimestamp2 byte = 0x11
	myTypeDatetime2  byte = 0x12
	myTypeTime2      byte = 0x13

	myTypeVector     byte = 0xf2 // MySQL 9
	myTypeJSON       byte = 0xf5
	myTypeNewDecimal byte = 0xf6
	myTypeEnum       byte = 0xf7
	myTypeSet        byte = 0xf8
	myTypeTinyBlob   byte = 0xf9
	myTypeMediumBlob byte = 0xfa
	myTypeLongBlob   byte = 0xfb
	myTypeBlob       byte = 0xfc
	myTypeVarString  byte = 0xfd
	myTypeString     byte = 0xfe
	myTypeGeometry   byte = 0xff
)
