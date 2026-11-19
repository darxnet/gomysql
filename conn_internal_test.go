package gomysql

import (
	"bytes"
	"testing"
)

func TestGetAuthResponse(t *testing.T) {
	t.Parallel()

	client := new(Conn)
	client.config = Config{User: "root", Password: "secret"}

	var authPluginName, authPluginData []byte
	authPluginName = []byte("mysql_native_password")
	authPluginData = []byte{70, 114, 92, 94, 1, 38, 11, 116, 63, 114, 23, 101, 126,
		103, 26, 95, 81, 17, 24, 21}
	exp := []byte{53, 177, 140, 159, 251, 189, 127, 53, 109, 252,
		172, 50, 211, 192, 240, 164, 26, 48, 207, 45}

	res, err := client.getAuthResponse(authPluginName, authPluginData)
	if err != nil {
		t.Fatal(err)
	}

	if !bytes.Equal(exp, res) {
		t.Fatalf("unexpected authResponse\n%v\n%v", exp, res)
	}
}
