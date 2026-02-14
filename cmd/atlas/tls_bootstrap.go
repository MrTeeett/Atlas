package main

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/json"
	"encoding/pem"
	"errors"
	"math/big"
	"net"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/MrTeeett/atlas/internal/config"
)

const (
	autoTLSCertName = "atlas.tls.crt"
	autoTLSKeyName  = "atlas.tls.key"
)

type tlsBootstrapInfo struct {
	CertFile  string
	KeyFile   string
	Generated bool
}

func ensureTLSBootstrap(configPath, listenAddr string, cfg *config.Config) (tlsBootstrapInfo, error) {
	if cfg == nil {
		return tlsBootstrapInfo{}, errors.New("nil config")
	}

	certFile := resolveRelativeToConfigDir(configPath, cfg.TLSCertFile)
	keyFile := resolveRelativeToConfigDir(configPath, cfg.TLSKeyFile)

	switch {
	case certFile != "" && keyFile != "":
		if err := validateTLSKeyPair(certFile, keyFile); err != nil {
			return tlsBootstrapInfo{}, err
		}
		cfg.CookieSecure = true
		return tlsBootstrapInfo{CertFile: certFile, KeyFile: keyFile}, nil
	case certFile != "" || keyFile != "":
		return tlsBootstrapInfo{}, errors.New("both tls_cert_file and tls_key_file must be set")
	}

	cfgDir := filepath.Dir(filepath.Clean(configPath))
	certFile = filepath.Join(cfgDir, autoTLSCertName)
	keyFile = filepath.Join(cfgDir, autoTLSKeyName)

	certExists := fileExists(certFile)
	keyExists := fileExists(keyFile)
	if certExists != keyExists {
		return tlsBootstrapInfo{}, errors.New("auto TLS files are incomplete; remove both atlas.tls.crt and atlas.tls.key or provide explicit tls_cert_file/tls_key_file")
	}

	if certExists && keyExists {
		if err := validateTLSKeyPair(certFile, keyFile); err != nil {
			return tlsBootstrapInfo{}, err
		}
		cfg.TLSCertFile = autoTLSCertName
		cfg.TLSKeyFile = autoTLSKeyName
		cfg.CookieSecure = true
		_ = persistTLSBootstrap(configPath, cfg)
		return tlsBootstrapInfo{CertFile: certFile, KeyFile: keyFile}, nil
	}

	dnsNames, ipAddrs := tlsSANs(listenAddr)
	certPEM, keyPEM, err := makeSelfSignedTLS(dnsNames, ipAddrs)
	if err != nil {
		return tlsBootstrapInfo{}, err
	}

	if err := writeTLSFileAtomic(certFile, certPEM, 0o600); err != nil {
		return tlsBootstrapInfo{}, err
	}
	if err := writeTLSFileAtomic(keyFile, keyPEM, 0o600); err != nil {
		return tlsBootstrapInfo{}, err
	}

	cfg.TLSCertFile = autoTLSCertName
	cfg.TLSKeyFile = autoTLSKeyName
	cfg.CookieSecure = true
	if err := persistTLSBootstrap(configPath, cfg); err != nil {
		return tlsBootstrapInfo{}, err
	}
	return tlsBootstrapInfo{CertFile: certFile, KeyFile: keyFile, Generated: true}, nil
}

func validateTLSKeyPair(certFile, keyFile string) error {
	certPEM, err := os.ReadFile(certFile)
	if err != nil {
		return err
	}
	keyPEM, err := os.ReadFile(keyFile)
	if err != nil {
		return err
	}
	_, err = tls.X509KeyPair(certPEM, keyPEM)
	if err != nil {
		return err
	}
	return nil
}

func makeSelfSignedTLS(dnsNames []string, ipAddrs []net.IP) ([]byte, []byte, error) {
	priv, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return nil, nil, err
	}

	serial, err := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 128))
	if err != nil {
		return nil, nil, err
	}

	cn := "atlas.local"
	if len(dnsNames) > 0 {
		cn = dnsNames[0]
	}

	tpl := &x509.Certificate{
		SerialNumber: serial,
		Subject:      pkix.Name{CommonName: cn},
		NotBefore:    time.Now().Add(-5 * time.Minute),
		NotAfter:     time.Now().Add(365 * 24 * time.Hour),
		KeyUsage:     x509.KeyUsageDigitalSignature | x509.KeyUsageKeyEncipherment,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		DNSNames:     dnsNames,
		IPAddresses:  ipAddrs,
	}

	der, err := x509.CreateCertificate(rand.Reader, tpl, tpl, &priv.PublicKey, priv)
	if err != nil {
		return nil, nil, err
	}

	certPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
	keyDER, err := x509.MarshalPKCS8PrivateKey(priv)
	if err != nil {
		return nil, nil, err
	}
	keyPEM := pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: keyDER})

	return certPEM, keyPEM, nil
}

func tlsSANs(listenAddr string) ([]string, []net.IP) {
	dnsSet := map[string]struct{}{"localhost": {}}
	ipSet := map[string]net.IP{
		"127.0.0.1": net.ParseIP("127.0.0.1"),
		"::1":       net.ParseIP("::1"),
	}

	host := strings.TrimSpace(listenAddr)
	if h, _, err := net.SplitHostPort(strings.TrimSpace(listenAddr)); err == nil {
		host = h
	}
	host = strings.Trim(strings.TrimSpace(host), "[]")

	if host == "" || host == "0.0.0.0" || host == "::" {
		if hn, err := os.Hostname(); err == nil {
			hn = strings.TrimSpace(hn)
			if hn != "" {
				dnsSet[hn] = struct{}{}
			}
		}
	} else if ip := net.ParseIP(host); ip != nil {
		ipSet[ip.String()] = ip
	} else {
		dnsSet[host] = struct{}{}
	}

	dnsNames := make([]string, 0, len(dnsSet))
	for n := range dnsSet {
		dnsNames = append(dnsNames, n)
	}
	slices.Sort(dnsNames)

	ipAddrs := make([]net.IP, 0, len(ipSet))
	keys := make([]string, 0, len(ipSet))
	for k := range ipSet {
		keys = append(keys, k)
	}
	slices.Sort(keys)
	for _, k := range keys {
		ipAddrs = append(ipAddrs, ipSet[k])
	}

	return dnsNames, ipAddrs
}

func persistTLSBootstrap(configPath string, cfg *config.Config) error {
	path := filepath.Clean(configPath)
	b, err := os.ReadFile(path)
	if err != nil {
		return err
	}

	var raw map[string]json.RawMessage
	if err := json.Unmarshal(b, &raw); err != nil {
		return err
	}
	if raw == nil {
		raw = map[string]json.RawMessage{}
	}

	certJSON, err := json.Marshal(strings.TrimSpace(cfg.TLSCertFile))
	if err != nil {
		return err
	}
	keyJSON, err := json.Marshal(strings.TrimSpace(cfg.TLSKeyFile))
	if err != nil {
		return err
	}
	raw["tls_cert_file"] = certJSON
	raw["tls_key_file"] = keyJSON
	raw["cookie_secure"] = json.RawMessage("true")

	out, err := json.MarshalIndent(raw, "", "  ")
	if err != nil {
		return err
	}
	return writeTLSFileAtomic(path, append(out, '\n'), 0o600)
}

func writeTLSFileAtomic(path string, data []byte, perm os.FileMode) error {
	path = filepath.Clean(path)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, perm); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

func fileExists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}
