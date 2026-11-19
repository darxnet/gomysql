package gomysql

import (
	"errors"
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

var (
	// ErrMalformedPacket means a packet from the server could not be parsed.
	// The connection is unusable afterwards.
	ErrMalformedPacket = errors.New("gomysql: malformed packet")

	// ErrOutOfSync means the server sent a packet the protocol did not
	// expect at this point (wrong sequence id or packet type). The
	// connection is unusable afterwards.
	ErrOutOfSync = errors.New("gomysql: commands out of sync")
)

var (
	errServerTooOld         = errors.New("gomysql: server too old, MySQL 4.1 protocol required")
	errOldPassword          = errors.New("gomysql: server requested the insecure mysql_old_password")
	errServerDontSupportSSL = errors.New("gomysql: server does not support TLS")
	errLocalInfile          = errors.New("gomysql: LOAD DATA LOCAL INFILE is not supported")
	errExpectedEOF          = outOfSync("expected an EOF packet")
	errMalformedHandshake   = malformed("handshake")

	errConnWroteFewerBytesThanSent = errors.New("gomysql: wrote fewer bytes than sent")
	errPacketPayloadEmpty          = malformed("empty payload")
)

type wrapped struct {
	err error
	msg string
}

func (w *wrapped) Error() string { return w.err.Error() + ": " + w.msg }
func (w *wrapped) Unwrap() error { return w.err }

func malformed(msg string) error { return &wrapped{ErrMalformedPacket, msg} }
func outOfSync(msg string) error { return &wrapped{ErrOutOfSync, msg} }

func errUnexpectedPacketIdentity(b byte) error {
	return outOfSync("unexpected packet type " + b2hex(b))
}

func errPacketSequenceMismatch(expected, received byte) error {
	return outOfSync("sequence id " + b2string(received) + ", expected " + b2string(expected))
}

func errPacketSequenceCompressedMismatch(expected, received byte) error {
	return outOfSync("compressed sequence id " + b2string(received) + ", expected " + b2string(expected))
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
	return errors.New("gomysql: cannot Scan column type " + b2hex(from) + " into " + toType)
}
