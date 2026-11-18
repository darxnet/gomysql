module github.com/darxnet/gomysql/bench

go 1.26

require (
	github.com/darxnet/gomysql v0.0.0
	github.com/go-sql-driver/mysql v1.10.2
)

require filippo.io/edwards25519 v1.2.0 // indirect

replace github.com/darxnet/gomysql => ../
