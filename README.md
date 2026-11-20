# gomysql

[![Go Reference](https://pkg.go.dev/badge/github.com/darxnet/gomysql.svg)](https://pkg.go.dev/github.com/darxnet/gomysql)
[![Go](https://github.com/darxnet/gomysql/actions/workflows/release.yml/badge.svg)](https://github.com/darxnet/gomysql/actions/workflows/release.yml)
[![Coverage](https://codecov.io/gh/darxnet/gomysql/graph/badge.svg)](https://codecov.io/gh/darxnet/gomysql)

`gomysql` is a MySQL client for Go built for bulk reads: it streams result
rows without per-row allocations and uses 2–4× less client CPU than
`database/sql` with `go-sql-driver/mysql`.

It is not related to
[go-mysql-org/go-mysql](https://github.com/go-mysql-org/go-mysql), a toolkit
for the replication protocol.

**Status: work in progress.** The API is being aligned with `database/sql`
and is not stable until v1.

## Usage

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

stmt, err := conn.Prepare("SELECT id, name FROM t")
if err != nil {
	return err
}
rows, err := stmt.Query()
if err != nil {
	return err
}
defer rows.Close()

var (
	id   uint64
	name string
)
for rows.Next() {
	if err := rows.Scan(&id, &name); err != nil {
		return err
	}
}
return rows.Err()
```

## License

Apache License 2.0, see [LICENSE](LICENSE).
