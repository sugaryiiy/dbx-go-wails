package database

import (
	"bytes"
	"crypto/tls"
	"crypto/x509"
	"encoding/pem"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
)

const databaseTLSFileLimit = 4 << 20

// databaseTLSConfig returns an independent TLS configuration for a connection.
// A supplied CA bundle is the complete trust store for that connection; leaving
// it empty uses the operating system's trusted roots. The host remains the
// database host even when a transport dials an SSH forwarding endpoint.
func databaseTLSConfig(c Config) (*tls.Config, error) {
	hasCA := strings.TrimSpace(c.CACert) != ""
	hasCert := strings.TrimSpace(c.ClientCert) != ""
	hasKey := strings.TrimSpace(c.ClientKey) != ""
	if !c.SSL {
		if hasCA || hasCert || hasKey {
			return nil, errors.New("TLS must be enabled to use certificate files")
		}
		return nil, nil
	}
	if c.Type == "sqlite" {
		return nil, errors.New("SQLite does not support TLS connection settings")
	}
	if hasCert != hasKey {
		return nil, errors.New("TLS client certificate and private key must be provided together")
	}
	host := strings.TrimSpace(c.Host)
	if host == "" {
		return nil, errors.New("TLS requires a database hostname for certificate verification")
	}
	// IPv6 configuration sometimes includes brackets. TLS expects the bare IP.
	if strings.HasPrefix(host, "[") && strings.HasSuffix(host, "]") {
		host = strings.TrimSuffix(strings.TrimPrefix(host, "["), "]")
	}
	cfg := &tls.Config{MinVersion: tls.VersionTLS12, ServerName: host}
	if hasCA {
		caPEM, err := readDatabaseTLSFile(c.CACert, "CA certificate")
		if err != nil {
			return nil, err
		}
		certs, err := parseDatabaseTLSCertificates(caPEM, "CA certificate")
		if err != nil {
			return nil, err
		}
		roots := x509.NewCertPool()
		for _, cert := range certs {
			roots.AddCert(cert)
		}
		cfg.RootCAs = roots
	}
	if hasCert {
		certPEM, err := readDatabaseTLSFile(c.ClientCert, "client certificate")
		if err != nil {
			return nil, err
		}
		if _, err := parseDatabaseTLSCertificates(certPEM, "client certificate"); err != nil {
			return nil, err
		}
		keyPEM, err := readDatabaseTLSFile(c.ClientKey, "client private key")
		if err != nil {
			return nil, err
		}
		keyPEM = bytes.TrimSpace(keyPEM)
		keyBlock, rest := pem.Decode(keyPEM)
		if keyBlock == nil || len(keyBlock.Headers) != 0 || len(bytes.TrimSpace(rest)) != 0 ||
			!bytes.HasPrefix(keyPEM, []byte("-----BEGIN "+keyBlock.Type+"-----")) {
			return nil, errors.New("TLS client private key file contains invalid PEM data")
		}
		cert, err := tls.X509KeyPair(certPEM, keyPEM)
		if err != nil {
			return nil, fmt.Errorf("TLS client certificate/private key is invalid: %w", err)
		}
		cfg.Certificates = []tls.Certificate{cert}
	}
	return cfg, nil
}

func parseDatabaseTLSCertificates(data []byte, role string) ([]*x509.Certificate, error) {
	var certs []*x509.Certificate
	for remaining := bytes.TrimSpace(data); len(remaining) > 0; {
		// pem.Decode skips leading text. A bundle must contain only PEM
		// certificates so truncated or accidentally mixed files fail closed.
		if !bytes.HasPrefix(remaining, []byte("-----BEGIN CERTIFICATE-----")) {
			return nil, fmt.Errorf("TLS %s file contains invalid PEM data", role)
		}
		block, rest := pem.Decode(remaining)
		if block == nil || block.Type != "CERTIFICATE" || len(block.Headers) != 0 {
			return nil, fmt.Errorf("TLS %s file contains invalid PEM data", role)
		}
		cert, err := x509.ParseCertificate(block.Bytes)
		if err != nil {
			return nil, fmt.Errorf("TLS %s is invalid: %w", role, err)
		}
		certs = append(certs, cert)
		remaining = bytes.TrimSpace(rest)
	}
	if len(certs) == 0 {
		return nil, fmt.Errorf("TLS %s file contains no certificates", role)
	}
	return certs, nil
}

func readDatabaseTLSFile(path, role string) ([]byte, error) {
	path = strings.TrimSpace(path)
	if strings.HasPrefix(path, "~/") || strings.HasPrefix(path, "~\\") {
		home, err := os.UserHomeDir()
		if err != nil {
			return nil, fmt.Errorf("TLS %s home directory could not be resolved: %w", role, err)
		}
		path = filepath.Join(home, path[2:])
	}
	// Check before opening to reject devices and named pipes without reading
	// them, and check the opened descriptor again before allocating its data.
	info, err := os.Stat(path)
	if err != nil {
		return nil, fmt.Errorf("TLS %s file could not be accessed: %w", role, err)
	}
	if !info.Mode().IsRegular() {
		return nil, fmt.Errorf("TLS %s must be a regular file", role)
	}
	if info.Size() > databaseTLSFileLimit {
		return nil, fmt.Errorf("TLS %s file exceeds the 4 MiB limit", role)
	}
	file, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("TLS %s file could not be opened: %w", role, err)
	}
	defer file.Close()
	info, err = file.Stat()
	if err != nil {
		return nil, fmt.Errorf("TLS %s file could not be inspected: %w", role, err)
	}
	if !info.Mode().IsRegular() {
		return nil, fmt.Errorf("TLS %s must be a regular file", role)
	}
	data, err := io.ReadAll(io.LimitReader(file, databaseTLSFileLimit+1))
	if err != nil {
		return nil, fmt.Errorf("TLS %s file could not be read: %w", role, err)
	}
	if len(data) > databaseTLSFileLimit {
		return nil, fmt.Errorf("TLS %s file exceeds the 4 MiB limit", role)
	}
	if len(bytes.TrimSpace(data)) == 0 {
		return nil, fmt.Errorf("TLS %s file is empty", role)
	}
	return data, nil
}
