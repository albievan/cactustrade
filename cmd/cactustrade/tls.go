package main

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"fmt"
	"math/big"
	"net"
	"os"
	"path/filepath"
	"time"
)

// tlsConfig loads the operator's certificate, or a self-signed one kept in dir/tls (made,
// or remade when it expires within 30 days). generated reports a new self-signed cert.
func tlsConfig(certFile, keyFile, dir string, now time.Time) (cfg *tls.Config, generated bool, err error) {
	if certFile == "" {
		certFile, keyFile = filepath.Join(dir, "tls", "cert.pem"), filepath.Join(dir, "tls", "key.pem")
		if !certValid(certFile, now.Add(30*24*time.Hour)) {
			if err := selfSigned(certFile, keyFile, now); err != nil {
				return nil, false, err
			}
			generated = true
		}
	}
	cert, err := tls.LoadX509KeyPair(certFile, keyFile)
	if err != nil {
		return nil, false, fmt.Errorf("tls: %w", err)
	}
	return &tls.Config{MinVersion: tls.VersionTLS12, Certificates: []tls.Certificate{cert}}, generated, nil
}

func certValid(certFile string, until time.Time) bool {
	b, err := os.ReadFile(certFile)
	if err != nil {
		return false
	}
	blk, _ := pem.Decode(b)
	if blk == nil {
		return false
	}
	c, err := x509.ParseCertificate(blk.Bytes)
	return err == nil && c.NotAfter.After(until)
}

func selfSigned(certFile, keyFile string, now time.Time) error {
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return fmt.Errorf("tls: %w", err)
	}
	serial, err := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 127))
	if err != nil {
		return fmt.Errorf("tls: %w", err)
	}
	names := []string{"localhost"}
	if h, err := os.Hostname(); err == nil && h != "" {
		names = append(names, h)
	}
	tpl := &x509.Certificate{
		SerialNumber: serial,
		Subject:      pkix.Name{CommonName: "cactusTrade (self-signed)"},
		NotBefore:    now.Add(-time.Hour),
		NotAfter:     now.Add(397 * 24 * time.Hour),
		KeyUsage:     x509.KeyUsageDigitalSignature,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		DNSNames:     names,
		IPAddresses:  []net.IP{net.IPv4(127, 0, 0, 1), net.IPv6loopback},
	}
	der, err := x509.CreateCertificate(rand.Reader, tpl, tpl, &key.PublicKey, key)
	if err != nil {
		return fmt.Errorf("tls: %w", err)
	}
	kb, err := x509.MarshalECPrivateKey(key)
	if err != nil {
		return fmt.Errorf("tls: %w", err)
	}
	if err := os.MkdirAll(filepath.Dir(certFile), 0o700); err != nil {
		return fmt.Errorf("tls: %w", err)
	}
	if err := os.WriteFile(keyFile, pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: kb}), 0o600); err != nil {
		return fmt.Errorf("tls: %w", err)
	}
	if err := os.WriteFile(certFile, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}), 0o600); err != nil {
		return fmt.Errorf("tls: %w", err)
	}
	return nil
}
