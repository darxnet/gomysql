//go:build go1.27

package sqldriver

import (
	"context"
	"database/sql/driver"
	"strings"
	"testing"
)

// ScanColumn takes the columns in order from column 0, and a row from its
// first column: a cursor left in one row is never read in the next.
func TestScanColumnOrder(t *testing.T) {
	t.Parallel()

	scanning, _ := diffDBs(t, nil)

	c, connErr := scanning.Conn(context.Background())
	if connErr != nil {
		t.Fatal(connErr)
	}
	defer func() { _ = c.Close() }()

	rawErr := c.Raw(func(dc any) error {
		dr, qerr := dc.(*conn).QueryContext(context.Background(), //nolint:forcetypeassert // our driver
			"SELECT 'a0', 'a1' UNION ALL SELECT 'b0', 'b1'", nil)
		if qerr != nil {
			return qerr
		}
		defer func() { _ = dr.Close() }()

		rs := dr.(*rows) //nolint:forcetypeassert // our rows

		var v string

		// Column 1 before column 0, then row a's column 0 and row b's column 1.
		if err := rs.NextRow(); err != nil {
			return err
		}

		if err := rs.ScanColumn(driver.ScanContext{}, 1, &v); !outOfOrder(err) {
			t.Errorf("column 1 first: %v", err)
		}

		if err := rs.ScanColumn(driver.ScanContext{}, 0, &v); err != nil || v != "a0" {
			t.Errorf("column 0: %q, %v", v, err)
		}

		if err := rs.NextRow(); err != nil {
			return err
		}

		if err := rs.ScanColumn(driver.ScanContext{}, 1, &v); !outOfOrder(err) {
			t.Errorf("column 1 of the next row without column 0: %q, %v", v, err)
		}

		return nil
	})
	if rawErr != nil {
		t.Fatal(rawErr)
	}
}

func outOfOrder(err error) bool {
	return err != nil && strings.Contains(err.Error(), "out of order")
}
