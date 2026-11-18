package gomysql

const (
	headerSize     = 4
	maxPacketSize  = 1 << 24
	maxPayloadSize = maxPacketSize - 1

	headerCompressSize = 7
)

const (
	readerBufferSize int = 0xffff // 64K
	// writerBufferSize only batches small packets: bufio.Writer sends a
	// write larger than its buffer straight to the socket.
	writerBufferSize      int = 4096
	writerMinCompressSize int = 50
)

const isFewer64bit = uint64(^uint(0)) < ^uint64(0)

const timeFormat string = "2006-01-02 15:04:05.000000"

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

// https://dev.mysql.com/doc/dev/mysql-server/latest/page_protocol_basic_character_set.html
const (
	CharSet_big5_chinese_ci          = 1
	CharSet_latin2_czech_cs          = 2
	CharSet_dec8_swedish_ci          = 3
	CharSet_cp850_general_ci         = 4
	CharSet_latin1_german1_ci        = 5
	CharSet_hp8_english_ci           = 6
	CharSet_koi8r_general_ci         = 7
	CharSet_latin1_swedish_ci        = 8
	CharSet_latin2_general_ci        = 9
	CharSet_swe7_swedish_ci          = 10
	CharSet_ascii_general_ci         = 11
	CharSet_ujis_japanese_ci         = 12
	CharSet_sjis_japanese_ci         = 13
	CharSet_cp1251_bulgarian_ci      = 14
	CharSet_latin1_danish_ci         = 15
	CharSet_hebrew_general_ci        = 16
	CharSet_tis620_thai_ci           = 18
	CharSet_euckr_korean_ci          = 19
	CharSet_latin7_estonian_cs       = 20
	CharSet_latin2_hungarian_ci      = 21
	CharSet_koi8u_general_ci         = 22
	CharSet_cp1251_ukrainian_ci      = 23
	CharSet_gb2312_chinese_ci        = 24
	CharSet_greek_general_ci         = 25
	CharSet_cp1250_general_ci        = 26
	CharSet_latin2_croatian_ci       = 27
	CharSet_gbk_chinese_ci           = 28
	CharSet_cp1257_lithuanian_ci     = 29
	CharSet_latin5_turkish_ci        = 30
	CharSet_latin1_german2_ci        = 31
	CharSet_armscii8_general_ci      = 32
	CharSet_utf8_general_ci          = 33
	CharSet_cp1250_czech_cs          = 34
	CharSet_ucs2_general_ci          = 35
	CharSet_cp866_general_ci         = 36
	CharSet_keybcs2_general_ci       = 37
	CharSet_macce_general_ci         = 38
	CharSet_macroman_general_ci      = 39
	CharSet_cp852_general_ci         = 40
	CharSet_latin7_general_ci        = 41
	CharSet_latin7_general_cs        = 42
	CharSet_macce_bin                = 43
	CharSet_cp1250_croatian_ci       = 44
	CharSet_utf8mb4_general_ci       = 45
	CharSet_utf8mb4_bin              = 46
	CharSet_latin1_bin               = 47
	CharSet_latin1_general_ci        = 48
	CharSet_latin1_general_cs        = 49
	CharSet_cp1251_bin               = 50
	CharSet_cp1251_general_ci        = 51
	CharSet_cp1251_general_cs        = 52
	CharSet_macroman_bin             = 53
	CharSet_utf16_general_ci         = 54
	CharSet_utf16_bin                = 55
	CharSet_utf16le_general_ci       = 56
	CharSet_cp1256_general_ci        = 57
	CharSet_cp1257_bin               = 58
	CharSet_cp1257_general_ci        = 59
	CharSet_utf32_general_ci         = 60
	CharSet_utf32_bin                = 61
	CharSet_utf16le_bin              = 62
	CharSet_binary                   = 63
	CharSet_armscii8_bin             = 64
	CharSet_ascii_bin                = 65
	CharSet_cp1250_bin               = 66
	CharSet_cp1256_bin               = 67
	CharSet_cp866_bin                = 68
	CharSet_dec8_bin                 = 69
	CharSet_greek_bin                = 70
	CharSet_hebrew_bin               = 71
	CharSet_hp8_bin                  = 72
	CharSet_keybcs2_bin              = 73
	CharSet_koi8r_bin                = 74
	CharSet_koi8u_bin                = 75
	CharSet_latin2_bin               = 77
	CharSet_latin5_bin               = 78
	CharSet_latin7_bin               = 79
	CharSet_cp850_bin                = 80
	CharSet_cp852_bin                = 81
	CharSet_swe7_bin                 = 82
	CharSet_utf8_bin                 = 83
	CharSet_big5_bin                 = 84
	CharSet_euckr_bin                = 85
	CharSet_gb2312_bin               = 86
	CharSet_gbk_bin                  = 87
	CharSet_sjis_bin                 = 88
	CharSet_tis620_bin               = 89
	CharSet_ucs2_bin                 = 90
	CharSet_ujis_bin                 = 91
	CharSet_geostd8_general_ci       = 92
	CharSet_geostd8_bin              = 93
	CharSet_latin1_spanish_ci        = 94
	CharSet_cp932_japanese_ci        = 95
	CharSet_cp932_bin                = 96
	CharSet_eucjpms_japanese_ci      = 97
	CharSet_eucjpms_bin              = 98
	CharSet_cp1250_polish_ci         = 99
	CharSet_utf16_unicode_ci         = 101
	CharSet_utf16_icelandic_ci       = 102
	CharSet_utf16_latvian_ci         = 103
	CharSet_utf16_romanian_ci        = 104
	CharSet_utf16_slovenian_ci       = 105
	CharSet_utf16_polish_ci          = 106
	CharSet_utf16_estonian_ci        = 107
	CharSet_utf16_spanish_ci         = 108
	CharSet_utf16_swedish_ci         = 109
	CharSet_utf16_turkish_ci         = 110
	CharSet_utf16_czech_ci           = 111
	CharSet_utf16_danish_ci          = 112
	CharSet_utf16_lithuanian_ci      = 113
	CharSet_utf16_slovak_ci          = 114
	CharSet_utf16_spanish2_ci        = 115
	CharSet_utf16_roman_ci           = 116
	CharSet_utf16_persian_ci         = 117
	CharSet_utf16_esperanto_ci       = 118
	CharSet_utf16_hungarian_ci       = 119
	CharSet_utf16_sinhala_ci         = 120
	CharSet_utf16_german2_ci         = 121
	CharSet_utf16_croatian_ci        = 122
	CharSet_utf16_unicode_520_ci     = 123
	CharSet_utf16_vietnamese_ci      = 124
	CharSet_ucs2_unicode_ci          = 128
	CharSet_ucs2_icelandic_ci        = 129
	CharSet_ucs2_latvian_ci          = 130
	CharSet_ucs2_romanian_ci         = 131
	CharSet_ucs2_slovenian_ci        = 132
	CharSet_ucs2_polish_ci           = 133
	CharSet_ucs2_estonian_ci         = 134
	CharSet_ucs2_spanish_ci          = 135
	CharSet_ucs2_swedish_ci          = 136
	CharSet_ucs2_turkish_ci          = 137
	CharSet_ucs2_czech_ci            = 138
	CharSet_ucs2_danish_ci           = 139
	CharSet_ucs2_lithuanian_ci       = 140
	CharSet_ucs2_slovak_ci           = 141
	CharSet_ucs2_spanish2_ci         = 142
	CharSet_ucs2_roman_ci            = 143
	CharSet_ucs2_persian_ci          = 144
	CharSet_ucs2_esperanto_ci        = 145
	CharSet_ucs2_hungarian_ci        = 146
	CharSet_ucs2_sinhala_ci          = 147
	CharSet_ucs2_german2_ci          = 148
	CharSet_ucs2_croatian_ci         = 149
	CharSet_ucs2_unicode_520_ci      = 150
	CharSet_ucs2_vietnamese_ci       = 151
	CharSet_ucs2_general_mysql500_ci = 159
	CharSet_utf32_unicode_ci         = 160
	CharSet_utf32_icelandic_ci       = 161
	CharSet_utf32_latvian_ci         = 162
	CharSet_utf32_romanian_ci        = 163
	CharSet_utf32_slovenian_ci       = 164
	CharSet_utf32_polish_ci          = 165
	CharSet_utf32_estonian_ci        = 166
	CharSet_utf32_spanish_ci         = 167
	CharSet_utf32_swedish_ci         = 168
	CharSet_utf32_turkish_ci         = 169
	CharSet_utf32_czech_ci           = 170
	CharSet_utf32_danish_ci          = 171
	CharSet_utf32_lithuanian_ci      = 172
	CharSet_utf32_slovak_ci          = 173
	CharSet_utf32_spanish2_ci        = 174
	CharSet_utf32_roman_ci           = 175
	CharSet_utf32_persian_ci         = 176
	CharSet_utf32_esperanto_ci       = 177
	CharSet_utf32_hungarian_ci       = 178
	CharSet_utf32_sinhala_ci         = 179
	CharSet_utf32_german2_ci         = 180
	CharSet_utf32_croatian_ci        = 181
	CharSet_utf32_unicode_520_ci     = 182
	CharSet_utf32_vietnamese_ci      = 183
	CharSet_utf8_unicode_ci          = 192
	CharSet_utf8_icelandic_ci        = 193
	CharSet_utf8_latvian_ci          = 194
	CharSet_utf8_romanian_ci         = 195
	CharSet_utf8_slovenian_ci        = 196
	CharSet_utf8_polish_ci           = 197
	CharSet_utf8_estonian_ci         = 198
	CharSet_utf8_spanish_ci          = 199
	CharSet_utf8_swedish_ci          = 200
	CharSet_utf8_turkish_ci          = 201
	CharSet_utf8_czech_ci            = 202
	CharSet_utf8_danish_ci           = 203
	CharSet_utf8_lithuanian_ci       = 204
	CharSet_utf8_slovak_ci           = 205
	CharSet_utf8_spanish2_ci         = 206
	CharSet_utf8_roman_ci            = 207
	CharSet_utf8_persian_ci          = 208
	CharSet_utf8_esperanto_ci        = 209
	CharSet_utf8_hungarian_ci        = 210
	CharSet_utf8_sinhala_ci          = 211
	CharSet_utf8_german2_ci          = 212
	CharSet_utf8_croatian_ci         = 213
	CharSet_utf8_unicode_520_ci      = 214
	CharSet_utf8_vietnamese_ci       = 215
	CharSet_utf8_general_mysql500_ci = 223
	CharSet_utf8mb4_unicode_ci       = 224
	CharSet_utf8mb4_icelandic_ci     = 225
	CharSet_utf8mb4_latvian_ci       = 226
	CharSet_utf8mb4_romanian_ci      = 227
	CharSet_utf8mb4_slovenian_ci     = 228
	CharSet_utf8mb4_polish_ci        = 229
	CharSet_utf8mb4_estonian_ci      = 230
	CharSet_utf8mb4_spanish_ci       = 231
	CharSet_utf8mb4_swedish_ci       = 232
	CharSet_utf8mb4_turkish_ci       = 233
	CharSet_utf8mb4_czech_ci         = 234
	CharSet_utf8mb4_danish_ci        = 235
	CharSet_utf8mb4_lithuanian_ci    = 236
	CharSet_utf8mb4_slovak_ci        = 237
	CharSet_utf8mb4_spanish2_ci      = 238
	CharSet_utf8mb4_roman_ci         = 239
	CharSet_utf8mb4_persian_ci       = 240
	CharSet_utf8mb4_esperanto_ci     = 241
	CharSet_utf8mb4_hungarian_ci     = 242
	CharSet_utf8mb4_sinhala_ci       = 243
	CharSet_utf8mb4_german2_ci       = 244
	CharSet_utf8mb4_croatian_ci      = 245
	CharSet_utf8mb4_unicode_520_ci   = 246
	CharSet_utf8mb4_vietnamese_ci    = 247
	CharSet_gb18030_chinese_ci       = 248
	CharSet_gb18030_bin              = 249
	CharSet_gb18030_unicode_520_ci   = 250
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
