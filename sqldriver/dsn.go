package sqldriver

import (
	"crypto/tls"
	"errors"
	"fmt"
	"maps"
	"net"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/darxnet/gomysql"
)

// refused are the parameters of go-sql-driver/mysql that gomysql does not
// do as they ask, with the reason.
var refused = map[string]string{ //nolint:gosec // G101: names of parameters, not credentials
	"allowAllFiles":            "LoadData reads an io.Reader, never a file",
	"allowFallbackToPlaintext": "TLS, if configured, is required",
	"allowNativePasswords":     "mysql_native_password is always allowed",
	"allowOldPasswords":        "the old password hash is broken and never sent",
	"checkConnLiveness":        "connections are not checked with MSG_PEEK; an idle one is pinged before reuse",
	"columnsWithAlias":         "columns are named as the server names them",
	"interpolateParams":        "arguments are always interpolated; Prepare makes prepared statements",
	"maxAllowedPacket":         "the server refuses a packet larger than its max_allowed_packet",
	"multiStatements":          "one statement per query",
	"parseTime":                "DATE, DATETIME and TIMESTAMP are always time.Time",
	"readTimeout":              "deadlines come from the context",
	"serverPubKey":             "use NewConnector with gomysql.Config.ServerPublicKey",
	"strict":                   "removed from go-sql-driver too",
	"timeTruncate":             "not supported",
	"writeTimeout":             "deadlines come from the context",
}

// parseDSN parses a DSN of go-sql-driver/mysql:
//
//	[user[:password]@][net[(address)]]/dbname[?param=value&...]
func parseDSN(dsn string) (config gomysql.Config, timeout time.Duration, err error) {
	slash := strings.LastIndexByte(dsn, '/')
	if slash < 0 {
		return config, 0, errors.New("gomysql/sqldriver: DSN without the / before the database name")
	}

	head, tail := dsn[:slash], dsn[slash+1:]

	if at := strings.LastIndexByte(head, '@'); at >= 0 {
		config.User, config.Password, _ = strings.Cut(head[:at], ":")
		head = head[at+1:]
	}

	if addrErr := parseAddress(&config, head); addrErr != nil {
		return config, 0, addrErr
	}

	dbname, query, _ := strings.Cut(tail, "?")
	if config.Database, err = url.PathUnescape(dbname); err != nil {
		return config, 0, fmt.Errorf("gomysql/sqldriver: database name: %w", err)
	}

	timeout, err = parseParams(&config, query)

	return config, timeout, err
}

// parseAddress parses net(address): tcp by default, at 127.0.0.1:3306 or
// /tmp/mysql.sock; a TCP address without a port gets 3306, an IPv6 one
// brackets too.
func parseAddress(config *gomysql.Config, s string) error {
	network, address := s, ""

	if open := strings.IndexByte(s, '('); open >= 0 {
		if !strings.HasSuffix(s, ")") {
			return fmt.Errorf("gomysql/sqldriver: address %q without ); a / in the database name or a value "+
				"must be escaped as %%2F", s)
		}

		network, address = s[:open], s[open+1:len(s)-1]
	}

	switch network {
	case "", "tcp":
		config.Network = "tcp"

		switch _, port, err := net.SplitHostPort(address); {
		case address == "":
			address = "127.0.0.1:3306"
		case err != nil: // no port, or IPv6 without brackets
			address = net.JoinHostPort(strings.Trim(address, "[]"), "3306")
		case port == "":
			return fmt.Errorf("gomysql/sqldriver: address %q with an empty port", address)
		}
	case "unix":
		config.Network = "unix"

		if address == "" {
			address = "/tmp/mysql.sock"
		}
	default:
		return fmt.Errorf("gomysql/sqldriver: unknown network %q", network)
	}

	config.Address = address

	return nil
}

// parseParams applies the parameters of query to config; the rest set
// session variables.
func parseParams(config *gomysql.Config, query string) (timeout time.Duration, err error) {
	if query == "" {
		return 0, nil
	}

	params, err := url.ParseQuery(query)
	if err != nil {
		return 0, fmt.Errorf("gomysql/sqldriver: parameters: %w", err)
	}

	var charset string

	// Sorted, so that the session variables are set in a known order.
	for _, key := range slices.Sorted(maps.Keys(params)) {
		if len(params[key]) > 1 {
			return 0, fmt.Errorf("gomysql/sqldriver: parameter %s given more than once", key)
		}

		value := params.Get(key)

		if reason, ok := refused[key]; ok {
			return 0, fmt.Errorf("gomysql/sqldriver: parameter %s is not supported: %s", key, reason)
		}

		switch key {
		case "tls":
			config.TLS, err = parseTLS(value)
		case "collation":
			config.Collation = value
		case "charset":
			// The first of a list, as go-sql-driver would try in turn.
			charset, _, _ = strings.Cut(value, ",")
		case "loc":
			config.Location, err = time.LoadLocation(value)
		case "compress":
			config.Compress, err = parseBool(key, value)
		case "clientFoundRows":
			config.FoundRows, err = parseBool(key, value)
		case "allowCleartextPasswords":
			config.AllowCleartextPasswords, err = parseBool(key, value)
		case "allowPublicKeyRetrieval":
			config.AllowPublicKeyRetrieval, err = parseBool(key, value)
		case "rejectReadOnly":
			config.RejectReadOnly, err = parseBool(key, value)
		case "connectionAttributes":
			config.ConnectAttrs, err = parseAttrs(value)
		case "timeout":
			if timeout, err = time.ParseDuration(value); err == nil && timeout < 0 {
				err = fmt.Errorf("negative %v", timeout)
			}
		default:
			// A session variable; its value is SQL, as written in the DSN.
			if !isIdentifier(key) {
				return 0, fmt.Errorf("gomysql/sqldriver: parameter %q is not a variable name", key)
			}

			config.InitCommands = append(config.InitCommands, "SET "+key+"="+value)
		}

		if err != nil {
			return 0, fmt.Errorf("gomysql/sqldriver: parameter %s: %w", key, err)
		}
	}

	// A charset without a collation gets the default collation of the
	// server for it, as with go-sql-driver: SET NAMES, before the other
	// session variables.
	if charset != "" && config.Collation == "" {
		if !isIdentifier(charset) {
			return 0, fmt.Errorf("gomysql/sqldriver: parameter charset: %q is not a character set name", charset)
		}

		config.InitCommands = append([]string{"SET NAMES " + charset}, config.InitCommands...)
	}

	return timeout, nil
}

// parseTLS accepts skip-verify in any case, as go-sql-driver, and the
// values of parseBool.
func parseTLS(value string) (*tls.Config, error) {
	if strings.EqualFold(value, "skip-verify") {
		return &tls.Config{InsecureSkipVerify: true}, nil //nolint:gosec // G402: asked for by the DSN
	}

	on, err := strconv.ParseBool(value)

	switch {
	case err != nil:
		return nil, fmt.Errorf("%q: only true, skip-verify and false; "+
			"a tls.Config of its own goes through NewConnector", value)
	case on:
		return &tls.Config{MinVersion: tls.VersionTLS12}, nil
	default:
		return nil, nil //nolint:nilnil // no TLS
	}
}

// parseBool accepts the values of strconv.ParseBool: those of
// go-sql-driver (1, true, TRUE, True and the false ones), and t, T, f, F.
func parseBool(key, value string) (bool, error) {
	b, err := strconv.ParseBool(value)
	if err != nil {
		return false, fmt.Errorf("%s=%q: not a boolean", key, value)
	}

	return b, nil
}

// parseAttrs parses key:value,key:value.
func parseAttrs(value string) (map[string]string, error) {
	attrs := make(map[string]string)

	for pair := range strings.SplitSeq(value, ",") {
		k, v, ok := strings.Cut(pair, ":")
		if !ok {
			return nil, fmt.Errorf("%q is not key:value", pair)
		}

		attrs[k] = v
	}

	return attrs, nil
}

// isIdentifier reports whether s is a name of letters, digits and _, as
// session variables and character sets are; upper case too.
func isIdentifier(s string) bool {
	if s == "" {
		return false
	}

	for _, c := range []byte(s) {
		letter := 'a' <= c && c <= 'z' || 'A' <= c && c <= 'Z'
		if !letter && (c < '0' || c > '9') && c != '_' {
			return false
		}
	}

	return true
}
