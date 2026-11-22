package gomysql_test

import (
	"errors"
	"os"
	"strings"
	"testing"

	"github.com/darxnet/gomysql"
)

func sessionConfig(t *testing.T) gomysql.Config {
	t.Helper()

	addr := os.Getenv("GOMYSQL_ADDR")
	if addr == "" {
		t.Skip("GOMYSQL_ADDR not set")
	}

	return gomysql.Config{
		Network: "tcp", Address: addr, User: "b", Password: "b", Database: "gomysql",
		Collation: "utf8mb4_bin", AllowPublicKeyRetrieval: true,
		InitCommands: []string{"SET time_zone = '+03:00'", "SET @init = 5"},
	}
}

// session returns the time zone, @init, the collation of the connection
// and the current user.
func session(t *testing.T, c *gomysql.Conn) (zone string, init int, collation, user string) {
	t.Helper()

	var initNull *int
	if err := c.QueryRow("SELECT @@time_zone, @init, @@collation_connection, CURRENT_USER()").
		Scan(&zone, &initNull, &collation, &user); err != nil {
		t.Fatal(err)
	}

	if initNull != nil {
		init = *initNull
	}

	return zone, init, collation, user
}

func TestConformanceResetConnection(t *testing.T) {
	t.Parallel()

	c, err := gomysql.Connect(sessionConfig(t))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = c.Close() }()
	defer confRecover(t)

	if zone, init, _, _ := session(t, c); zone != "+03:00" || init != 5 {
		t.Fatalf("after connect: %s %d", zone, init)
	}

	mustExec(t, c, "SET time_zone = '+01:00'")
	mustExec(t, c, "SET @init = 6, @other = 1")
	mustExec(t, c, "CREATE TEMPORARY TABLE reset_tmp (id INT)")

	stmt, err := c.Prepare("SELECT 1")
	if err != nil {
		t.Fatal(err)
	}

	if err := c.ResetConnection(); err != nil {
		t.Fatal(err)
	}

	// Init commands again, the collation of Config, not the global one.
	if zone, init, collation, _ := session(t, c); zone != "+03:00" || init != 5 || collation != "utf8mb4_bin" {
		t.Errorf("after reset: %s %d %s", zone, init, collation)
	}

	var other *int
	if err := c.QueryRow("SELECT @other").Scan(&other); err != nil || other != nil {
		t.Errorf("@other after reset: %v, %v", other, err)
	}

	if _, err := c.Exec("SELECT id FROM reset_tmp"); err == nil {
		t.Error("the temporary table survived")
	}

	if _, err := stmt.Exec(); err != nil {
		t.Errorf("a statement prepared before the reset: %v", err)
	}

	_ = stmt.Close()
}

func TestConformanceInitCommands(t *testing.T) {
	t.Parallel()

	cfg := sessionConfig(t)
	cfg.InitCommands = []string{"SET @a = 1", "SET nope = 1"}

	if _, err := gomysql.Connect(cfg); err == nil || !strings.Contains(err.Error(), "SET nope = 1") {
		t.Errorf("a failing init command: %v", err)
	}
}

func TestConformanceChangeUser(t *testing.T) {
	t.Parallel()

	for _, compress := range []bool{false, true} {
		cfg := sessionConfig(t)
		cfg.Compress = compress

		c, err := gomysql.Connect(cfg)
		if err != nil {
			t.Fatal(err)
		}

		func() {
			defer func() { _ = c.Close() }()
			defer confRecover(t)

			mustExec(t, c, "SET @init = 7")

			if err := c.ChangeUser("root", "root", ""); err != nil {
				t.Skipf("no root account: %v", err)
			}

			zone, init, collation, user := session(t, c)
			if !strings.HasPrefix(user, "root@") || zone != "+03:00" || init != 5 || collation != "utf8mb4_bin" {
				t.Errorf("compress %v, as root: %s %s %d %s", compress, user, zone, init, collation)
			}

			if err := c.ChangeUser("b", "b", "gomysql"); err != nil {
				t.Fatal(err)
			}

			if _, _, _, user := session(t, c); !strings.HasPrefix(user, "b@") {
				t.Errorf("back as b: %s", user)
			}

			// A wrong password breaks the connection; Reconnect connects
			// as the user before.
			if err := c.ChangeUser("root", "wrong", ""); !errors.Is(err, &gomysql.Error{Number: 1045}) {
				t.Errorf("wrong password: %v", err)
			}

			if err := c.Ping(); !errors.Is(err, gomysql.ErrBadConn) {
				t.Errorf("Ping after a failed ChangeUser: %v", err)
			}

			if err := c.Reconnect(); err != nil {
				t.Fatal(err)
			}

			if _, _, _, user := session(t, c); !strings.HasPrefix(user, "b@") {
				t.Errorf("after Reconnect: %s", user)
			}
		}()
	}
}
