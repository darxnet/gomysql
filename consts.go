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

// Capability flags, all of those MySQL defines: the client asks in the
// handshake response for some of those the server offers (see clientCaps),
// and each says why it is asked for or not.
//
// https://dev.mysql.com/doc/dev/mysql-server/latest/group__group__cs__capabilities__flags.html
//
//nolint:unused // all flags, also those not asked for, with the reason
const (
	// Asked for: obsolete since 4.1.1, but MariaDB calls it CLIENT_MYSQL
	// and takes a client without it for a MariaDB one.
	clientLongPassword uint32 = 0x00000001
	// With Config.FoundRows: affected rows count the rows matched.
	clientFoundRows uint32 = 0x00000002
	// Not asked for: the 4.1 protocol sends all column flags anyway.
	clientLongFlag uint32 = 0x00000004
	// With Config.Database.
	clientConnectWithDB uint32 = 0x00000008
	// Not asked for: deprecated; it forbids database.table.column.
	clientNoSchema uint32 = 0x00000010
	// With Config.Compress.
	clientCompress uint32 = 0x00000020
	// Not asked for: no effect since MySQL 3.22.
	clientODBC uint32 = 0x00000040
	// Asked for: LOAD DATA LOCAL, answered only with the reader of LoadData.
	clientLocalFiles uint32 = 0x00000080
	// Not asked for: it changes how the server parses function names, as
	// sql_mode IGNORE_SPACE does.
	clientIgnoreSpace uint32 = 0x00000100
	// Asked for and required: servers without it are refused.
	clientProtocol41 uint32 = 0x00000200
	// Not asked for: the server would close idle connections after
	// interactive_timeout, and the pool follows wait_timeout.
	clientInteractive uint32 = 0x00000400
	// With Config.TLS.
	clientSSL uint32 = 0x00000800
	// Not asked for: a flag of the C client library, not of the protocol.
	clientIgnoreSIGPIPE uint32 = 0x00001000
	// Not asked for: with the 4.1 protocol, OK packets carry the status
	// flags anyway.
	clientTransactions uint32 = 0x00002000
	// Not asked for: deprecated, of the 4.1 protocol before its release.
	clientReserved uint32 = 0x00004000
	// Asked for: the 4.1 authentication (CLIENT_RESERVED2, formerly
	// CLIENT_SECURE_CONNECTION); MariaDB still names it SECURE_CONNECTION.
	clientSecureConn uint32 = 0x00008000
	// Not asked for: one statement per query, which keeps stacked
	// statements out of an SQL injection.
	clientMultiStatements uint32 = 0x00010000
	// Asked for: CALL returns its result sets (Rows.NextResultSet).
	clientMultiResults uint32 = 0x00020000
	// Asked for: the same for CALL in a prepared statement.
	clientPSMultiResults uint32 = 0x00040000
	// Asked for: authentication plugins and the auth switch.
	clientPluginAuth uint32 = 0x00080000
	// With Config.ConnectAttrs, if the server has it.
	clientConnectAttrs uint32 = 0x00100000
	// For an auth response longer than 255 bytes.
	clientPluginAuthLenEncClientData uint32 = 0x00200000
	// Not asked for: an account with an expired password is refused at
	// once (error 1862), instead of a session that can only change it.
	clientCanHandleExpiredPasswords uint32 = 0x00400000
	// Not asked for: session state changes are not read; the escaping mode
	// comes from serverStatusNoBackslashEscapes.
	clientSessionTrack uint32 = 0x00800000
	// Asked for if the server has it: OK packets instead of EOF.
	clientDeprecateEOF uint32 = 0x01000000
	// Not asked for: rows are decoded with their column definitions.
	clientOptionalResultsetMetadata uint32 = 0x02000000
	// Not asked for: compression is zlib only.
	clientZstdCompressionAlgorithm uint32 = 0x04000000
	// Not asked for: query attributes are not supported.
	clientQueryAttributes uint32 = 0x08000000
	// Not asked for: multi-factor authentication is not supported.
	clientMultiFactorAuthentication uint32 = 0x10000000
	// Not asked for: reserved to extend the flags to 64 bits.
	clientCapabilityExtension uint32 = 0x20000000
	// Not asked for: deprecated; the tls.Config verifies the server.
	clientSSLVerifyServerCert uint32 = 0x40000000
	// Not asked for: a flag of the C client library, not of the protocol.
	clientRememberOptions uint32 = 0x80000000
)

// Status flags, all of those MySQL defines (0x0004 is not used), as
// OK and EOF packets carry them; each says why it is read or not.
//
// https://dev.mysql.com/doc/dev/mysql-server/latest/mysql__com_8h.html
//
//nolint:unused // all flags, also those not read, with the reason
const (
	// Read: the pool closes a connection returned inside a transaction.
	serverStatusInTrans uint16 = 0x0001
	// Not read: serverStatusInTrans is what the pool needs.
	serverStatusAutocommit uint16 = 0x0002
	// Read: another result set follows (CALL, Rows.NextResultSet).
	serverStatusMoreResultsExists uint16 = 0x0008
	// Not read: hints for the slow query log.
	serverStatusNoGoodIndexUsed uint16 = 0x0010
	serverStatusNoIndexUsed     uint16 = 0x0020
	// Not read: server-side cursors (COM_STMT_FETCH) are not supported.
	serverStatusCursorExists uint16 = 0x0040
	serverStatusLastRowSent  uint16 = 0x0080
	// Not read: it says only that some database was dropped.
	serverStatusDbDropped uint16 = 0x0100
	// Read: how arguments are escaped, and the statements cached for it.
	serverStatusNoBackslashEscapes uint16 = 0x0200
	// Not read: the column definitions of every execute are read anyway.
	serverStatusMetadataChanged uint16 = 0x0400
	// Not read: a hint for the slow query log.
	serverStatusQueryWasSlow uint16 = 0x0800
	// Not read: the OUT parameters of a prepared CALL come as one more
	// result set.
	serverStatusPsOutParams uint16 = 0x1000
	// Not read: set only together with serverStatusInTrans.
	serverStatusInTransReadonly uint16 = 0x2000
	// Not read: it comes with clientSessionTrack, which is not asked for.
	serverStatusSessionStateChanged uint16 = 0x4000
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
