package bench

import (
	"bytes"
	"database/sql"
	"fmt"
	"io"
	"testing"

	"github.com/go-sql-driver/mysql"
)

// BenchmarkLoadData loads 200000 TSV rows with LOAD DATA LOCAL INFILE into
// an emptied InnoDB table. The server needs local_infile=ON.
func BenchmarkLoadData(b *testing.B) {
	const rows = 200000

	var tsv bytes.Buffer
	for i := range rows {
		fmt.Fprintf(&tsv, "%d\tname %d\n", i, i)
	}

	c := openCC(b)

	if _, err := c.Exec("CREATE TABLE IF NOT EXISTS ld (id INT PRIMARY KEY, name VARCHAR(50)) ENGINE=InnoDB"); err != nil {
		b.Fatal(err)
	}

	load := "LOAD DATA LOCAL INFILE 'Reader::ld' INTO TABLE ld CHARACTER SET utf8mb4 (id, name)"

	run := func(b *testing.B, loadOnce func() error) {
		b.Helper()

		cpu0 := cpuTime()
		n := 0

		for b.Loop() {
			b.StopTimer()

			if _, err := c.Exec("TRUNCATE TABLE ld"); err != nil {
				b.Fatal(err)
			}

			b.StartTimer()

			if err := loadOnce(); err != nil {
				b.Fatal(err)
			}

			n += rows
		}

		report(b, n, cpu0)
	}

	b.Run("gomysql", func(b *testing.B) {
		run(b, func() error {
			_, err := c.LoadData(load, bytes.NewReader(tsv.Bytes()))

			return err
		})
	})

	b.Run("go-sql-driver", func(b *testing.B) {
		db := openStd(b)

		mysql.RegisterReaderHandler("ld", func() io.Reader { return bytes.NewReader(tsv.Bytes()) })
		defer mysql.DeregisterReaderHandler("ld")

		run(b, func() error {
			res, err := db.Exec(load)
			if err == nil {
				if n, _ := res.RowsAffected(); n != rows {
					err = sql.ErrNoRows
				}
			}

			return err
		})
	})

}
