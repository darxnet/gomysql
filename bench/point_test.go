package bench

import (
	"database/sql"
	"testing"
)

// BenchmarkPointNative is a point query with an argument through the
// native API: per-query allocations.
func BenchmarkPointNative(b *testing.B) {
	c := openCC(b)

	var (
		id  uint64
		ext string
	)

	b.ReportAllocs()

	for b.Loop() {
		err := c.QueryRow("SELECT id, ext, ts, keepDays FROM cm WHERE id = ?", 1).Scan(&id, &ext, new(uint32), new(uint16))
		if err != nil && err != sql.ErrNoRows {
			b.Fatal(err)
		}
	}
}
