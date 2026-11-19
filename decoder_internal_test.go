package gomysql

import (
	"errors"
	"testing"
)

// columnDefinition returns the definition of column id INT of table db.t.
func columnDefinition() []byte {
	var dst []byte
	for _, s := range []string{"def", "db", "t", "t", "id", "id"} {
		dst = appendLenEncString(dst, s)
	}

	// fixed-length fields: charset 33, length 11, type, flags, decimals,
	// filler
	return append(dst, 0x0c, 33, 0, 11, 0, 0, 0, myTypeLong, 0, 0, 0, 0, 0)
}

// parsers run every packet parser that bounds-checks its input.
var parsers = map[string]func(buf []byte) error{
	"OK": func(buf []byte) error {
		return (&Conn{}).processOKPacket(buf)
	},
	"EOF": func(buf []byte) error {
		return (&Conn{}).processEOFPacket(buf)
	},
	"EOF/deprecate": func(buf []byte) error {
		return (&Conn{capFlags: clientDeprecateEOF}).processEOFPacket(buf)
	},
	"column count": func(buf []byte) error {
		_, err := columnCount(buf)
		return err
	},
	"column definition": func(buf []byte) error {
		return new(ColumnDefinition).parse(buf)
	},
	"handshake": func(buf []byte) error {
		_, _, err := (&Conn{}).handshakeV10(buf)
		return err
	},
	"ERR": func(buf []byte) error {
		var e *Error
		if err := (&Conn{}).processERRPacket(buf); !errors.As(err, &e) {
			return err
		}

		return nil
	},
}

func TestParsersRejectTruncatedPackets(t *testing.T) {
	t.Parallel()

	valid := map[string][]byte{
		"OK":                {iOK, 0xfc, 0x00, 0x01, 0x05, 0x02, 0x00, 0x00, 0x00},
		"EOF":               {iEOF, 0x00, 0x00, 0x02, 0x00},
		"EOF/deprecate":     {iEOF, 0x00, 0x00, 0x02, 0x00, 0x00, 0x00},
		"column count":      {0xfc, 0x00, 0x01},
		"column definition": columnDefinition(),
		"handshake":         handshakeV10(fakeCapabilities, "mysql_native_password"),
		"ERR":               append([]byte{iERR, 0x15, 0x04, '#', '2', '8', '0', '0', '0'}, "denied"...),
	}

	for name, parse := range parsers {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			buf := valid[name]
			if err := parse(buf); err != nil {
				t.Fatalf("valid packet: %v", err)
			}

			for n := range buf {
				err := parse(buf[:n:n])
				if err != nil && !errors.Is(err, ErrBadConn) {
					t.Errorf("%d bytes: %v, want ErrBadConn", n, err)
				}
			}
		})
	}
}

// TestParsersRejectShortPackets checks the prefixes that cannot be valid.
func TestParsersRejectShortPackets(t *testing.T) {
	t.Parallel()

	short := map[string][][]byte{
		"OK":                {{iOK}, {iOK, 0, 0}, {iOK, 0, 0, 2}, {iOK, 0xfc, 0}},
		"EOF":               {{iEOF}, {iEOF, 0, 0, 2}},
		"EOF/deprecate":     {{iEOF, 0, 0, 2}, {iEOF, 0xfd, 0, 0, 0, 0, 2}},
		"column count":      {{}, {0}, {0xfb}, {0xfc, 0}, {0xfe, 0, 0, 0, 0, 0, 0, 1, 0}},
		"column definition": {{3, 'd', 'e'}, columnDefinition()[:25]},
	}

	for name, packets := range short {
		for _, buf := range packets {
			if err := parsers[name](buf); !errors.Is(err, ErrBadConn) {
				t.Errorf("%s %x: %v, want ErrBadConn", name, buf, err)
			}
		}
	}
}

// TestEOFStatusFlagsAfterLongCounts checks that the status flags of an OK
// packet with the EOF header are found after length-encoded integers that
// take more than one byte.
func TestEOFStatusFlagsAfterLongCounts(t *testing.T) {
	t.Parallel()

	conn := &Conn{capFlags: clientDeprecateEOF}
	buf := []byte{iEOF, 0xfc, 0x00, 0x01, 0xfc, 0x00, 0x02, byte(serverStatusMoreResultsExists), 0x00, 0x00, 0x00}

	if err := conn.processEOFPacket(buf); err != nil {
		t.Fatal(err)
	}

	if conn.statusFlags != serverStatusMoreResultsExists {
		t.Fatalf("status flags %#x, want %#x", conn.statusFlags, serverStatusMoreResultsExists)
	}
}

func TestColumnDefinitionOwnsNames(t *testing.T) {
	t.Parallel()

	buf := columnDefinition()

	var column ColumnDefinition
	if err := column.parse(buf); err != nil {
		t.Fatal(err)
	}

	clear(buf)

	if string(column.Schema) != "db" || string(column.Name) != "id" || string(column.OrgName) != "id" {
		t.Fatalf("names changed with the read buffer: %q %q %q", column.Schema, column.Name, column.OrgName)
	}

	if column.Type != myTypeLong || column.Len != 11 || column.CharSet != 33 {
		t.Fatalf("fixed fields: %+v", column)
	}

	column.Name = append(column.Name, 'x')
	if string(column.OrgName) != "id" {
		t.Fatalf("appending to Name changed OrgName: %q", column.OrgName)
	}
}

func FuzzParsers(f *testing.F) {
	f.Add([]byte{iOK, 0, 0, 2, 0, 0, 0})
	f.Add([]byte{iEOF, 0xfc, 0, 1, 0, 2, 0})
	f.Add(columnDefinition())
	f.Add(handshakeV10(fakeCapabilities, "mysql_native_password"))

	f.Fuzz(func(t *testing.T, buf []byte) {
		for name, parse := range parsers {
			if err := parse(buf); err != nil && !errors.Is(err, ErrBadConn) && !errors.Is(err, errServerTooOld) {
				t.Errorf("%s: %v", name, err)
			}
		}
	})
}
