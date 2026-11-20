package gomysql

import (
	"encoding/binary"
	"testing"
)

// The replay server answers every execute with a prebuilt binary result of
// a typical bulk-load shape: (INT UNSIGNED, BIGINT UNSIGNED,
// SMALLINT UNSIGNED). Statement id n returns n rows.

var replayColumns = []fakeColumn{
	{"audienceID", myTypeLong, fakeUnsigned},
	{"userID", myTypeLongLong, fakeUnsigned},
	{"keepDays", myTypeShort, fakeUnsigned},
}

//nolint:gosec // G115: test data
func replayRow(i int, dst []byte) []byte {
	dst = binary.LittleEndian.AppendUint32(dst, uint32(i%500))
	dst = binary.LittleEndian.AppendUint64(dst, uint64(i)*2654435761)

	return binary.LittleEndian.AppendUint16(dst, uint16(i%90))
}

func newReplayServer(tb testing.TB, deprecateEOF bool) *fakeServer {
	tb.Helper()

	results := map[uint32][]byte{}

	return newFakeServer(tb, deprecateEOF, func(c *fakeConn, cmd byte, p []byte) error {
		switch cmd {
		case comStmtPrepare:
			// The query text is the row count.
			n := uint32(0)
			for _, d := range p {
				n = n*10 + uint32(d-'0')
			}

			if _, ok := results[n]; !ok {
				results[n] = c.binaryResult(replayColumns, int(n), replayRow)
				c.seq = 1
			}

			return c.prepareOK(n, replayColumns)

		case comStmtExecute:
			_, err := c.conn.Write(results[binary.LittleEndian.Uint32(p)])
			return err

		case comStmtClose:
			return nil
		}

		return c.errPacket(1047, "08S01", "unknown command")
	})
}

func replayQuery(tb testing.TB, stmt *Stmt, args []any) int {
	tb.Helper()

	rows, err := stmt.Query()
	if err != nil {
		tb.Fatal(err)
	}

	n := 0
	for rows.Next() {
		if err := rows.Scan(args...); err != nil {
			tb.Fatal(err)
		}
		n++
	}

	if err := rows.Err(); err != nil {
		tb.Fatal(err)
	}

	return n
}

func TestReplayRows(t *testing.T) {
	t.Parallel()

	for _, deprecateEOF := range []bool{false, true} {
		conn, err := Connect(newReplayServer(t, deprecateEOF).config())
		if err != nil {
			t.Fatal(err)
		}

		stmt, err := conn.Prepare("1000")
		if err != nil {
			t.Fatal(err)
		}

		var (
			aud  uint32
			user uint64
			keep uint16
		)

		rows, err := stmt.Query()
		if err != nil {
			t.Fatal(err)
		}

		n := 0
		for rows.Next() {
			if err := rows.Scan(&aud, &user, &keep); err != nil {
				t.Fatal(err)
			}
			if aud != uint32(n%500) || user != uint64(n)*2654435761 || keep != uint16(n%90) {
				t.Fatalf("deprecateEOF=%v row %d: got (%d, %d, %d)", deprecateEOF, n, aud, user, keep)
			}
			n++
		}

		if err := rows.Err(); err != nil || n != 1000 {
			t.Fatalf("deprecateEOF=%v: %d rows, err %v", deprecateEOF, n, err)
		}

		_ = conn.Close()
	}
}

// TestReplayAllocsPerRow guards the bulk-read path: reading more rows must not
// allocate more.
//
//nolint:paralleltest // AllocsPerRun counts process-wide allocations
func TestReplayAllocsPerRow(t *testing.T) {
	conn, err := Connect(newReplayServer(t, true).config())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = conn.Close() }()

	var (
		aud  uint32
		user uint64
		keep uint16
	)

	args := []any{&aud, &user, &keep}

	allocs := func(query string) float64 {
		stmt, err := conn.Prepare(query)
		if err != nil {
			t.Fatal(err)
		}

		return testing.AllocsPerRun(20, func() { replayQuery(t, stmt, args) })
	}

	one, many := allocs("1"), allocs("10000")
	if many > one {
		t.Errorf("allocs per query: %v for 1 row, %v for 10000 rows; want no per-row allocations", one, many)
	}
}

func BenchmarkReplayInts(b *testing.B) {
	conn, err := Connect(newReplayServer(b, true).config())
	if err != nil {
		b.Fatal(err)
	}
	defer func() { _ = conn.Close() }()

	stmt, err := conn.Prepare("100000")
	if err != nil {
		b.Fatal(err)
	}

	var (
		aud  uint32
		user uint64
		keep uint16
	)

	args := []any{&aud, &user, &keep}
	rows := 0

	b.ReportAllocs()

	for b.Loop() {
		rows += replayQuery(b, stmt, args)
	}

	b.ReportMetric(float64(b.Elapsed().Nanoseconds())/float64(rows), "ns/row")
}

const fakeUnsigned uint16 = 0x20 // UNSIGNED_FLAG

// BenchmarkReplayStructs reads 100000 rows into a reused slice of structs:
// by hand with Scan, with ScanStruct, and with AppendStructs.
func BenchmarkReplayStructs(b *testing.B) {
	type row struct {
		AudienceID uint32
		UserID     uint64
		KeepDays   uint16
	}

	conn, err := Connect(newReplayServer(b, true).config())
	if err != nil {
		b.Fatal(err)
	}
	defer func() { _ = conn.Close() }()

	stmt, err := conn.Prepare("100000")
	if err != nil {
		b.Fatal(err)
	}

	read := map[string]func(dst []row) ([]row, error){
		"Scan": func(dst []row) ([]row, error) {
			rows, qerr := stmt.Query()
			if qerr != nil {
				return dst, qerr
			}

			var r row
			for rows.Next() {
				if serr := rows.Scan(&r.AudienceID, &r.UserID, &r.KeepDays); serr != nil {
					return dst, serr
				}

				dst = append(dst, r)
			}

			return dst, rows.Close()
		},
		"ScanStruct": func(dst []row) ([]row, error) {
			rows, qerr := stmt.Query()
			if qerr != nil {
				return dst, qerr
			}

			var r row
			for rows.Next() {
				if serr := rows.ScanStruct(&r); serr != nil {
					return dst, serr
				}

				dst = append(dst, r)
			}

			return dst, rows.Close()
		},
		"AppendStructs": func(dst []row) ([]row, error) {
			return AppendStructs(dst)(stmt.Query())
		},
	}

	for _, name := range []string{"Scan", "ScanStruct", "AppendStructs"} {
		b.Run(name, func(b *testing.B) {
			var (
				dst []row
				n   int
			)

			b.ReportAllocs()

			for b.Loop() {
				if dst, err = read[name](dst[:0]); err != nil {
					b.Fatal(err)
				}

				n += len(dst)
			}

			b.ReportMetric(float64(b.Elapsed().Nanoseconds())/float64(n), "ns/row")
		})
	}
}
