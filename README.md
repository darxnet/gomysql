# gomysql

[![Go Reference](https://pkg.go.dev/badge/github.com/darxnet/gomysql.svg)](https://pkg.go.dev/github.com/darxnet/gomysql)
[![Go](https://github.com/darxnet/gomysql/actions/workflows/release.yml/badge.svg)](https://github.com/darxnet/gomysql/actions/workflows/release.yml)
[![Coverage](https://codecov.io/gh/darxnet/gomysql/graph/badge.svg)](https://codecov.io/gh/darxnet/gomysql)

A MySQL and MariaDB client for Go, made for programs that read a lot.

- Reads rows without allocating memory for each one.
- Uses 2–4× less CPU than `database/sql` with `go-sql-driver/mysql`.
- Has an API that looks like `database/sql`, and also works as a
  `database/sql` driver.

Not to be confused with
[go-mysql-org/go-mysql](https://github.com/go-mysql-org/go-mysql), a toolkit
for MySQL replication.

**Status:** the API may still change before v1.

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

A `Conn` is a single connection. Use it from one goroutine at a time. While a
query's `Rows` are open, other calls on the same connection return `ErrBusy`
(nothing is sent to the server). Read the rows to the end or close them first.

## The pool

`Open` creates a `DB`: a pool of connections that many goroutines can share.
Like `sql.Open`, it does not connect until a connection is needed.

```go
db, err := gomysql.Open(config)
if err != nil {
	return err
}
defer db.Close()

conn, err := db.Conn(ctx) // waits (within ctx) if all connections are busy
if err != nil {
	return err
}
defer conn.Close() // gives the connection back to the pool
```

`DB` has no `Query` or `Exec`. Take a connection with `db.Conn`, use it, then
`Close` it. Don't touch it after `Close`: another goroutine may already be
using it. To keep a connection for yourself, call `conn.Hijack()`. It leaves
the pool, and its `Close` really closes it.

The settings are the same as in `database/sql`, with different defaults:

| Setting | Default |
|---|---|
| `SetMaxOpenConns` | max(4, GOMAXPROCS), but always 5 below the account's connection limit, so that admins can still log in |
| `SetMaxIdleConns` | no limit (`database/sql` keeps only 2, so under load it keeps opening and closing connections) |
| `SetConnMaxIdleTime` | about a minute before the server's `wait_timeout` would close the connection; no limit if the server doesn't report it |
| `SetConnMaxLifetime` | 30 minutes, minus a random 0–10% so that connections don't all close at once |

**A returned connection keeps its session.** Session variables, user
variables and temporary tables stay for the next user, as in `database/sql`.
The pool doesn't reset the session, because that would also drop the
connection's prepared statements. Instead it closes a returned connection
that:

- is broken;
- still has open `Rows`;
- is inside a transaction (even one started with a plain
  `START TRANSACTION` query);
- was switched to another user or database;
- is too old, or more connections are open than the limit allows.

**Dead connections.** A connection that sat idle for more than a second is
pinged before it's handed out, and replaced if it doesn't answer. Load
balancers that drop idle TCP connections (Azure after 4 minutes, AWS NLB after
350 s) are not a problem: Go sends TCP keepalives every 15 s. Proxies with
their own idle timeout (RDS Proxy: 30 minutes, TiDB Cloud: 340 s) are: set
`SetConnMaxIdleTime` below their timeout.

## database/sql

```go
import _ "github.com/darxnet/gomysql/sqldriver"

db, err := sql.Open("gomysql", "user:password@tcp(127.0.0.1:3306)/db?loc=Local")

// or, for settings a DSN can't hold (TLS, InitCommands, keys):
db := sql.OpenDB(sqldriver.NewConnector(config))
```

The DSN uses the go-sql-driver format. These parameters are supported:

- `tls` (`true`, `skip-verify` or `false`)
- `collation` and `charset`
- `loc`
- `timeout` (for connecting)
- `compress`
- `clientFoundRows`
- `allowCleartextPasswords`
- `allowPublicKeyRetrieval`
- `rejectReadOnly`
- `connectionAttributes`

Other go-sql-driver parameters, such as `parseTime`, `multiStatements` or
`readTimeout`, are an error that says why. This way a DSN never silently asks
for something gomysql doesn't do.

Any other parameter sets a session variable on connect:
`time_zone=%27%2B00:00%27` runs `SET time_zone='+00:00'`. The value is
inserted into SQL as is, so never build it from user input. Write a `/` in a
value or in the database name as `%2F`.

The driver works like `Conn`:

- **Arguments** are inserted into the query text on the client (see
  [Arguments](#arguments)).
- **`Prepare`** creates a real prepared statement on the server.
- **Values** come as numbers (`int64`, `uint64`, `float32`, `float64`) and
  `time.Time` for dates. Text, JSON, `DECIMAL`, `TIME` and binary strings come
  as bytes: `sql.RawBytes` gets them without copying, `string` and `[]byte`
  with one copy.
- **Retries.** `database/sql` retries a statement when the driver returns
  `driver.ErrBadConn`. gomysql returns it only when it's sure the statement
  didn't run: the connection was already broken, or (with `rejectReadOnly`) a
  read-only server refused the statement.
- **Stored procedures.** `rows.NextResultSet()` moves to the next result of a
  `CALL`.

`sqldriver.Raw` gives you the `*gomysql.Conn` behind a `*sql.Conn`, to use
`LoadData`, `ScanStruct` and the rest of the native API:

```go
err := sqldriver.Raw(sqlConn, func(conn *gomysql.Conn) error {
	_, err := conn.LoadData("LOAD DATA LOCAL INFILE 'x' INTO TABLE t", r)
	return err
})
```

On Go 1.27 and later the driver scans straight into your variables
(`driver.RowsColumnScanner`). That makes it about twice as fast as
go-sql-driver, with no allocations per row for numbers and `sql.RawBytes`
(see [Performance](#performance)).

## Arguments

`Query`, `Exec` and `QueryRow` put the arguments into the query text on the
client and send it as one statement: one round trip, no prepared statement.

```go
conn.Query("SELECT * FROM users WHERE id = ?", 5)
conn.Query("SELECT * FROM users WHERE id = :id", sql.Named("id", 5))
conn.Query("SELECT * FROM users WHERE id IN (?)", []int{1, 2, 3})
```

**Placeholders**

- Use `?` or `:name`, but not both in one query.
- Named arguments come from `sql.Named`, from one struct (`:user_id` finds the
  field `UserID`, as in [Structs](#structs)), or from one `map[string]any`.
- A `?` or `:name` inside a string, a quoted name or a comment is just text.
- A `:` right after a name or another `:` is text too, so `lbl:LOOP` and
  `@a:=1` work. (This also means `id:id` is sent as is, and the server
  reports the error.)
- MySQL runs the text inside `/*!80000 … */` only on version 8.0.0 or newer.
  The driver doesn't check the version, so a `?` inside is always a
  placeholder.
- A query without arguments is sent unchanged.

**Slices** (except `[]byte`) become lists: `IN (?)` with `[]int{1, 2}` becomes
`IN (1, 2)`. An empty slice is an error. Slices don't work in prepared
statements.

**Types** are converted as in `database/sql`. A `driver.Valuer` gives its
value, a pointer gives what it points to (nil is `NULL`), and a named type
such as `type ID int64` counts as its base type. Any other type is an error,
never a silent `fmt.Sprint`.

**How values are written**

| Go value | In SQL |
|---|---|
| `string`, `json.RawMessage` | quoted and escaped string |
| `[]byte` | `_binary'…'` |
| `bool` | `1` or `0` |
| floats | shortest exact form; a `float32` keeps its own digits (`0.1`, not `0.10000000149011612`); NaN and infinity are an error |
| `time.Time` | in `Config.Location`, up to 6 decimal places; the zero time is `'0000-00-00'` |
| `time.Duration` | number of nanoseconds: `5*time.Second` is `5000000000` |

`time.Duration` is not a `TIME` value. Written into a `TIME` column, it is
an error or a wrong value. Pass `TIME` values as strings: `"12:34:56"`.

**Escaping safety**

- If the server runs with `NO_BACKSLASH_ESCAPES`, strings are escaped to
  match, also after a `SET sql_mode`.
- With the character sets big5, cp932, gbk, gb18030 and sjis, a multibyte
  character can hide a backslash, so escaping is not safe there. String
  arguments are refused; use `Prepare` instead.

## Scanning values

`Scan` works like in `database/sql`. It fills strings, `[]byte`,
`sql.RawBytes`, `any`, `bool`, integers, floats, `time.Time`, any
`sql.Scanner` (such as `sql.NullString`), pointers to pointers (nil for
`NULL`), and types based on these.

What may surprise you:

- **`NULL`** fits only into `[]byte`, `sql.RawBytes`, `any`, a pointer to a
  pointer or an `sql.Scanner`. Anything else returns an error, as in
  `database/sql`.
- **Numbers that don't fit** are an error: 300 won't go into an `int8`.
- **`any` and `sql.Scanner`** get the same types in both protocols:

  | Column | Go type |
  |---|---|
  | integers | `int64` (`BIGINT UNSIGNED` and `BIT`: `uint64`) |
  | `FLOAT`, `DOUBLE` | `float32`, `float64` |
  | dates | `time.Time` |
  | JSON | `json.RawMessage` (MariaDB sends JSON as text, so `string`) |
  | text | `string` |
  | binary strings, `DECIMAL`, `TIME` | `[]byte` |

- **Dates** (`DATE`, `DATETIME`, `TIMESTAMP`) become `time.Time` in
  `Config.Location` (UTC by default). The zero date becomes the zero
  `time.Time`. Scanned into a string, a date keeps the server's format.
- **`TIME`** goes only into a string or `[]byte`, as `"-12:34:56.5"`, not
  into `time.Duration` (see the note in [Arguments](#arguments)).
  `time.Duration` accepts integers, as nanoseconds.
- **`BIT`** (up to 64 bits) goes into integers and `bool`.
- **JSON and text** go into a pointer to a struct, map, array or slice through
  `json.Unmarshal`.
- **Copies.** `[]byte` gets its own copy. `sql.RawBytes` points into the
  driver's buffer and is valid only until the next `Next`, `Scan` or `Close`.

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
users, err = gomysql.AppendStructs(users[:0])(conn.Query(q)) // reuses the slice
```

Why `AppendStructs(dst)(conn.Query(q))` and not
`AppendStructs(dst, conn.Query(q))`? Go allows a call with two results only as
the sole argument of another call.

`rows.ScanStruct(&v)` fills a struct from the current row:

- **Column → field.** The `db` tag wins. Without a tag, the column name is
  matched to the field name exactly, then ignoring case, then ignoring
  underscores: `user_id` goes into `UserID`. `db:"-"` skips a field.
- **Embedded structs** count as part of the outer struct. A nil pointer to an
  exported embedded struct is allocated; a nil pointer to an unexported one is
  an error.
- **Every column needs a field**, or it's an error. Fields without a column
  are left alone, so one struct can serve several queries.
- **Speed.** Scanning row after row into the same struct is as fast as
  `Scan`.
- **Your own logic.** A type that implements `RowScanner` scans itself.

## Prepared statements

`Prepare` creates a prepared statement on the server. Each connection keeps
its statements in a cache:

- **Reuse.** Preparing the same text again returns the cached `*Stmt`, without
  asking the server.
- **`Stmt.Close` does nothing.** The statement stays in the cache.
- **Size.** The cache holds max(64, `max_prepared_stmt_count` /
  `max_connections`) statements and drops the least recently used. If the
  server says it has too many statements in total (error 1461), the cache
  drops half of its own and stays smaller.
- **Automatic re-prepare.** If a statement was dropped from the cache, lost
  in a reconnect, or the server no longer knows it (for example after an
  Aurora failover), it is prepared again on the next use.
- **`UseDatabase`** clears the cache, because a statement stays tied to the
  database it was prepared in. A `USE db` query doesn't.

**MySQL 8 and parameter types.** Since 8.0.22, MySQL remembers the type of
each parameter from the first run of a statement, and converts later
arguments to it. Pass a string where an integer was before and you get `0`;
anything after a `DATETIME` becomes a zero date; a large integer after a
`DOUBLE` gets rounded. Oracle says this is by design and advises a separate
statement for each set of types (bugs
[#102942](https://bugs.mysql.com/bug.php?id=102942),
[#113240](https://bugs.mysql.com/bug.php?id=113240)).

gomysql does that for you: when an argument changes its kind (integer,
float, string or date; `NULL` doesn't count), the statement is prepared
again. Mixing types works, at the cost of one extra round trip on each
change.

## Errors, contexts and transactions

- **`*gomysql.Error`** is an error from the server, with its number, SQLSTATE
  and message: `errors.Is(err, &gomysql.Error{Number: 1062})` checks for a
  duplicate key.
- **`ErrBadConn`** means the connection can't be used any more: a network
  error, a broken packet, a canceled context, or a server error that closes
  the connection. The cause is wrapped. Only `Reconnect` and `Close` work
  after it. The statement may or may not have run.
- **`ErrBusy`**: the `Rows` of an earlier query are still open. Nothing was
  sent.
- **`sql.ErrConnDone`**: the connection is closed.
- **Contexts.** The context's deadline becomes the socket's deadline.
  Canceling the context stops the statement and breaks the connection (no
  `KILL QUERY` is sent, as in pgx).
- **Transactions.** The context of `BeginTx` applies until `Commit` or
  `Rollback`. When it ends, the socket is closed, so the server rolls the
  transaction back right away.
- **`Config.RejectReadOnly`.** After a failover the old primary becomes
  read-only. With this option, a statement it refuses breaks the connection,
  so the next connection goes to the new primary, and `database/sql` retries
  the statement there. Careful with `CALL`: a procedure may have run in part
  before it was refused.
- **No automatic retries.** gomysql never resends a statement that was
  already sent.

## Sessions

- **`Config.InitCommands`** run after connecting, `Reconnect`,
  `ResetConnection` and `ChangeUser`. If one fails, so does the connection.
- **`ResetConnection`** resets the session without reconnecting, then sets
  the `Config` collation again (MySQL resets it to the server's default,
  `latin1` on 5.7).
- **`ChangeUser`** logs in as another user on the same connection.
- **`Config.Collation`** is sent during the handshake, which only allows ids
  below 256. A server that doesn't know the collation silently uses its
  default.
- **Stored procedures.** `Rows.NextResultSet` moves to the next `SELECT` of a
  `CALL`. Several statements in one query (`a; b`) are not supported.

## LOAD DATA

```go
res, err := conn.LoadData("LOAD DATA LOCAL INFILE 'rows.csv' INTO TABLE t FIELDS TERMINATED BY ','", reader)
```

- The data comes from `reader`; the file name in the query doesn't matter.
  Nothing is read from disk. If the server asks for a file outside `LoadData`,
  it gets an empty one.
- The server must have `local_infile=ON` (off by default since MySQL 8.0).
- If `reader` returns an error, the connection is closed so the server
  aborts the load. An InnoDB table then gets none of the rows.

## Authentication and TLS

- **Supported:** `caching_sha2_password` (the default since MySQL 8.0),
  `mysql_native_password`, `sha256_password` and `mysql_clear_password`.
  Add your own with `Config.AuthPlugins`.
- **`mysql_old_password` is refused.** Its hash is broken, and a fake server
  could ask for it to steal the password.
- **`caching_sha2_password` without TLS.** After a server restart or
  `FLUSH PRIVILEGES`, the server asks for the full password once:
  - over TLS or a unix socket, nothing else is needed;
  - otherwise the password is encrypted with the server's RSA key. Give the
    key in `Config.ServerPublicKey`, or set `Config.AllowPublicKeyRetrieval`
    to fetch it from the server. The second is easier but unsafe: a man in
    the middle can send his own key and read the password.
- **TLS** is turned on with `Config.TLS`, and then it's required: there's no
  fallback to plain text. Without a `ServerName`, the host from `Address` is
  verified. `InsecureSkipVerify` exposes the password to a man in the middle.
- **Clear-text passwords** (`mysql_clear_password`, for PAM or LDAP) are sent
  only over TLS or a unix socket, unless you set
  `Config.AllowCleartextPasswords`.

## Performance

CPU time per row, reading 1M rows from MySQL 8.4.11 in Docker, against
go-sql-driver/mysql v1.10.2 (Apple M3 Pro, Go 1.27.2). Rows show what the
columns are scanned into.

| | gomysql | sqldriver (Go 1.27), text | sqldriver (Go 1.27), prepared | go-sql-driver, text | go-sql-driver, prepared |
|---|---|---|---|---|---|
| integers | 48 ns | 110 ns | 96 ns | 209 ns | 190 ns |
| `sql.RawBytes` | 100 ns | 161 ns | 148 ns | 278 ns | 250 ns |
| strings | 115 ns | 185 ns | 169 ns | 301 ns | 266 ns |

Allocations for the same 1M rows:

| | gomysql | sqldriver | go-sql-driver |
|---|---|---|---|
| integers | 1 for the whole query | 12 (prepared: 19) | 2.3M (prepared: 3.3M) |
| strings | one per string | one per string | 5M |

A query for a single row by id:

| | gomysql | sqldriver | go-sql-driver |
|---|---|---|---|
| memory | 528 B | 782 B | 864 B |
| allocations | 7 | 14 | 23 |

The comparison benchmarks are in the [bench](bench) module. Start a server
with `scripts/testdb.sh mysql:8.4 3306`, then run
`GOMYSQL_ADDR=127.0.0.1:3306 go test -C bench -run '^$' -bench . -benchmem`.

## Compatibility

- **Servers:** MySQL 5.7, 8.0, 8.4 and 9.x, Percona Server 5.7 and 8.4,
  MariaDB 10.6 and 11.4. Every change is tested on all of them.
- **Go:** `go.mod` requires 1.24; tested on the two latest Go releases.
- **Not supported:**
  - several statements in one query (`a; b`);
  - server-side cursors (`COM_STMT_FETCH`): rows are already streamed, so
    they would only add round trips;
  - sending one argument in chunks (`COM_STMT_SEND_LONG_DATA`); use
    `LOAD DATA` for large data;
  - the replication protocol (see go-mysql-org/go-mysql);
  - checking idle connections with `MSG_PEEK`; the pool pings instead.

## License

Apache License 2.0, see [LICENSE](LICENSE).
