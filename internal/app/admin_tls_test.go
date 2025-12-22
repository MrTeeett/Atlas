package app

import (
	"bytes"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/json"
	"encoding/pem"
	"math/big"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/MrTeeett/atlas/internal/config"
	"github.com/MrTeeett/atlas/internal/userdb"
)

func TestAdminTLSWritesFilesAndUpdatesConfig(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "atlas.json")

	fileCfg := config.Config{
		Listen:        "127.0.0.1:1234",
		Root:          "/",
		BasePath:      "/x",
		ServiceName:   "atlas.service",
		MasterKeyFile: filepath.Join(dir, "atlas.master.key"),
		UserDBPath:    filepath.Join(dir, "atlas.users.db"),
		FWDBPath:      filepath.Join(dir, "atlas.firewall.db"),
	}
	b, _ := json.MarshalIndent(fileCfg, "", "  ")
	if err := os.WriteFile(cfgPath, append(b, '\n'), 0o600); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	masterKey, err := config.EnsureMasterKeyFile(fileCfg.MasterKeyFile)
	if err != nil {
		t.Fatalf("EnsureMasterKeyFile: %v", err)
	}
	store, err := userdb.Open(fileCfg.UserDBPath, masterKey)
	if err != nil {
		t.Fatalf("userdb.Open: %v", err)
	}
	if err := store.UpsertUser("admin", "pw"); err != nil {
		t.Fatalf("UpsertUser: %v", err)
	}
	if err := store.SetPermissions("admin", "admin", true, true, true, true, true, nil); err != nil {
		t.Fatalf("SetPermissions: %v", err)
	}

	sessionSecret := sha256.Sum256(append(append([]byte{}, masterKey...), []byte("atlas:session:v1")...))
	srv, err := New(Config{
		RootDir:    fileCfg.Root,
		BasePath:   "/x",
		AuthStore:  store,
		Secret:     sessionSecret[:],
		ConfigPath: cfgPath,
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	h := srv.Handler()

	// Login.
	form := url.Values{}
	form.Set("user", "admin")
	form.Set("pass", "pw")
	r := httptest.NewRequest(http.MethodPost, "http://example/x/login", strings.NewReader(form.Encode()))
	r.Header.Set("content-type", "application/x-www-form-urlencoded")
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	cookie := strings.Split(w.Header().Get("Set-Cookie"), ";")[0]
	if cookie == "" {
		t.Fatalf("expected cookie")
	}

	// CSRF
	r = httptest.NewRequest(http.MethodGet, "http://example/x/api/me", nil)
	r.Header.Set("Cookie", cookie)
	w = httptest.NewRecorder()
	h.ServeHTTP(w, r)
	var me struct {
		CSRF string `json:"csrf"`
	}
	_ = json.Unmarshal(w.Body.Bytes(), &me)
	if me.CSRF == "" {
		t.Fatalf("expected csrf")
	}

	certPEM, keyPEM := genSelfSignedKeypair(t)

	reqBody, _ := json.Marshal(map[string]string{"cert_pem": certPEM, "key_pem": keyPEM})
	r = httptest.NewRequest(http.MethodPost, "http://example/x/api/admin/tls", bytes.NewReader(reqBody))
	r.Header.Set("content-type", "application/json")
	r.Header.Set("Cookie", cookie)
	r.Header.Set("X-Atlas-CSRF", me.CSRF)
	w = httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if w.Code != http.StatusOK {
		t.Fatalf("status=%d body=%q", w.Code, w.Body.String())
	}

	// Config updated
	cfg2, err := config.Load(cfgPath)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg2.TLSCertFile == "" || cfg2.TLSKeyFile == "" {
		t.Fatalf("expected tls files set, got cert=%q key=%q", cfg2.TLSCertFile, cfg2.TLSKeyFile)
	}
	if !cfg2.CookieSecure {
		t.Fatalf("expected cookie_secure=true")
	}

	// Files exist
	certPath := cfg2.TLSCertFile
	keyPath := cfg2.TLSKeyFile
	if !filepath.IsAbs(certPath) {
		certPath = filepath.Join(dir, certPath)
	}
	if !filepath.IsAbs(keyPath) {
		keyPath = filepath.Join(dir, keyPath)
	}
	if _, err := os.Stat(certPath); err != nil {
		t.Fatalf("cert missing: %v", err)
	}
	if _, err := os.Stat(keyPath); err != nil {
		t.Fatalf("key missing: %v", err)
	}
}

func TestAdminTLSSetPathsUpdatesConfig(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "atlas.json")

	fileCfg := config.Config{
		Listen:        "127.0.0.1:1234",
		Root:          "/",
		BasePath:      "/x",
		ServiceName:   "atlas.service",
		MasterKeyFile: filepath.Join(dir, "atlas.master.key"),
		UserDBPath:    filepath.Join(dir, "atlas.users.db"),
		FWDBPath:      filepath.Join(dir, "atlas.firewall.db"),
	}
	b, _ := json.MarshalIndent(fileCfg, "", "  ")
	if err := os.WriteFile(cfgPath, append(b, '\n'), 0o600); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	masterKey, err := config.EnsureMasterKeyFile(fileCfg.MasterKeyFile)
	if err != nil {
		t.Fatalf("EnsureMasterKeyFile: %v", err)
	}
	store, err := userdb.Open(fileCfg.UserDBPath, masterKey)
	if err != nil {
		t.Fatalf("userdb.Open: %v", err)
	}
	if err := store.UpsertUser("admin", "pw"); err != nil {
		t.Fatalf("UpsertUser: %v", err)
	}
	if err := store.SetPermissions("admin", "admin", true, true, true, true, true, nil); err != nil {
		t.Fatalf("SetPermissions: %v", err)
	}

	sessionSecret := sha256.Sum256(append(append([]byte{}, masterKey...), []byte("atlas:session:v1")...))
	srv, err := New(Config{
		RootDir:    fileCfg.Root,
		BasePath:   "/x",
		AuthStore:  store,
		Secret:     sessionSecret[:],
		ConfigPath: cfgPath,
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	h := srv.Handler()

	// Login.
	form := url.Values{}
	form.Set("user", "admin")
	form.Set("pass", "pw")
	r := httptest.NewRequest(http.MethodPost, "http://example/x/login", strings.NewReader(form.Encode()))
	r.Header.Set("content-type", "application/x-www-form-urlencoded")
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	cookie := strings.Split(w.Header().Get("Set-Cookie"), ";")[0]
	if cookie == "" {
		t.Fatalf("expected cookie")
	}

	// CSRF
	r = httptest.NewRequest(http.MethodGet, "http://example/x/api/me", nil)
	r.Header.Set("Cookie", cookie)
	w = httptest.NewRecorder()
	h.ServeHTTP(w, r)
	var me struct {
		CSRF string `json:"csrf"`
	}
	_ = json.Unmarshal(w.Body.Bytes(), &me)
	if me.CSRF == "" {
		t.Fatalf("expected csrf")
	}

	certPEM, keyPEM := genSelfSignedKeypair(t)
	certPath := filepath.Join(dir, "fullchain.pem")
	keyPath := filepath.Join(dir, "privkey.pem")
	if err := os.WriteFile(certPath, []byte(certPEM), 0o644); err != nil {
		t.Fatalf("WriteFile cert: %v", err)
	}
	if err := os.WriteFile(keyPath, []byte(keyPEM), 0o600); err != nil {
		t.Fatalf("WriteFile key: %v", err)
	}

	reqBody, _ := json.Marshal(map[string]string{"cert_path": certPath, "key_path": keyPath})
	r = httptest.NewRequest(http.MethodPost, "http://example/x/api/admin/tls", bytes.NewReader(reqBody))
	r.Header.Set("content-type", "application/json")
	r.Header.Set("Cookie", cookie)
	r.Header.Set("X-Atlas-CSRF", me.CSRF)
	w = httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if w.Code != http.StatusOK {
		t.Fatalf("status=%d body=%q", w.Code, w.Body.String())
	}

	cfg2, err := config.Load(cfgPath)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg2.TLSCertFile != certPath || cfg2.TLSKeyFile != keyPath {
		t.Fatalf("expected tls files set to paths, got cert=%q key=%q", cfg2.TLSCertFile, cfg2.TLSKeyFile)
	}
	if !cfg2.CookieSecure {
		t.Fatalf("expected cookie_secure=true")
	}

	// Must not create default atlas.tls.* files.
	if _, err := os.Stat(filepath.Join(dir, "atlas.tls.crt")); err == nil {
		t.Fatalf("unexpected atlas.tls.crt created")
	}
	if _, err := os.Stat(filepath.Join(dir, "atlas.tls.key")); err == nil {
		t.Fatalf("unexpected atlas.tls.key created")
	}
}

func genSelfSignedKeypair(t *testing.T) (certPEM, keyPEM string) {
	t.Helper()

	priv, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatalf("GenerateKey: %v", err)
	}
	serial, _ := rand.Int(rand.Reader, big.NewInt(1<<62))
	tpl := x509.Certificate{
		SerialNumber: serial,
		Subject:      pkix.Name{CommonName: "atlas.test"},
		NotBefore:    time.Now().Add(-time.Minute),
		NotAfter:     time.Now().Add(time.Hour),
		KeyUsage:     x509.KeyUsageDigitalSignature | x509.KeyUsageKeyEncipherment,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		DNSNames:     []string{"localhost"},
	}
	der, err := x509.CreateCertificate(rand.Reader, &tpl, &tpl, &priv.PublicKey, priv)
	if err != nil {
		t.Fatalf("CreateCertificate: %v", err)
	}
	var certBuf bytes.Buffer
	_ = pem.Encode(&certBuf, &pem.Block{Type: "CERTIFICATE", Bytes: der})
	var keyBuf bytes.Buffer
	_ = pem.Encode(&keyBuf, &pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(priv)})
	return certBuf.String(), keyBuf.String()
}
