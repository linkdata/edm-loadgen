package pki

import (
	"crypto/tls"
	"crypto/x509"
	"os"
	"testing"
)

func TestEnsureGeneratesAndIsIdempotent(t *testing.T) {
	dir := t.TempDir()
	b, err := Ensure(dir, []string{"127.0.0.1", "localhost"}, "kid-test")
	if err != nil {
		t.Fatalf("Ensure: %v", err)
	}
	for _, p := range []string{b.CACert, b.CAKey, b.ServerCert, b.ServerKey, b.ClientCert, b.ClientKey, b.JWSKey} {
		st, err := os.Stat(p)
		if err != nil {
			t.Errorf("missing %s: %v", p, err)
			continue
		}
		if st.Size() == 0 {
			t.Errorf("%s is empty", p)
		}
	}

	// Second call must not regenerate; modtimes should be unchanged.
	before, _ := os.Stat(b.ServerCert)
	if _, err := Ensure(dir, []string{"127.0.0.1"}, "kid-test"); err != nil {
		t.Fatalf("second Ensure: %v", err)
	}
	after, _ := os.Stat(b.ServerCert)
	if !before.ModTime().Equal(after.ModTime()) {
		t.Errorf("server cert was regenerated on second Ensure")
	}
}

func TestServerCertVerifiesAgainstCA(t *testing.T) {
	dir := t.TempDir()
	b, err := Ensure(dir, []string{"127.0.0.1", "localhost"}, "kid-test")
	if err != nil {
		t.Fatal(err)
	}

	caPEM, _ := os.ReadFile(b.CACert)
	roots := x509.NewCertPool()
	if !roots.AppendCertsFromPEM(caPEM) {
		t.Fatal("could not load CA into pool")
	}
	pair, err := tls.LoadX509KeyPair(b.ServerCert, b.ServerKey)
	if err != nil {
		t.Fatalf("load server pair: %v", err)
	}
	leaf, err := x509.ParseCertificate(pair.Certificate[0])
	if err != nil {
		t.Fatalf("parse server leaf: %v", err)
	}
	if _, err := leaf.Verify(x509.VerifyOptions{Roots: roots, DNSName: "localhost"}); err != nil {
		t.Errorf("verify server cert: %v", err)
	}
}

func TestClientCertVerifiesAgainstCA(t *testing.T) {
	dir := t.TempDir()
	b, err := Ensure(dir, nil, "kid-test")
	if err != nil {
		t.Fatal(err)
	}
	caPEM, _ := os.ReadFile(b.CACert)
	roots := x509.NewCertPool()
	roots.AppendCertsFromPEM(caPEM)
	pair, err := tls.LoadX509KeyPair(b.ClientCert, b.ClientKey)
	if err != nil {
		t.Fatal(err)
	}
	leaf, err := x509.ParseCertificate(pair.Certificate[0])
	if err != nil {
		t.Fatal(err)
	}
	if _, err := leaf.Verify(x509.VerifyOptions{
		Roots:     roots,
		KeyUsages: []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth},
	}); err != nil {
		t.Errorf("verify client cert: %v", err)
	}
}
