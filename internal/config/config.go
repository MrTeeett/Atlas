package config

import (
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"math/big"
	"os"
	"path/filepath"
	"strings"
)

type Config struct {
	Listen string `json:"listen"`
	Root   string `json:"root"`

	// BasePath is a URL path prefix (e.g. "/abc123") under which the whole UI/API is served.
	// Use "/" to serve at root.
	BasePath string `json:"base_path"`

	// TLSCertFile/TLSKeyFile enable HTTPS when both are set.
	// Relative paths are resolved against the config directory.
	TLSCertFile string `json:"tls_cert_file,omitempty"`
	TLSKeyFile  string `json:"tls_key_file,omitempty"`

	CookieSecure       bool   `json:"cookie_secure"`
	EnableExec         bool   `json:"enable_exec"`
	EnableFW           bool   `json:"enable_firewall"`
	EnableAdminActions bool   `json:"enable_admin_actions"`
	ServiceName        string `json:"service_name"`

	// Daemonize detaches the process when started from a TTY (so it doesn't block the shell).
	// It is ignored when stdout isn't a TTY (e.g. systemd).
	Daemonize bool `json:"daemonize"`

	// LogLevel controls logging verbosity: "debug", "info", "warn", "error", "off".
	LogLevel string `json:"log_level"`
	// LogFile is where logs are written. Relative paths are resolved against the config directory.
	LogFile string `json:"log_file"`
	// LogStdout mirrors logs to stdout (default false).
	LogStdout bool `json:"log_stdout"`

	FSSudo  bool     `json:"fs_sudo"`
	FSUsers []string `json:"fs_users"`

	// MasterKeyFile stores a 32-byte random key (base64).
	// It's used to derive both session signing secret and user DB encryption key.
	MasterKeyFile string `json:"master_key_file"`

	UserDBPath string `json:"user_db_path"`
	FWDBPath   string `json:"firewall_db_path"`
}

// DefaultAllAllowed returns a config with permissive defaults (everything enabled).
// It still binds to 127.0.0.1 by default; expose it via SSH tunnel/reverse proxy if needed.
func DefaultAllAllowed(configPath string) Config {
	port, err := randomPort(20_000, 60_000)
	if err != nil {
		port = 8080
	}
	hash, err := randomHex(12)
	if err != nil {
		hash = "atlas"
	}
	cfg := Config{
		Listen:             fmt.Sprintf("127.0.0.1:%d", port),
		Root:               "/",
		BasePath:           "/" + hash,
		CookieSecure:       false,
		EnableExec:         true,
		EnableFW:           true,
		EnableAdminActions: true,
		ServiceName:        "atlas.service",
		FSSudo:             true,
		FSUsers:            []string{"*"},
	}
	cfg.applyDefaults(configPath)
	return cfg
}

func Load(path string) (Config, error) {
	path = filepath.Clean(path)
	b, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			cfg := DefaultAllAllowed(path)
			if err := writeFileAtomic(path, cfg, 0o600); err != nil {
				return Config{}, err
			}
			return cfg, nil
		}
		return Config{}, err
	}

	var raw map[string]json.RawMessage
	_ = json.Unmarshal(b, &raw)

	var cfg Config
	if err := json.Unmarshal(b, &cfg); err != nil {
		return Config{}, err
	}

	// Migrate old configs (no base_path) by generating a random base_path, and (optionally)
	// randomizing listen port if it is empty or the old default.
	_, hasBasePath := raw["base_path"]
	basePathVal := strings.TrimSpace(cfg.BasePath)
	needsUpgrade := !hasBasePath || basePathVal == ""

	changed := false
	if needsUpgrade {
		if basePathVal == "" {
			if hash, err := randomHex(12); err == nil {
				cfg.BasePath = "/" + hash
				changed = true
			}
		}
		if strings.TrimSpace(cfg.Listen) == "" || strings.TrimSpace(cfg.Listen) == "127.0.0.1:8080" {
			if port, err := randomPort(20_000, 60_000); err == nil {
				cfg.Listen = fmt.Sprintf("127.0.0.1:%d", port)
				changed = true
			}
		}
	}

	cfg.applyDefaults(path)
	if cfg.Listen == "" {
		return Config{}, errors.New("config: listen is required")
	}
	if cfg.Root == "" {
		cfg.Root = "/"
	}

	if changed {
		if err := writeFileAtomic(path, cfg, 0o600); err != nil {
			return Config{}, err
		}
	}
	return cfg, nil
}

func (c *Config) applyDefaults(configPath string) {
	if c.Listen == "" {
		c.Listen = "127.0.0.1:8080"
	}
	if c.Root == "" {
		c.Root = "/"
	}
	if strings.TrimSpace(c.BasePath) == "" {
		c.BasePath = "/"
	}
	c.BasePath = normalizeBasePath(c.BasePath)
	c.TLSCertFile = strings.TrimSpace(c.TLSCertFile)
	c.TLSKeyFile = strings.TrimSpace(c.TLSKeyFile)
	if c.TLSCertFile != "" {
		c.TLSCertFile = filepath.Clean(c.TLSCertFile)
	}
	if c.TLSKeyFile != "" {
		c.TLSKeyFile = filepath.Clean(c.TLSKeyFile)
	}
	if c.MasterKeyFile == "" {
		c.MasterKeyFile = filepath.Join(filepath.Dir(configPath), "atlas.master.key")
	}
	if c.UserDBPath == "" {
		c.UserDBPath = filepath.Join(filepath.Dir(configPath), "atlas.users.db")
	}
	if c.FWDBPath == "" {
		c.FWDBPath = filepath.Join(filepath.Dir(configPath), "atlas.firewall.db")
	}
	if c.ServiceName == "" {
		c.ServiceName = "atlas.service"
	}
	if strings.TrimSpace(c.LogLevel) == "" {
		c.LogLevel = "info"
	}
	if strings.TrimSpace(c.LogFile) == "" {
		c.LogFile = filepath.Join(filepath.Dir(configPath), "atlas.log")
	}
	c.FSUsers = normalizeCSV(c.FSUsers)
}

func EnsureMasterKeyFile(path string) ([]byte, error) {
	path = filepath.Clean(path)
	b, err := os.ReadFile(path)
	if err == nil {
		key, err := decodeKey(strings.TrimSpace(string(b)))
		if err != nil {
			return nil, err
		}
		if len(key) != 32 {
			return nil, errors.New("master key must be 32 bytes")
		}
		return key, nil
	}
	if !errors.Is(err, os.ErrNotExist) {
		return nil, err
	}

	key := make([]byte, 32)
	if _, err := rand.Read(key); err != nil {
		return nil, err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return nil, err
	}
	enc := base64.RawStdEncoding.EncodeToString(key)
	if err := os.WriteFile(path, []byte(enc+"\n"), 0o600); err != nil {
		return nil, err
	}
	return key, nil
}

func decodeKey(s string) ([]byte, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return nil, errors.New("empty key")
	}
	return base64.RawStdEncoding.DecodeString(s)
}

func normalizeCSV(in []string) []string {
	var out []string
	for _, s := range in {
		s = strings.TrimSpace(s)
		if s == "" {
			continue
		}
		out = append(out, s)
	}
	return out
}

func writeFileAtomic(path string, cfg Config, perm os.FileMode) error {
	b, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return err
	}

	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}

	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, append(b, '\n'), perm); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

func randomHex(nBytes int) (string, error) {
	b := make([]byte, nBytes)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return hex.EncodeToString(b), nil
}

func randomPort(min, max int) (int, error) {
	if min <= 0 || max <= 0 || min > max {
		return 0, errors.New("invalid port range")
	}
	span := max - min + 1
	n, err := rand.Int(rand.Reader, big.NewInt(int64(span)))
	if err != nil {
		return 0, err
	}
	return min + int(n.Int64()), nil
}

func normalizeBasePath(p string) string {
	p = strings.TrimSpace(p)
	if p == "" || p == "/" {
		return "/"
	}
	if !strings.HasPrefix(p, "/") {
		p = "/" + p
	}
	p = strings.TrimRight(p, "/")
	if p == "" {
		return "/"
	}
	return p
}
