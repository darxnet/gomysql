package gomysql_test

import (
	"crypto/rsa"
	"crypto/x509"
	"database/sql"
	"encoding/pem"
	"errors"
	"os"
	"strings"
	"testing"

	"github.com/darxnet/gomysql"
)

// Authentication with each plugin the server has, with and without a
// password, over plain TCP, TLS, a configured RSA key and a retrieved one.
// FLUSH PRIVILEGES before each attempt empties the cache of
// caching_sha2_password, so that it needs full authentication.
//
// It needs the root account of scripts/testdb.sh. Not parallel: a login
// in one subtest would fill the cache another one empties.
//
//nolint:paralleltest // see above
func TestConformanceAuth(t *testing.T) {
	addr := os.Getenv("GOMYSQL_ADDR")
	if addr == "" {
		t.Skip("GOMYSQL_ADDR not set")
	}

	defer confRecover(t)

	root, err := gomysql.Connect(gomysql.Config{
		Network: "tcp", Address: addr, User: "root", Password: "root", AllowPublicKeyRetrieval: true,
	})
	if err != nil {
		t.Skipf("no root account: %v", err)
	}

	t.Cleanup(func() { _ = root.Close() })

	var version string
	if err := root.QueryRow("SELECT VERSION()").Scan(&version); err != nil {
		t.Fatal(err)
	}

	mariaDB := strings.Contains(version, "MariaDB")
	hasTLS := serverHasTLS(t, root)

	keys := map[string]*rsa.PublicKey{
		"caching_sha2_password": serverKey(t, root, "Caching_sha2_password_rsa_public_key"),
		"sha256_password":       serverKey(t, root, "Rsa_public_key"),
	}

	type mode struct {
		name string
		cfg  func(*gomysql.Config, string)
		// needs the server's key: it fails with a password the server has
		// not cached
		insecure bool
	}

	modes := []mode{
		{"plain", func(*gomysql.Config, string) {}, true},
		{"retrieval", func(c *gomysql.Config, _ string) { c.AllowPublicKeyRetrieval = true }, false},
		{"key", func(c *gomysql.Config, p string) { c.ServerPublicKey = keys[p] }, false},
		{"tls", func(c *gomysql.Config, _ string) { c.TLS = testTLSConfig() }, false},
	}

	for _, plugin := range []string{"mysql_native_password", "caching_sha2_password", "sha256_password"} {
		for _, password := range []string{"pw-" + plugin, ""} {
			user := "auth_" + strings.Split(plugin, "_")[0] + map[bool]string{true: "_empty", false: ""}[password == ""]

			if !createUser(t, root, user, plugin, password, mariaDB) {
				t.Logf("%s: the server has no %s", version, plugin)

				break
			}

			for _, m := range modes {
				t.Run(user+"/"+m.name, func(t *testing.T) {
					defer confRecover(t)

					switch {
					case m.name == "tls" && !hasTLS:
						t.Skip("no TLS")
					case m.name == "key" && keys[plugin] == nil:
						t.Skip("no RSA key")
					}

					cfg := gomysql.Config{Network: "tcp", Address: addr, User: user, Password: password}
					m.cfg(&cfg, plugin)
					authCase(t, root, &cfg, plugin, m.insecure && plugin != "mysql_native_password")
				})
			}
		}
	}
}

// authCase connects with cfg after FLUSH PRIVILEGES. needsKey: a password
// needs TLS or the server's key, which cfg does not have.
func authCase(t *testing.T, root *gomysql.Conn, cfg *gomysql.Config, plugin string, needsKey bool) {
	t.Helper()

	if _, err := root.Exec("FLUSH PRIVILEGES"); err != nil {
		t.Fatal(err)
	}

	c, err := gomysql.Connect(*cfg)

	if needsKey && cfg.Password != "" {
		if err == nil || !strings.Contains(err.Error(), "AllowPublicKeyRetrieval") {
			t.Errorf("without TLS or a key: %v, want an error", err)
		}

		return
	}

	if err != nil {
		t.Fatal(err)
	}

	var got string
	if err := c.QueryRow("SELECT CURRENT_USER()").Scan(&got); err != nil || !strings.HasPrefix(got, cfg.User+"@") {
		t.Errorf("CURRENT_USER %q, %v", got, err)
	}

	_ = c.Close()

	// Cached now: the scramble is enough, without TLS or a key.
	if plugin == "caching_sha2_password" && cfg.Password != "" {
		fast := gomysql.Config{Network: "tcp", Address: cfg.Address, User: cfg.User, Password: cfg.Password}

		if c, err := gomysql.Connect(fast); err != nil {
			t.Errorf("fast auth: %v", err)
		} else {
			_ = c.Close()
		}
	}

	// A wrong password is refused by the server.
	if !needsKey {
		wrong := *cfg
		wrong.Password += "x"

		if _, err := gomysql.Connect(wrong); !errors.Is(err, &gomysql.Error{Number: 1045}) {
			t.Errorf("wrong password: %v, want error 1045", err)
		}
	}
}

// createUser creates user, or reports false if the server has no plugin.
func createUser(t *testing.T, root *gomysql.Conn, user, plugin, password string, mariaDB bool) bool {
	t.Helper()

	identified := "IDENTIFIED WITH " + plugin + " BY '" + password + "'"

	if mariaDB {
		if plugin != "mysql_native_password" {
			return false
		}

		identified = "IDENTIFIED BY '" + password + "'"
	}

	if _, err := root.Exec("DROP USER IF EXISTS '" + user + "'@'%'"); err != nil {
		t.Fatal(err)
	}

	if _, err := root.Exec("CREATE USER '" + user + "'@'%' " + identified); err != nil {
		t.Logf("CREATE USER %s: %v", user, err)

		return false
	}

	t.Cleanup(func() { _, _ = root.Exec("DROP USER IF EXISTS '" + user + "'@'%'") })

	return true
}

// serverHasTLS reports whether the server accepts TLS. have_ssl is gone in
// MySQL 8.0.26+, where TLS is always built in.
func serverHasTLS(t *testing.T, c *gomysql.Conn) bool {
	t.Helper()

	haveSSL := "YES"
	if err := c.QueryRow("SHOW VARIABLES LIKE 'have_ssl'").Scan(new(string), &haveSSL); err != nil &&
		!errors.Is(err, sql.ErrNoRows) {
		t.Fatal(err)
	}

	return haveSSL == "YES"
}

// serverKey returns the RSA key the server shows in a status variable, or
// nil.
func serverKey(t *testing.T, c *gomysql.Conn, variable string) *rsa.PublicKey {
	t.Helper()

	var key string
	if err := c.QueryRow("SHOW STATUS LIKE '"+variable+"'").Scan(new(string), &key); err != nil {
		return nil
	}

	block, _ := pem.Decode([]byte(key))
	if block == nil {
		return nil
	}

	pub, err := x509.ParsePKIXPublicKey(block.Bytes)
	if err != nil {
		t.Fatal(err)
	}

	rsaKey, _ := pub.(*rsa.PublicKey)

	return rsaKey
}
