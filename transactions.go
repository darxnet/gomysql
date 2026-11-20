package gomysql

// Tx is a transaction: the commands of the connection between Begin and
// Commit or Rollback.
type Tx struct {
	conn *Conn
}

// Begin starts a transaction (START TRANSACTION).
func (conn *Conn) Begin() (*Tx, error) {
	if _, err := conn.Exec("START TRANSACTION"); err != nil {
		return nil, err
	}

	return &Tx{conn: conn}, nil
}

// Commit commits the transaction.
func (tx *Tx) Commit() error {
	_, err := tx.conn.Exec("COMMIT")

	return err
}

// Rollback rolls the transaction back.
func (tx *Tx) Rollback() error {
	_, err := tx.conn.Exec("ROLLBACK")

	return err
}

// Exec is Conn.Exec in the transaction.
func (tx *Tx) Exec(query string, args ...any) (*Result, error) {
	return tx.conn.Exec(query, args...)
}

// Prepare is Conn.Prepare in the transaction.
func (tx *Tx) Prepare(query string) (*Stmt, error) {
	return tx.conn.Prepare(query)
}

// Query is Conn.Query in the transaction.
func (tx *Tx) Query(query string, args ...any) (*Rows, error) {
	return tx.conn.Query(query, args...)
}

// QueryRow is Conn.QueryRow in the transaction.
func (tx *Tx) QueryRow(query string, args ...any) *Row {
	return tx.conn.QueryRow(query, args...)
}
