package app

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

type adminAutostartStatusResponse struct {
	Supported      bool   `json:"supported"`
	ActionsEnabled bool   `json:"actions_enabled"`
	ServiceName    string `json:"service_name"`
	UnitName       string `json:"unit_name"`
	UnitPath       string `json:"unit_path"`
	UnitExists     bool   `json:"unit_exists"`
	Enabled        bool   `json:"enabled"`
	Active         bool   `json:"active"`
	Message        string `json:"message,omitempty"`
}

type adminAutostartSetRequest struct {
	Enabled bool `json:"enabled"`
}

func (s *Server) HandleAdminAutostart(w http.ResponseWriter, r *http.Request) {
	unitName, unitPath, err := s.autostartUnit()
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	systemctl, err := exec.LookPath("systemctl")
	supported := err == nil
	unitExists := fileExists(unitPath)

	resp := adminAutostartStatusResponse{
		Supported:      supported,
		ActionsEnabled: s.cfg.EnableAdminActions,
		ServiceName:    strings.TrimSpace(s.cfg.ServiceName),
		UnitName:       unitName,
		UnitPath:       unitPath,
		UnitExists:     unitExists,
	}

	switch r.Method {
	case http.MethodGet:
		if !supported {
			resp.Message = "systemctl not found"
			writeJSON(w, resp)
			return
		}
		en, enMsg := systemctlBool(r.Context(), systemctl, "is-enabled", unitName)
		ac, acMsg := systemctlBool(r.Context(), systemctl, "is-active", unitName)
		resp.Enabled = en
		resp.Active = ac
		resp.Message = strings.TrimSpace(strings.Trim(strings.Join([]string{enMsg, acMsg}, " "), " "))
		writeJSON(w, resp)
		return

	case http.MethodPost:
	default:
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}

	if !s.cfg.EnableAdminActions {
		http.Error(w, "admin actions are disabled (enable_admin_actions=false)", http.StatusForbidden)
		return
	}
	if !supported {
		http.Error(w, "systemctl not found", http.StatusInternalServerError)
		return
	}

	var req adminAutostartSetRequest
	if err := json.NewDecoder(io.LimitReader(r.Body, 1<<20)).Decode(&req); err != nil {
		http.Error(w, "bad json", http.StatusBadRequest)
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), 8*time.Second)
	defer cancel()

	if req.Enabled {
		if err := s.ensureSystemdUnit(ctx, unitName, unitPath); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		// Enable autostart only; do not start the service here (start/restart is a separate action).
		if err := s.runRoot(ctx, systemctl, "enable", unitName); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		writeJSON(w, adminActionResponse{Ok: true, Message: "autostart enabled"})
		return
	}

	// Disable only; do not delete unit file here (use uninstall/remove).
	// Do not stop the service on disable; only remove autostart.
	if err := s.runRoot(ctx, systemctl, "disable", unitName); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	writeJSON(w, adminActionResponse{Ok: true, Message: "autostart disabled"})
}

func (s *Server) autostartUnit() (unitName, unitPath string, _ error) {
	name := strings.TrimSpace(s.cfg.ServiceName)
	if name == "" {
		return "", "", errors.New("service_name is not configured")
	}
	unitName = name
	if !strings.HasSuffix(unitName, ".service") {
		unitName += ".service"
	}
	unitPath = filepath.Join("/etc/systemd/system", unitName)
	return unitName, unitPath, nil
}

func (s *Server) ensureSystemdUnit(ctx context.Context, unitName, unitPath string) error {
	if fileExists(unitPath) {
		_ = s.runRoot(ctx, "systemctl", "daemon-reload")
		return nil
	}
	if s.cfg.ConfigPath == "" {
		return errors.New("config path is not configured")
	}
	exe, err := os.Executable()
	if err != nil || strings.TrimSpace(exe) == "" {
		return errors.New("cannot determine executable path")
	}

	cfgPath := filepath.Clean(s.cfg.ConfigPath)
	wd := filepath.Dir(cfgPath)

	contents := s.systemdUnitContents(exe, cfgPath, wd)

	tmp, err := os.CreateTemp(wd, ".atlas-unit-*.service")
	if err != nil {
		return err
	}
	tmpPath := tmp.Name()
	_ = tmp.Close()
	defer func() { _ = os.Remove(tmpPath) }()
	if err := os.WriteFile(tmpPath, []byte(contents), 0o644); err != nil {
		return err
	}

	if err := s.runRoot(ctx, "install", "-m", "0644", tmpPath, unitPath); err != nil {
		return err
	}
	if err := s.runRoot(ctx, "systemctl", "daemon-reload"); err != nil {
		return err
	}
	return nil
}

func (s *Server) systemdUnitContents(exePath, cfgPath, workDir string) string {
	exePath = filepath.Clean(exePath)
	cfgPath = filepath.Clean(cfgPath)
	workDir = filepath.Clean(workDir)

	uid := os.Getuid()
	gid := os.Getgid()

	// systemd supports quoting in ExecStart for arguments with spaces.
	return fmt.Sprintf(`[Unit]
Description=Atlas web panel
After=network-online.target
Wants=network-online.target

[Service]
Type=simple
User=%d
Group=%d
WorkingDirectory=%s
ExecStart=%q -config %q
Restart=on-failure
RestartSec=2

[Install]
WantedBy=multi-user.target
`, uid, gid, workDir, exePath, cfgPath)
}

func (s *Server) runRoot(ctx context.Context, bin string, args ...string) error {
	cmd := s.rootCmd(ctx, bin, args...)
	out, err := cmd.CombinedOutput()
	if err != nil {
		msg := strings.TrimSpace(string(out))
		if msg == "" {
			msg = err.Error()
		}
		return fmt.Errorf("%s %s: %s", bin, strings.Join(args, " "), msg)
	}
	return nil
}

func fileExists(path string) bool {
	if strings.TrimSpace(path) == "" {
		return false
	}
	_, err := os.Stat(path)
	return err == nil
}

func systemctlBool(ctx context.Context, systemctl string, subcmd string, unit string) (bool, string) {
	ctx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, systemctl, subcmd, unit)
	out, err := cmd.CombinedOutput()
	if err == nil {
		return true, ""
	}
	s := strings.TrimSpace(string(out))
	// systemctl exits non-zero for disabled/inactive, but that's not an error for us.
	return false, s
}
