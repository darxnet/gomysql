package gomysql

import (
	"database/sql"
	"errors"
	"reflect"
	"testing"
	"time"
)

func TestCommands(t *testing.T) {
	t.Parallel()

	conn := openResultConn(t, true)

	if err := conn.UseDatabase("db"); err != nil {
		t.Errorf("UseDatabase: %v", err)
	}

	if s, err := conn.Statistics(); s != "Uptime: 5" || err != nil {
		t.Errorf("Statistics: %q, %v", s, err)
	}
}

func TestTx(t *testing.T) {
	t.Parallel()

	conn := openResultConn(t, true)

	for _, end := range []func(*Tx) error{(*Tx).Commit, (*Tx).Rollback} {
		tx, err := conn.Begin()
		if err != nil {
			t.Fatal(err)
		}

		var s string
		if err = tx.QueryRow("rows").Scan(&s); err != nil || s != "x" {
			t.Errorf("Tx.QueryRow: %q, %v", s, err)
		}

		rows, err := tx.Query("rows")
		if err != nil || rows.Close() != nil {
			t.Errorf("Tx.Query: %v", err)
		}

		if _, err = tx.Exec("COMMIT"); err != nil {
			t.Errorf("Tx.Exec: %v", err)
		}

		stmt, err := tx.Prepare("s")
		if err != nil || stmt.Close() != nil {
			t.Errorf("Tx.Prepare: %v", err)
		}

		if err := end(tx); err != nil {
			t.Errorf("end: %v", err)
		}
	}
}

func TestStmtQueryRow(t *testing.T) {
	t.Parallel()

	conn := openResultConn(t, true)
	stmt := prepare(t, conn, "s")

	row := stmt.QueryRow()
	if err := row.Err(); err != nil {
		t.Fatal(err)
	}

	var v int64
	if err := row.Scan(&v); err != nil || v != 7 {
		t.Errorf("Scan: %d, %v", v, err)
	}

	if err := conn.QueryRow("unknown").Err(); !errors.Is(err, &Error{Number: 1047}) {
		t.Errorf("Row.Err: %v, want error 1047", err)
	}
}

func TestColumns(t *testing.T) {
	t.Parallel()

	conn := openResultConn(t, true)

	rows, err := conn.Query("rows")
	if err != nil {
		t.Fatal(err)
	}

	names, err := rows.Columns()
	if err != nil || !reflect.DeepEqual(names, []string{"v"}) {
		t.Errorf("Columns: %v, %v", names, err)
	}

	types, err := rows.ColumnTypes()
	if err != nil || len(types) != 1 || types[0].Name() != "v" {
		t.Fatalf("ColumnTypes: %v, %v", types, err)
	}

	if err := rows.Close(); err != nil {
		t.Fatal(err)
	}

	if _, err := rows.Columns(); err == nil {
		t.Error("Columns after Close: no error")
	}

	if _, err := rows.ColumnTypes(); err == nil {
		t.Error("ColumnTypes after Close: no error")
	}
}

func TestColumnType(t *testing.T) {
	t.Parallel()

	const (
		notNull  = flagNotNull
		unsigned = flagUnsigned
		bin      = collationBinary
		utf8mb4  = 45
	)

	type want struct {
		name                string
		scan                reflect.Type
		length              int64
		precision, scale    int64
		hasLength, hasScale bool
	}

	for _, tt := range []struct {
		col  columnDefinition
		want want
	}{
		{columnDefinition{Type: myTypeTiny, Flags: notNull}, want{name: "TINYINT", scan: reflect.TypeFor[int8]()}},
		{
			columnDefinition{Type: myTypeTiny, Flags: unsigned},
			want{name: "TINYINT UNSIGNED", scan: reflect.TypeFor[sql.Null[uint8]]()},
		},
		{
			columnDefinition{Type: myTypeLongLong, Flags: notNull | unsigned},
			want{name: "BIGINT UNSIGNED", scan: reflect.TypeFor[uint64]()},
		},
		{columnDefinition{Type: myTypeLong}, want{name: "INT", scan: reflect.TypeFor[sql.Null[int32]]()}},
		{columnDefinition{Type: myTypeYear, Flags: notNull}, want{name: "YEAR", scan: reflect.TypeFor[uint16]()}},
		{columnDefinition{Type: myTypeDouble, Flags: notNull}, want{name: "DOUBLE", scan: reflect.TypeFor[float64]()}},
		{
			columnDefinition{Type: myTypeNewDecimal, Flags: notNull, Len: 12, Decimals: 2},
			want{name: "DECIMAL", scan: reflect.TypeFor[string](), precision: 10, scale: 2, hasScale: true},
		},
		{
			columnDefinition{Type: myTypeVarString, Flags: notNull, Collation: utf8mb4, Len: 1020},
			want{name: "VARCHAR", scan: reflect.TypeFor[string](), length: 1020, hasLength: true},
		},
		{
			columnDefinition{Type: myTypeVarString, Collation: bin, Len: 16},
			want{name: "VARBINARY", scan: reflect.TypeFor[[]byte](), length: 16, hasLength: true},
		},
		{
			columnDefinition{Type: myTypeBlob, Collation: utf8mb4, Len: 65535},
			want{name: "TEXT", scan: reflect.TypeFor[sql.Null[string]](), length: 65535, hasLength: true},
		},
		{
			columnDefinition{Type: myTypeString, Flags: flagEnum | notNull, Collation: utf8mb4, Len: 4},
			want{name: "ENUM", scan: reflect.TypeFor[string](), length: 4, hasLength: true},
		},
		{columnDefinition{Type: myTypeDatetime2}, want{name: "DATETIME", scan: reflect.TypeFor[sql.Null[time.Time]]()}},
		{columnDefinition{Type: myTypeTime, Flags: notNull}, want{name: "TIME", scan: reflect.TypeFor[string]()}},
		{columnDefinition{Type: myTypeBit, Collation: bin}, want{name: "BIT", scan: reflect.TypeFor[sql.Null[uint64]]()}},
		{
			columnDefinition{Type: myTypeJSON, Collation: bin, Len: 1 << 30},
			want{name: "JSON", scan: reflect.TypeFor[sql.Null[string]](), length: 1 << 30, hasLength: true},
		},
		{columnDefinition{Type: myTypeNull}, want{name: "NULL", scan: reflect.TypeFor[any]()}},
	} {
		ct := &ColumnType{col: tt.col}

		length, hasLength := ct.Length()
		precision, scale, hasScale := ct.DecimalSize()
		nullable, _ := ct.Nullable()

		got := want{ct.DatabaseTypeName(), ct.ScanType(), length, precision, scale, hasLength, hasScale}
		if !reflect.DeepEqual(got, tt.want) || nullable != (tt.col.Flags&notNull == 0) {
			t.Errorf("%+v:\n got %+v, nullable %v\nwant %+v", tt.col, got, nullable, tt.want)
		}
	}
}
