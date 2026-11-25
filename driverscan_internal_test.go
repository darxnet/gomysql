package gomysql

import (
	"database/sql"
	"testing"
)

// BenchmarkScanColumn scans the replay row (INT UNSIGNED, BIGINT UNSIGNED,
// SMALLINT UNSIGNED) column by column, as sqldriver does on Go 1.27, into
// the integer destinations of the direct path.
func BenchmarkScanColumn(b *testing.B) {
	var (
		aud   uint32
		user  uint64
		keep  uint16
		i64   [3]int64
		n     [3]int
		nulls [3]sql.NullInt64
	)

	dests := map[string][]any{
		"typed":     {&aud, &user, &keep},
		"int64":     {&i64[0], &i64[1], &i64[2]},
		"int":       {&n[0], &n[1], &n[2]},
		"NullInt64": {&nulls[0], &nulls[1], &nulls[2]},
	}

	for _, protocol := range []string{"binary", "text"} {
		for _, name := range []string{"typed", "int64", "int", "NullInt64"} {
			b.Run(protocol+"/"+name, func(b *testing.B) {
				rows := scanBenchRows(protocol == "binary")
				dest := dests[name]

				b.ReportAllocs()

				for b.Loop() {
					for i, d := range dest {
						if _, ok, err := rows.scanColumn(i, d); !ok || err != nil {
							b.Fatal(i, ok, err)
						}
					}
				}
			})
		}
	}
}
