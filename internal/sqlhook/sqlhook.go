// Package sqlhook gives gomysql/sqldriver the unexported parts of gomysql
// it needs, without exporting them to users.
package sqlhook

import "database/sql/driver"

// Values scans the current row of a *gomysql.Rows into dest for
// database/sql: bytes, of text too, point into the row, valid until the
// next row; database/sql copies them where it needs. gomysql sets it.
var Values func(rows any, dest []driver.Value) error //nolint:gochecknoglobals // set by gomysql, read by sqldriver
