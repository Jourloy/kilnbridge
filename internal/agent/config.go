package agent

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/hex"
	"encoding/json"
	"encoding/pem"
	"errors"
	"math/big"
	"net"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"time"

	"github.com/jourloy/kilnbridge/internal/platform"
)

type Config struct {
	Name          string `json:"name"`
	Listen        string `json:"listen"`
	AutoStart     bool   `json:"autoStart"`
	AutoUpdate    bool   `json:"autoUpdate"`
	Repository    string `json:"repository"`
	Paused        bool   `json:"paused"`
	RetentionDays int    `json:"retentionDays"`
}
type secrets struct {
	Key        string `json:"key"`
	Admin      string `json:"admin"`
	CertPEM    []byte `json:"certPem"`
	PrivatePEM []byte `json:"privatePem"`
}

func loadConfig(dir string) (Config, secrets, error) {
	host, _ := os.Hostname()
	c := Config{Name: host, Listen: "0.0.0.0:8743", AutoUpdate: true, Repository: "Jourloy/kilnbridge", RetentionDays: 7}
	if err := os.MkdirAll(dir, 0700); err != nil {
		return c, secrets{}, err
	}
	b, err := os.ReadFile(filepath.Join(dir, "config.json"))
	if err == nil {
		if err = json.Unmarshal(b, &c); err != nil {
			return c, secrets{}, err
		}
	} else if !os.IsNotExist(err) {
		return c, secrets{}, err
	}
	if err = c.Validate(); err != nil {
		return c, secrets{}, err
	}
	var s secrets
	b, err = os.ReadFile(filepath.Join(dir, "secrets.bin"))
	if err == nil {
		b, err = platform.Unprotect(b)
		if err != nil {
			return c, s, err
		}
		err = json.Unmarshal(b, &s)
		return c, s, err
	}
	if !os.IsNotExist(err) {
		return c, s, err
	}
	s.Key = platform.Token()
	s.Admin = platform.Token()
	s.CertPEM, s.PrivatePEM, err = newCertificate(host)
	if err == nil {
		err = saveSecrets(dir, s)
	}
	if err == nil {
		err = platform.WriteJSON(filepath.Join(dir, "config.json"), c)
	}
	return c, s, err
}
func (c Config) Validate() error {
	if len(c.Name) == 0 || len(c.Name) > 80 {
		return errors.New("machine name must contain 1–80 characters")
	}
	h, p, e := net.SplitHostPort(c.Listen)
	port, portErr := strconv.Atoi(p)
	if e != nil || net.ParseIP(h) == nil || portErr != nil || port < 0 || port > 65535 {
		return errors.New("listen must be an IP address and port")
	}
	if !regexp.MustCompile(`^[a-zA-Z0-9_.-]+/[a-zA-Z0-9_.-]+$`).MatchString(c.Repository) {
		return errors.New("repository must be owner/name")
	}
	if c.RetentionDays < 1 || c.RetentionDays > 90 {
		return errors.New("retention must be between 1 and 90 days")
	}
	return nil
}
func saveSecrets(dir string, s secrets) error {
	b, e := json.Marshal(s)
	if e != nil {
		return e
	}
	b, e = platform.Protect(b)
	if e != nil {
		return e
	}
	return platform.AtomicWrite(filepath.Join(dir, "secrets.bin"), b, 0600)
}
func newCertificate(host string) ([]byte, []byte, error) {
	key, e := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if e != nil {
		return nil, nil, e
	}
	serial, e := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 128))
	if e != nil {
		return nil, nil, e
	}
	t := &x509.Certificate{SerialNumber: serial, Subject: pkix.Name{CommonName: "Kilnbridge — " + host}, DNSNames: []string{host, "localhost"}, IPAddresses: []net.IP{net.ParseIP("127.0.0.1")}, NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().AddDate(10, 0, 0), KeyUsage: x509.KeyUsageDigitalSignature, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth}}
	der, e := x509.CreateCertificate(rand.Reader, t, t, &key.PublicKey, key)
	if e != nil {
		return nil, nil, e
	}
	private, e := x509.MarshalPKCS8PrivateKey(key)
	if e != nil {
		return nil, nil, e
	}
	return pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}), pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: private}), nil
}
func (s secrets) TLS() (*tls.Config, string, error) {
	cert, e := tls.X509KeyPair(s.CertPEM, s.PrivatePEM)
	if e != nil {
		return nil, "", e
	}
	digest := sha256.Sum256(cert.Certificate[0])
	return &tls.Config{Certificates: []tls.Certificate{cert}, MinVersion: tls.VersionTLS12}, hex.EncodeToString(digest[:]), nil
}
