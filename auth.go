package gomysql

import (
	"bytes"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha1" //nolint:gosec // G505: mysql_native_password and RSA-OAEP of MySQL are defined with SHA-1
	"crypto/sha256"
	"crypto/x509"
	"encoding/pem"
	"errors"
	"fmt"
)

// AuthPlugin is a client side authentication method of the server, for
// methods gomysql does not have or to replace one it has
// (Config.AuthPlugins).
//
// Built in are mysql_native_password, caching_sha2_password,
// sha256_password and mysql_clear_password. mysql_old_password is refused
// unless a plugin of that name is given.
type AuthPlugin interface {
	// Name is the name the server gives the method, such as
	// "client_ed25519".
	Name() string

	// Respond answers the server: first its nonce (Auth.Step 0), then
	// each payload of AuthMoreData (Step 1, 2, ...). A nil response sends
	// nothing and waits for the server; []byte{} sends an empty packet.
	Respond(a *Auth, data []byte) ([]byte, error)
}

// Auth is what an AuthPlugin may use.
type Auth struct {
	User     string
	Password string

	// Nonce is the data of the handshake or of the auth switch request;
	// MySQL's 20-byte nonces are followed by a NUL.
	Nonce []byte

	// Step is 0 for the nonce, then 1, 2, ... for each AuthMoreData.
	Step int

	// Secure is true over TLS or a unix socket, where the password may be
	// sent as it is.
	Secure bool

	Config *Config
}

const (
	pluginNative = "mysql_native_password"
	pluginOld    = "mysql_old_password"

	iAuthMoreData byte = 0x01

	// maxAuthRounds bounds the packets of a server that never ends.
	maxAuthRounds = 32
)

var (
	errCleartext = errors.New("gomysql: the server asks for the password in clear text (mysql_clear_password) " +
		"without TLS; set Config.TLS or Config.AllowCleartextPasswords")
	errNeedPublicKey = errors.New("gomysql: the server needs the password encrypted with its RSA key; " +
		"set Config.TLS, Config.ServerPublicKey or Config.AllowPublicKeyRetrieval")
	errAuthRounds = errors.New("gomysql: authentication does not end")
)

var builtinPlugins = map[string]AuthPlugin{
	pluginNative:            nativePassword{},
	"caching_sha2_password": cachingSHA2Password{},
	"sha256_password":       sha256Password{},
	"mysql_clear_password":  clearPassword{},
}

// plugin returns the plugin named name: one of Config.AuthPlugins, else a
// built-in one; nil if none.
func (conn *Conn) plugin(name string) AuthPlugin {
	for _, p := range conn.config.AuthPlugins {
		if p.Name() == name {
			return p
		}
	}

	return builtinPlugins[name]
}

// newAuth starts an authentication with the server's plugin and nonce:
// its Auth, and the plugin, mysql_native_password for one gomysql does not
// know, which the server switches from.
func (conn *Conn) newAuth(name string, nonce []byte) (*Auth, AuthPlugin) {
	a := &Auth{
		User:     conn.config.User,
		Password: conn.config.Password,
		Nonce:    nonce,
		Secure:   conn.secure(),
		Config:   &conn.config,
	}

	plugin := conn.plugin(name)
	if plugin == nil {
		plugin = builtinPlugins[pluginNative]
	}

	return a, plugin
}

// authenticate sends the handshake response with the answer of the
// server's plugin to its nonce, and runs the exchange until the server
// accepts or refuses.
func (conn *Conn) authenticate(name string, nonce []byte) error {
	a, plugin := conn.newAuth(name, nonce)

	response, err := plugin.Respond(a, nonce)
	if err != nil {
		return err
	}

	if err := conn.handshakeResponse41(plugin.Name(), response); err != nil {
		return err
	}

	return conn.authExchange(a, plugin, nonce)
}

// authExchange answers the server until it accepts or refuses.
func (conn *Conn) authExchange(a *Auth, plugin AuthPlugin, nonce []byte) error {
	for range maxAuthRounds {
		buf, err := conn.readPacket()
		if err != nil {
			return err
		}

		switch buf[0] {
		case iOK:
			return conn.authOK(buf)
		case iERR:
			return conn.processERRPacket(buf)
		case iEOF:
			if plugin, err = conn.authSwitch(a, buf, nonce); err != nil {
				return err
			}
		case iAuthMoreData:
			err = conn.authMore(a, plugin, buf[1:])
			if err != nil {
				return err
			}
		default:
			return unexpectedPacket(buf[0])
		}
	}

	return errAuthRounds
}

// secure reports whether the password may be sent as it is.
func (conn *Conn) secure() bool {
	return conn.config.TLS != nil || conn.config.Network == "unix"
}

// authOK ends authentication. Compression starts after the first one, not
// after ChangeUser.
func (conn *Conn) authOK(buf []byte) error {
	if err := conn.processOKPacket(buf); err != nil {
		return err
	}

	if conn.config.Compress && conn.compress == nil {
		return conn.startCompression()
	}

	return nil
}

// authMore answers AuthMoreData.
func (conn *Conn) authMore(a *Auth, plugin AuthPlugin, data []byte) error {
	a.Step++

	response, err := plugin.Respond(a, bytes.Clone(data))
	if err != nil || response == nil {
		return err
	}

	return conn.writePacket(response)
}

// authSwitch answers an AuthSwitchRequest: plugin name, NUL, its data.
// Without them it is the OldAuthSwitchRequest of mysql_old_password, with
// the first 8 bytes of the handshake nonce.
func (conn *Conn) authSwitch(a *Auth, buf, nonce []byte) (AuthPlugin, error) {
	name, data := pluginOld, nonce[:min(8, len(nonce))]

	if len(buf) > 1 {
		end := bytes.IndexByte(buf, 0x00)
		if end < 0 {
			return nil, malformed("auth switch request")
		}

		name, data = string(buf[1:end]), bytes.Clone(buf[end+1:])
	}

	plugin := conn.plugin(name)
	if plugin == nil {
		if name == pluginOld {
			return nil, errOldPassword
		}

		return nil, fmt.Errorf("gomysql: unsupported auth plugin %s", name)
	}

	a.Nonce, a.Step = data, 0

	response, err := plugin.Respond(a, data)
	if err != nil {
		return nil, err
	}

	// The server waits for an answer to a switch, even an empty one.
	if response == nil {
		response = []byte{}
	}

	return plugin, conn.writePacket(response)
}

// scramble20 returns the first 20 bytes of a MySQL nonce.
func scramble20(nonce []byte) ([]byte, error) {
	if len(nonce) < 20 {
		return nil, malformed("auth nonce")
	}

	return nonce[:20], nil
}

// nativePassword is mysql_native_password:
// SHA1(password) XOR SHA1(nonce, SHA1(SHA1(password))).
type nativePassword struct{}

func (nativePassword) Name() string { return pluginNative }

func (nativePassword) Respond(a *Auth, data []byte) ([]byte, error) {
	if a.Step > 0 {
		return nil, malformed("mysql_native_password data")
	}

	if a.Password == "" {
		return []byte{}, nil
	}

	nonce, err := scramble20(data)
	if err != nil {
		return nil, err
	}

	hash := sha1.Sum([]byte(a.Password)) //nolint:gosec // G401: defined with SHA-1
	hash2 := sha1.Sum(hash[:])           //nolint:gosec // G401: defined with SHA-1

	h := sha1.New() //nolint:gosec // G401: defined with SHA-1
	h.Write(nonce)
	h.Write(hash2[:])

	response := h.Sum(nil)
	for i := range response {
		response[i] ^= hash[i]
	}

	return response, nil
}

// clearPassword is mysql_clear_password: the password and a NUL, only
// where it is safe or allowed.
type clearPassword struct{}

func (clearPassword) Name() string { return "mysql_clear_password" }

func (clearPassword) Respond(a *Auth, _ []byte) ([]byte, error) {
	if !a.Secure && !a.Config.AllowCleartextPasswords {
		return nil, errCleartext
	}

	return append([]byte(a.Password), 0), nil
}

// cachingSHA2Password is caching_sha2_password. The server answers the
// scramble with fast_auth_success (3) if it has the password cached, or
// with perform_full_authentication (4): then the password goes as it is
// over TLS, or encrypted with the server's RSA key.
//
// https://dev.mysql.com/doc/dev/mysql-server/latest/page_caching_sha2_authentication_exchanges.html
type cachingSHA2Password struct{}

func (cachingSHA2Password) Name() string { return "caching_sha2_password" }

func (cachingSHA2Password) Respond(a *Auth, data []byte) ([]byte, error) {
	const (
		fastAuthSuccess  = 3
		fullAuth         = 4
		requestPublicKey = 2
	)

	if a.Step == 0 {
		if a.Password == "" {
			return []byte{}, nil
		}

		nonce, err := scramble20(data)
		if err != nil {
			return nil, err
		}

		return scrambleSHA256(a.Password, nonce), nil
	}

	switch {
	case len(data) == 1 && data[0] == fastAuthSuccess:
		return nil, nil
	case len(data) == 1 && data[0] == fullAuth:
		return fullAuthResponse(a, requestPublicKey)
	}

	return encryptWithPEM(a, data)
}

// scrambleSHA256 is XOR(SHA256(password), SHA256(SHA256(SHA256(password)), nonce)).
func scrambleSHA256(password string, nonce []byte) []byte {
	hash := sha256.Sum256([]byte(password))
	hash2 := sha256.Sum256(hash[:])

	h := sha256.New()
	h.Write(hash2[:])
	h.Write(nonce)

	response := h.Sum(nil)
	for i := range response {
		response[i] ^= hash[i]
	}

	return response
}

// sha256Password is sha256_password: the password over TLS, or encrypted
// with the server's RSA key.
type sha256Password struct{}

func (sha256Password) Name() string { return "sha256_password" }

func (sha256Password) Respond(a *Auth, data []byte) ([]byte, error) {
	const requestPublicKey = 1

	if a.Step > 0 {
		return encryptWithPEM(a, data)
	}

	if a.Password == "" {
		return []byte{0}, nil
	}

	return fullAuthResponse(a, requestPublicKey)
}

// fullAuthResponse sends the password over TLS, encrypted with the
// configured key, or asks for the server's key with request.
func fullAuthResponse(a *Auth, request byte) ([]byte, error) {
	switch {
	case a.Secure:
		return append([]byte(a.Password), 0), nil
	case a.Config.ServerPublicKey != nil:
		return encryptPassword(a, a.Config.ServerPublicKey)
	case a.Config.AllowPublicKeyRetrieval:
		return []byte{request}, nil
	}

	return nil, errNeedPublicKey
}

// encryptWithPEM encrypts the password with the key the server sent,
// which only a request allowed by Config.AllowPublicKeyRetrieval brings.
func encryptWithPEM(a *Auth, data []byte) ([]byte, error) {
	if !a.Config.AllowPublicKeyRetrieval || a.Secure || a.Config.ServerPublicKey != nil {
		return nil, malformed("auth data")
	}

	block, _ := pem.Decode(data)
	if block == nil {
		return nil, malformed("server RSA key")
	}

	key, err := parsePublicKey(block.Bytes)
	if err != nil {
		return nil, err
	}

	return encryptPassword(a, key)
}

func parsePublicKey(der []byte) (*rsa.PublicKey, error) {
	if key, err := x509.ParsePKCS1PublicKey(der); err == nil {
		return key, nil
	}

	key, err := x509.ParsePKIXPublicKey(der)
	if err != nil {
		return nil, fmt.Errorf("gomysql: server RSA key: %w", err)
	}

	rsaKey, ok := key.(*rsa.PublicKey)
	if !ok {
		return nil, malformed("server RSA key")
	}

	return rsaKey, nil
}

// encryptPassword encrypts the password and a NUL, XORed with the nonce,
// with RSA-OAEP and SHA-1, as the server decrypts it.
func encryptPassword(a *Auth, key *rsa.PublicKey) ([]byte, error) {
	nonce, err := scramble20(a.Nonce)
	if err != nil {
		return nil, err
	}

	plain := append([]byte(a.Password), 0)
	for i := range plain {
		plain[i] ^= nonce[i%len(nonce)]
	}

	return rsa.EncryptOAEP(sha1.New(), rand.Reader, key, plain, nil) //nolint:gosec // G401: the server uses SHA-1
}
