package gomysql

import (
	"context"
	"fmt"
)

const comResetConnection byte = 0x1f

// ResetConnection resets the session (COM_RESET_CONNECTION, MySQL 5.7.3+,
// MariaDB 10.2.4+) without a new connection or authentication: it rolls
// back the transaction, drops temporary tables, user variables and
// prepared statements, and resets session variables. Statements prepared
// before fail afterwards. Config.InitCommands run again.
func (conn *Conn) ResetConnection() error {
	return conn.ResetConnectionContext(context.Background())
}

// ResetConnectionContext is ResetConnection within ctx.
func (conn *Conn) ResetConnectionContext(ctx context.Context) error {
	if err := conn.begin(ctx); err != nil {
		return err
	}

	err := conn.simpleCommand(comResetConnection, "")
	if err == nil {
		conn.newSession()

		// MySQL resets the character set and collation of the connection
		// to the global ones too: latin1 on 5.7, utf8mb4_0900_ai_ci on 8.0.
		err = conn.exec(conn.config.setNames())
		if err != nil {
			conn.breakWith(err)
		} else {
			err = conn.initSession()
		}
	}

	return conn.end(err)
}

// exec runs a statement within the current command.
func (conn *Conn) exec(query string) error {
	if err := writeCommand(conn, comQuery, query); err != nil {
		return err
	}

	_, err := conn.readResult()

	return err
}

// newSession forgets what belonged to the session before: its prepared
// statements and transaction.
func (conn *Conn) newSession() {
	conn.session++
	conn.endTx()
	conn.limits = serverLimits{}
}

// initSession runs Config.InitCommands. A failure breaks the connection:
// the session is not what the configuration asks for.
func (conn *Conn) initSession() error {
	for _, command := range conn.config.InitCommands {
		if err := conn.exec(command); err != nil {
			err = fmt.Errorf("gomysql: init command %q: %w", command, err)
			conn.breakWith(err)

			return err
		}
	}

	return nil
}

const comChangeUser byte = 0x11

// ChangeUser authenticates as another user on the same connection
// (COM_CHANGE_USER) and makes database the default one; the session is
// reset as by ResetConnection, and Config.InitCommands run again. If it
// fails, the connection is broken; Reconnect connects as the previous
// user.
func (conn *Conn) ChangeUser(user, password, database string) error {
	return conn.ChangeUserContext(context.Background(), user, password, database)
}

// ChangeUserContext is ChangeUser within ctx.
func (conn *Conn) ChangeUserContext(ctx context.Context, user, password, database string) error {
	if err := conn.begin(ctx); err != nil {
		return err
	}

	old := conn.config
	conn.config.User, conn.config.Password, conn.config.Database = user, password, database

	err := conn.changeUser()
	if err == nil {
		conn.database = database
		conn.newSession()
		err = conn.initSession()
	}

	if err != nil {
		conn.config.User, conn.config.Password, conn.config.Database = old.User, old.Password, old.Database
		conn.breakWith(err)
	}

	return conn.end(err)
}

// changeUser sends COM_CHANGE_USER with the answer of the handshake's
// plugin to its nonce, and runs the exchange that follows.
//
// https://dev.mysql.com/doc/dev/mysql-server/latest/page_protocol_com_change_user.html
func (conn *Conn) changeUser() error {
	a, plugin := conn.newAuth(conn.authPlugin, conn.nonce)

	response, err := plugin.Respond(a, conn.nonce)
	if err != nil {
		return err
	}

	if len(response) > 255 {
		return fmt.Errorf("gomysql: ChangeUser: an auth response of %d bytes", len(response))
	}

	collation, err := conn.config.collationID()
	if err != nil {
		return err
	}

	buf := append([]byte(conn.config.User), 0)
	buf = append(buf, byte(len(response))) //nolint:gosec // G115: at most 255
	buf = append(buf, response...)
	buf = append(buf, conn.config.Database...)
	buf = append(buf, 0, collation, 0) // character set, 2 bytes
	buf = append(buf, plugin.Name()...)
	buf = append(buf, 0)

	if conn.capFlags&clientConnectAttrs != 0 {
		buf = appendConnectAttrs(buf, conn.config.ConnectAttrs)
	}

	if err := writeCommand(conn, comChangeUser, buf); err != nil {
		return err
	}

	return conn.authExchange(a, plugin, conn.nonce)
}
