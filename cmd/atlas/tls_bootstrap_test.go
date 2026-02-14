package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/MrTeeett/atlas/internal/config"
)

func TestEnsureTLSBootstrapGeneratesAndPersists(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "atlas.json")
	if err := os.WriteFile(cfgPath, []byte("{\"listen\":\"127.0.0.1:18443\",\"root\":\"/\",\"base_path\":\"/\"}\n"), 0o600); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	cfg, err := config.Load(cfgPath)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}

	info, err := ensureTLSBootstrap(cfgPath, cfg.Listen, &cfg)
	if err != nil {
		t.Fatalf("ensureTLSBootstrap: %v", err)
	}
	if !info.Generated {
		t.Fatalf("expected Generated=true on first run")
	}
	if cfg.TLSCertFile != autoTLSCertName || cfg.TLSKeyFile != autoTLSKeyName {
		t.Fatalf("unexpected tls files in config: cert=%q key=%q", cfg.TLSCertFile, cfg.TLSKeyFile)
	}
	if !cfg.CookieSecure {
		t.Fatalf("expected cookie_secure=true in runtime config")
	}
	if err := validateTLSKeyPair(info.CertFile, info.KeyFile); err != nil {
		t.Fatalf("validateTLSKeyPair: %v", err)
	}

	raw, err := os.ReadFile(cfgPath)
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	var onDisk map[string]any
	if err := json.Unmarshal(raw, &onDisk); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}
	if got, _ := onDisk["tls_cert_file"].(string); got != autoTLSCertName {
		t.Fatalf("tls_cert_file=%q", got)
	}
	if got, _ := onDisk["tls_key_file"].(string); got != autoTLSKeyName {
		t.Fatalf("tls_key_file=%q", got)
	}
	if got, ok := onDisk["cookie_secure"].(bool); !ok || !got {
		t.Fatalf("cookie_secure=%v", onDisk["cookie_secure"])
	}

	cfg2, err := config.Load(cfgPath)
	if err != nil {
		t.Fatalf("Load second: %v", err)
	}
	info2, err := ensureTLSBootstrap(cfgPath, cfg2.Listen, &cfg2)
	if err != nil {
		t.Fatalf("ensureTLSBootstrap second: %v", err)
	}
	if info2.Generated {
		t.Fatalf("expected Generated=false on second run")
	}
}

func TestEnsureTLSBootstrapRejectsPartialTLSConfig(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "atlas.json")
	if err := os.WriteFile(cfgPath, []byte("{\"listen\":\"127.0.0.1:18443\",\"root\":\"/\",\"base_path\":\"/\",\"tls_cert_file\":\"cert.pem\"}\n"), 0o600); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	cfg, err := config.Load(cfgPath)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}

	_, err = ensureTLSBootstrap(cfgPath, cfg.Listen, &cfg)
	if err == nil {
		t.Fatalf("expected error")
	}
	if !strings.Contains(err.Error(), "both tls_cert_file and tls_key_file must be set") {
		t.Fatalf("unexpected error: %v", err)
	}
}
