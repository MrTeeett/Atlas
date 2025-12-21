package config

import (
	"encoding/json"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestLoadCreatesIfMissing(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	path := filepath.Join(dir, "atlas.json")

	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("expected config file created: %v", err)
	}

	if cfg.BasePath == "" || cfg.BasePath == "/" || !strings.HasPrefix(cfg.BasePath, "/") {
		t.Fatalf("expected base_path to be non-root, got %q", cfg.BasePath)
	}

	host, portStr, err := net.SplitHostPort(cfg.Listen)
	if err != nil {
		t.Fatalf("listen split: %v", err)
	}
	if host != "127.0.0.1" {
		t.Fatalf("expected host 127.0.0.1, got %q", host)
	}
	if portStr == "" {
		t.Fatalf("expected non-empty port")
	}

	if !cfg.EnableExec || !cfg.EnableFW || !cfg.EnableAdminActions || !cfg.FSSudo {
		t.Fatalf("expected permissive defaults (all enabled)")
	}
	if len(cfg.FSUsers) != 1 || cfg.FSUsers[0] != "*" {
		t.Fatalf("expected fs_users ['*'], got %#v", cfg.FSUsers)
	}
}

func TestLoadMigratesAddsBasePath(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	path := filepath.Join(dir, "atlas.json")
	if err := os.WriteFile(path, []byte("{\"listen\":\"127.0.0.1:8080\",\"root\":\"/\"}\n"), 0o600); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.BasePath == "" || cfg.BasePath == "/" || !strings.HasPrefix(cfg.BasePath, "/") {
		t.Fatalf("expected base_path to be non-root, got %q", cfg.BasePath)
	}

	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	var m map[string]any
	if err := json.Unmarshal(b, &m); err != nil {
		t.Fatalf("json: %v", err)
	}
	if _, ok := m["base_path"]; !ok {
		t.Fatalf("expected base_path written to file")
	}
}

func TestBasePathNormalized(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	path := filepath.Join(dir, "atlas.json")
	if err := os.WriteFile(path, []byte("{\"listen\":\"127.0.0.1:9000\",\"root\":\"/\",\"base_path\":\"abc/\"}\n"), 0o600); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.BasePath != "/abc" {
		t.Fatalf("expected /abc, got %q", cfg.BasePath)
	}
}
