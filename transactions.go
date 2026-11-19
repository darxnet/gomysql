package gomysql

type Tx struct {
	conn *Conn
}

func (conn *Conn) Begin() (tx *Tx, err error) {
	_, err = conn.Exec("START TRANSACTION")
	if err != nil {
		return tx, err
	}

	tx = new(Tx)
	tx.conn = conn

	return tx, err
}

func (tx *Tx) Commit() (err error) {
	_, err = tx.conn.Exec("COMMIT")
	return err
}

func (tx *Tx) Rollback() (err error) {
	_, err = tx.conn.Exec("ROLLBACK")
	return err
}

func (tx *Tx) Exec(query string, args ...any) (result *Result, err error) {
	return tx.conn.Exec(query, args)
}

func (tx *Tx) Prepare(query string) (stmt *Stmt, err error) {
	return tx.conn.Prepare(query)
}

func (tx *Tx) Query(query string, args ...any) (rows *Rows, err error) {
	return tx.conn.Query(query, args...)
}

func (tx *Tx) QueryRow(query string, args ...any) (row *Row) {
	return tx.conn.QueryRow(query, args)
}
