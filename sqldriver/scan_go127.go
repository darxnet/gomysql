//go:build go1.27

package sqldriver

import (
	"database/sql"
	"database/sql/driver"

	"github.com/darxnet/gomysql/internal/sqlhook"
)

var _ driver.RowsColumnScanner = (*rows)(nil)

// NextRow reads the next row for ScanColumn; database/sql calls it instead
// of Next since Go 1.27.
func (rs *rows) NextRow() error { return rs.advance() }

// ScanColumn scans column i into dest: gomysql converts the common
// destinations itself, database/sql the rest from the driver value.
func (rs *rows) ScanColumn(ctx driver.ScanContext, i int, dest any) error {
	v, done, err := sqlhook.ScanColumn(rs.r, i, dest)
	if done || err != nil {
		return err
	}

	return sql.ConvertAssign(ctx, dest, v)
}
