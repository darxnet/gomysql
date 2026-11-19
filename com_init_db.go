package gomysql

const comInitDB byte = 0x02

// UseDB makes schema the default database (COM_INIT_DB).
func (conn *Conn) UseDB(schema string) error {
	return simpleCommand(conn, comInitDB, schema)
}
