package app

import (
	"crypto/tls"
	"encoding/json"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"

	"github.com/MrTeeett/atlas/internal/config"
)

type adminTLSRequest struct {
	CertPEM string `json:"cert_pem"`
	KeyPEM  string `json:"key_pem"`
}

type adminTLSResponse struct {
	Ok      bool   `json:"ok"`
	Message string `json:"message,omitempty"`
}

func (s *Server) HandleAdminTLS(w http.ResponseWriter, r *http.Request) {
	if s.cfg.ConfigPath == "" {
		http.Error(w, "config path is not configured", http.StatusInternalServerError)
		return
	}
	if r.Method != http.MethodPost {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}

	var req adminTLSRequest
	if err := json.NewDecoder(io.LimitReader(r.Body, 8<<20)).Decode(&req); err != nil {
		http.Error(w, "bad json", http.StatusBadRequest)
		return
	}
	certPEM := strings.TrimSpace(req.CertPEM)
	keyPEM := strings.TrimSpace(req.KeyPEM)
	if certPEM == "" || keyPEM == "" {
		http.Error(w, "cert_pem and key_pem are required", http.StatusBadRequest)
		return
	}
	if _, err := tls.X509KeyPair([]byte(certPEM), []byte(keyPEM)); err != nil {
		http.Error(w, "bad certificate/key pair: "+err.Error(), http.StatusBadRequest)
		return
	}

	// Load config (to preserve other fields) and update TLS paths.
	path := filepath.Clean(s.cfg.ConfigPath)
	cfg, err := config.Load(path)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	cfgDir := filepath.Dir(path)
	certFile := strings.TrimSpace(cfg.TLSCertFile)
	keyFile := strings.TrimSpace(cfg.TLSKeyFile)
	if certFile == "" {
		certFile = "atlas.tls.crt"
	}
	if keyFile == "" {
		keyFile = "atlas.tls.key"
	}

	certAbs := resolveInDir(cfgDir, certFile)
	keyAbs := resolveInDir(cfgDir, keyFile)

	if err := writeFileAtomic(certAbs, []byte(certPEM+"\n"), 0o600); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	if err := writeFileAtomic(keyAbs, []byte(keyPEM+"\n"), 0o600); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	// Prefer storing relative paths when files are in the config directory.
	cfg.TLSCertFile = relIfInDir(cfgDir, certAbs)
	cfg.TLSKeyFile = relIfInDir(cfgDir, keyAbs)
	cfg.CookieSecure = true

	// Persist updated config. Requires restart to apply.
	b, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, append(b, '\n'), 0o600); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	if err := os.Rename(tmp, path); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	writeJSON(w, adminTLSResponse{Ok: true, Message: "saved (restart required)"})
}

func writeFileAtomic(path string, data []byte, perm os.FileMode) error {
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

func resolveInDir(dir, p string) string {
	p = strings.TrimSpace(p)
	if p == "" {
		return ""
	}
	if filepath.IsAbs(p) {
		return filepath.Clean(p)
	}
	return filepath.Join(dir, p)
}

func relIfInDir(dir, absPath string) string {
	dir = filepath.Clean(dir)
	absPath = filepath.Clean(absPath)
	rel, err := filepath.Rel(dir, absPath)
	if err != nil {
		return absPath
	}
	if rel == "." || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return absPath
	}
	if strings.Contains(rel, string(filepath.Separator)) {
		// Keep nested relative paths.
		return rel
	}
	return rel
}
