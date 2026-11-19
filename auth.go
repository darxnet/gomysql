package gomysql

import (
	"bytes"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha1" //nolint:gosec // G505: mysql_native_password is defined with SHA-1
	"crypto/sha256"
	"crypto/x509"
	"encoding/pem"
	"errors"
)

// getAuthResponse computes the auth response of a plugin.
//
//nolint:gocognit,gosec // G401: mysql_native_password is defined with SHA-1; replaced by plugins in ROADMAP phase 7
func (conn *Conn) getAuthResponse(authPluginName, authPluginData []byte) (authResponse []byte, err error) {
	switch string(authPluginName) {
	case "mysql_old_password":
		err = errOldPassword
		return authResponse, err

	// https://dev.mysql.com/doc/internals/en/secure-password-authentication.html
	case "mysql_native_password":
		// SHA1(password) XOR SHA1(nonce + SHA1(SHA1(password)))
		challengeLen := 20
		hasher := sha1.New()

		hasher.Write([]byte(conn.config.Password))
		passwordSha1 := hasher.Sum(nil)
		hasher.Reset()

		hasher.Write(passwordSha1)
		passwordSha1Sha1 := hasher.Sum(nil)
		hasher.Reset()

		hasher.Write(authPluginData[:challengeLen])
		hasher.Write(passwordSha1Sha1)
		authResponse = hasher.Sum(nil)[:challengeLen]

		for i := range authResponse {
			authResponse[i] ^= passwordSha1[i]
		}
		return authResponse, err

	// https://dev.mysql.com/doc/internals/en/clear-text-authentication.html
	case "mysql_clear_password":
		authResponse = append([]byte(conn.config.Password), 0x00)
		return authResponse, err

	// https://dev.mysql.com/doc/internals/en/sha256.html
	case "sha256_password":
		if conn.config.Password == "" {
			// send empty string
			return authResponse, err
		} else {
			// https://dev.mysql.com/doc/internals/en/public-key-retrieval.html
			if conn.config.ServerPubKey == nil {
				authResponse = []byte{0x01}
				return authResponse, err
			} else {
				pass := []byte(conn.config.Password)
				for i := range pass {
					pass[i] ^= authPluginData[i%len(authPluginData)]
				}
				authResponse, err = rsa.EncryptOAEP(sha256.New(), rand.Reader, conn.config.ServerPubKey, pass, nil)
				if err != nil {
					return authResponse, err
				}
				return authResponse, err
			}
		}

	case "caching_sha2_password":
		if conn.config.Password == "" {
			// send empty string
			return authResponse, err
		} else {
			// Scramble - XOR(SHA256(password), SHA256(SHA256(SHA256(password)), Nonce))
			challengeLen := 20
			hasher := sha256.New()

			hasher.Write([]byte(conn.config.Password))
			passwordSha256 := hasher.Sum(nil)

			hasher.Write(passwordSha256)
			passwordSha256Sha256 := hasher.Sum(nil)
			hasher.Reset()

			hasher.Write(passwordSha256Sha256)
			hasher.Write(authPluginData[:challengeLen])
			authResponse = hasher.Sum(nil)[:challengeLen]

			for i := range authResponse {
				authResponse[i] ^= passwordSha256[i]
			}
			return authResponse, err
		}

	// https://dev.mysql.com/doc/dev/mysql-server/latest/page_caching_sha2_authentication_exchanges.html
	case "caching_sha2_password_full":
		if conn.config.ServerPubKey == nil {
			authResponse = []byte{0x02}
			return authResponse, err
		} else {
			pass := []byte(conn.config.Password)
			for i := range pass {
				pass[i] ^= authPluginData[i%len(authPluginData)]
			}
			authResponse, err = rsa.EncryptOAEP(sha256.New(), rand.Reader, conn.config.ServerPubKey, pass, nil)
			if err != nil {
				return authResponse, err
			}
			return authResponse, err
		}

	default:
		err = errors.New("gomysql: unsupported auth plugin " + string(authPluginName))
		return authResponse, err
	}
}

// handshakeAuth handles the server's answers until authentication ends.
//
//nolint:gocognit // replaced by an auth loop in ROADMAP phase 7
func (conn *Conn) handshakeAuth(authPluginName, authPluginData []byte) (err error) {
	buf, err := conn.readPacket()
	if err != nil {
		return err
	}

	switch buf[0] {
	case iOK:
		conn.processOKPacket(buf)
		if conn.config.IsCompress {
			conn.startCompression()
		}
		return err

	case iERR:
		err = conn.processERRPacket(buf)
		return err

	case iEOF:
		if len(buf) == 1 {
			// Protocol::OldAuthSwitchRequest asks for mysql_old_password.
			_ = conn.conn.Close()
			err = errOldPassword

			return err
		} else {
			// Protocol::AuthSwitchRequest
			authPluginNameIndex := bytes.IndexByte(buf, 0x00)
			if authPluginNameIndex == -1 {
				err = malformed("auth switch request")
				return err
			}
			authPluginName = buf[1:authPluginNameIndex]
			authPluginData = buf[authPluginNameIndex+1:]
		}

		err = conn.authSwitchResponse(authPluginName, authPluginData)
		if err != nil {
			return err
		}

		return conn.handshakeAuth(authPluginName, authPluginData)

	// Protocol::AuthMoreData
	case 0x01:
		switch string(authPluginName) {
		case "sha256_password", "caching_sha2_password":
			pubPem, _ := pem.Decode(buf[1:])
			if pubPem.Type != "" && pubPem.Type != "RSA PUBLIC KEY" {
				err = malformed("RSA key type " + pubPem.Type)
				return err
			}
			var parsedKey any
			parsedKey, err = x509.ParsePKIXPublicKey(pubPem.Bytes)
			if err != nil {
				return err
			}
			if pubKey, ok := parsedKey.(*rsa.PublicKey); !ok {
				err = malformed("RSA key")
				return err
			} else {
				conn.config.ServerPubKey = pubKey
			}
		default:
			authPluginData = buf[1:]
		}

		err = conn.authSwitchResponse(authPluginName, authPluginData)
		if err != nil {
			return err
		}
		return conn.handshakeAuth(authPluginName, authPluginData)

	case 0x03:
		switch string(authPluginName) {
		case "caching_sha2_password":
			// fast_auth_success
			return conn.handshakeAuth(authPluginName, authPluginData)
		default:
			return errUnexpectedPacketIdentity(buf[0])
		}

	case 0x04:
		switch string(authPluginName) {
		case "caching_sha2_password":
			// perform_full_authentication
			authPluginName = append(authPluginName, "_full"...)
			return conn.handshakeAuth(authPluginName, authPluginData)
		default:
			return errUnexpectedPacketIdentity(buf[0])
		}

	default:
		err = errUnexpectedPacketIdentity(buf[0])
		return err
	}
}

func (conn *Conn) authSwitchResponse(authPluginName, authPluginData []byte) (err error) {
	authResponse, err := conn.getAuthResponse(authPluginName, authPluginData)
	if err != nil {
		_ = conn.conn.Close()
		return err
	}

	err = conn.writePacket(authResponse)
	if err != nil {
		_ = conn.conn.Close()
		return err
	}

	return err
}
