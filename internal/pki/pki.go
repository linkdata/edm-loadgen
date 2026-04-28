// Package pki generates the small set of self-signed certificates and keys
// edm-loadgen's embedded MQTT broker needs to talk TLS to a native EDM, plus
// the JWS signing key EDM uses to sign payloads. Everything is ECDSA P-256
// to match the dnstapir convention.
//
// All files are generated idempotently: if they already exist, Ensure returns
// without touching them. Re-running pki.Ensure on a populated directory is a
// no-op. To rotate, delete the directory.
package pki

import (
	"crypto/ecdsa"
	"crypto/ed25519"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"math/big"
	"net"
	"os"
	"path/filepath"
	"time"
)

// FileNames lists the file basenames Ensure writes inside the keys directory.
// EDM expects exactly these names when its --mqtt-* flags are pointed at the
// directory; the README and the run/serve startup printout reuse them too.
const (
	CACertFile     = "ca.crt"
	CAKeyFile      = "ca.key"
	ServerCertFile = "server.crt"
	ServerKeyFile  = "server.key"
	ClientCertFile = "client.crt"
	ClientKeyFile  = "client.key"
	JWSKeyFile     = "jws.key"
)

// Bundle is a path-resolved view of the keys directory after Ensure returns.
// All fields are absolute paths.
type Bundle struct {
	Dir            string
	CACert         string
	CAKey          string
	ServerCert     string
	ServerKey      string
	ClientCert     string
	ClientKey      string
	JWSKey         string
}

// Ensure makes sure dir exists and contains all of the files named by the
// constants above. Missing files are generated; existing files are kept.
//
// hosts is the list of DNS names and/or IP addresses the server cert should
// be valid for; defaults to ["127.0.0.1", "localhost"] when empty.
//
// jwsKeyID is set as the JWK "kid" field on the JWS signing key. EDM uses
// the kid to derive its publish topic ("events/up/<kid>/new_qname"), so this
// also dictates which topic prefix the broker should match.
func Ensure(dir string, hosts []string, jwsKeyID string) (b Bundle, err error) {
	if len(hosts) == 0 {
		hosts = []string{"127.0.0.1", "localhost"}
	}
	if jwsKeyID == "" {
		jwsKeyID = "edm-loadgen"
	}
	if err = os.MkdirAll(dir, 0o700); err != nil {
		err = fmt.Errorf("pki: mkdir %s: %w", dir, err)
		return
	}
	abs, err := filepath.Abs(dir)
	if err != nil {
		err = fmt.Errorf("pki: abs %s: %w", dir, err)
		return
	}
	b = Bundle{
		Dir:        abs,
		CACert:     filepath.Join(abs, CACertFile),
		CAKey:      filepath.Join(abs, CAKeyFile),
		ServerCert: filepath.Join(abs, ServerCertFile),
		ServerKey:  filepath.Join(abs, ServerKeyFile),
		ClientCert: filepath.Join(abs, ClientCertFile),
		ClientKey:  filepath.Join(abs, ClientKeyFile),
		JWSKey:     filepath.Join(abs, JWSKeyFile),
	}

	caCert, caKey, err := loadOrCreateCA(b.CACert, b.CAKey)
	if err != nil {
		return
	}
	if err = ensureLeaf(b.ServerCert, b.ServerKey, "edm-loadgen-broker", hosts, caCert, caKey, true); err != nil {
		return
	}
	if err = ensureLeaf(b.ClientCert, b.ClientKey, "edm-loadgen-client", nil, caCert, caKey, false); err != nil {
		return
	}
	if err = ensureJWK(b.JWSKey, jwsKeyID); err != nil {
		return
	}
	return
}

// loadOrCreateCA returns the CA cert/key from disk, or generates a new pair
// and writes them.
func loadOrCreateCA(certPath, keyPath string) (cert *x509.Certificate, key *ecdsa.PrivateKey, err error) {
	if exists(certPath) && exists(keyPath) {
		return loadCertAndKey(certPath, keyPath)
	}

	key, err = ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		err = fmt.Errorf("pki: generate ca key: %w", err)
		return
	}
	tmpl := &x509.Certificate{
		SerialNumber:          serial(),
		Subject:               pkix.Name{CommonName: "edm-loadgen Dev CA"},
		NotBefore:             time.Now().Add(-time.Hour),
		NotAfter:              time.Now().Add(10 * 365 * 24 * time.Hour),
		KeyUsage:              x509.KeyUsageCertSign | x509.KeyUsageCRLSign,
		BasicConstraintsValid: true,
		IsCA:                  true,
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		err = fmt.Errorf("pki: create ca cert: %w", err)
		return
	}
	cert, err = x509.ParseCertificate(der)
	if err != nil {
		err = fmt.Errorf("pki: parse ca cert: %w", err)
		return
	}
	if err = writeCertPEM(certPath, der); err != nil {
		return
	}
	if err = writeKeyPEM(keyPath, key); err != nil {
		return
	}
	return
}

// ensureLeaf writes a CA-signed leaf cert + private key. server controls the
// extended-key-usage shape (server-auth vs. client-auth).
func ensureLeaf(certPath, keyPath, cn string, hosts []string, caCert *x509.Certificate, caKey *ecdsa.PrivateKey, server bool) error {
	if exists(certPath) && exists(keyPath) {
		return nil
	}
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return fmt.Errorf("pki: generate leaf key: %w", err)
	}
	tmpl := &x509.Certificate{
		SerialNumber: serial(),
		Subject:      pkix.Name{CommonName: cn},
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().Add(5 * 365 * 24 * time.Hour),
		KeyUsage:     x509.KeyUsageDigitalSignature | x509.KeyUsageKeyEncipherment,
	}
	if server {
		tmpl.ExtKeyUsage = []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth}
		for _, h := range hosts {
			if ip := net.ParseIP(h); ip != nil {
				tmpl.IPAddresses = append(tmpl.IPAddresses, ip)
			} else {
				tmpl.DNSNames = append(tmpl.DNSNames, h)
			}
		}
	} else {
		tmpl.ExtKeyUsage = []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth}
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, caCert, &key.PublicKey, caKey)
	if err != nil {
		return fmt.Errorf("pki: create leaf cert (%s): %w", cn, err)
	}
	if err := writeCertPEM(certPath, der); err != nil {
		return err
	}
	return writeKeyPEM(keyPath, key)
}

// ensureJWK writes an Ed25519 JWK file (RFC 8037 OKP/Ed25519). EDM expects
// its --mqtt-signing-key-file to be a JWK in this exact form (it forces
// alg=EdDSA after parsing) and uses the kid as the topic component:
//
//	events/up/<kid>/new_qname
type jwkOKP struct {
	Kty string `json:"kty"`
	Crv string `json:"crv"`
	X   string `json:"x"`
	D   string `json:"d"`
	Kid string `json:"kid"`
	Alg string `json:"alg"`
}

func ensureJWK(path, kid string) error {
	if exists(path) {
		return nil
	}
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return fmt.Errorf("pki: generate ed25519: %w", err)
	}
	enc := base64.RawURLEncoding
	j := jwkOKP{
		Kty: "OKP",
		Crv: "Ed25519",
		X:   enc.EncodeToString(pub),
		D:   enc.EncodeToString(priv.Seed()),
		Kid: kid,
		Alg: "EdDSA",
	}
	body, err := json.MarshalIndent(j, "", "  ")
	if err != nil {
		return fmt.Errorf("pki: marshal jwk: %w", err)
	}
	if err := os.WriteFile(path, body, 0o600); err != nil {
		return fmt.Errorf("pki: write %s: %w", path, err)
	}
	return nil
}

func loadCertAndKey(certPath, keyPath string) (*x509.Certificate, *ecdsa.PrivateKey, error) {
	certPEM, err := os.ReadFile(certPath)
	if err != nil {
		return nil, nil, fmt.Errorf("pki: read %s: %w", certPath, err)
	}
	keyPEM, err := os.ReadFile(keyPath)
	if err != nil {
		return nil, nil, fmt.Errorf("pki: read %s: %w", keyPath, err)
	}
	cb, _ := pem.Decode(certPEM)
	if cb == nil {
		return nil, nil, fmt.Errorf("pki: no PEM block in %s", certPath)
	}
	cert, err := x509.ParseCertificate(cb.Bytes)
	if err != nil {
		return nil, nil, fmt.Errorf("pki: parse %s: %w", certPath, err)
	}
	kb, _ := pem.Decode(keyPEM)
	if kb == nil {
		return nil, nil, fmt.Errorf("pki: no PEM block in %s", keyPath)
	}
	key, err := x509.ParseECPrivateKey(kb.Bytes)
	if err != nil {
		// Fall back to PKCS#8 just in case.
		generic, err2 := x509.ParsePKCS8PrivateKey(kb.Bytes)
		if err2 != nil {
			return nil, nil, fmt.Errorf("pki: parse %s: %w", keyPath, err)
		}
		ec, ok := generic.(*ecdsa.PrivateKey)
		if !ok {
			return nil, nil, errors.New("pki: key is not ECDSA")
		}
		key = ec
	}
	return cert, key, nil
}

func writeCertPEM(path string, der []byte) error {
	pemBytes := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
	if err := os.WriteFile(path, pemBytes, 0o644); err != nil {
		return fmt.Errorf("pki: write %s: %w", path, err)
	}
	return nil
}

func writeKeyPEM(path string, key *ecdsa.PrivateKey) error {
	der, err := x509.MarshalECPrivateKey(key)
	if err != nil {
		return fmt.Errorf("pki: marshal key for %s: %w", path, err)
	}
	pemBytes := pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: der})
	if err := os.WriteFile(path, pemBytes, 0o600); err != nil {
		return fmt.Errorf("pki: write %s: %w", path, err)
	}
	return nil
}

func serial() *big.Int {
	max := new(big.Int).Lsh(big.NewInt(1), 128)
	n, err := rand.Int(rand.Reader, max)
	if err != nil {
		// rand.Reader failure is panic-worthy; we rely on the OS RNG.
		panic(err)
	}
	return n
}

func exists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}
