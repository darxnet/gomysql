package gomysql

import (
	"database/sql"
	"errors"
	"strings"
	"testing"
)

type namedBase struct {
	ID   int64
	Note string `db:"note,omitempty"`
}

type namedUser struct {
	namedBase

	Name    string `db:"user_name"`
	Secret  string `db:"-"`
	private int    //nolint:unused // must be skipped
	ID      string // hides namedBase.ID
}

func TestInterpolateNamed(t *testing.T) {
	t.Parallel()

	user := namedUser{namedBase: namedBase{ID: 1, Note: "n"}, Name: "o'k", ID: "x"}

	for _, tt := range []struct {
		query string
		args  []any
		want  string
	}{
		{"SELECT :a, :b, :a", []any{sql.Named("a", 1), sql.Named("b", "x")}, "SELECT 1, 'x', 1"},
		{"SELECT :user_name, :note, :id, :Id", []any{user}, "SELECT 'o\\'k', 'n', 'x', 'x'"},
		{"SELECT :user_name", []any{&user}, "SELECT 'o\\'k'"},
		{"SELECT :k, :n", []any{map[string]any{"k": []byte("v"), "n": nil}}, "SELECT _binary'v', NULL"},
		{"SELECT :k", []any{map[string]int{"k": 2}}, "SELECT 2"},

		// Where :name is not a placeholder.
		{"SELECT ':a', `:a`, /* :a */ :a", []any{sql.Named("a", 1)}, "SELECT ':a', `:a`, /* :a */ 1"},
		{"SET @x:=:a; lbl:LOOP", []any{sql.Named("a", 1)}, "SET @x:=1; lbl:LOOP"},
		{"SELECT :a::b, :1", []any{sql.Named("a", 1)}, "SELECT 1::b, :1"},

		// Lists.
		{"SELECT 1 IN (?), ?", []any{[]int{1, 2, 3}, []byte("b")}, "SELECT 1 IN (1, 2, 3), _binary'b'"},
		{"SELECT 1 IN (:ids)", []any{sql.Named("ids", []string{"a", "b"})}, "SELECT 1 IN ('a', 'b')"},
		{"SELECT ?", []any{[]any{nil, true, ptrTo(2)}}, "SELECT NULL, 1, 2"},
		{"SELECT ?", []any{sql.NullString{}}, "SELECT NULL"}, // a struct, but a value
	} {
		got, err := (&Conn{}).interpolate(tt.query, tt.args)
		if err != nil || got != tt.want {
			t.Errorf("%q %v:\n got %q, %v\nwant %q", tt.query, tt.args, got, err, tt.want)
		}
	}
}

func TestInterpolateNamedErrors(t *testing.T) {
	t.Parallel()

	for _, tt := range []struct {
		query string
		args  []any
		want  string
	}{
		{"SELECT ?, :a", []any{sql.Named("a", 1)}, "are for :name placeholders"},
		{"SELECT :a, ?", []any{sql.Named("a", 1)}, "mixes ? and :name"},
		{"SELECT :a", []any{1}, ":name placeholders need sql.Named arguments"},
		{"SELECT :a", []any{sql.Named("b", 1)}, "no sql.Named argument for :a"},
		{"SELECT :a", []any{sql.Named("a", 1), sql.Named("b", 2)}, "no :b placeholder"},
		{"SELECT :a", []any{sql.Named("a", 1), sql.Named("a", 2)}, `two sql.Named arguments "a"`},
		{"SELECT :a", []any{sql.Named("", 1)}, "without a name"},
		{"SELECT :a", []any{sql.Named("a", 1), 2}, "mixed"},
		{"SELECT :a", []any{map[string]any{}}, `no key "a"`},
		{"SELECT :secret", []any{namedUser{}}, "no field of gomysql.namedUser for :secret"},
		{"SELECT 1", []any{namedUser{}}, "no :name placeholders"},
		{"SELECT :a", []any{map[string]any{"a": struct{}{}}}, ":a: gomysql: unsupported argument type"},
		{"SELECT :ID", []any{struct{ *namedBase }{}}, "nil pointer"},
	} {
		_, err := (&Conn{}).interpolate(tt.query, tt.args)
		if err == nil || !strings.Contains(err.Error(), tt.want) {
			t.Errorf("%q %v: %v, want %q", tt.query, tt.args, err, tt.want)
		}
	}
}

func TestRewriteNamed(t *testing.T) {
	t.Parallel()

	got, names, err := rewriteNamed("SELECT :a, ':b', :c, :a", true)
	if want := "SELECT ?, ':b', ?, ?"; err != nil || got != want || strings.Join(names, ",") != "a,c,a" {
		t.Errorf("got %q %v %v; want %q a,c,a", got, names, err, want)
	}

	if got, names, err := rewriteNamed("SELECT ?, lbl:x", true); got != "SELECT ?, lbl:x" || names != nil || err != nil {
		t.Errorf("no names: %q %v %v", got, names, err)
	}

	if _, _, err := rewriteNamed("SELECT ?, :a", true); !errors.Is(err, errMixedPlaceholders) {
		t.Errorf("mixed: %v", err)
	}
}

func FuzzNamed(f *testing.F) {
	for _, q := range []string{"SELECT :a, ':b', `:c` -- :d\n", "lbl:LOOP @x:=:a /* :a */", "SELECT ?, :a", ":a::b"} {
		f.Add(q)
	}

	f.Fuzz(func(t *testing.T, query string) {
		sent, names, err := rewriteNamed(query, true)
		if err == nil && names != nil {
			if n := countPlaceholders(sent, true); n != len(names) {
				t.Fatalf("%q: %q has %d placeholders for %v", query, sent, n, names)
			}
		}

		_, _ = (&Conn{}).interpolate(query, []any{map[string]any{}})
		_, _ = (&Conn{}).interpolate(query, []any{sql.Named("a", []int{1, 2})})
	})
}
