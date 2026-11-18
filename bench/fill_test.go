package bench

import (
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"fmt"
	"strconv"
	"strings"
	"sync"
)

const (
	fillRows  = 1_000_000
	fillBatch = 5_000
)

var (
	fillOnce sync.Once
	fillErr  error
)

// fill creates the benchmark tables au and cm unless they already hold
// fillRows rows. Rows are generated here rather than in SQL because MySQL 5.7
// has no recursive CTEs.
func fill(addr string) error {
	db, err := sql.Open("mysql", "b:b@tcp("+addr+")/gomysql")
	if err != nil {
		return err
	}
	defer func() { _ = db.Close() }()

	tables := []struct {
		name, ddl string
		row       func(n uint64) string
	}{
		{
			"au",
			"CREATE TABLE au (audienceID INT UNSIGNED NOT NULL, userID BIGINT UNSIGNED NOT NULL, " +
				"keepDays SMALLINT UNSIGNED NOT NULL, PRIMARY KEY (audienceID, userID))",
			func(n uint64) string {
				return fmt.Sprintf("(%d,%d,%d)", n%500, n*2654435761, n%90)
			},
		},
		{
			"cm",
			"CREATE TABLE cm (id BIGINT UNSIGNED NOT NULL PRIMARY KEY, ext VARCHAR(64) NOT NULL, " +
				"ts INT UNSIGNED NOT NULL, keepDays SMALLINT UNSIGNED NOT NULL)",
			func(n uint64) string {
				sum := sha256.Sum256([]byte(strconv.FormatUint(n, 10)))
				return fmt.Sprintf("(%d,'%s',%d,%d)", n, hex.EncodeToString(sum[:]), 1_700_000_000+n, n%90)
			},
		},
	}

	for _, t := range tables {
		var count int
		if db.QueryRow("SELECT COUNT(*) FROM "+t.name).Scan(&count) == nil && count == fillRows {
			continue
		}

		if _, err := db.Exec("DROP TABLE IF EXISTS " + t.name); err != nil {
			return err
		}

		if _, err := db.Exec(t.ddl); err != nil {
			return err
		}

		var q strings.Builder
		for start := uint64(1); start <= fillRows; start += fillBatch {
			q.Reset()
			q.WriteString("INSERT INTO " + t.name + " VALUES ")

			for n := start; n < start+fillBatch; n++ {
				if n > start {
					q.WriteByte(',')
				}
				q.WriteString(t.row(n))
			}

			if _, err := db.Exec(q.String()); err != nil {
				return fmt.Errorf("fill %s: %w", t.name, err)
			}
		}
	}

	return nil
}
