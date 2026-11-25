package sqldriver

import (
	"strings"
	"testing"
	"time"
)

func TestParseDSN(t *testing.T) {
	t.Parallel()

	config, timeout, err := parseDSN("u:p@ss:w@rd@tcp(db.example:3307)/my%2Fdb?" +
		"loc=Europe%2FKyiv&collation=utf8mb4_bin&compress=true&clientFoundRows=1&" +
		"allowCleartextPasswords=false&connectionAttributes=app:x,env:prod&timeout=3s&" +
		"time_zone=%27%2B00:00%27&sql_mode=%27STRICT_ALL_TABLES%27&tls=skip-verify")
	if err != nil {
		t.Fatal(err)
	}

	switch {
	case config.User != "u" || config.Password != "p@ss:w@rd":
		t.Errorf("user %q, password %q", config.User, config.Password)
	case config.Network != "tcp" || config.Address != "db.example:3307" || config.Database != "my/db":
		t.Errorf("%s %s %s", config.Network, config.Address, config.Database)
	case config.Location == nil || config.Location.String() != "Europe/Kyiv" || config.Collation != "utf8mb4_bin":
		t.Errorf("loc %v, collation %s", config.Location, config.Collation)
	case !config.Compress || !config.FoundRows || config.AllowCleartextPasswords:
		t.Errorf("flags %+v", config)
	case config.ConnectAttrs["app"] != "x" || config.ConnectAttrs["env"] != "prod" || timeout != 3*time.Second:
		t.Errorf("attrs %v, timeout %v", config.ConnectAttrs, timeout)
	case config.TLS == nil || !config.TLS.InsecureSkipVerify:
		t.Errorf("tls %+v", config.TLS)
	case strings.Join(config.InitCommands, "; ") != "SET sql_mode='STRICT_ALL_TABLES'; SET time_zone='+00:00'":
		t.Errorf("init commands %q", config.InitCommands)
	}
}

func TestParseDSNAddress(t *testing.T) {
	t.Parallel()

	for dsn, want := range map[string]string{
		"/db":                     "tcp 127.0.0.1:3306",
		"tcp(db)/db":              "tcp db:3306",
		"tcp([::1])/db":           "tcp [::1]:3306",
		"tcp([::1]:3307)/db":      "tcp [::1]:3307",
		"unix/db":                 "unix /tmp/mysql.sock",
		"u@unix(/run/my.sock)/db": "unix /run/my.sock",
	} {
		config, _, err := parseDSN(dsn)
		if got := config.Network + " " + config.Address; err != nil || got != want {
			t.Errorf("%s: %s, %v; want %s", dsn, got, err, want)
		}
	}
}

// The booleans of strconv.ParseBool, and skip-verify in any case.
func TestParseDSNBooleans(t *testing.T) {
	t.Parallel()

	config, _, err := parseDSN("/db?compress=t&clientFoundRows=TRUE&rejectReadOnly=F&tls=T")
	if err != nil || !config.Compress || !config.FoundRows || config.RejectReadOnly ||
		config.TLS == nil || config.TLS.InsecureSkipVerify {
		t.Errorf("%+v, %v", config, err)
	}

	config, _, err = parseDSN("/db?tls=SKIP-VERIFY")
	if err != nil || config.TLS == nil || !config.TLS.InsecureSkipVerify {
		t.Errorf("SKIP-VERIFY: %+v, %v", config.TLS, err)
	}

	config, _, err = parseDSN("/db?tls=0")
	if err != nil || config.TLS != nil {
		t.Errorf("tls=0: %+v, %v", config.TLS, err)
	}
}

func TestParseDSNErrors(t *testing.T) {
	t.Parallel()

	for _, dsn := range []string{
		"no-slash",
		"tcp(db/db",
		"udp(db)/db",
		"/db?parseTime=true",
		"/db?multiStatements=false",
		"/db?readTimeout=1s",
		"/db?tls=preferred",
		"/db?tls=custom",
		"/db?compress=maybe",
		"/db?compress=yes",
		"/db?tls=tRuE",
		"/db?timeout=soon",
		"/db?loc=Nowhere%2FCity",
		"/db?connectionAttributes=novalue",
		"/db?%zz",
	} {
		if _, _, err := parseDSN(dsn); err == nil {
			t.Errorf("%s: no error", dsn)
		}
	}
}

func TestParseDSNCharset(t *testing.T) {
	t.Parallel()

	config, _, err := parseDSN("/db?charset=latin1,utf8mb4&time_zone=%27%2B00:00%27")
	if err != nil || config.Collation != "" ||
		strings.Join(config.InitCommands, "; ") != "SET NAMES latin1; SET time_zone='+00:00'" {
		t.Errorf("charset: %q %q, %v", config.Collation, config.InitCommands, err)
	}

	config, _, err = parseDSN("/db?charset=latin1&collation=latin1_bin")
	if err != nil || config.Collation != "latin1_bin" || len(config.InitCommands) != 0 {
		t.Errorf("charset with collation: %q %q, %v", config.Collation, config.InitCommands, err)
	}

	if _, _, err := parseDSN("/db?charset=utf8mb4;DROP"); err == nil {
		t.Error("a charset that is not a name")
	}
}

func TestParseDSNAddressesAndNames(t *testing.T) {
	t.Parallel()

	for dsn, want := range map[string]string{
		"tcp(::1)/db":           "[::1]:3306",
		"tcp([::1])/db":         "[::1]:3306",
		"tcp(db.example)/db":    "db.example:3306",
		"tcp(10.0.0.1:3307)/db": "10.0.0.1:3307",
	} {
		config, _, err := parseDSN(dsn)
		if err != nil || config.Address != want {
			t.Errorf("%s: %q, %v; want %q", dsn, config.Address, err, want)
		}
	}

	config, _, err := parseDSN("/db?charset=UTF8MB4")
	if err != nil || len(config.InitCommands) != 1 || config.InitCommands[0] != "SET NAMES UTF8MB4" {
		t.Errorf("upper-case charset: %q, %v", config.InitCommands, err)
	}

	for _, dsn := range []string{
		"/db?sql_mode=a&sql_mode=b", // given twice
		"/db?%3Bx=1",                // not a variable name
		"/db?a-b=1",
		"/db?charset=utf8;x",
		"tcp(h:)/db",      // empty port
		"/db?timeout=-1s", // negative
		"tcp(h)/my/db",    // unescaped /
	} {
		if _, _, err := parseDSN(dsn); err == nil {
			t.Errorf("%s: no error", dsn)
		}
	}
}
