package gomysql

import (
	"reflect"
	"strings"
	"testing"
)

// namedRows returns a text row with a column per name, the values given.
func namedRows(names []string, values ...string) *Rows {
	rows := &Rows{}

	for i, name := range names {
		rows.columns = append(rows.columns, columnDefinition{Name: []byte(name), Type: myTypeVarString})
		rows.buf = appendLenEncString(rows.buf, values[i])
	}

	return rows
}

type structBase struct {
	ID   int64
	Note string `db:"note"`
}

// StructDeep is exported: ScanStruct allocates embedded pointers to it.
type StructDeep struct {
	Deep string
}

type structDeep struct {
	Deep string
}

type structUser struct {
	structBase
	*StructDeep

	UserID    uint64
	Name      string `db:"full_name,omitempty"`
	Skipped   string `db:"-"`
	CreatedAt string
	Note      string // hides structBase.Note
	unused    int
}

func TestScanStruct(t *testing.T) {
	t.Parallel()

	rows := namedRows(
		[]string{"id", "user_id", "full_name", "created_at", "deep", "NOTE"},
		"7", "8", "Ann", "2026-11-20", "d", "top",
	)

	var u structUser
	if err := rows.ScanStruct(&u); err != nil {
		t.Fatal(err)
	}

	if u.ID != 7 || u.UserID != 8 || u.Name != "Ann" || u.CreatedAt != "2026-11-20" ||
		u.StructDeep == nil || u.Deep != "d" || u.Note != "top" || u.structBase.Note != "" {
		t.Errorf("%+v", u)
	}

	_ = u.unused

	// The plan is cached per Rows and type; a second scan reuses it.
	if err := rows.ScanStruct(&u); err != nil || u.ID != 7 {
		t.Errorf("again: %v", err)
	}

	for _, tc := range []struct {
		names []string
		dest  any
		want  string
	}{
		{[]string{"id", "nope"}, &structUser{}, `column "nope" has no field`},
		{[]string{"skipped"}, &structUser{}, `column "skipped" has no field`},
		{[]string{"user_id", "UserID"}, &structUser{}, "same field"},
		{[]string{"full__name"}, &structUser{}, "no field"}, // underscores only for untagged fields
		{[]string{"deep"}, &struct{ *structDeep }{}, "unexported embedded"},
		{[]string{"id"}, structUser{}, "pointer to a struct"},
		{[]string{"id"}, new(int64), "pointer to a struct"},
		{[]string{"id"}, (*structUser)(nil), "pointer to a struct"},
	} {
		values := make([]string, len(tc.names))
		for i := range values {
			values[i] = "1"
		}

		err := namedRows(tc.names, values...).ScanStruct(tc.dest)
		if err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("%v into %T: %v, want %q", tc.names, tc.dest, err, tc.want)
		}
	}
}

type scannerRow struct{ a, b string }

func (s *scannerRow) ScanRow(rows *Rows) error { return rows.Scan(&s.a, &s.b) }

func TestScanStructRowScanner(t *testing.T) {
	t.Parallel()

	var s scannerRow
	if err := namedRows([]string{"x", "y"}, "1", "2").ScanStruct(&s); err != nil || s.a != "1" || s.b != "2" {
		t.Errorf("%+v, %v", s, err)
	}
}

func TestStructLookup(t *testing.T) {
	t.Parallel()

	type ambiguous struct {
		UserID  int
		User_ID int //nolint:revive,staticcheck // the case under test
		Userid  int
	}

	info := structFields(reflect.TypeFor[ambiguous]())

	for name, want := range map[string]int{"UserID": 0, "User_ID": 1, "userid": 0, "user_id": 1, "u_s_e_r_i_d": 0} {
		if index := info.lookup(name); len(index) != 1 || index[0] != want {
			t.Errorf("%s: %v, want field %d", name, index, want)
		}
	}
}

// The destinations of the last struct are kept, but not through pointers
// to embedded structs, which may change.
func TestScanStructCache(t *testing.T) {
	t.Parallel()

	rows := namedRows([]string{"id", "deep"}, "7", "d")

	var u structUser
	for range 2 {
		if err := rows.ScanStruct(&u); err != nil || u.ID != 7 || u.Deep != "d" {
			t.Fatalf("%+v, %v", u, err)
		}
	}

	old := u.StructDeep
	u.StructDeep = &StructDeep{}

	if err := rows.ScanStruct(&u); err != nil || u.Deep != "d" || old.Deep != "d" {
		t.Errorf("new embedded pointer: %+v, %v", u, err)
	}

	type flat struct {
		ID   int64
		Deep string
	}

	var a, b flat
	if err := rows.ScanStruct(&a); err != nil || a.ID != 7 {
		t.Fatal(err)
	}

	if rows.destOf == nil {
		t.Error("destinations of a flat struct not kept")
	}

	rows.buf = namedRows([]string{"id", "deep"}, "8", "e").buf

	if err := rows.ScanStruct(&b); err != nil || b.ID != 8 || a.ID != 7 {
		t.Errorf("another struct: %+v %+v, %v", a, b, err)
	}

	rows.releaseDest()

	if rows.destOf != nil || rows.dest[0] != nil {
		t.Error("released destinations kept")
	}
}

func BenchmarkScanStruct(b *testing.B) {
	type row struct {
		Aud  uint32 `db:"aud"`
		User uint64 `db:"user"`
		Keep uint16 `db:"keep"`
	}

	rows := scanBenchRows(true)
	for i, name := range []string{"aud", "user", "keep"} {
		rows.columns[i].Name = []byte(name)
	}

	b.Run("same", func(b *testing.B) {
		var r row

		b.ReportAllocs()

		for b.Loop() {
			if err := rows.ScanStruct(&r); err != nil {
				b.Fatal(err)
			}
		}
	})

	b.Run("each", func(b *testing.B) {
		r := make([]row, 2)

		b.ReportAllocs()

		for i := 0; b.Loop(); i++ {
			if err := rows.ScanStruct(&r[i%2]); err != nil {
				b.Fatal(err)
			}
		}
	})
}
