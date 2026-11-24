// Package sqldriver registers gomysql as the database/sql driver "gomysql":
//
//	import _ "github.com/darxnet/gomysql/sqldriver"
//
//	db, err := sql.Open("gomysql", "user:password@tcp(127.0.0.1:3306)/db?loc=Local")
//
// or, with a full gomysql.Config (TLS, InitCommands, Location):
//
//	db := sql.OpenDB(sqldriver.NewConnector(config))
//
// The connections behave as gomysql.Conn: arguments are interpolated into
// the text of the query (Prepare makes a real prepared statement, cached
// per connection), and values are those of gomysql, whichever protocol:
// int64 and uint64, float32 and float64, time.Time for DATE, DATETIME and
// TIMESTAMP. Text, JSON, binary strings, DECIMAL and TIME are []byte that
// point into the row, as database/sql takes them best: sql.RawBytes gets
// them without a copy, []byte and string with one, and any gets []byte.
//
// Rows have one result set: the others, such as those of a CALL, are
// read and dropped.
//
// sql.Conn.Raw gives the gomysql.Conn, for LoadData, ScanStruct and the
// rest of its API:
//
//	err := conn.Raw(func(dc any) error {
//		c := dc.(interface{ Conn() *gomysql.Conn }).Conn()
//		...
//	})
package sqldriver

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"io"
	"reflect"
	"time"

	"github.com/darxnet/gomysql"
	"github.com/darxnet/gomysql/internal/sqlhook"
)

func init() { //nolint:gochecknoinits // registering the driver is what importing the package is for
	sql.Register("gomysql", Driver{})
}

// Driver is the database/sql driver of gomysql.
type Driver struct{}

// Open connects with dsn; see OpenConnector.
func (d Driver) Open(dsn string) (driver.Conn, error) {
	c, err := d.OpenConnector(dsn)
	if err != nil {
		return nil, err
	}

	return c.Connect(context.Background())
}

// OpenConnector parses dsn, in the format of go-sql-driver/mysql:
//
//	[user[:password]@][net[(address)]]/dbname[?param=value&...]
//
// The parameters that have a gomysql.Config field are tls (true,
// skip-verify or false), collation, loc, compress, clientFoundRows,
// allowCleartextPasswords, allowPublicKeyRetrieval, rejectReadOnly and
// connectionAttributes; timeout limits connecting, and charset without
// collation runs SET NAMES. The other parameters of go-sql-driver are
// refused, for gomysql would not do what they ask. Any other parameter
// sets a session variable when connecting: time_zone=%27%2B00:00%27 runs
// SET time_zone='+00:00'.
func (Driver) OpenConnector(dsn string) (driver.Connector, error) {
	config, timeout, err := parseDSN(dsn)
	if err != nil {
		return nil, err
	}

	return &connector{config: config, timeout: timeout}, nil
}

// NewConnector returns a connector of config, for sql.OpenDB.
//
//nolint:gocritic // hugeParam: once per pool
func NewConnector(config gomysql.Config) driver.Connector {
	return &connector{config: config}
}

type connector struct {
	config  gomysql.Config
	timeout time.Duration // of connecting, from the DSN
}

func (c *connector) Connect(ctx context.Context) (driver.Conn, error) {
	if c.timeout > 0 {
		var cancel context.CancelFunc

		ctx, cancel = context.WithTimeout(ctx, c.timeout)
		defer cancel()
	}

	cc, err := gomysql.ConnectContext(ctx, c.config)
	if err != nil {
		return nil, err
	}

	return &conn{c: cc, used: time.Now()}, nil
}

func (*connector) Driver() driver.Driver { return Driver{} }

// pingAfter is how long a connection may be idle before ResetSession
// pings it, as the pool of gomysql does.
const pingAfter = time.Second

// conn is a gomysql.Conn as a driver.Conn. Whether it is broken is the
// state of the gomysql.Conn, also after commands through sql.Conn.Raw.
type conn struct {
	c    *gomysql.Conn
	used time.Time // the end of the last command through the driver
}

var (
	_ driver.Conn               = (*conn)(nil)
	_ driver.ConnBeginTx        = (*conn)(nil)
	_ driver.ConnPrepareContext = (*conn)(nil)
	_ driver.ExecerContext      = (*conn)(nil)
	_ driver.QueryerContext     = (*conn)(nil)
	_ driver.Pinger             = (*conn)(nil)
	_ driver.SessionResetter    = (*conn)(nil)
	_ driver.Validator          = (*conn)(nil)
	_ driver.NamedValueChecker  = (*conn)(nil)
)

// Conn returns the gomysql connection, for sql.Conn.Raw.
func (c *conn) Conn() *gomysql.Conn { return c.c }

// done records the end of a command. A statement refused by a read-only
// server, with Config.RejectReadOnly, gives driver.ErrBadConn: it did not
// run, so database/sql retries it on a new connection, which may reach the
// new primary.
func (c *conn) done(err error) error {
	c.used = time.Now()

	if err != nil && c.broken() && sqlhook.RefusedReadOnly(err) {
		return driver.ErrBadConn
	}

	return err
}

func (c *conn) broken() bool { return sqlhook.Broken(c.c) }

// ready returns driver.ErrBadConn for a connection broken before: nothing
// is sent, so database/sql may retry on another one. After a command was
// sent, the error is that of gomysql, never driver.ErrBadConn: the command
// may have run.
func (c *conn) ready() error {
	if c.broken() {
		return driver.ErrBadConn
	}

	return nil
}

func (c *conn) Prepare(query string) (driver.Stmt, error) {
	return c.PrepareContext(context.Background(), query)
}

func (c *conn) PrepareContext(ctx context.Context, query string) (driver.Stmt, error) {
	if err := c.ready(); err != nil {
		return nil, err
	}

	st, err := c.c.PrepareContext(ctx, query)
	if derr := c.done(err); derr != nil {
		return nil, derr
	}

	return &stmt{conn: c, st: st}, nil
}

func (c *conn) Close() error { return c.c.Close() }

func (c *conn) Begin() (driver.Tx, error) {
	return c.BeginTx(context.Background(), driver.TxOptions{})
}

func (c *conn) BeginTx(ctx context.Context, opts driver.TxOptions) (driver.Tx, error) {
	if err := c.ready(); err != nil {
		return nil, err
	}

	t, err := c.c.BeginTx(ctx, &sql.TxOptions{Isolation: sql.IsolationLevel(opts.Isolation), ReadOnly: opts.ReadOnly})
	if derr := c.done(err); derr != nil {
		return nil, derr
	}

	return &tx{conn: c, t: t}, nil
}

func (c *conn) ExecContext(ctx context.Context, query string, args []driver.NamedValue) (driver.Result, error) {
	if err := c.ready(); err != nil {
		return nil, err
	}

	res, err := c.c.ExecContext(ctx, query, namedArgs(args)...)
	if derr := c.done(err); derr != nil {
		return nil, derr
	}

	return res, nil
}

func (c *conn) QueryContext(ctx context.Context, query string, args []driver.NamedValue) (driver.Rows, error) {
	if err := c.ready(); err != nil {
		return nil, err
	}

	r, err := c.c.QueryContext(ctx, query, namedArgs(args)...)
	if derr := c.done(err); derr != nil {
		return nil, derr
	}

	return newRows(c, r), nil
}

// Ping returns driver.ErrBadConn for a dead connection, so that db.Ping
// tries another one, but the error of ctx as it is: a canceled Ping does
// not break a connection that was fine.
func (c *conn) Ping(ctx context.Context) error {
	if err := c.ready(); err != nil {
		return err
	}

	err := c.done(c.c.PingContext(ctx))
	if err != nil && ctx.Err() == nil && c.broken() {
		return driver.ErrBadConn
	}

	return err
}

// ResetSession runs before database/sql reuses the connection: a broken
// one is dropped, and one idle for more than pingAfter is pinged, so that
// a connection the server closed meanwhile is replaced instead of failing
// the next statement. The session itself is kept, as by go-sql-driver.
func (c *conn) ResetSession(ctx context.Context) error {
	if err := c.ready(); err != nil {
		return err
	}

	if time.Since(c.used) > pingAfter {
		return c.Ping(ctx)
	}

	return nil
}

func (c *conn) IsValid() bool { return !c.broken() }

// CheckNamedValue accepts every argument as it is: gomysql converts them
// itself, with uint64 above 1<<63, slices for IN (?) and sql.Named.
func (*conn) CheckNamedValue(*driver.NamedValue) error { return nil }

// namedArgs returns the arguments for gomysql: named ones as sql.Named.
func namedArgs(args []driver.NamedValue) []any {
	if len(args) == 0 {
		return nil
	}

	out := make([]any, len(args))

	for i, a := range args {
		if a.Name != "" {
			out[i] = sql.Named(a.Name, a.Value)
		} else {
			out[i] = a.Value
		}
	}

	return out
}

// valueArgs is namedArgs of the arguments of Stmt.Exec and Stmt.Query.
func valueArgs(args []driver.Value) []any {
	out := make([]any, len(args))
	for i, a := range args {
		out[i] = a
	}

	return out
}

type tx struct {
	conn *conn
	t    *gomysql.Tx
}

func (t *tx) Commit() error   { return t.conn.done(t.t.Commit()) }
func (t *tx) Rollback() error { return t.conn.done(t.t.Rollback()) }

type stmt struct {
	conn *conn
	st   *gomysql.Stmt
}

var (
	_ driver.StmtExecContext   = (*stmt)(nil)
	_ driver.StmtQueryContext  = (*stmt)(nil)
	_ driver.NamedValueChecker = (*stmt)(nil)
)

// Close does nothing: the statement stays cached by its connection.
func (s *stmt) Close() error { return s.st.Close() }

// NumInput is -1: gomysql counts the arguments, :name ones too.
func (*stmt) NumInput() int { return -1 }

func (s *stmt) Exec(args []driver.Value) (driver.Result, error) {
	return s.exec(context.Background(), valueArgs(args))
}

func (s *stmt) ExecContext(ctx context.Context, args []driver.NamedValue) (driver.Result, error) {
	return s.exec(ctx, namedArgs(args))
}

func (s *stmt) exec(ctx context.Context, args []any) (driver.Result, error) {
	if err := s.conn.ready(); err != nil {
		return nil, err
	}

	res, err := s.st.ExecContext(ctx, args...)
	if derr := s.conn.done(err); derr != nil {
		return nil, derr
	}

	return res, nil
}

func (s *stmt) Query(args []driver.Value) (driver.Rows, error) {
	return s.query(context.Background(), valueArgs(args))
}

func (s *stmt) QueryContext(ctx context.Context, args []driver.NamedValue) (driver.Rows, error) {
	return s.query(ctx, namedArgs(args))
}

func (s *stmt) query(ctx context.Context, args []any) (driver.Rows, error) {
	if err := s.conn.ready(); err != nil {
		return nil, err
	}

	r, err := s.st.QueryContext(ctx, args...)
	if derr := s.conn.done(err); derr != nil {
		return nil, derr
	}

	return newRows(s.conn, r), nil
}

func (*stmt) CheckNamedValue(*driver.NamedValue) error { return nil }

// rows is gomysql.Rows as driver.Rows.
type rows struct {
	conn  *conn
	r     *gomysql.Rows
	cols  []string
	types []*gomysql.ColumnType
}

var (
	_ driver.RowsColumnTypeDatabaseTypeName = (*rows)(nil)
	_ driver.RowsColumnTypeLength           = (*rows)(nil)
	_ driver.RowsColumnTypeNullable         = (*rows)(nil)
	_ driver.RowsColumnTypePrecisionScale   = (*rows)(nil)
	_ driver.RowsColumnTypeScanType         = (*rows)(nil)
)

func newRows(c *conn, r *gomysql.Rows) *rows {
	rs := &rows{conn: c, r: r}
	rs.cols, _ = r.Columns()

	return rs
}

func (rs *rows) Columns() []string { return rs.cols }

func (rs *rows) Close() error {
	return rs.conn.done(rs.r.Close())
}

// Next scans the row into dest. Bytes, of text too, point into the row
// until the next one: database/sql copies them, but for sql.RawBytes.
func (rs *rows) Next(dest []driver.Value) error {
	if !rs.r.Next() {
		if err := rs.conn.done(rs.r.Err()); err != nil {
			return err
		}

		return io.EOF
	}

	return sqlhook.Values(rs.r, dest)
}

func (rs *rows) colType(i int) *gomysql.ColumnType {
	if rs.types == nil {
		rs.types, _ = rs.r.ColumnTypes()
	}

	return rs.types[i]
}

func (rs *rows) ColumnTypeDatabaseTypeName(i int) string { return rs.colType(i).DatabaseTypeName() }

func (rs *rows) ColumnTypeLength(i int) (int64, bool) { return rs.colType(i).Length() }

func (rs *rows) ColumnTypeNullable(i int) (nullable, ok bool) { return rs.colType(i).Nullable() }

func (rs *rows) ColumnTypePrecisionScale(i int) (precision, scale int64, ok bool) {
	return rs.colType(i).DecimalSize()
}

// ColumnTypeScanType is that of gomysql, but string for TIME: it comes
// as bytes ("12:34:56"), which database/sql cannot convert to the
// time.Duration of gomysql.
func (rs *rows) ColumnTypeScanType(i int) reflect.Type {
	ct := rs.colType(i)
	if ct.DatabaseTypeName() != "TIME" {
		return ct.ScanType()
	}

	if nullable, _ := ct.Nullable(); nullable {
		return reflect.TypeFor[sql.NullString]()
	}

	return reflect.TypeFor[string]()
}
