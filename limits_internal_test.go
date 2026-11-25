package gomysql

import (
	"context"
	"os"
	"testing"
)

func TestServerLimitsLive(t *testing.T) {
	t.Parallel()

	addr := os.Getenv("GOMYSQL_ADDR")
	if addr == "" {
		t.Skip("GOMYSQL_ADDR not set")
	}

	conn, err := Connect(Config{
		Network: "tcp", Address: addr, User: "b", Password: "b", AllowPublicKeyRetrieval: true,
		InitCommands: []string{"SET SESSION wait_timeout = 1234"},
	})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = conn.Close() }()

	l, err := conn.serverLimits(context.Background())
	if err != nil {
		t.Fatal(err)
	}

	// wait_timeout of the session, after the init commands.
	if l.maxAllowedPacket < 1024 || l.maxPreparedStmtCount <= 0 || l.maxConnections <= 0 ||
		l.waitTimeout.Seconds() != 1234 {
		t.Errorf("%s: %+v", conn.server, *l)
	}

	if err := conn.ResetConnection(); err != nil || conn.limits.loaded {
		t.Errorf("limits kept after a reset: %v", err)
	}
}
