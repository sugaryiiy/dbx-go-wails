package database

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"errors"
	"fmt"
	"io"
	"math/big"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

type databaseTLSFixture struct {
	ca       *x509.Certificate
	caKey    ed25519.PrivateKey
	caPEM    []byte
	caPath   string
	server   tls.Certificate
	client   tls.Certificate
	certPath string
	keyPath  string
}

func newDatabaseTLSFixture(t *testing.T) databaseTLSFixture {
	t.Helper()
	_, caKey, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	ca := &x509.Certificate{
		SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "DBX ephemeral test CA"},
		NotBefore: now.Add(-time.Hour), NotAfter: now.Add(time.Hour),
		IsCA: true, BasicConstraintsValid: true, KeyUsage: x509.KeyUsageCertSign,
	}
	der, err := x509.CreateCertificate(rand.Reader, ca, ca, caKey.Public(), caKey)
	if err != nil {
		t.Fatal(err)
	}
	ca, err = x509.ParseCertificate(der)
	if err != nil {
		t.Fatal(err)
	}
	f := databaseTLSFixture{ca: ca, caKey: caKey, caPEM: pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})}
	dir := t.TempDir()
	f.caPath = writeDatabaseTLSFixtureFile(t, dir, "ca.pem", f.caPEM)
	f.server, _, _ = f.issue(t, false)
	var certPEM, keyPEM []byte
	f.client, certPEM, keyPEM = f.issue(t, true)
	f.certPath = writeDatabaseTLSFixtureFile(t, dir, "client.pem", certPEM)
	f.keyPath = writeDatabaseTLSFixtureFile(t, dir, "client-key.pem", keyPEM)
	return f
}

func (f databaseTLSFixture) issue(t *testing.T, client bool) (tls.Certificate, []byte, []byte) {
	t.Helper()
	_, key, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	template := &x509.Certificate{
		SerialNumber: big.NewInt(2), Subject: pkix.Name{CommonName: "DBX ephemeral test server"},
		NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(time.Hour),
		KeyUsage: x509.KeyUsageDigitalSignature, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		DNSNames: []string{"db.internal"}, IPAddresses: []net.IP{net.ParseIP("127.0.0.1")},
	}
	if client {
		template.SerialNumber = big.NewInt(3)
		template.Subject.CommonName = "DBX ephemeral test client"
		template.ExtKeyUsage = []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth}
		template.DNSNames, template.IPAddresses = nil, nil
	}
	der, err := x509.CreateCertificate(rand.Reader, template, f.ca, key.Public(), f.caKey)
	if err != nil {
		t.Fatal(err)
	}
	keyDER, err := x509.MarshalPKCS8PrivateKey(key)
	if err != nil {
		t.Fatal(err)
	}
	certPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
	keyPEM := pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: keyDER})
	cert, err := tls.X509KeyPair(certPEM, keyPEM)
	if err != nil {
		t.Fatal(err)
	}
	return cert, certPEM, keyPEM
}

func writeDatabaseTLSFixtureFile(t *testing.T, dir, name string, data []byte) string {
	t.Helper()
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, data, 0600); err != nil {
		t.Fatal(err)
	}
	return path
}

// Exchange application data too: TLS 1.3 can report a rejected client
// certificate only on the client's first read after its handshake returns.
func databaseTLSHandshake(t *testing.T, clientCfg, serverCfg *tls.Config) error {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	done := make(chan error, 1)
	go func() {
		conn, err := listener.Accept()
		if err != nil {
			done <- err
			return
		}
		defer conn.Close()
		conn.SetDeadline(time.Now().Add(3 * time.Second))
		secure := tls.Server(conn, serverCfg)
		if err = secure.HandshakeContext(ctx); err == nil {
			_, err = secure.Write([]byte{1})
		}
		done <- err
	}()
	conn, err := (&net.Dialer{}).DialContext(ctx, "tcp", listener.Addr().String())
	if err != nil {
		listener.Close()
		<-done
		return err
	}
	defer conn.Close()
	conn.SetDeadline(time.Now().Add(3 * time.Second))
	secure := tls.Client(conn, clientCfg)
	if err = secure.HandshakeContext(ctx); err == nil {
		_, err = io.ReadFull(secure, make([]byte, 1))
	}
	conn.Close()
	serverErr := <-done
	return errors.Join(err, serverErr)
}

func TestDatabaseTLSConfigDefaultsAndValidation(t *testing.T) {
	for _, kind := range []string{"mysql", "postgres", "sqlite"} {
		cfg, err := databaseTLSConfig(Config{Type: kind})
		if err != nil || cfg != nil {
			t.Fatalf("TLS disabled for %s: %v, %v", kind, cfg, err)
		}
	}
	cfg, err := databaseTLSConfig(Config{Type: "mysql", Host: "db.internal", SSL: true})
	if err != nil {
		t.Fatal(err)
	}
	if cfg.ServerName != "db.internal" || cfg.MinVersion != tls.VersionTLS12 || cfg.InsecureSkipVerify || cfg.RootCAs != nil {
		t.Fatal("default TLS configuration must use verified system trust and TLS 1.2+")
	}
	ipv6, err := databaseTLSConfig(Config{Type: "postgres", Host: "[::1]", SSL: true})
	if err != nil || ipv6.ServerName != "::1" {
		t.Fatalf("IPv6 host: %#v, %v", ipv6, err)
	}
	for name, c := range map[string]Config{
		"CA with TLS disabled":   {CACert: "ignored.pem"},
		"cert with TLS disabled": {ClientCert: "ignored.pem"},
		"key with TLS disabled":  {ClientKey: "ignored.pem"},
		"SQLite TLS":             {Type: "sqlite", SSL: true, Host: "database.sqlite"},
		"missing hostname":       {Type: "mysql", SSL: true},
		"missing client key":     {Type: "mysql", SSL: true, Host: "db.internal", ClientCert: "ignored.pem"},
		"missing client cert":    {Type: "mysql", SSL: true, Host: "db.internal", ClientKey: "ignored.pem"},
	} {
		t.Run(name, func(t *testing.T) {
			if cfg, err := databaseTLSConfig(c); err == nil || cfg != nil {
				t.Fatalf("invalid TLS config accepted: %#v, %v", cfg, err)
			}
		})
	}
}

func TestDatabaseTLSVerifiedHandshake(t *testing.T) {
	f := newDatabaseTLSFixture(t)
	other := newDatabaseTLSFixture(t)
	server := &tls.Config{MinVersion: tls.VersionTLS12, Certificates: []tls.Certificate{f.server}}
	for _, test := range []struct {
		name, host, ca string
		ok             bool
	}{
		{"trusted DNS", "db.internal", f.caPath, true},
		{"trusted IP", "127.0.0.1", f.caPath, true},
		{"wrong hostname", "wrong.internal", f.caPath, false},
		{"wrong CA", "db.internal", other.caPath, false},
		{"untrusted private CA", "db.internal", "", false},
	} {
		t.Run(test.name, func(t *testing.T) {
			cfg, err := databaseTLSConfig(Config{Type: "postgres", Host: test.host, SSL: true, CACert: test.ca})
			if err != nil {
				t.Fatal(err)
			}
			err = databaseTLSHandshake(t, cfg, server)
			if (err == nil) != test.ok {
				t.Fatalf("handshake success=%t, want %t: %v", err == nil, test.ok, err)
			}
		})
	}
	cfg, err := databaseTLSConfig(Config{Type: "mysql", Host: "db.internal", SSL: true, CACert: f.caPath})
	if err != nil {
		t.Fatal(err)
	}
	legacy := server.Clone()
	legacy.MinVersion, legacy.MaxVersion = tls.VersionTLS10, tls.VersionTLS11
	if err := databaseTLSHandshake(t, cfg, legacy); err == nil {
		t.Fatal("accepted obsolete TLS protocol")
	}
}

func TestDatabaseTLSMutualHandshake(t *testing.T) {
	f := newDatabaseTLSFixture(t)
	other := newDatabaseTLSFixture(t)
	clientRoots := x509.NewCertPool()
	clientRoots.AddCert(f.ca)
	server := &tls.Config{
		MinVersion: tls.VersionTLS12, Certificates: []tls.Certificate{f.server},
		ClientAuth: tls.RequireAndVerifyClientCert, ClientCAs: clientRoots,
	}
	for _, test := range []struct {
		name, cert, key string
		ok              bool
	}{
		{"trusted client", f.certPath, f.keyPath, true},
		{"missing client", "", "", false},
		{"untrusted client", other.certPath, other.keyPath, false},
	} {
		t.Run(test.name, func(t *testing.T) {
			cfg, err := databaseTLSConfig(Config{Type: "mysql", Host: "db.internal", SSL: true, CACert: f.caPath, ClientCert: test.cert, ClientKey: test.key})
			if err != nil {
				t.Fatal(err)
			}
			if err = databaseTLSHandshake(t, cfg, server); (err == nil) != test.ok {
				t.Fatalf("mutual TLS success=%t, want %t: %v", err == nil, test.ok, err)
			}
		})
	}
	if _, err := databaseTLSConfig(Config{Type: "mysql", Host: "db.internal", SSL: true, ClientCert: f.certPath, ClientKey: other.keyPath}); err == nil {
		t.Fatal("accepted mismatched client certificate and key")
	}
}

func TestDatabaseTLSRejectsInvalidFiles(t *testing.T) {
	f := newDatabaseTLSFixture(t)
	dir := t.TempDir()
	oversize := filepath.Join(dir, "oversize.pem")
	file, err := os.Create(oversize)
	if err != nil {
		t.Fatal(err)
	}
	err = file.Truncate(databaseTLSFileLimit + 1)
	file.Close()
	if err != nil {
		t.Fatal(err)
	}
	cases := map[string]string{
		"directory":       dir,
		"missing":         filepath.Join(dir, "missing.pem"),
		"oversized":       oversize,
		"empty":           writeDatabaseTLSFixtureFile(t, dir, "empty.pem", nil),
		"non PEM":         writeDatabaseTLSFixtureFile(t, dir, "invalid.pem", []byte("not a certificate")),
		"corrupt DER":     writeDatabaseTLSFixtureFile(t, dir, "corrupt.pem", pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: []byte("invalid DER")})),
		"trailing junk":   writeDatabaseTLSFixtureFile(t, dir, "trailing.pem", append(append([]byte{}, f.caPEM...), []byte("invalid trailer")...)),
		"leading junk":    writeDatabaseTLSFixtureFile(t, dir, "leading.pem", append([]byte("invalid prefix"), f.caPEM...)),
		"non certificate": f.keyPath,
	}
	for name, path := range cases {
		t.Run(name, func(t *testing.T) {
			if _, err := databaseTLSConfig(Config{Type: "postgres", Host: "db.internal", SSL: true, CACert: path}); err == nil {
				t.Fatal("invalid CA file accepted")
			}
		})
	}
	for _, role := range []string{"cert", "key"} {
		for name, path := range cases {
			t.Run(fmt.Sprintf("client %s %s", role, name), func(t *testing.T) {
				c := Config{Type: "mysql", Host: "db.internal", SSL: true, ClientCert: f.certPath, ClientKey: f.keyPath}
				if role == "cert" {
					c.ClientCert = path
				} else {
					c.ClientKey = path
				}
				if _, err := databaseTLSConfig(c); err == nil {
					if role == "key" && path == f.keyPath {
						return // The key fixture is valid for the key slot.
					}
					t.Fatal("invalid client certificate/key accepted")
				}
			})
		}
	}
}

func TestDatabaseTLSRejectsMixedClientPEM(t *testing.T) {
	f := newDatabaseTLSFixture(t)
	_, certPEM, keyPEM := f.issue(t, true)
	for _, test := range []struct {
		name, role string
		data       []byte
	}{
		{"certificate prefix", "cert", append([]byte("unexpected text\n"), certPEM...)},
		{"certificate suffix", "cert", append(append([]byte{}, certPEM...), []byte("truncated certificate")...)},
		{"key prefix", "key", append([]byte("unexpected text\n"), keyPEM...)},
		{"key suffix", "key", append(append([]byte{}, keyPEM...), []byte("truncated key")...)},
		{"multiple keys", "key", append(append([]byte{}, keyPEM...), keyPEM...)},
	} {
		t.Run(test.name, func(t *testing.T) {
			dir := t.TempDir()
			c := Config{Type: "mysql", Host: "db.internal", SSL: true,
				ClientCert: writeDatabaseTLSFixtureFile(t, dir, "client.pem", certPEM),
				ClientKey:  writeDatabaseTLSFixtureFile(t, dir, "key.pem", keyPEM)}
			if test.role == "cert" {
				c.ClientCert = writeDatabaseTLSFixtureFile(t, dir, "mixed.pem", test.data)
			} else {
				c.ClientKey = writeDatabaseTLSFixtureFile(t, dir, "mixed.pem", test.data)
			}
			if _, err := databaseTLSConfig(c); err == nil {
				t.Fatal("accepted mixed or truncated client PEM")
			}
		})
	}
}

func TestDatabaseTLSCertificatePathsAndIsolation(t *testing.T) {
	f := newDatabaseTLSFixture(t)
	other := newDatabaseTLSFixture(t)
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	writeDatabaseTLSFixtureFile(t, home, "ca.pem", f.caPEM)
	cfg, err := databaseTLSConfig(Config{Type: "postgres", Host: "db.internal", SSL: true, CACert: "~/ca.pem"})
	if err != nil {
		t.Fatal(err)
	}
	if len(cfg.RootCAs.Subjects()) != 1 {
		t.Fatal("custom CA did not create an isolated trust store")
	}
	second, err := databaseTLSConfig(Config{Type: "postgres", Host: "another.internal", SSL: true, CACert: other.caPath})
	if err != nil {
		t.Fatal(err)
	}
	second.RootCAs.AddCert(f.ca)
	if cfg.ServerName != "db.internal" || len(cfg.RootCAs.Subjects()) != 1 || second.RootCAs == cfg.RootCAs {
		t.Fatal("TLS trust or hostname leaked between connections")
	}
	if data, err := readDatabaseTLSFile(" ~/ca.pem ", "CA certificate"); err != nil || !strings.Contains(string(data), "BEGIN CERTIFICATE") {
		t.Fatalf("home-relative path failed: %v", err)
	}
}
