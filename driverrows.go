package gomysql

import (
	"database/sql/driver"

	"github.com/darxnet/gomysql/internal/sqlhook"
)

//nolint:gochecknoinits // gives sqldriver the unexported scan without exporting it
func init() {
	sqlhook.Values = func(rows any, dest []driver.Value) error {
		return rows.(*Rows).driverValues(dest) //nolint:forcetypeassert // sqldriver passes its *Rows
	}
}

// driverDest is a destination of Scan for sqldriver: the driver.Value of a
// column, from refDriverValue.
type driverDest struct{ v *driver.Value }

// driverValues scans the current row into dest for sqldriver. The
// destinations are kept while database/sql passes the same dest.
func (rows *Rows) driverValues(dest []driver.Value) error {
	if len(dest) > 0 && (len(rows.driverDests) != len(dest) || rows.driverOf != &dest[0]) {
		rows.driverDests = make([]any, len(dest))
		for i := range dest {
			rows.driverDests[i] = driverDest{&dest[i]}
		}

		rows.driverOf = &dest[0]
	}

	return rows.Scan(rows.driverDests...)
}
