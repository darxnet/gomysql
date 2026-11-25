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
// Rows.NextResultSet moves to the next result set, such as the next
// SELECT of a stored procedure (CALL).
//
// driver.ErrBadConn, after which database/sql retries on another
// connection, comes only when nothing ran: for a connection broken before
// the statement, and with Config.RejectReadOnly for a statement a
// read-only server refused, as after a failover. An error after the
// statement was sent is that of gomysql: the statement may have run. An
// idle connection is pinged before it is reused after a second, so that
// one the server closed is replaced.
//
// Raw gives the gomysql.Conn under a sql.Conn, for LoadData, ScanStruct and
// the rest of its API:
//
//	err := sqldriver.Raw(sqlConn, func(conn *gomysql.Conn) error {
//		...
//	})
package sqldriver

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"fmt"
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
// collation runs SET NAMES; with collation, charset is unused. The other
// parameters of go-sql-driver are refused, for gomysql would not do what
// they ask, and a parameter given twice is an error. Any other parameter
// sets a session variable when connecting: time_zone=%27%2B00:00%27 runs
// SET time_zone='+00:00'. Its value is SQL, so it must never come from
// users. Values are URL-escaped: a / in one, as in loc=Europe%2FKyiv,
// must be.
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

// Raw calls f with the gomysql connection under c, through sql.Conn.Raw.
// The connection stays c's: f must not keep it or close it.
func Raw(c *sql.Conn, f func(conn *gomysql.Conn) error) error {
	return c.Raw(func(dc any) error {
		cn, ok := dc.(*conn)
		if !ok {
			return fmt.Errorf("gomysql/sqldriver: Raw on a %T connection, not gomysql", dc)
		}

		return f(cn.c)
	})
}

// done records the end of a command.
func (c *conn) done(err error) error {
	c.used = time.Now()

	return err
}

// started is done for the start of a statement: Exec, Query, Prepare or
// Begin. One refused by a read-only server, with Config.RejectReadOnly,
// gives driver.ErrBadConn: it did not run, so database/sql retries it on
// a new connection, which may reach the new primary, outside a
// transaction. Later refusals, as while reading rows or at Commit, come as
// they are: work may have run. A procedure (CALL) refused after it wrote
// is retried too: the server does not tell the two apart.
func (c *conn) started(err error) error {
	err = c.done(err)
	if err != nil && c.broken() && sqlhook.RefusedReadOnly(err) {
		return fmt.Errorf("%w: %w", driver.ErrBadConn, err)
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
	if derr := c.started(err); derr != nil {
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
	if derr := c.started(err); derr != nil {
		return nil, derr
	}

	return &tx{conn: c, t: t}, nil
}

func (c *conn) ExecContext(ctx context.Context, query string, args []driver.NamedValue) (driver.Result, error) {
	if err := c.ready(); err != nil {
		return nil, err
	}

	res, err := c.c.ExecContext(ctx, query, namedArgs(args)...)
	if derr := c.started(err); derr != nil {
		return nil, derr
	}

	return res, nil
}

func (c *conn) QueryContext(ctx context.Context, query string, args []driver.NamedValue) (driver.Rows, error) {
	if err := c.ready(); err != nil {
		return nil, err
	}

	r, err := c.c.QueryContext(ctx, query, namedArgs(args)...)
	if derr := c.started(err); derr != nil {
		return nil, derr
	}

	return newRows(c, r), nil
}

// Ping returns driver.ErrBadConn for a dead connection, so that db.Ping
// tries another one, but the error of ctx as it is. A context done before
// the Ping leaves the connection as it was; one that ends during it breaks
// the connection, as it ends any command.
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

// IsValid is false for a broken connection, and for one left with open
// Rows, as through sql.Conn.Raw: database/sql then drops it.
func (c *conn) IsValid() bool { return !c.broken() && !sqlhook.Busy(c.c) }

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

// Close is that of gomysql.Stmt, which keeps the statement cached by its
// connection.
func (s *stmt) Close() error { return s.st.Close() }

// NumInput is -1: gomysql counts the arguments, :name ones too.
func (*stmt) NumInput() int { return -1 }

// Exec and Query are of driver.Stmt; database/sql calls the Context ones.
func (s *stmt) Exec(args []driver.Value) (driver.Result, error) {
	return s.exec(context.Background(), values(args))
}

func (s *stmt) ExecContext(ctx context.Context, args []driver.NamedValue) (driver.Result, error) {
	return s.exec(ctx, namedArgs(args))
}

func (s *stmt) exec(ctx context.Context, args []any) (driver.Result, error) {
	if err := s.conn.ready(); err != nil {
		return nil, err
	}

	res, err := s.st.ExecContext(ctx, args...)
	if derr := s.conn.started(err); derr != nil {
		return nil, derr
	}

	return res, nil
}

func (s *stmt) Query(args []driver.Value) (driver.Rows, error) {
	return s.query(context.Background(), values(args))
}

// values returns the arguments of Exec and Query for gomysql.
func values(args []driver.Value) []any {
	out := make([]any, len(args))
	for i, a := range args {
		out[i] = a
	}

	return out
}

func (s *stmt) QueryContext(ctx context.Context, args []driver.NamedValue) (driver.Rows, error) {
	return s.query(ctx, namedArgs(args))
}

func (s *stmt) query(ctx context.Context, args []any) (driver.Rows, error) {
	if err := s.conn.ready(); err != nil {
		return nil, err
	}

	r, err := s.st.QueryContext(ctx, args...)
	if derr := s.conn.started(err); derr != nil {
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
	_ driver.RowsNextResultSet              = (*rows)(nil)
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
	if err := rs.advance(); err != nil {
		return err
	}

	return sqlhook.Values(rs.r, dest)
}

// HasNextResultSet reports whether another result set may follow, once
// the rows are read to their end.
func (rs *rows) HasNextResultSet() bool { return sqlhook.HasNextResultSet(rs.r) }

// NextResultSet moves to the next result set with columns, such as the
// next SELECT of a CALL: io.EOF if there is none.
func (rs *rows) NextResultSet() error {
	if !rs.r.NextResultSet() {
		if err := rs.conn.done(rs.r.Err()); err != nil {
			return err
		}

		return io.EOF
	}

	rs.cols, _ = rs.r.Columns()
	rs.types = nil

	return nil
}

// advance reads the next row: io.EOF at the end.
func (rs *rows) advance() error {
	if rs.r.Next() {
		return nil
	}

	if err := rs.conn.done(rs.r.Err()); err != nil {
		return err
	}

	return io.EOF
}

// colType returns the type of column i, read on first use; an empty one
// if the rows are closed, which database/sql does not ask about.
func (rs *rows) colType(i int) *gomysql.ColumnType {
	if rs.types == nil {
		types, err := rs.r.ColumnTypes()
		if err != nil || i >= len(types) {
			return &gomysql.ColumnType{}
		}

		rs.types = types
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
