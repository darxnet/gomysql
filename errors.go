package gomysql

import (
	"errors"
)

var errServerTooOld = errors.New("server too old: MySQL 4.1 protocol required")

var errOldPassword = errors.New("server requested mysql_old_password, which is insecure and not supported")

var errServerDontSupportSSL = errors.New("server dont support SSL")

var errConnWroteFewerBytesThanSent = errors.New("conn: wrote fewer bytes than sent")

var errPacketPayloadEmpty = errors.New("packet: wrong payload length")

func errUnexpectedPacketIdentity(b byte) error {
	return errors.New("packet: unexpected identity " + b2hex(b))
}

func errPacketSequenceMismatch(expected, received byte) error {
	return errors.New("packet: wrong sequence, expected " + b2string(expected) + ", received " + b2string(received))
}

func errPacketSequenceCompressedMismatch(expected, received byte) error {
	return errors.New("packet: wrong sequenceCompressed, expected " + b2string(expected) +
		", received " + b2string(received))
}

var errLocalInfile = errors.New("LOAD DATA LOCAL INFILE is not supported")

func errExecuteUnsupportedArg(argType string) error {
	return errors.New("execute: arg type not supported yet: " + argType)
}

func errScanUnsupportedDest(destType string) error {
	return errors.New("dest type not supported yet: " + destType)
}

func errScanDestFewerThanSource(field string) error {
	return errors.New("scan: destination has fewer bytes than source: " + field)
}

func errScanUnsupportedPair(from byte, toType string) error {
	return errors.New("scan: not supported convert from MYSQL `" + b2hex(from) + "` to `" + toType + "` yet")
}
