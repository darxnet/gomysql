package gomysql

import (
	"crypto/tls"
	"testing"
)

func TestCollationID(t *testing.T) {
	t.Parallel()

	for name, want := range map[string]uint8{
		"":                   defaultCollation,
		"utf8mb4_general_ci": 45,
		"UTF8MB4_BIN":        46,
		"binary":             collationBinary,
		"utf8_general_ci":    33, // MySQL 5.7
		"utf8mb3_general_ci": 33,
		"utf8mb4_0900_ai_ci": 255,
	} {
		if got, err := (&Config{Collation: name}).collationID(); got != want || err != nil {
			t.Errorf("collationID(%q) = %d, %v; want %d", name, got, err, want)
		}
	}

	// Ids above 255 cannot go into the handshake.
	for _, name := range []string{"utf8mb4_0900_as_cs", "nope"} {
		if _, err := (&Config{Collation: name}).collationID(); err == nil {
			t.Errorf("collationID(%q): no error", name)
		}
	}
}

func TestTLSServerName(t *testing.T) {
	t.Parallel()

	for _, tt := range []struct {
		address string
		tls     *tls.Config
		want    string
	}{
		{"db.example:3306", &tls.Config{}, "db.example"},
		{"[::1]:3306", &tls.Config{}, "::1"},
		{"/run/mysqld.sock", &tls.Config{}, "/run/mysqld.sock"},
		{"db.example:3306", &tls.Config{ServerName: "other"}, "other"},
		{"db.example:3306", &tls.Config{InsecureSkipVerify: true}, ""}, //nolint:gosec // tested
	} {
		config := Config{Address: tt.address, TLS: tt.tls}
		if got := config.tlsConfig().ServerName; got != tt.want {
			t.Errorf("ServerName for %q = %q, want %q", tt.address, got, tt.want)
		}

		if tt.tls.ServerName == "" && !tt.tls.InsecureSkipVerify && config.TLS.ServerName != "" {
			t.Errorf("Config.TLS changed")
		}
	}
}
