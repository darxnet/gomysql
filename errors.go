package gomysql

import (
	"errors"
	"fmt"
	"strconv"
)

// Error is an error returned by the server (an ERR packet). It has the same
// fields and message format as mysql.MySQLError of go-sql-driver.
type Error struct {
	Message  string
	Number   uint16
	SQLState [5]byte
}

// Error formats the error as "Error <number> (<state>): <message>", or
// without the state for errors sent before the handshake.
func (e *Error) Error() string {
	s := "Error " + strconv.FormatUint(uint64(e.Number), 10)
	if e.SQLState != [5]byte{} {
		s += " (" + string(e.SQLState[:]) + ")"
	}

	return s + ": " + e.Message
}

// Is reports whether target is an *Error with the same Number, so that
// errors.Is(err, &gomysql.Error{Number: 1062}) matches a duplicate key.
func (e *Error) Is(target error) bool {
	t, ok := target.(*Error) //nolint:errorlint // Is compares this error only, unwrapping is errors.Is's job
	return ok && t.Number == e.Number
}

// ErrBadConn means the connection is unusable: the server sent a packet
// that could not be parsed or that the protocol did not expect (wrong
// sequence id or packet type). Discard the connection.
//
// It is not driver.ErrBadConn: the command may already have run on the
// server, so repeating it on another connection is not safe.
var ErrBadConn = errors.New("gomysql: bad connection")

var (
	errServerTooOld         = errors.New("gomysql: server too old, MySQL 4.1 protocol required")
	errOldPassword          = errors.New("gomysql: server requested the insecure mysql_old_password")
	errServerDontSupportSSL = errors.New("gomysql: server does not support TLS")
	errLocalInfile          = errors.New("gomysql: LOAD DATA LOCAL INFILE is not supported")
	errMalformedHandshake   = malformed("handshake")

	errPacketPayloadEmpty = malformed("empty payload")
)

type wrapped struct {
	err error
	msg string
}

func (w *wrapped) Error() string { return w.err.Error() + ": " + w.msg }
func (w *wrapped) Unwrap() error { return w.err }

func malformed(msg string) error { return &wrapped{ErrBadConn, "malformed packet: " + msg} }
func outOfSync(msg string) error { return &wrapped{ErrBadConn, "commands out of sync: " + msg} }

func errUnexpectedPacketIdentity(b byte) error {
	return outOfSync(fmt.Sprintf("unexpected packet type %#02x", b))
}

func errPacketSequenceMismatch(expected, received byte) error {
	return outOfSync(fmt.Sprintf("sequence id %d, expected %d", received, expected))
}

func errExecuteUnsupportedArg(argType string) error {
	return errors.New("gomysql: unsupported argument type " + argType)
}

func errScanUnsupportedDest(destType string) error {
	return errors.New("gomysql: unsupported Scan destination " + destType)
}

func errScanDestFewerThanSource(field string) error {
	return errors.New("gomysql: Scan destination too small for column " + field)
}

func errScanUnsupportedPair(from byte, toType string) error {
	return fmt.Errorf("gomysql: cannot Scan column type %#02x into %s", from, toType)
}
