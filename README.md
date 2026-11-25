# gomysql

[![Go Reference](https://pkg.go.dev/badge/github.com/darxnet/gomysql.svg)](https://pkg.go.dev/github.com/darxnet/gomysql)
[![Go](https://github.com/darxnet/gomysql/actions/workflows/release.yml/badge.svg)](https://github.com/darxnet/gomysql/actions/workflows/release.yml)
[![Coverage](https://codecov.io/gh/darxnet/gomysql/graph/badge.svg)](https://codecov.io/gh/darxnet/gomysql)

`gomysql` is a MySQL and MariaDB client for Go, built for heavy reads. It
streams result rows without per-row allocations and uses 2–4× less client CPU
than `database/sql` with `go-sql-driver/mysql`. Its API follows `database/sql`,
and its `sqldriver` package plugs it into `database/sql` itself.

It is not related to
[go-mysql-org/go-mysql](https://github.com/go-mysql-org/go-mysql), a toolkit
for the replication protocol.

**Status:** the API follows `database/sql` closely but is not stable until v1.

- [Quick start](#quick-start)
- [The pool](#the-pool)
- [database/sql](#databasesql)
- [Arguments](#arguments)
- [Scanning values](#scanning-values)
- [Structs](#structs)
- [Prepared statements](#prepared-statements)
- [Errors, contexts and transactions](#errors-contexts-and-transactions)
- [Sessions](#sessions)
- [LOAD DATA](#load-data)
- [Authentication and TLS](#authentication-and-tls)
- [Performance](#performance)
- [Compatibility](#compatibility)

## Quick start

```go
conn, err := gomysql.Connect(gomysql.Config{
	Network:  "tcp",
	Address:  "127.0.0.1:3306",
	User:     "user",
	Password: "password",
	Database: "db",
})
if err != nil {
	return err
}
defer conn.Close()

rows, err := conn.Query("SELECT id, name FROM users WHERE status = ?", status)
if err != nil {
	return err
}
defer rows.Close()

for rows.Next() {
	var (
		id   uint64
		name string
	)
	if err := rows.Scan(&id, &name); err != nil {
		return err
	}
}
return rows.Err()
```

A `Conn` is one connection and is not safe for concurrent use. Until the
`Rows` of a query are read to the end or closed, other commands on the
connection fail with `ErrBusy`. Nothing is sent in that case.

## The pool

`Open` returns a `DB`, a pool safe for concurrent use. Like `sql.Open`, it
connects only when a connection is needed:

```go
db, err := gomysql.Open(config)
if err != nil {
	return err
}
defer db.Close()

conn, err := db.Conn(ctx) // waits within ctx if the pool is full
if err != nil {
	return err
}
defer conn.Close() // returns the connection to the pool
```

`DB` has no `Query` or `Exec` of its own. `db.Conn` gives a plain `*Conn` with
all its methods, and `Close` returns it to the pool. Do not use it after
`Close`, because another goroutine may have it by then. `conn.Hijack()` takes a
connection out of the pool: its place is freed, and `Close` really closes it.

The setters are those of `database/sql`. The defaults differ:

| Setting | Default |
|---|---|
| `SetMaxOpenConns` | max(4, GOMAXPROCS), never above the connection limit of the account (`max_user_connections`, else `max_connections`) less 5, left to administrators and migrations |
| `SetMaxIdleConns` | all of them (`database/sql` keeps 2, which makes connections open and close under load) |
| `SetConnMaxIdleTime` | 65 s before the server's `wait_timeout` (half of it below 130 s); none if the server does not say |
| `SetConnMaxLifetime` | 30 minutes, each connection up to 10% earlier so that they do not close together |

When a connection comes back, its session is kept as it is: session
variables, user variables and temporary tables pass to the next user, as in
`database/sql`. The pool does not reset it, because a reset would also drop
the connection's prepared statements. Instead, a returned connection is closed
if it:

- is broken;
- has open `Rows`;
- is in a transaction, including one started with `START TRANSACTION` as
  text;
- was switched to another user or database;
- is too old, or above the limits.

A connection idle for more than a second is pinged when it is taken, and
replaced if it is dead. Load balancers that drop idle TCP connections (Azure
LB after 4 minutes, AWS NLB after 350 s) do not see the connection as idle:
Go sends TCP keepalives every 15 s. Proxies with an idle timeout of their own
(RDS Proxy: 30 minutes, TiDB Cloud: 340 s) need `SetConnMaxIdleTime` below it.

## database/sql

```go
import _ "github.com/darxnet/gomysql/sqldriver"

db, err := sql.Open("gomysql", "user:password@tcp(127.0.0.1:3306)/db?loc=Local")
// or, with a full Config (TLS, InitCommands, keys):
db := sql.OpenDB(sqldriver.NewConnector(config))
```

The DSN has the format of go-sql-driver. These parameters have a `Config`
field:

- `tls` (`true`, `skip-verify` or `false`)
- `collation`
- `loc`
- `compress`
- `clientFoundRows`
- `allowCleartextPasswords`
- `allowPublicKeyRetrieval`
- `rejectReadOnly`
- `connectionAttributes`

`timeout` limits connecting, and `charset` without `collation` runs
`SET NAMES`.

The other parameters of go-sql-driver are refused, with the reason, so that a
DSN does not silently ask for something gomysql does not do. Examples are
`parseTime`, `multiStatements`, `readTimeout` and `interpolateParams`. Any
other parameter sets a session variable when connecting:
`time_zone=%27%2B00:00%27` runs `SET time_zone='+00:00'`. Its value is SQL, so
it must never come from users. A `/` in a value or in the database name is
escaped as `%2F`.

The connections behave as `Conn` does:

- **Arguments** are interpolated into the text of the query.
- **`Prepare`** makes a real prepared statement.
- **Values** come typed: integers, floats, `time.Time` and `BIT` as numbers.
  Text, JSON, binary strings, `DECIMAL` and `TIME` come as `[]byte` pointing
  into the row: `sql.RawBytes` gets them without a copy, `string` and `[]byte`
  with one, and `any` with `[]byte`.
- **Retries:** `database/sql` retries a statement on `driver.ErrBadConn`. The
  driver returns it only when nothing ran: for a connection broken before the
  statement, and, with `rejectReadOnly`, for a statement a read-only server
  refused.
- **Stored procedures:** `rows.NextResultSet()` reads the next result set of a
  `CALL`.

`sqldriver.Raw` gives the `*gomysql.Conn` under a `*sql.Conn`, for `LoadData`,
`ScanStruct` and the rest of the native API:

```go
err := sqldriver.Raw(sqlConn, func(conn *gomysql.Conn) error {
	_, err := conn.LoadData("LOAD DATA LOCAL INFILE 'x' INTO TABLE t", r)
	return err
})
```

On Go 1.27 and later the driver implements `driver.RowsColumnScanner` and
scans straight into your variables. This is about twice as fast as
go-sql-driver, with no allocation per row for numbers and `sql.RawBytes` (see
[Performance](#performance)).

## Arguments

`Query`, `Exec` and `QueryRow` replace the placeholders with the arguments on
the client and send one statement: one round trip, no prepared statement.

- **Placeholders** are `?` for positional arguments or `:name` for named ones,
  not both in one query. Named arguments come from `sql.Named`, from one struct
  (fields found as by `ScanStruct`, so `:user_id` finds `UserID`), or from one
  `map[string]any`.
- **What is not a placeholder:**
  - a `?` or `:name` in a string, a quoted identifier or a comment, as the
    server sees it;
  - a `:` after an identifier or another `:`, as in `lbl:LOOP` or `@a:=1`.
    So `id:id` is text, and the server reports the error.
- **Versioned comments:** inside `/*!80000 … */`, `?` and `:name` are always
  placeholders, even when the server ignores the comment for its version.
- **Without arguments** the query is sent as it is.
- **Slices** other than `[]byte` become lists: `IN (?)` with `[]int{1, 2}` is
  `IN (1, 2)`. An empty slice is an error. Slices work only in `Query` and
  `Exec`, not in prepared statements.
- **Conversions** are those of `database/sql`:
  - a `driver.Valuer` gives its value;
  - a pointer gives what it points to (nil is NULL);
  - a named type gives its basic type.
  - Other types are an error, not a silent string.
- **Literals:**
  - strings are quoted and escaped;
  - `[]byte` becomes `_binary'…'`;
  - `bool` becomes 1 or 0;
  - floats get their shortest exact form, and `float32` is not widened to
    `float64` digits;
  - NaN and infinities are an error;
  - `time.Time` is written in `Config.Location` with up to 6 fractional
    digits; the zero time is `'0000-00-00'`;
  - `time.Duration` is its integer number of nanoseconds;
  - `json.RawMessage` is a string.
- **`NO_BACKSLASH_ESCAPES`** is followed as the server reports it, also after
  `SET sql_mode`.
- **Unsafe collations:** big5, cp932, gbk, gb18030 and sjis can hide a
  backslash inside a multibyte character, so string arguments are refused
  there. Use `Prepare`.

## Scanning values

`Scan` converts as `database/sql` does: into strings, `[]byte`,
`sql.RawBytes`, `any`, `bool`, integers, floats, `time.Time`, `time.Duration`,
an `sql.Scanner` such as `sql.NullString`, a pointer to a pointer (nil for
NULL), or a type based on one of these. Things that may surprise:

- **NULL** goes only into `[]byte`, `sql.RawBytes`, `any`, a pointer to a
  pointer and an `sql.Scanner`. Into anything else it is an error, as in
  `database/sql`.
- **Range:** out-of-range numbers are an error. A value such as 300 does not
  fit into an `int8`.
- **Into `any` or an `sql.Scanner`** a value comes the same in both protocols:
  - integers as `int64`, but `BIGINT UNSIGNED` and `BIT` as `uint64`;
  - `FLOAT` as `float32`, `DOUBLE` as `float64`;
  - dates as `time.Time`;
  - JSON as `json.RawMessage` (MariaDB sends JSON as text, a `string`);
  - text as `string`;
  - binary strings, `DECIMAL` and `TIME` as `[]byte`.
- **Dates:** `DATE`, `DATETIME` and `TIMESTAMP` are `time.Time` in
  `Config.Location`, UTC by default. The zero date is the zero `time.Time`.
  Scanned into a string they keep the server's format.
- **`TIME`** goes into `time.Duration`. Integers do too, as nanoseconds.
- **`BIT`** values up to 64 bits go into integers and `bool`.
- **JSON** and text go into a pointer to a struct, map, array or slice through
  `json.Unmarshal`.
- **Copies:** `[]byte` gets a copy. `sql.RawBytes` points into the row and is
  valid until the next `Next`, `Scan` or `Close`.

## Structs

```go
type User struct {
	ID       uint64
	Name     string
	JoinedAt time.Time `db:"created"`
}

users, err := gomysql.Structs[User](conn.Query("SELECT id, name, created FROM users"))
user, err := gomysql.Struct[User](conn.QueryRow("SELECT id, name, created FROM users WHERE id = ?", id))
ids, err := gomysql.Values[uint64](conn.Query("SELECT id FROM users"))
users, err = gomysql.AppendStructs(users[:0])(conn.Query(q)) // reuse a slice
```

`AppendStructs(dst)(conn.Query(q))` looks unusual because Go does not allow
`f(dst, conn.Query(q))`: a call with several results can only be the sole
argument of another call.

`rows.ScanStruct(&v)` scans the current row into a struct:

- **Matching columns to fields.** A column goes into the field of the same
  name. The `db` tag wins; without it the field name is compared exactly, then
  ignoring case, then ignoring underscores, so `user_id` goes into `UserID`.
  `db:"-"` skips a field.
- **Embedded structs:** their fields count as fields of the struct. A nil
  pointer to an exported embedded struct is allocated. A nil pointer to an
  unexported one is an error.
- **Every column needs a field.** A column without one is an error. Fields
  without a column keep their values, so one struct can serve several queries.
- **Speed:** scanning into the same struct row after row costs as much as
  `Scan`, because pointers to its fields are kept until `Close`.
- **Custom scanning:** a type that implements `RowScanner` scans itself.

## Prepared statements

`Prepare` makes a server-side prepared statement, which uses the binary
protocol. Each connection caches its statements:

- **Reuse.** `Prepare` of the same text returns the same `*Stmt` without
  asking the server.
- **`Stmt.Close` does nothing.** The statement stays cached and shared.
- **Cache size.** The cache holds max(64, `max_prepared_stmt_count` /
  `max_connections`) statements and closes the least recently used beyond
  that. If the server reports it holds too many statements of all connections
  (error 1461), the cache closes half of its own and stays smaller.
- **Re-preparing.** A statement that left the cache, was lost by `Reconnect`,
  `ResetConnection` or `ChangeUser`, or is unknown to the server (error 1243,
  as after an Aurora failover) is prepared again when executed.
- **`UseDatabase`** closes the cached statements, because a statement keeps
  the database it was prepared in. A `USE` statement sent as a query does not.

**Parameter types on MySQL 8.** Since 8.0.22, MySQL keeps the type a parameter
had in an earlier execution of a statement and converts later arguments to
it. A string after an integer comes back as `0`. Anything after a `DATETIME`
comes back as a zero date. A large integer after a `DOUBLE` is rounded. This
is documented behaviour; Oracle advises one statement per set of types (bugs
[#102942](https://bugs.mysql.com/bug.php?id=102942) and
[#113240](https://bugs.mysql.com/bug.php?id=113240)).

gomysql follows that advice. It prepares the statement again when the class
of an argument changes: integer, floating point, string or date (NULL keeps
the class). Mixing types on one statement therefore works, at the cost of an
extra round trip on each change. MariaDB and MySQL 5.7 do not keep the type
and pay the same small cost.

## Errors, contexts and transactions

- **`*gomysql.Error`** is an error of the server, with its number, SQLSTATE
  and message. Compare the number with
  `errors.Is(err, &gomysql.Error{Number: 1062})` or `errors.As`.
- **`ErrBadConn`** matches every error that left the connection unusable, with
  the cause wrapped: I/O errors, malformed packets, canceled contexts,
  connection-level server errors (SQLSTATE class 08) and errors the server
  sends before closing (4031 after `wait_timeout`). Only `Reconnect` and
  `Close` work after it. The statement may or may not have run.
- **`ErrBusy`** means the `Rows` of an earlier query are still open. Nothing
  was sent.
- **`sql.ErrConnDone`** is returned after `Close`.
- **Contexts:** a context's deadline becomes the deadline of the socket.
  Canceling it interrupts the statement and breaks the connection; no
  `KILL QUERY` is sent, as in pgx.
- **Transactions:** the context of `BeginTx` applies until `Commit` or
  `Rollback`. When it is done the socket is closed, so the server rolls the
  transaction back at once.
- **`Config.RejectReadOnly`:** after a failover the old primary turns
  read-only. With this option, a statement it refuses breaks the connection,
  and a new connection reaches the new primary. Through `database/sql` the
  statement is retried there. A `CALL` refused in the middle may have run in
  part.
- **No automatic retries.** gomysql never retries a statement that was sent.

## Sessions

- **`Config.InitCommands`** run after connecting, `Reconnect`,
  `ResetConnection` and `ChangeUser`. If one fails, the connection fails.
- **`ResetConnection`** (`COM_RESET_CONNECTION`) resets the session without
  reconnecting. Afterwards it sets the collation of `Config` again, because
  MySQL resets the connection's character set to the server's (`latin1` on
  5.7).
- **`ChangeUser`** authenticates as another user on the same connection.
- **`Config.Collation`** is set in the handshake, which allows only collation
  ids below 256. A server that does not know the collation falls back to its
  default without an error.
- **Stored procedures:** `Rows.NextResultSet` moves to the next `SELECT` of a
  `CALL`. Multi-statement queries (`a; b`) are not supported.

## LOAD DATA

```go
res, err := conn.LoadData("LOAD DATA LOCAL INFILE 'rows.csv' INTO TABLE t FIELDS TERMINATED BY ','", reader)
```

- **Source:** the file is read from the `io.Reader`; its name in the statement
  does not matter. Nothing is read from the disk, and a server that asks for a
  file outside `LoadData` gets an empty one.
- **Server setting:** the server must allow it with `local_infile=ON`, which
  is off by default since MySQL 8.0.
- **Errors:** if the reader fails, the connection is closed so that the server
  aborts the statement. An InnoDB table then gets none of the rows.

## Authentication and TLS

- **Plugins:** `caching_sha2_password` (the default since MySQL 8.0),
  `mysql_native_password`, `sha256_password` and `mysql_clear_password` are
  supported. More can be added with `Config.AuthPlugins`.
- **Old passwords:** `mysql_old_password` is refused. Its hash is broken, and
  a server or a man in the middle could ask for it to get the password.
- **Full authentication without TLS.** After a server restart or
  `FLUSH PRIVILEGES`, `caching_sha2_password` needs a full authentication:
  - with TLS or a unix socket, no key is needed;
  - without TLS it needs the server's RSA key: either `Config.ServerPublicKey`,
    or `Config.AllowPublicKeyRetrieval`, which asks the server for it. A man
    in the middle can then send his own key and read the password.
- **TLS** is set with `Config.TLS`. It is required once set; there is no
  fallback to plain text. Without a `ServerName` the host of `Address` is
  verified. `InsecureSkipVerify` exposes the password to a man in the middle.
- **Clear text:** `mysql_clear_password` (PAM, LDAP) sends the password only
  over TLS or a unix socket, unless `Config.AllowCleartextPasswords` is set.

## Performance

Scanning 1M rows on MySQL 8.4, CPU per row on an Apple M3 Pro. The column
names are what the benchmarks scan.

| | gomysql | sqldriver (Go 1.27) | go-sql-driver, text | go-sql-driver, prepared |
|---|---|---|---|---|
| integers | 48 ns | 107 ns | 209 ns | 190 ns |
| `sql.RawBytes` | 100 ns | 152 ns | 272 ns | 244 ns |
| strings | 119 ns | 169 ns | 294 ns | 258 ns |

Allocations for the same 1M rows:

| | gomysql | sqldriver | go-sql-driver |
|---|---|---|---|
| integers | 1 for the whole query | 14 | 2.3M |
| strings | one per string | one per string | 5M |

A point query with an argument allocates 528 bytes in 7 allocations through
gomysql. Through `sqldriver` it takes 782 bytes in 14, against 864 bytes in
23 for go-sql-driver. Benchmarks against go-sql-driver live in the `bench`
module.

## Compatibility

- **Servers:** MySQL 5.7, 8.0, 8.4 and 9.x, Percona Server 5.7 and 8.4, and
  MariaDB 10.6 and 11.4. Every change is tested against all of them.
- **Go:** 1.24 and later. On Go 1.27 the `database/sql` driver scans directly
  through `driver.RowsColumnScanner`.
- **Not supported:**
  - multi-statement queries;
  - cursors (`COM_STMT_FETCH`);
  - long data (`COM_STMT_SEND_LONG_DATA`);
  - the replication protocol;
  - a check of idle connections with `MSG_PEEK` (the pool pings instead).

## License

Apache License 2.0, see [LICENSE](LICENSE).
