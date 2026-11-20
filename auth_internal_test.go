package gomysql

import (
	"bytes"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha1" //nolint:gosec // as the server
	"crypto/x509"
	"encoding/hex"
	"encoding/pem"
	"errors"
	"strings"
	"testing"
)

func TestGetAuthResponse(t *testing.T) {
	t.Parallel()

	authPluginData := []byte{70, 114, 92, 94, 1, 38, 11, 116, 63, 114, 23, 101, 126,
		103, 26, 95, 81, 17, 24, 21}
	exp := []byte{53, 177, 140, 159, 251, 189, 127, 53, 109, 252,
		172, 50, 211, 192, 240, 164, 26, 48, 207, 45}

	res, err := nativePassword{}.Respond(&Auth{Password: "secret", Config: &Config{}}, authPluginData)
	if err != nil {
		t.Fatal(err)
	}

	if !bytes.Equal(exp, res) {
		t.Fatalf("unexpected authResponse\n%v\n%v", exp, res)
	}
}

func TestScrambleSHA256(t *testing.T) {
	t.Parallel()

	// The vectors of go-sql-driver.
	nonce := []byte{10, 47, 74, 111, 75, 73, 34, 48, 88, 76, 114, 74, 37, 13, 3, 80, 82, 2, 23, 21}

	for pass, want := range map[string]string{ //nolint:gosec // G101: test vectors
		"secret":  "f490e76f66d9d86665ce54d98c78d0acfe2fb0b08b423da807144873d30b312c",
		"secret2": "abc3934a012cf342e876071c8ee202de51785b430258a7a0138bc79c4d800bc6",
	} {
		if got := hex.EncodeToString(scrambleSHA256(pass, nonce)); got != want {
			t.Errorf("%q: %s, want %s", pass, got, want)
		}
	}
}

// fakeNonce is the nonce of handshakeV10, without its NUL.
const fakeNonce = "abcdefghijklmnopqrst"

// authOf returns the auth response and plugin name of a
// HandshakeResponse41 with CLIENT_PLUGIN_AUTH and a database.
func authOf(t *testing.T, pkt []byte) (response []byte, plugin string) {
	t.Helper()

	rest := pkt[32:]
	rest = rest[bytes.IndexByte(rest, 0)+1:] // user
	n := int(rest[0])
	response, rest = rest[1:1+n], rest[1+n:]
	rest = rest[bytes.IndexByte(rest, 0)+1:] // database

	return response, string(rest[:bytes.IndexByte(rest, 0)])
}

// authServer runs script after the handshake of plugin and the client's
// response, which it checks against want unless nil.
func authServer(t *testing.T, plugin string, want []byte, script func(c *fakeConn) error) Config {
	t.Helper()

	return rawServer(t, func(c *fakeConn) error {
		if err := c.write(handshakeV10(fakeCapabilities, plugin)); err != nil {
			return err
		}

		pkt, err := c.read()
		if err != nil {
			return err
		}

		if got, _ := authOf(t, pkt); want != nil && !bytes.Equal(got, want) {
			t.Errorf("handshake response %x, want %x", got, want)
		}

		return script(c)
	})
}

// expect reads a packet and checks it.
func expect(t *testing.T, c *fakeConn, want []byte) error {
	t.Helper()

	got, err := c.read()
	if err == nil && !bytes.Equal(got, want) {
		t.Errorf("client sent %q, want %q", got, want)
	}

	return err
}

//nolint:gocognit // a table of server scripts
func TestAuthCachingSHA2(t *testing.T) {
	t.Parallel()

	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}

	der, err := x509.MarshalPKIXPublicKey(&key.PublicKey)
	if err != nil {
		t.Fatal(err)
	}

	pemKey := pem.EncodeToMemory(&pem.Block{Type: "PUBLIC KEY", Bytes: der})
	scramble := scrambleSHA256("p", []byte(fakeNonce))

	// decrypt checks the encrypted password.
	decrypt := func(c *fakeConn) error {
		enc, err := c.read()
		if err != nil {
			return err
		}

		plain, err := rsa.DecryptOAEP(sha1.New(), nil, key, enc, nil) //nolint:gosec // as the server
		if err != nil {
			t.Errorf("decrypt: %v", err)
		}

		for i := range plain {
			plain[i] ^= fakeNonce[i%len(fakeNonce)]
		}

		if string(plain) != "p\x00" {
			t.Errorf("password %q", plain)
		}

		return c.ok()
	}

	for _, tt := range []struct {
		name    string
		cfg     func(*Config)
		script  func(c *fakeConn) error
		wantErr error
	}{
		{"fast", nil, func(c *fakeConn) error {
			if err := c.write([]byte{iAuthMoreData, 3}); err != nil {
				return err
			}

			return c.ok()
		}, nil},
		{"retrieval", func(cfg *Config) { cfg.AllowPublicKeyRetrieval = true }, func(c *fakeConn) error {
			if err := c.write([]byte{iAuthMoreData, 4}); err != nil {
				return err
			}

			if err := expect(t, c, []byte{2}); err != nil {
				return err
			}

			if err := c.write(append([]byte{iAuthMoreData}, pemKey...)); err != nil {
				return err
			}

			return decrypt(c)
		}, nil},
		{"key", func(cfg *Config) { cfg.ServerPublicKey = &key.PublicKey }, func(c *fakeConn) error {
			if err := c.write([]byte{iAuthMoreData, 4}); err != nil {
				return err
			}

			return decrypt(c)
		}, nil},
		{"no key", nil, func(c *fakeConn) error {
			if err := c.write([]byte{iAuthMoreData, 4}); err != nil {
				return err
			}

			return expectClosed(t, c)
		}, errNeedPublicKey},
		{"unrequested key", nil, func(c *fakeConn) error {
			if err := c.write(append([]byte{iAuthMoreData}, pemKey...)); err != nil {
				return err
			}

			return expectClosed(t, c)
		}, ErrBadConn},
		{"endless", nil, func(c *fakeConn) error {
			for range maxAuthRounds {
				if err := c.write([]byte{iAuthMoreData, 3}); err != nil {
					return err
				}
			}

			return expectClosed(t, c)
		}, errAuthRounds},
	} {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			cfg := authServer(t, "caching_sha2_password", scramble, tt.script)
			if tt.cfg != nil {
				tt.cfg(&cfg)
			}

			conn, err := Connect(cfg)
			if !errors.Is(err, tt.wantErr) {
				t.Fatalf("Connect: %v, want %v", err, tt.wantErr)
			}

			if conn != nil {
				_ = conn.Close()
			}
		})
	}
}

// oldPassword is a plugin of a user who wants mysql_old_password.
type oldPassword struct{ nonce []byte }

func (*oldPassword) Name() string { return pluginOld }

func (p *oldPassword) Respond(a *Auth, data []byte) ([]byte, error) {
	p.nonce = data

	return []byte("old"), nil
}

//nolint:gocognit // a table of server scripts
func TestAuthSwitch(t *testing.T) {
	t.Parallel()

	sw := func(name, data string) []byte { return append([]byte{iEOF}, name+"\x00"+data...) }
	old := &oldPassword{}

	for _, tt := range []struct {
		name    string
		cfg     func(*Config)
		request []byte
		want    []byte // the answer to the switch; nil: the client closes
		wantErr error
	}{
		{"native", nil, sw(pluginNative, fakeNonce+"\x00"), nil, nil},
		{"native empty", func(c *Config) { c.Password = "" }, sw(pluginNative, fakeNonce+"\x00"), []byte{}, nil},
		{"clear", nil, sw("mysql_clear_password", ""), nil, errCleartext},
		{"clear allowed", func(c *Config) { c.AllowCleartextPasswords = true }, sw("mysql_clear_password", ""),
			[]byte("p\x00"), nil},
		{"unknown", nil, sw("dialog", ""), nil, nil},
		{"old", nil, []byte{iEOF}, nil, errOldPassword},
		{"old plugin", func(c *Config) { c.AuthPlugins = []AuthPlugin{old} }, []byte{iEOF}, []byte("old"), nil},
	} {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			cfg := authServer(t, pluginNative, nil, func(c *fakeConn) error {
				if err := c.write(tt.request); err != nil {
					return err
				}

				switch {
				case tt.name == "native":
					resp, err := nativePassword{}.Respond(&Auth{Password: "p"}, []byte(fakeNonce))
					if err != nil {
						return err
					}

					if err := expect(t, c, resp); err != nil {
						return err
					}
				case tt.want == nil:
					return expectClosed(t, c)
				default:
					if err := expect(t, c, tt.want); err != nil {
						return err
					}
				}

				return c.ok()
			})
			if tt.cfg != nil {
				tt.cfg(&cfg)
			}

			conn, err := Connect(cfg)

			switch {
			case tt.name == "unknown":
				if err == nil || !strings.Contains(err.Error(), "unsupported auth plugin dialog") {
					t.Fatalf("Connect: %v", err)
				}
			case !errors.Is(err, tt.wantErr):
				t.Fatalf("Connect: %v, want %v", err, tt.wantErr)
			}

			if conn != nil {
				_ = conn.Close()
			}

			if tt.name == "old plugin" && string(old.nonce) != fakeNonce[:8] {
				t.Errorf("mysql_old_password got the nonce %q, want the first 8 bytes", old.nonce)
			}
		})
	}
}

// A plugin of Config.AuthPlugins answers the handshake in place of a
// built-in one, and an unknown plugin is answered as mysql_native_password.
func TestAuthPluginChoice(t *testing.T) {
	t.Parallel()

	custom := fixedPlugin{name: pluginNative, response: []byte("mine")}

	for _, tt := range []struct {
		server, wantPlugin string
		plugins            []AuthPlugin
		want               []byte
	}{
		{pluginNative, pluginNative, []AuthPlugin{custom}, []byte("mine")},
		{"auth_gssapi_client", pluginNative, nil, nil},
		{"client_ed25519", "client_ed25519", []AuthPlugin{fixedPlugin{"client_ed25519", []byte("sig")}}, []byte("sig")},
	} {
		cfg := rawServer(t, func(c *fakeConn) error {
			if err := c.write(handshakeV10(fakeCapabilities, tt.server)); err != nil {
				return err
			}

			pkt, err := c.read()
			if err != nil {
				return err
			}

			response, plugin := authOf(t, pkt)
			if plugin != tt.wantPlugin || tt.want != nil && !bytes.Equal(response, tt.want) {
				t.Errorf("%s: answered %s %q, want %s %q", tt.server, plugin, response, tt.wantPlugin, tt.want)
			}

			return c.ok()
		})
		cfg.AuthPlugins = tt.plugins

		conn, err := Connect(cfg)
		if err != nil {
			t.Fatalf("%s: %v", tt.server, err)
		}

		_ = conn.Close()
	}
}

type fixedPlugin struct {
	name     string
	response []byte
}

func (p fixedPlugin) Name() string { return p.name }

func (p fixedPlugin) Respond(*Auth, []byte) ([]byte, error) { return p.response, nil }
