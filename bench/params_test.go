package bench

import (
	"database/sql"
	"fmt"
	"math"
	"os"
	"testing"
	"time"

	"github.com/darxnet/gomysql"
)

// TestDiffParams checks the binary-protocol parameter encoding: each value is
// inserted once by go-sql-driver and once through a gomysql prepared
// statement, and the stored values must be equal.
//
// GOMYSQL_ADDR=127.0.0.1:3306 go test -run DiffParams -v
func TestDiffParams(t *testing.T) {
	addr := os.Getenv("GOMYSQL_ADDR")
	if addr == "" {
		t.Skip("GOMYSQL_ADDR not set")
	}

	ref := openRef(t, addr, "")

	cc, err := gomysql.Connect(gomysql.Config{
		Network: "tcp", Address: addr, User: "b", Password: "b",
		Database: "gomysql", Collation: "utf8mb4_bin",
	})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = cc.Close() }()

	tm := time.Date(2026, 11, 18, 13, 31, 33, 123456000, time.UTC)

	cases := []struct {
		typ string
		v   any
	}{
		{"BIGINT", 0},
		{"BIGINT", -1},
		{"BIGINT", math.MinInt64},
		{"BIGINT", math.MaxInt64},
		{"TINYINT", int8(-128)},
		{"SMALLINT", int16(-32768)},
		{"INT", int32(math.MinInt32)},
		{"BIGINT", int64(math.MinInt64)},
		{"BIGINT UNSIGNED", uint(math.MaxUint64)},
		{"TINYINT UNSIGNED", uint8(255)},
		{"SMALLINT UNSIGNED", uint16(65535)},
		{"INT UNSIGNED", uint32(math.MaxUint32)},
		{"BIGINT UNSIGNED", uint64(math.MaxUint64)},
		{"FLOAT", float32(-1.5)},
		{"DOUBLE", math.MaxFloat64},
		{"DOUBLE", -0.1},
		{"TINYINT", true},
		{"TINYINT", false},
		{"VARCHAR(64)", "ünï'\"\\\x00"},
		{"VARBINARY(8)", []byte{0, 0xff, 'a'}},
		{"VARCHAR(8)", nil},
		{"DATETIME(6)", tm},
		{"DATETIME(6)", tm.Truncate(time.Second)},
		{"DATE", time.Date(2026, 11, 18, 0, 0, 0, 0, time.UTC)},
	}

	for _, c := range cases {
		name := fmt.Sprintf("%s/%T/%v", c.typ, c.v, c.v)
		t.Run(name, func(t *testing.T) {
			mustStd(t, ref, "DROP TABLE IF EXISTS params")
			mustStd(t, ref, "CREATE TABLE params (id INT PRIMARY KEY, v "+c.typ+" NULL) DEFAULT CHARSET=utf8mb4")
			mustStd(t, ref, "INSERT INTO params VALUES (1, ?)", c.v)

			stmt, err := cc.Prepare("INSERT INTO params VALUES (2, ?)")
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = stmt.Close() }()

			if _, err := stmt.Query(c.v); err != nil {
				t.Fatalf("gomysql: %v", err)
			}

			var want, got sql.RawBytes

			rows, err := ref.Query("SELECT a.v, b.v FROM params a, params b WHERE a.id = 1 AND b.id = 2")
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = rows.Close() }()

			if !rows.Next() {
				t.Fatalf("no row: %v", rows.Err())
			}

			if err := rows.Scan(&want, &got); err != nil {
				t.Fatal(err)
			}

			if (want == nil) != (got == nil) || string(want) != string(got) {
				t.Errorf("stored %q, want %q", got, want)
			}
		})
	}
}
