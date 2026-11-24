package sqldriver

import (
	"database/sql/driver"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/darxnet/gomysql"
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

	config, _, err := parseDSN("/db?charset=latin1,utf8mb4")
	if err != nil || config.Collation != "latin1_general_ci" {
		t.Errorf("charset: %q, %v", config.Collation, err)
	}

	config, _, err = parseDSN("/db?charset=latin1&collation=latin1_bin")
	if err != nil || config.Collation != "latin1_bin" {
		t.Errorf("charset with collation: %q, %v", config.Collation, err)
	}
}

func TestParseDSNFlags(t *testing.T) {
	t.Parallel()

	config, _, err := parseDSN("/db?allowPublicKeyRetrieval=true&rejectReadOnly=1")
	if err != nil || !config.AllowPublicKeyRetrieval || !config.RejectReadOnly {
		t.Errorf("%+v, %v", config, err)
	}
}

// With RejectReadOnly, a statement refused by a read-only server gives
// driver.ErrBadConn, so database/sql retries it on a new connection; other
// errors do not.
func TestDoneReadOnly(t *testing.T) {
	t.Parallel()

	ro := fmt.Errorf("%w: %w", &gomysql.Error{Number: 1290, Message: "running with the --read-only option"},
		gomysql.ErrBadConn)
	file := &gomysql.Error{Number: 1290, Message: "running with the --secure-file-priv option"}

	c := &conn{rejectReadOnly: true}
	if err := c.done(ro); !errors.Is(err, driver.ErrBadConn) || !c.bad {
		t.Errorf("read-only: %v, bad %v", err, c.bad)
	}

	c = &conn{rejectReadOnly: true}
	if err := c.done(file); errors.Is(err, driver.ErrBadConn) || c.bad {
		t.Errorf("secure-file-priv: %v, bad %v", err, c.bad)
	}

	c = &conn{}
	if err := c.done(ro); errors.Is(err, driver.ErrBadConn) || !c.bad {
		t.Errorf("without RejectReadOnly: %v, bad %v", err, c.bad)
	}
}
