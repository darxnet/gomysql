package gomysql

// Open connects and authenticates. On failure it returns a nil Conn and
// leaves no socket open.
func Open(config Config) (*Conn, error) { //nolint:gocritic // hugeParam: once per connection
	conn := &Conn{config: config}

	if err := conn.Conn(); err != nil {
		return nil, err
	}

	return conn, nil
}
