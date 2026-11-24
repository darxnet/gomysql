package gomysql

import (
	"crypto/rsa"
	"crypto/tls"
	"fmt"
	"net"
	"strings"
	"time"
)

// Config describes a connection.
type Config struct {
	// Network and Address are the arguments of net.Dial: "tcp" and
	// "host:port", or "unix" and the path of the socket.
	Network string
	Address string

	User     string
	Password string

	// Database is the default database of the session; empty for none.
	Database string

	// TLS, if not nil, upgrades the connection to TLS before sending the
	// password. Without a ServerName, the host of Address is verified.
	TLS *tls.Config

	// ServerPublicKey is the RSA key that encrypts the password for
	// caching_sha2_password and sha256_password without TLS, when the
	// server has no cached hash of it (after a restart or FLUSH
	// PRIVILEGES) or always (sha256_password).
	ServerPublicKey *rsa.PublicKey

	// AllowPublicKeyRetrieval, without TLS and ServerPublicKey, asks the
	// server for its RSA key. A man in the middle can send his own and
	// read the password.
	AllowPublicKeyRetrieval bool

	// AllowCleartextPasswords sends the password in clear text without TLS
	// or a unix socket when the server asks for mysql_clear_password, as
	// PAM and LDAP authentication do.
	AllowCleartextPasswords bool

	// AuthPlugins are authentication methods besides the built-in ones,
	// or replacing those of the same name.
	AuthPlugins []AuthPlugin

	// Collation of the session, such as "utf8mb4_0900_ai_ci", as SET NAMES
	// sets it; "utf8mb4_general_ci" if empty. The handshake can set only
	// collations with an id below 256; a server that does not know the
	// collation falls back to its default without an error.
	Collation string

	// Location is the time zone of DATE, DATETIME and TIMESTAMP values:
	// time.Time arguments are converted to it, and values read are in it.
	// UTC if nil. TIMESTAMP values are also converted by the server, from
	// and to the time_zone of the session.
	Location *time.Location

	// InitCommands are SQL statements run in order after connecting, after
	// Reconnect, ResetConnection and ChangeUser, such as
	// "SET time_zone = '+00:00'". If one fails, so does the connection.
	InitCommands []string

	// ConnectAttrs are sent to the server, which shows them in
	// performance_schema.session_connect_attrs.
	ConnectAttrs map[string]string

	// FoundRows makes RowsAffected count the rows matched by UPDATE, not
	// only the ones changed (CLIENT_FOUND_ROWS).
	FoundRows bool

	// RejectReadOnly closes the connection when the server refuses a
	// statement because it is read-only: 1290 for --read-only or
	// --super-read-only, 1836 for read-only mode. After a failover the old
	// primary turns read-only, and a new connection reaches the new one.
	// The error then matches ErrBadConn. The statement did not run, but a
	// procedure (CALL) may have run in part before the refusal.
	RejectReadOnly bool

	// Compress turns on the compressed protocol.
	Compress bool
}

const (
	defaultCollation = 45 // utf8mb4_general_ci
	collationBinary  = 63 // binary: bytes, not text
)

// collationID returns the id of config.Collation.
func (config *Config) collationID() (uint8, error) {
	if config.Collation == "" {
		return defaultCollation, nil
	}

	id, ok := collations[strings.ToLower(config.Collation)]
	if !ok {
		return 0, fmt.Errorf("gomysql: unknown collation %q, or one with an id above 255", config.Collation)
	}

	return id, nil
}

// setNames returns SET NAMES for the collation of the session.
func (config *Config) setNames() string {
	collation := strings.ToLower(config.Collation)
	if collation == "" {
		collation = "utf8mb4_general_ci"
	}

	charset, _, _ := strings.Cut(collation, "_")

	return "SET NAMES " + charset + " COLLATE " + collation
}

// tlsConfig returns config.TLS, with the host of Address as ServerName if
// it has none.
func (config *Config) tlsConfig() *tls.Config {
	c := config.TLS
	if c.ServerName != "" || c.InsecureSkipVerify {
		return c
	}

	host, _, err := net.SplitHostPort(config.Address)
	if err != nil {
		host = config.Address
	}

	c = c.Clone()
	c.ServerName = host

	return c
}
