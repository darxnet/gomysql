package gomysql

import (
	"crypto/rsa"
	"crypto/tls"
	"fmt"
	"net"
	"strings"
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
	// caching_sha2_password and sha256_password without TLS. If nil, it is
	// requested from the server, which a man in the middle can replace.
	ServerPublicKey *rsa.PublicKey

	// Collation of the session, such as "utf8mb4_0900_ai_ci", as SET NAMES
	// sets it; "utf8mb4_general_ci" if empty. The handshake can set only
	// collations with an id below 256; a server that does not know the
	// collation falls back to its default without an error.
	Collation string

	// ConnectAttrs are sent to the server, which shows them in
	// performance_schema.session_connect_attrs.
	ConnectAttrs map[string]string

	// FoundRows makes RowsAffected count the rows matched by UPDATE, not
	// only the ones changed (CLIENT_FOUND_ROWS).
	FoundRows bool

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
