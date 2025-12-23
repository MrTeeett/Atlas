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
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/MrTeeett/atlas/internal/auth"
	"github.com/MrTeeett/atlas/internal/config"
)

type adminStore interface {
	ListUsers() []string
	GetUser(user string) (auth.UserInfo, bool, error)
	UpsertUser(user, pass string) error
	DeleteUser(user string) error
	SetPermissions(user string, role string, canExec bool, canProcs bool, canFW bool, fsSudo bool, fsAny bool, fsUsers []string) error
	SetSudoPassword(user string, pass string) error
	GetSudoPassword(user string) (string, bool, error)
}

func (s *Server) adminStore() (adminStore, error) {
	if s.cfg.AuthStore == nil {
		return nil, errors.New("auth store is not configured")
	}
	st, ok := s.cfg.AuthStore.(adminStore)
	if !ok {
		return nil, errors.New("auth store does not support admin operations")
	}
	return st, nil
}

type adminUser struct {
	User     string   `json:"user"`
	Role     string   `json:"role"`
	CanExec  bool     `json:"can_exec"`
	CanProcs bool     `json:"can_procs"`
	CanFW    bool     `json:"can_fw"`
	FSSudo   bool     `json:"fs_sudo"`
	FSAny    bool     `json:"fs_any"`
	FSUsers  []string `json:"fs_users"`
}

type adminUsersResponse struct {
	Users []adminUser `json:"users"`
}

type adminUserUpsertRequest struct {
	User     string   `json:"user"`
	Pass     string   `json:"pass,omitempty"`
	Role     string   `json:"role"`
	CanExec  bool     `json:"can_exec"`
	CanProcs bool     `json:"can_procs"`
	CanFW    bool     `json:"can_fw"`
	FSSudo   bool     `json:"fs_sudo"`
	FSAny    bool     `json:"fs_any"`
	FSUsers  []string `json:"fs_users"`
}

func (s *Server) HandleAdminUsers(w http.ResponseWriter, r *http.Request) {
	st, err := s.adminStore()
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	switch r.Method {
	case http.MethodGet:
		users := st.ListUsers()
		sort.Strings(users)
		var out []adminUser
		for _, u := range users {
			info, ok, err := st.GetUser(u)
			if err != nil {
				http.Error(w, err.Error(), http.StatusInternalServerError)
				return
			}
			if !ok {
				continue
			}
			out = append(out, adminUser{
				User:     info.User,
				Role:     info.Role,
				CanExec:  info.CanExec,
				CanProcs: info.CanProcs,
				CanFW:    info.CanFW,
				FSSudo:   info.FSSudo,
				FSAny:    info.FSAny,
				FSUsers:  append([]string{}, info.FSUsers...),
			})
		}
		writeJSON(w, adminUsersResponse{Users: out})
		return

	case http.MethodPost:
	default:
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}

	var req adminUserUpsertRequest
	if err := json.NewDecoder(io.LimitReader(r.Body, 1<<20)).Decode(&req); err != nil {
		http.Error(w, "bad json", http.StatusBadRequest)
		return
	}
	req.User = strings.TrimSpace(req.User)
	if req.User == "" {
		http.Error(w, "user is required", http.StatusBadRequest)
		return
	}
	if req.Pass == "" {
		http.Error(w, "pass is required", http.StatusBadRequest)
		return
	}
	if strings.TrimSpace(req.Role) == "" {
		req.Role = "user"
	}

	if err := st.UpsertUser(req.User, req.Pass); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	if err := st.SetPermissions(req.User, req.Role, req.CanExec, req.CanProcs, req.CanFW, req.FSSudo, req.FSAny, req.FSUsers); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	w.WriteHeader(http.StatusCreated)
}

func (s *Server) HandleAdminUserID(w http.ResponseWriter, r *http.Request) {
	// /api/admin/users/{user}
	path := strings.TrimPrefix(r.URL.Path, "/api/admin/users/")
	path = strings.TrimPrefix(path, "/")
	if path == "" {
		http.NotFound(w, r)
		return
	}
	user := path
	st, err := s.adminStore()
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	me, _ := s.auth.Username(r)

	switch r.Method {
	case http.MethodPut:
		var req adminUserUpsertRequest
		if err := json.NewDecoder(io.LimitReader(r.Body, 1<<20)).Decode(&req); err != nil {
			http.Error(w, "bad json", http.StatusBadRequest)
			return
		}
		if strings.TrimSpace(req.Role) == "" {
			req.Role = "user"
		}
		// Optional password update.
		if req.Pass != "" {
			if err := st.UpsertUser(user, req.Pass); err != nil {
				http.Error(w, err.Error(), http.StatusInternalServerError)
				return
			}
		}
		if err := st.SetPermissions(user, req.Role, req.CanExec, req.CanProcs, req.CanFW, req.FSSudo, req.FSAny, req.FSUsers); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		w.WriteHeader(http.StatusNoContent)
		return

	case http.MethodDelete:
		if user == me {
			http.Error(w, "cannot delete current user", http.StatusBadRequest)
			return
		}
		if err := st.DeleteUser(user); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		w.WriteHeader(http.StatusNoContent)
		return
	default:
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
}

type adminConfigResponse struct {
	ConfigPath         string        `json:"config_path"`
	ServiceName        string        `json:"service_name"`
	EnableAdminActions bool          `json:"enable_admin_actions"`
	Config             config.Config `json:"config"`
}

func (s *Server) HandleAdminConfig(w http.ResponseWriter, r *http.Request) {
	if s.cfg.ConfigPath == "" {
		http.Error(w, "config path is not configured", http.StatusInternalServerError)
		return
	}
	switch r.Method {
	case http.MethodGet:
		cfg, err := config.Load(s.cfg.ConfigPath)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		writeJSON(w, adminConfigResponse{
			ConfigPath:         s.cfg.ConfigPath,
			ServiceName:        s.cfg.ServiceName,
			EnableAdminActions: s.cfg.EnableAdminActions,
			Config:             cfg,
		})
		return

	case http.MethodPut:
	default:
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}

	// Update config file on disk. Requires restart to apply.
	var req config.Config
	if err := json.NewDecoder(io.LimitReader(r.Body, 1<<20)).Decode(&req); err != nil {
		http.Error(w, "bad json", http.StatusBadRequest)
		return
	}
	if strings.TrimSpace(req.Listen) == "" {
		http.Error(w, "listen is required", http.StatusBadRequest)
		return
	}
	if strings.TrimSpace(req.Root) == "" {
		req.Root = "/"
	}
	req.BasePath = strings.TrimSpace(req.BasePath)
	if req.BasePath == "" {
		req.BasePath = "/"
	}
	if !strings.HasPrefix(req.BasePath, "/") {
		req.BasePath = "/" + req.BasePath
	}
	req.BasePath = strings.TrimRight(req.BasePath, "/")
	if req.BasePath == "" {
		req.BasePath = "/"
	}

	path := filepath.Clean(s.cfg.ConfigPath)
	b, err := json.MarshalIndent(req, "", "  ")
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
	w.WriteHeader(http.StatusNoContent)
}

type adminActionRequest struct {
	Action  string `json:"action"`  // reboot|shutdown|restart
	Confirm string `json:"confirm"` // must match upper(Action)
}

type adminActionResponse struct {
	Ok      bool   `json:"ok"`
	Message string `json:"message,omitempty"`
}

func (s *Server) HandleAdminAction(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	if !s.cfg.EnableAdminActions {
		http.Error(w, "admin actions are disabled (enable_admin_actions=false)", http.StatusForbidden)
		return
	}
	var req adminActionRequest
	if err := json.NewDecoder(io.LimitReader(r.Body, 1<<20)).Decode(&req); err != nil {
		http.Error(w, "bad json", http.StatusBadRequest)
		return
	}
	action := strings.ToLower(strings.TrimSpace(req.Action))
	confirm := strings.ToUpper(strings.TrimSpace(req.Confirm))
	if confirm != strings.ToUpper(action) {
		http.Error(w, "confirm mismatch", http.StatusBadRequest)
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), 8*time.Second)
	defer cancel()

	var cmd *exec.Cmd
	switch action {
	case "restart":
		if strings.TrimSpace(s.cfg.ServiceName) == "" {
			http.Error(w, "service_name is not configured", http.StatusBadRequest)
			return
		}
		unit := strings.TrimSpace(s.cfg.ServiceName)
		if !strings.HasSuffix(unit, ".service") {
			unit += ".service"
		}
		cmd = s.rootCmd(ctx, "systemctl", "restart", unit)
	case "reboot":
		cmd = s.rootCmd(ctx, "systemctl", "reboot")
	case "shutdown":
		cmd = s.rootCmd(ctx, "systemctl", "poweroff")
	default:
		http.Error(w, "unknown action", http.StatusBadRequest)
		return
	}

	if action == "restart" {
		out, err := cmd.CombinedOutput()
		if err != nil {
			msg := strings.TrimSpace(string(out))
			if msg == "" {
				msg = err.Error()
			}
			http.Error(w, msg, http.StatusInternalServerError)
			return
		}
		msg := strings.TrimSpace(string(out))
		if msg == "" {
			msg = "service restarted"
		}
		writeJSON(w, adminActionResponse{Ok: true, Message: msg})
		return
	}

	// For reboot/shutdown: fire-and-forget.
	if err := cmd.Start(); err != nil {
		http.Error(w, fmt.Sprintf("start command: %v", err), http.StatusInternalServerError)
		return
	}
	writeJSON(w, adminActionResponse{Ok: true, Message: "command started"})
}

func (s *Server) rootCmd(ctx context.Context, bin string, args ...string) *exec.Cmd {
	path, err := exec.LookPath(bin)
	if err != nil {
		// Fallback: try as-is.
		path = bin
	}
	if os.Geteuid() == 0 {
		return exec.CommandContext(ctx, path, args...)
	}
	if sudo, err := exec.LookPath("sudo"); err == nil {
		all := append([]string{"-n", "--", path}, args...)
		return exec.CommandContext(ctx, sudo, all...)
	}
	return exec.CommandContext(ctx, path, args...)
}

type adminLogsResponse struct {
	Enabled   bool     `json:"enabled"`
	Path      string   `json:"path"`
	SizeBytes int64    `json:"size_bytes"`
	Truncated bool     `json:"truncated"`
	Lines     []string `json:"lines"`
}

func (s *Server) HandleAdminLogs(w http.ResponseWriter, r *http.Request) {
	path := strings.TrimSpace(s.cfg.LogPath)
	level := strings.TrimSpace(strings.ToLower(s.cfg.LogLevel))
	enabled := path != "" && level != "off" && level != "none" && level != "disabled"

	if !enabled {
		writeJSON(w, adminLogsResponse{Enabled: false, Path: path, SizeBytes: 0, Lines: nil, Truncated: false})
		return
	}

	path = filepath.Clean(path)

	if r.Method != http.MethodGet {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}

	if r.URL.Query().Get("download") == "1" {
		f, err := os.Open(path)
		if err != nil {
			http.Error(w, err.Error(), http.StatusNotFound)
			return
		}
		defer f.Close()
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		w.Header().Set("Content-Disposition", fmt.Sprintf(`attachment; filename="%s"`, filepath.Base(path)))
		_, _ = io.Copy(w, f)
		return
	}

	n := 200
	if v := strings.TrimSpace(r.URL.Query().Get("n")); v != "" {
		if i, err := strconv.Atoi(v); err == nil {
			n = i
		}
	}
	if n < 1 {
		n = 1
	}
	if n > 5000 {
		n = 5000
	}

	lines, size, truncated, err := tailLines(path, n, 1<<20)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			writeJSON(w, adminLogsResponse{Enabled: true, Path: path, SizeBytes: 0, Lines: nil, Truncated: false})
			return
		}
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	writeJSON(w, adminLogsResponse{Enabled: true, Path: path, SizeBytes: size, Lines: lines, Truncated: truncated})
}

func tailLines(path string, n int, maxBytes int64) (lines []string, size int64, truncated bool, _ error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, 0, false, err
	}
	defer f.Close()

	st, err := f.Stat()
	if err != nil {
		return nil, 0, false, err
	}
	size = st.Size()
	if size == 0 {
		return nil, 0, false, nil
	}

	start := int64(0)
	if size > maxBytes {
		start = size - maxBytes
		truncated = true
	}
	if _, err := f.Seek(start, io.SeekStart); err != nil {
		return nil, 0, false, err
	}
	b, err := io.ReadAll(io.LimitReader(f, maxBytes))
	if err != nil {
		return nil, 0, false, err
	}

	parts := strings.Split(string(b), "\n")
	if truncated && len(parts) > 0 {
		// Drop partial first line.
		parts = parts[1:]
	}
	// Drop trailing empty line.
	if len(parts) > 0 && parts[len(parts)-1] == "" {
		parts = parts[:len(parts)-1]
	}
	if len(parts) <= n {
		return parts, size, truncated, nil
	}
	return parts[len(parts)-n:], size, truncated, nil
}

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	_ = json.NewEncoder(w).Encode(v)
}
