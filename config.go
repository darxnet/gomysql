package gomysql

import (
	"crypto/rsa"
	"crypto/tls"
)

type Config struct {
	Attr              map[string]string
	TlsConfig         *tls.Config
	ServerPubKey      *rsa.PublicKey
	Protocol          string
	Address           string
	User              string
	Password          string
	DbName            string
	IsSSL             bool
	Charset           byte
	IsClientFoundRows bool
	IsCompress        bool
}
