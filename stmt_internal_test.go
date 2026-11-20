package gomysql

import (
	"encoding/binary"
	"errors"
	"testing"
)

// eofWith ends rows like fakeConn.eof, with the given status flags.
func (c *fakeConn) eofWith(dst []byte, status uint16) []byte {
	// EOF: header, warnings, status; or an OK packet with the EOF header:
	// header, affected rows, last insert id, status, warnings.
	p := binary.LittleEndian.AppendUint16([]byte{iEOF, 0, 0}, status)
	if c.deprecateEOF {
		p = append(p, 0, 0)
	}

	return c.frame(dst, p)
}

// resultSet appends a result set: column definitions and rows, each row
// already encoded, without the packet that ends the rows.
func (c *fakeConn) resultSet(dst []byte, cols []fakeColumn, rows ...[]byte) []byte {
	dst = c.frame(dst, appendLenEncInt(nil, uint64(len(cols))))
	for _, col := range cols {
		dst = c.frame(dst, col.definition())
	}

	dst = c.eof(dst, false)

	for _, row := range rows {
		dst = c.frame(dst, row)
	}

	return dst
}

func (c *fakeConn) okWith(dst []byte, affected, insertID byte, status uint16) []byte {
	p := binary.LittleEndian.AppendUint16([]byte{iOK, affected, insertID}, status)

	return c.frame(dst, append(p, 0, 0)) // warnings
}

// binaryRow encodes a binary row of one BIGINT column.
func binaryRow(v uint64) []byte {
	return binary.LittleEndian.AppendUint64([]byte{iOK, 0}, v)
}

var (
	colString = []fakeColumn{{"v", myTypeVarString, 0}}
	colInt    = []fakeColumn{{"v", myTypeLongLong, 0}}
	colRenamd = []fakeColumn{{"w", myTypeLongLong, 0}}
)

// newResultServer answers prepared statements and queries named after
// the result they get.
func newResultServer(t *testing.T, deprecateEOF bool) *fakeServer {
	t.Helper()

	return newFakeServer(t, deprecateEOF, func(c *fakeConn, cmd byte, p []byte) error {
		var out []byte

		switch cmd {
		case comPing, comInitDB:
			return c.ok()

		case comStatistics:
			out = c.frame(nil, []byte("Uptime: 5"))

		case comStmtPrepare:
			// The statement id is the first byte of the query. The column
			// is a string as far as PREPARE can tell.
			cols := colString
			if p[0] == 'i' {
				cols = nil
			}

			return c.prepareOK(uint32(p[0]), cols)

		case comStmtClose:
			return nil

		case comStmtExecute:
			switch p[0] {
			case 's': // SELECT ?: the type is known only now
				out = c.eofWith(c.resultSet(nil, colInt, binaryRow(7)), 0)
			case 'r': // a column renamed since PREPARE
				out = c.eofWith(c.resultSet(nil, colRenamd, binaryRow(8)), 0)
			case 'i': // INSERT
				out = c.okWith(nil, 3, 9, 0)
			case 'e': // rows, then an error
				out = c.resultSet(nil, colInt, binaryRow(1), binaryRow(2))
				out = c.frame(out, append([]byte{iERR, 0x25, 0x05, '#'}, "70100interrupted"...))
			}

		case comQuery:
			switch string(p) {
			case "ok then rows": // CALL p(): an OK with more results, then rows
				out = c.okWith(nil, 2, 0, serverStatusMoreResultsExists)
				out = c.eofWith(c.resultSet(out, colString, []byte{1, 'x'}), 0)
			case "rows then error":
				out = c.eofWith(c.resultSet(nil, colString, []byte{1, 'x'}), serverStatusMoreResultsExists)
				out = c.frame(out, append([]byte{iERR, 0x25, 0x05, '#'}, "70100interrupted"...))
			case "fewer columns":
				out = c.frame(nil, []byte{2})
				out = c.frame(out, colString[0].definition())
				out = c.eofWith(out, 0)
			case "START TRANSACTION", "COMMIT", "ROLLBACK":
				out = c.okWith(nil, 0, 0, 0)
			case "rows":
				out = c.eofWith(c.resultSet(nil, colString, []byte{1, 'x'}), 0)
			case "rows then ok":
				out = c.eofWith(c.resultSet(nil, colString, []byte{1, 'x'}), serverStatusMoreResultsExists)
				out = c.okWith(out, 0, 0, 0)
			}
		}

		if out == nil {
			return c.errPacket(1047, "08S01", "unknown command")
		}

		_, err := c.conn.Write(out)

		return err
	})
}

var eofModes = map[string]bool{"EOF": false, "deprecate EOF": true}

// openResultConn connects to a new result server and checks, at the end of
// the test, that the connection is still in step.
func openResultConn(t *testing.T, deprecateEOF bool) *Conn {
	t.Helper()

	conn, err := Connect(newResultServer(t, deprecateEOF).config())
	if err != nil {
		t.Fatal(err)
	}

	t.Cleanup(func() {
		if err := conn.Ping(); err != nil {
			t.Errorf("Ping after the test: %v", err)
		}

		_ = conn.Close()
	})

	return conn
}

func prepare(t *testing.T, conn *Conn, query string) *Stmt {
	t.Helper()

	stmt, err := conn.Prepare(query)
	if err != nil {
		t.Fatal(err)
	}

	return stmt
}

func queryInts(t *testing.T, stmt *Stmt) ([]int64, error) {
	t.Helper()

	rows, err := stmt.Query()
	if err != nil {
		t.Fatal(err)
	}

	var got []int64

	for rows.Next() {
		var v int64
		if err := rows.Scan(&v); err != nil {
			t.Fatal(err)
		}

		got = append(got, v)
	}

	if rows.Next() {
		t.Error("Next after the end returned true")
	}

	if err := rows.Close(); !errors.Is(err, rows.Err()) {
		t.Errorf("Close: %v, Err: %v", err, rows.Err())
	}

	return got, rows.Err()
}

func TestStmtColumnsFromExecute(t *testing.T) {
	t.Parallel()

	for name, deprecateEOF := range eofModes {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			conn := openResultConn(t, deprecateEOF)
			stmt := prepare(t, conn, "s")

			for range 2 {
				got, err := queryInts(t, stmt)
				if err != nil || len(got) != 1 || got[0] != 7 {
					t.Errorf("SELECT ?: %v, %v; want [7]", got, err)
				}
			}

			stmt = prepare(t, conn, "r")
			if got, err := queryInts(t, stmt); err != nil || len(got) != 1 || got[0] != 8 {
				t.Errorf("renamed column: %v, %v; want [8]", got, err)
			}

			if string(stmt.columns[0].Name) != "w" {
				t.Errorf("column name %q after EXECUTE, want w", stmt.columns[0].Name)
			}
		})
	}
}

func TestStmtWithoutResultSet(t *testing.T) {
	t.Parallel()

	for name, deprecateEOF := range eofModes {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			conn := openResultConn(t, deprecateEOF)
			stmt := prepare(t, conn, "i")

			rows, err := stmt.Query()
			if err != nil || rows == nil {
				t.Fatalf("Query: %v, %v", rows, err)
			}

			if rows.Next() || rows.Close() != nil {
				t.Errorf("rows of INSERT: Next true or Close %v", rows.Err())
			}

			res, err := stmt.Exec()
			if err != nil {
				t.Fatal(err)
			}

			affected, _ := res.RowsAffected()
			id, _ := res.LastInsertId()

			if affected != 3 || id != 9 {
				t.Errorf("RowsAffected %d, LastInsertId %d; want 3, 9", affected, id)
			}
		})
	}
}

func TestBinaryRowsStopOnError(t *testing.T) {
	t.Parallel()

	for name, deprecateEOF := range eofModes {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			conn := openResultConn(t, deprecateEOF)
			got, err := queryInts(t, prepare(t, conn, "e"))
			if len(got) != 2 || !errors.Is(err, &Error{Number: 1317}) {
				t.Errorf("got %v, %v; want 2 rows and error 1317", got, err)
			}
		})
	}
}

func TestMoreResults(t *testing.T) {
	t.Parallel()

	for name, deprecateEOF := range eofModes {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			conn := openResultConn(t, deprecateEOF)
			res, err := conn.Exec("ok then rows")
			if err != nil {
				t.Fatal(err)
			}

			if affected, _ := res.RowsAffected(); affected != 2 {
				t.Errorf("RowsAffected %d, want 2 from the first result", affected)
			}

			if err = conn.Ping(); err != nil {
				t.Fatalf("Ping after Exec: %v", err)
			}

			rows, err := conn.Query("rows then ok")
			if err != nil {
				t.Fatal(err)
			}

			if !rows.Next() {
				t.Fatal("no row", rows.Err())
			}

			if err := rows.Close(); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestErrorInLaterResult(t *testing.T) {
	t.Parallel()

	for name, deprecateEOF := range eofModes {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			conn := openResultConn(t, deprecateEOF)

			if _, err := conn.Exec("rows then error"); !errors.Is(err, &Error{Number: 1317}) {
				t.Errorf("Exec: %v, want error 1317 of the second result", err)
			}
		})
	}
}

func TestFewerColumnDefinitions(t *testing.T) {
	t.Parallel()

	conn, err := Connect(newResultServer(t, false).config())
	if err != nil {
		t.Fatal(err)
	}

	defer func() { _ = conn.Close() }()

	if _, err := conn.Exec("fewer columns"); !errors.Is(err, ErrBadConn) {
		t.Errorf("Exec: %v, want ErrBadConn", err)
	}
}
