// Package sqlhook gives gomysql/sqldriver the unexported parts of gomysql
// it needs, without exporting them to users. gomysql sets the functions
// when it is initialized.
package sqlhook

import "database/sql/driver"

//nolint:gochecknoglobals // set by gomysql, read by sqldriver
var (
	// Values scans the current row of a *gomysql.Rows into dest: bytes,
	// of text too, point into the row until the next one; database/sql
	// copies them where it needs.
	Values func(rows any, dest []driver.Value) error

	// ScanColumn scans column i of the current row of a *gomysql.Rows
	// into dest where gomysql converts as database/sql would; otherwise
	// it returns the driver value for database/sql to convert.
	ScanColumn func(rows any, i int, dest any) (v driver.Value, done bool, err error)

	// Broken reports whether a *gomysql.Conn is broken or closed: only
	// Reconnect or Close are left.
	Broken func(conn any) bool

	// RefusedReadOnly reports whether err is a statement refused by a
	// read-only server, which Config.RejectReadOnly turns into a broken
	// connection.
	RefusedReadOnly func(err error) bool
)
