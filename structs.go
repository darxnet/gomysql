package gomysql

import (
	"errors"
	"fmt"
	"reflect"
)

// RowScanner is a type that scans a row itself, without reflection.
// ScanStruct, Struct, Structs and AppendStructs call ScanRow of a pointer
// that implements it.
type RowScanner interface {
	ScanRow(rows *Rows) error
}

// ScanStruct copies the columns of the current row into the fields of the
// struct dest points to, with the conversions of Scan. A column goes into
// the field of its name: the db tag (without options after a comma) or
// the name of the field, exactly, else ignoring case, else, for a field
// without a tag, also ignoring underscores, so that user_id goes into
// UserID. Fields of embedded structs count as fields of dest; db:"-"
// skips a field.
//
// Every column needs a field; fields without a column keep their values,
// so one struct can serve several queries. Use Values for a single column
// of a struct type such as time.Time.
//
// If dest is a RowScanner, ScanStruct calls its ScanRow.
//
// Scanning into the same struct again, row after row, costs as much as
// Scan: the pointers to its fields are kept until Close.
func (rows *Rows) ScanStruct(dest any) error {
	// The same struct as before: its fields are the destinations.
	if rows.destOf != nil && dest == rows.destOf {
		return rows.Scan(rows.dest...)
	}

	if s, ok := dest.(RowScanner); ok {
		return s.ScanRow(rows)
	}

	v := reflect.ValueOf(dest)
	if v.Kind() != reflect.Pointer || v.IsNil() || v.Elem().Kind() != reflect.Struct {
		return fmt.Errorf("gomysql: ScanStruct needs a pointer to a struct, not %T", dest)
	}

	plan, err := rows.structPlan(v.Type().Elem())
	if err != nil {
		return err
	}

	rows.releaseDest()

	if cap(rows.dest) < len(plan) {
		rows.dest = make([]any, len(plan))
	}

	dests := rows.dest[:len(plan)]

	for i, index := range plan {
		field, ferr := fieldAlloc(v.Elem(), index)
		if ferr != nil {
			clear(dests)

			return ferr
		}

		dests[i] = field.Addr().Interface()
	}

	rows.dest = dests

	// Pointers to embedded structs may change between calls: then the
	// fields are found again.
	if rows.direct {
		rows.destOf = dest
	}

	err = rows.Scan(dests...)

	if !rows.direct {
		rows.releaseDest()
	}

	return err
}

// releaseDest drops the pointers into the struct of ScanStruct.
func (rows *Rows) releaseDest() {
	clear(rows.dest)
	rows.destOf = nil
}

// structPlan returns the index of the field of each column in t, once per
// Rows and type.
func (rows *Rows) structPlan(t reflect.Type) ([][]int, error) {
	if rows.structType == t {
		return rows.plan, nil
	}

	info := structFields(t)
	plan := make([][]int, len(rows.columns))
	seen := make(map[*int]string, len(rows.columns))
	direct := true

	for i := range rows.columns {
		name := string(rows.columns[i].Name)

		index := info.lookup(name)
		if index == nil {
			return nil, fmt.Errorf("gomysql: column %q has no field in %s", name, t)
		}

		if other, dup := seen[&index[0]]; dup {
			return nil, fmt.Errorf("gomysql: columns %q and %q go into the same field of %s", other, name, t)
		}

		seen[&index[0]] = name
		plan[i] = index
		direct = direct && !throughPointer(t, index)
	}

	rows.structType, rows.plan, rows.direct = t, plan, direct

	return plan, nil
}

// throughPointer reports whether the field of t at index is in an
// embedded struct a pointer points to.
func throughPointer(t reflect.Type, index []int) bool {
	for _, i := range index[:len(index)-1] {
		t = t.Field(i).Type
		if t.Kind() == reflect.Pointer {
			return true
		}
	}

	return false
}

// fieldAlloc returns the field of v at index, allocating nil pointers to
// embedded structs on the way.
func fieldAlloc(v reflect.Value, index []int) (reflect.Value, error) {
	for k, i := range index {
		if k > 0 && v.Kind() == reflect.Pointer {
			if v.IsNil() {
				if !v.CanSet() {
					return reflect.Value{}, fmt.Errorf("gomysql: nil pointer to the unexported embedded %s", v.Type())
				}

				v.Set(reflect.New(v.Type().Elem()))
			}

			v = v.Elem()
		}

		v = v.Field(i)
	}

	return v, nil
}

// ScanStruct copies the columns of the row into the struct dest points to,
// like Rows.ScanStruct, and closes the rows. Without a row it returns
// sql.ErrNoRows.
func (r *Row) ScanStruct(dest any) error {
	return r.scan(func(rows *Rows) error { return rows.ScanStruct(dest) })
}

// Struct returns the row as a T, a struct, like Row.ScanStruct.
func Struct[T any](r *Row) (T, error) {
	var v T
	if err := r.ScanStruct(&v); err != nil {
		var zero T

		return zero, err
	}

	return v, nil
}

// Value returns the only column of the row as a T, like Row.Scan.
func Value[T any](r *Row) (T, error) {
	var v T
	if err := r.Scan(&v); err != nil {
		var zero T

		return zero, err
	}

	return v, nil
}

// Structs returns the rows as structs, like Rows.ScanStruct, and closes
// them; nil if there are none. It takes what Query returns:
//
//	users, err := gomysql.Structs[User](conn.Query("SELECT id, name FROM users"))
func Structs[T any](rows *Rows, err error) ([]T, error) {
	return AppendStructs[T](nil)(rows, err)
}

// AppendStructs returns a function that appends the rows to dst as
// structs, like Structs, so that a slice can be reused. The function takes
// what Query returns:
//
//	users, err = gomysql.AppendStructs(users[:0])(conn.Query(q))
func AppendStructs[T any](dst []T) func(*Rows, error) ([]T, error) {
	return func(rows *Rows, err error) ([]T, error) {
		return appendRows(dst, rows, err, true)
	}
}

// Values returns the only column of the rows, like Rows.Scan, and closes
// them; nil if there are none. It takes what Query returns:
//
//	ids, err := gomysql.Values[uint64](conn.Query("SELECT id FROM users"))
func Values[T any](rows *Rows, err error) ([]T, error) {
	return AppendValues[T](nil)(rows, err)
}

// AppendValues returns a function that appends the only column of the
// rows to dst, like Values:
//
//	ids, err = gomysql.AppendValues(ids[:0])(conn.Query(q))
func AppendValues[T any](dst []T) func(*Rows, error) ([]T, error) {
	return func(rows *Rows, err error) ([]T, error) {
		return appendRows(dst, rows, err, false)
	}
}

// appendRows scans each row into one T, zeroed first, and appends it to
// dst: the destinations of a struct are found once, not per row.
func appendRows[T any](dst []T, rows *Rows, err error, structs bool) ([]T, error) {
	if err != nil {
		return dst, err
	}

	if rows == nil {
		return dst, errors.New("gomysql: nil Rows")
	}

	defer func() { _ = rows.Close() }()

	var zero T

	v := new(T)

	for rows.Next() {
		*v = zero

		var err error
		if structs {
			err = rows.ScanStruct(v)
		} else {
			err = rows.Scan(v)
		}

		if err != nil {
			return dst, err
		}

		dst = append(dst, *v)
	}

	return dst, rows.Close()
}
