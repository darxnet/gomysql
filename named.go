package gomysql

import (
	"database/sql"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"sync"
	"time"
)

var (
	errMixedPlaceholders = errors.New("gomysql: the query mixes ? and :name placeholders")
	errNeedNamed         = errors.New("gomysql: :name placeholders need sql.Named arguments, a struct or a map")
	errNeedPositional    = errors.New("gomysql: ? placeholders need positional arguments; " +
		"sql.Named, struct and map arguments are for :name placeholders")
	errEmptySlice  = errors.New("gomysql: an empty slice has no SQL list: IN () is a syntax error")
	errSliceInStmt = errors.New("gomysql: a slice argument is expanded only by Conn.Query and Conn.Exec; " +
		"a prepared statement has a fixed number of placeholders")
)

// namedEnd returns the end of the :name placeholder at query[i], or i if
// none starts there. The colon must not follow an identifier or a colon,
// so labels (lbl:LOOP) and := are not placeholders.
func namedEnd(query string, i int) int {
	if query[i] != ':' || i > 0 && (isIdentByte(query[i-1]) || query[i-1] == ':') {
		return i
	}

	j := i + 1
	if j == len(query) || !isIdentByte(query[j]) || query[j] == '$' || query[j] >= '0' && query[j] <= '9' {
		return i
	}

	for j < len(query) && isIdentByte(query[j]) {
		j++
	}

	return j
}

// isIdentByte reports whether c can be part of an unquoted identifier;
// bytes of multibyte characters can.
func isIdentByte(c byte) bool {
	return c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' ||
		c == '_' || c == '$' || c >= 0x80
}

// binder gives the argument of each placeholder: positional ones in order,
// named ones by name from sql.NamedArg arguments, a struct or a map.
//
// It does not keep the positional arguments, so that the variadic slice of
// Conn.Query does not escape.
type binder struct {
	nargs  int            // number of positional arguments
	next   int            // the next positional argument
	names  map[string]int // sql.NamedArg: name to index in values
	values []any          // of sql.NamedArg
	used   []bool         // of values
	src    reflect.Value  // a struct or a map
	fields map[string][]int

	sawPositional, sawNamed bool
}

func (b *binder) init(args []any) error {
	b.nargs = len(args)

	if len(args) == 1 {
		if src, ok := namedSource(args[0]); ok {
			b.src = src
			if src.Kind() == reflect.Struct {
				b.fields = structFields(src.Type())
			}

			return nil
		}
	}

	for i, arg := range args {
		na, ok := arg.(sql.NamedArg)
		if !ok {
			if i > 0 && b.names != nil {
				return errors.New("gomysql: sql.Named and positional arguments are mixed")
			}

			return nil
		}

		if b.values == nil {
			b.values = make([]any, len(args))
		}

		b.values[i] = na.Value

		if na.Name == "" {
			return fmt.Errorf("gomysql: argument %d: sql.Named without a name", i)
		}

		if b.names == nil {
			b.names = make(map[string]int, len(args))
			b.used = make([]bool, len(args))
		}

		if _, dup := b.names[na.Name]; dup {
			return fmt.Errorf("gomysql: two sql.Named arguments %q", na.Name)
		}

		b.names[na.Name] = i
	}

	return nil
}

// isNamed reports whether the arguments are named.
func (b *binder) isNamed() bool {
	return b.names != nil || b.src.IsValid()
}

// namedSource returns the struct or map of arg, a struct that is not a
// value (driver.Valuer, time.Time) or a map with string keys, through
// pointers.
func namedSource(arg any) (reflect.Value, bool) {
	if arg == nil {
		return reflect.Value{}, false
	}

	switch arg.(type) {
	case time.Time, sql.NamedArg:
		return reflect.Value{}, false
	}

	v := reflect.ValueOf(arg)
	if v.Type().Implements(valuerType) {
		return reflect.Value{}, false
	}

	for v.Kind() == reflect.Pointer && !v.IsNil() {
		v = v.Elem()
	}

	switch {
	case v.Kind() == reflect.Struct:
		t := v.Type()

		return v, !t.Implements(valuerType) && t != reflect.TypeFor[time.Time]() && t != reflect.TypeFor[sql.NamedArg]()
	case v.Kind() == reflect.Map && v.Type().Key().Kind() == reflect.String:
		return v, true
	}

	return reflect.Value{}, false
}

// positional returns the index in args of the argument of a ?
// placeholder; ok is false when there are no more.
func (b *binder) positional() (i int, ok bool, err error) {
	if b.sawNamed {
		return 0, false, errMixedPlaceholders
	}

	b.sawPositional = true

	if b.isNamed() {
		return 0, false, errNeedPositional
	}

	if b.next == b.nargs {
		return 0, false, nil
	}

	b.next++

	return b.next - 1, true, nil
}

// named returns the argument of a :name placeholder.
func (b *binder) named(name string) (any, error) {
	if b.sawPositional {
		return nil, errMixedPlaceholders
	}

	b.sawNamed = true

	switch {
	case b.names != nil:
		i, ok := b.names[name]
		if !ok {
			return nil, fmt.Errorf("gomysql: no sql.Named argument for :%s", name)
		}

		b.used[i] = true

		return b.values[i], nil
	case b.src.Kind() == reflect.Map:
		v := b.src.MapIndex(reflect.ValueOf(name).Convert(b.src.Type().Key()))
		if !v.IsValid() {
			return nil, fmt.Errorf("gomysql: no key %q in the map for :%s", name, name)
		}

		return v.Interface(), nil
	case b.src.IsValid():
		index := lookupField(b.fields, name)
		if index == nil {
			return nil, fmt.Errorf("gomysql: no field of %s for :%s", b.src.Type(), name)
		}

		v, err := b.src.FieldByIndexErr(index)
		if err != nil {
			return nil, fmt.Errorf("gomysql: field for :%s: %w", name, err)
		}

		return v.Interface(), nil
	}

	return nil, errNeedNamed
}

// done checks that the arguments were used, given the number of ?
// placeholders of query.
func (b *binder) done(query string, backslash bool) error {
	switch {
	case b.names != nil:
		for name, i := range b.names {
			if !b.used[i] {
				return fmt.Errorf("gomysql: no :%s placeholder for the sql.Named argument", name)
			}
		}
	case b.src.IsValid():
		if !b.sawNamed {
			return fmt.Errorf("gomysql: a %s argument, but no :name placeholders", b.src.Type())
		}
	case b.next != b.nargs:
		return errArgCount(countPlaceholders(query, backslash), b.nargs)
	}

	return nil
}

var fieldCache sync.Map // reflect.Type to map[string][]int

// structFields maps the names of the fields of t, through embedded
// structs, to their index: the db tag without options after a comma, or
// the name of the field; db:"-" skips a field. Shallower fields hide deeper ones, as in encoding/json.
func structFields(t reflect.Type) map[string][]int {
	if f, ok := fieldCache.Load(t); ok {
		return f.(map[string][]int) //nolint:forcetypeassert // only maps are stored
	}

	fields := make(map[string][]int)
	depth := make(map[string]int)

	var walk func(t reflect.Type, index []int)

	walk = func(t reflect.Type, index []int) {
		for i := range t.NumField() {
			f := t.Field(i)

			tag, tagged := f.Tag.Lookup("db")
			tag, _, _ = strings.Cut(tag, ",") // options, as in sqlx and pgx

			if tag == "-" {
				continue
			}

			idx := make([]int, len(index)+1)
			copy(idx, index)
			idx[len(index)] = i

			ft := f.Type
			if ft.Kind() == reflect.Pointer {
				ft = ft.Elem()
			}

			if f.Anonymous && !tagged && ft.Kind() == reflect.Struct {
				walk(ft, idx)

				continue
			}

			if !f.IsExported() {
				continue
			}

			name := f.Name
			if tagged && tag != "" {
				name = tag
			}

			if d, ok := depth[name]; !ok || len(idx) < d {
				fields[name], depth[name] = idx, len(idx)
			}
		}
	}

	walk(t, nil)

	f, _ := fieldCache.LoadOrStore(t, fields)

	return f.(map[string][]int) //nolint:forcetypeassert // only maps are stored
}

// lookupField finds name exactly, or else ignoring case.
func lookupField(fields map[string][]int, name string) []int {
	if index, ok := fields[name]; ok {
		return index
	}

	for n, index := range fields {
		if strings.EqualFold(n, name) {
			return index
		}
	}

	return nil
}

// expandable reports whether arg is a slice to expand into a list: not
// bytes and not a driver.Valuer.
func expandable(arg any) (reflect.Value, bool) {
	if arg == nil {
		return reflect.Value{}, false
	}

	v := reflect.ValueOf(arg)
	if v.Kind() != reflect.Slice || v.Type().Elem().Kind() == reflect.Uint8 || v.Type().Implements(valuerType) {
		return reflect.Value{}, false
	}

	return v, true
}

// rewriteNamed replaces the :name placeholders of query with ?, for
// Prepare, and returns their names; none if query has none.
func rewriteNamed(query string, backslash bool) (sent string, names []string, err error) {
	var (
		buf      []byte
		question bool
		last     int
	)

	for i := 0; i < len(query); {
		if end := skipNonCode(query, i, backslash); end > i {
			i = end

			continue
		}

		if query[i] == '?' {
			question = true
		}

		end := namedEnd(query, i)
		if end == i {
			i++

			continue
		}

		buf = append(append(buf, query[last:i]...), '?')
		names = append(names, query[i+1:end])
		last, i = end, end
	}

	if names == nil {
		return query, nil, nil
	}

	if question {
		return "", nil, errMixedPlaceholders
	}

	return string(append(buf, query[last:]...)), names, nil
}
