package gomysql

import (
	"database/sql"
	"database/sql/driver"
	"encoding/json"
	"errors"
	"math"
	"strings"
	"testing"
	"time"
)

type (
	argInt    int8
	argString string
	argBytes  []byte
)

func TestInterpolate(t *testing.T) {
	t.Parallel()

	when := time.Date(2026, 11, 18, 13, 31, 33, 120000000, time.UTC)

	for _, tt := range []struct {
		query string
		args  []any
		want  string
	}{
		{"SELECT ?, ?, ?", []any{1, -2, uint64(math.MaxUint64)}, "SELECT 1, -2, 18446744073709551615"},
		{"SELECT ?, ?", []any{1.0000001234, float32(0.1)}, "SELECT 1.0000001234, 0.1"},
		{"SELECT ?, ?, ?", []any{true, false, nil}, "SELECT 1, 0, NULL"},
		{"SELECT ?", []any{"it's \\ \"x\" \x00\n\r\x1a"}, `SELECT 'it\'s \\ \"x\" \0\n\r\Z'`},
		{"SELECT ?, ?", []any{[]byte("a'b"), []byte(nil)}, `SELECT _binary'a\'b', NULL`},
		{"SELECT ?, ?", []any{when, time.Time{}}, "SELECT '2026-11-18 13:31:33.120000', '0000-00-00'"},
		{"SELECT ?", []any{time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)}, "SELECT '2026-01-02 03:04:05'"},

		// Conversions as in database/sql.
		{"SELECT ?, ?, ?", []any{argInt(65), argString("x"), argBytes("y")}, "SELECT 65, 'x', _binary'y'"},
		{"SELECT ?, ?", []any{new(int64), (*string)(nil)}, "SELECT 0, NULL"},
		{"SELECT ?, ?", []any{sql.NullString{String: "s", Valid: true}, sql.NullInt64{}}, "SELECT 's', NULL"},
		{"SELECT ?", []any{(*sql.NullInt64)(nil)}, "SELECT NULL"},
		{"SELECT ?, ?", []any{json.RawMessage(`{"a":1}`), json.RawMessage(nil)}, `SELECT '{\"a\":1}', NULL`},

		// A ? is a placeholder only where the server sees one.
		{"SELECT '?', \"?\", `?`, ?", []any{1}, "SELECT '?', \"?\", `?`, 1"},
		{`SELECT 'a\'?', 'b''?', ?`, []any{1}, `SELECT 'a\'?', 'b''?', 1`},
		{"SELECT ? # ?\n, ? -- ?\n", []any{1, 2}, "SELECT 1 # ?\n, 2 -- ?\n"},
		{"SELECT ?--?", []any{1, 2}, "SELECT 1--2"},        // -- needs a space to start a comment
		{"SELECT ? --\x7f?", []any{1}, "SELECT 1 --\x7f?"}, // or a control character
		{"SELECT /* ? */ ?, /*! ? */", []any{1, 2}, "SELECT /* ? */ 1, /*! 2 */"},
		{"SELECT `a``?`, ?", []any{1}, "SELECT `a``?`, 1"},
	} {
		got, err := (&Conn{}).interpolate(tt.query, tt.args)
		if err != nil || got != tt.want {
			t.Errorf("%q %v:\n got %q, %v\nwant %q", tt.query, tt.args, got, err, tt.want)
		}
	}
}

func TestInterpolateNoBackslashEscapes(t *testing.T) {
	t.Parallel()

	conn := &Conn{statusFlags: serverStatusNoBackslashEscapes}

	// A backslash is an ordinary character: it neither escapes nor ends.
	got, err := conn.interpolate(`SELECT 'a\', ?`, []any{`it's \`})
	if want := `SELECT 'a\', 'it''s \'`; err != nil || got != want {
		t.Errorf("got %q, %v; want %q", got, err, want)
	}
}

func TestInterpolateErrors(t *testing.T) {
	t.Parallel()

	for _, tt := range []struct {
		conn  *Conn
		query string
		args  []any
		want  string
	}{
		{&Conn{}, "SELECT ?, ?", []any{1}, "2 placeholders in the query, 1 arguments"},
		{&Conn{}, "SELECT ?", []any{1, 2}, "1 placeholders in the query, 2 arguments"},
		{&Conn{}, "SELECT '?'", []any{1}, "0 placeholders in the query, 1 arguments"},
		{&Conn{}, "SELECT ?", []any{math.NaN()}, "no NaN"},
		{&Conn{}, "SELECT ?", []any{struct{}{}}, "unsupported argument type struct {}"},
		{&Conn{}, "SELECT ?", []any{[]int{1}}, "unsupported argument type []int"},
		{&Conn{config: Config{Collation: "gbk_chinese_ci"}}, "SELECT ?", []any{"x"}, "use Prepare"},
		{&Conn{config: Config{Collation: "GBK_bin"}}, "SELECT ?", []any{argString("x")}, "use Prepare"},
		{&Conn{config: Config{Collation: "sjis_bin"}}, "SELECT ?, ?", []any{1, ptrTo("x")}, "use Prepare"},
		{&Conn{config: Config{Collation: "big5_bin"}}, "SELECT ?", []any{sql.NullString{Valid: true}}, "use Prepare"},
	} {
		_, err := tt.conn.interpolate(tt.query, tt.args)
		if err == nil || !strings.Contains(err.Error(), tt.want) {
			t.Errorf("%q %v: %v, want %q", tt.query, tt.args, err, tt.want)
		}
	}

	if _, err := convertArg(failingValuer{}); !errors.Is(err, errValuer) {
		t.Errorf("Valuer error: %v", err)
	}
}

var errValuer = errors.New("valuer")

type failingValuer struct{}

func (failingValuer) Value() (driver.Value, error) { return nil, errValuer }

// FuzzInterpolate checks that a string argument stays one literal: the
// query it is put into has no placeholder left, in both escaping modes.
func FuzzInterpolate(f *testing.F) {
	f.Add("SELECT ?", "x")
	f.Add("SELECT '\\'', ?", "'; DROP TABLE t; --")
	f.Add("SELECT ? -- ", "\\")

	f.Fuzz(func(t *testing.T, query, arg string) {
		for _, noBackslash := range []bool{false, true} {
			if countPlaceholders(query, !noBackslash) != 1 {
				continue
			}

			conn := &Conn{}
			if noBackslash {
				conn.statusFlags = serverStatusNoBackslashEscapes
			}

			got, err := conn.interpolate(query, []any{arg})
			if err != nil {
				t.Fatal(err)
			}

			if n := countPlaceholders(got, !noBackslash); n != 0 {
				t.Fatalf("noBackslash %v: %q with %q gives %q, with %d placeholders", noBackslash, query, arg, got, n)
			}
		}
	})
}

func ptrTo[T any](v T) *T { return &v }
