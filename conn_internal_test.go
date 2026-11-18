package gomysql

import (
	"bytes"
	"strconv"
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

func BenchmarkConvStringMain(b *testing.B) {
	b.ReportAllocs()
	for range b.N {
		if strconv.FormatUint(uint64(3), 10) != "3" {
			b.Fatal()
		}
		if strconv.FormatUint(uint64(23), 10) != "23" {
			b.Fatal()
		}
		if strconv.FormatUint(uint64(123), 10) != "123" {
			b.Fatal()
		}
	}
}

func BenchmarkConvStringCC(b *testing.B) {
	b.ReportAllocs()
	for range b.N {
		if b2string(3) != "3" {
			b.Fatal()
		}
		if b2string(23) != "23" {
			b.Fatal()
		}
		if b2string(123) != "123" {
			b.Fatal()
		}
	}
}

func BenchmarkConvHexMain(b *testing.B) {
	b.ReportAllocs()
	for range b.N {
		if strconv.FormatUint(uint64(3), 16) != "3" {
			b.Fatal()
		}
		if strconv.FormatUint(uint64(23), 16) != "17" {
			b.Fatal()
		}
		if strconv.FormatUint(uint64(123), 16) != "7b" {
			b.Fatal()
		}
	}
}

func BenchmarkConvHexCC(b *testing.B) {
	b.ReportAllocs()
	for range b.N {
		if b2hex(3) != "0x03" {
			b.Fatal()
		}
		if b2hex(23) != "0x17" {
			b.Fatal()
		}
		if b2hex(123) != "0x7b" {
			b.Fatal()
		}
	}
}
