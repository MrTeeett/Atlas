package app

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/MrTeeett/atlas/internal/config"
)

type adminUninstallRequest struct {
	Confirm string `json:"confirm"` // must be "DELETE"
}

type adminUninstallResponse struct {
	Ok      bool     `json:"ok"`
	Message string   `json:"message,omitempty"`
	Files   []string `json:"files,omitempty"`
}

func (s *Server) HandleAdminUninstall(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	if !s.cfg.EnableAdminActions {
		http.Error(w, "admin actions are disabled (enable_admin_actions=false)", http.StatusForbidden)
		return
	}

	var req adminUninstallRequest
	if err := json.NewDecoder(io.LimitReader(r.Body, 1<<20)).Decode(&req); err != nil {
		http.Error(w, "bad json", http.StatusBadRequest)
		return
	}
	if strings.TrimSpace(req.Confirm) != "DELETE" {
		http.Error(w, "confirm mismatch", http.StatusBadRequest)
		return
	}

	exe, _ := os.Executable()
	exe = strings.TrimSpace(exe)

	cfgPath := strings.TrimSpace(s.cfg.ConfigPath)
	if cfgPath == "" {
		http.Error(w, "config path is not configured", http.StatusInternalServerError)
		return
	}
	cfgPath = filepath.Clean(cfgPath)
	cfgDir := filepath.Dir(cfgPath)

	files := s.uninstallTargets(cfgPath, cfgDir, exe)
	pid := os.Getpid()

	// Run cleanup via an external shell so it can keep going after stopping this process/systemd unit.
	unitName, unitPath, _ := s.autostartUnit()
	script := buildUninstallScript(unitName, unitPath, cfgDir, pid, files)
	cmd := s.rootCmd(context.Background(), "sh", "-c", script)
	if err := cmd.Start(); err != nil {
		http.Error(w, "failed to start uninstall: "+err.Error(), http.StatusInternalServerError)
		return
	}

	// Respond immediately.
	writeJSON(w, adminUninstallResponse{Ok: true, Message: "uninstall started", Files: files})
}

func (s *Server) uninstallTargets(cfgPath, cfgDir, exePath string) []string {
	cfgDir = filepath.Clean(cfgDir)

	// Avoid config.Load here (it can create/upgrade files). We only need file paths.
	var cfg config.Config
	if b, err := os.ReadFile(cfgPath); err == nil {
		_ = json.Unmarshal(b, &cfg)
	}

	resolve := func(p string, def string) string {
		p = strings.TrimSpace(p)
		if p == "" {
			if def == "" {
				return ""
			}
			return filepath.Join(cfgDir, def)
		}
		if filepath.IsAbs(p) {
			return filepath.Clean(p)
		}
		return filepath.Join(cfgDir, filepath.Clean(p))
	}

	masterKey := resolve(cfg.MasterKeyFile, "atlas.master.key")
	userDB := resolve(cfg.UserDBPath, "atlas.users.db")
	fwDB := resolve(cfg.FWDBPath, "atlas.firewall.db")

	// TLS files: only remove if they look like Atlas-generated defaults inside cfgDir.
	cert := resolve(cfg.TLSCertFile, "")
	key := resolve(cfg.TLSKeyFile, "")
	var tlsFiles []string
	for _, p := range []string{cert, key} {
		if p == "" {
			continue
		}
		base := filepath.Base(p)
		if filepath.Dir(filepath.Clean(p)) == cfgDir && strings.HasPrefix(base, "atlas.tls.") {
			tlsFiles = append(tlsFiles, p)
		}
	}

	var out []string
	if exePath != "" {
		out = append(out, filepath.Clean(exePath))
	}
	out = append(out, cfgPath, masterKey, userDB, fwDB)
	out = append(out, tlsFiles...)
	return dedupNonEmpty(out)
}

func dedupNonEmpty(in []string) []string {
	seen := map[string]struct{}{}
	var out []string
	for _, p := range in {
		p = strings.TrimSpace(p)
		if p == "" {
			continue
		}
		p = filepath.Clean(p)
		if _, ok := seen[p]; ok {
			continue
		}
		seen[p] = struct{}{}
		out = append(out, p)
	}
	return out
}

func buildUninstallScript(unitName, unitPath, cfgDir string, pid int, files []string) string {
	var b strings.Builder
	b.WriteString("set -e\n")
	b.WriteString("export PATH=\"$PATH:/usr/sbin:/sbin\"\n")
	b.WriteString("UNIT=" + shellQuote(strings.TrimSpace(unitName)) + "\n")
	b.WriteString("UNIT_PATH=" + shellQuote(strings.TrimSpace(unitPath)) + "\n")
	b.WriteString("CFG_DIR=" + shellQuote(strings.TrimSpace(cfgDir)) + "\n")
	b.WriteString("PID=" + shellQuote(strconv.Itoa(pid)) + "\n")

	// Disable/stop service if systemd exists.
	b.WriteString("if command -v systemctl >/dev/null 2>&1 && [ -n \"$UNIT\" ]; then\n")
	b.WriteString("  systemctl disable --now \"$UNIT\" >/dev/null 2>&1 || true\n")
	b.WriteString("  systemctl stop \"$UNIT\" >/dev/null 2>&1 || true\n")
	b.WriteString("  if [ -n \"$UNIT_PATH\" ]; then rm -f -- \"$UNIT_PATH\" >/dev/null 2>&1 || true; fi\n")
	b.WriteString("  systemctl daemon-reload >/dev/null 2>&1 || true\n")
	b.WriteString("fi\n")

	// Remove files.
	for _, p := range files {
		p = strings.TrimSpace(p)
		if p == "" {
			continue
		}
		b.WriteString("rm -f -- " + shellQuote(p) + " >/dev/null 2>&1 || true\n")
	}

	// Remove directory if empty.
	b.WriteString("if [ -n \"$CFG_DIR\" ] && [ \"$CFG_DIR\" != \"/\" ]; then rmdir -- \"$CFG_DIR\" >/dev/null 2>&1 || true; fi\n")

	// Stop this process if still running.
	b.WriteString("if [ -n \"$PID\" ]; then kill -TERM \"$PID\" >/dev/null 2>&1 || true; fi\n")
	return b.String()
}

func shellQuote(s string) string {
	// Single-quote safe for POSIX sh.
	if s == "" {
		return "''"
	}
	return "'" + strings.ReplaceAll(s, "'", `'\"'\"'`) + "'"
}
