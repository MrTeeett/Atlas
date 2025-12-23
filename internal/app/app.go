package app

import (
	"errors"
	iofs "io/fs"
	"net/http"
	"strings"
	"time"

	"github.com/MrTeeett/atlas/internal/auth"
	filesvc "github.com/MrTeeett/atlas/internal/fs"
	"github.com/MrTeeett/atlas/internal/system"
	"github.com/MrTeeett/atlas/internal/ui"
)

type Config struct {
	ListenAddr string
	RootDir    string
	BasePath   string
	AuthStore  auth.Store
	Secret     []byte

	FSSudoEnabled bool
	FSSudoAny     bool
	FSSudoUsers   []string

	CookieSecure       bool
	EnableExec         bool
	EnableFW           bool
	FWDBPath           string
	ConfigPath         string
	ServiceName        string
	EnableAdminActions bool

	LogPath  string
	LogLevel string
}

type Server struct {
	cfg       Config
	auth      *auth.Auth
	stats     *system.StatsService
	info      *system.InfoService
	autostart *system.AutostartService
	fs        *filesvc.Service
	process   *system.ProcessService
	exec      *system.ExecService
	term      *system.TerminalService
	fw        *system.FirewallService
}

func New(cfg Config) (*Server, error) {
	if len(cfg.Secret) < 16 {
		return nil, errors.New("secret too short")
	}
	if cfg.RootDir == "" {
		cfg.RootDir = "/"
	}

	return &Server{
		cfg:       cfg,
		auth:      auth.New(auth.Config{Store: cfg.AuthStore, Secret: cfg.Secret, CookieSecure: cfg.CookieSecure, BasePath: cfg.BasePath}),
		stats:     system.NewStatsService(),
		info:      system.NewInfoService(),
		autostart: system.NewAutostartService(),
		fs:        filesvc.New(filesvc.Config{RootDir: cfg.RootDir, SudoEnabled: cfg.FSSudoEnabled, SudoAny: cfg.FSSudoAny, SudoUsers: cfg.FSSudoUsers, SudoPassword: sudoPasswordProvider(cfg.AuthStore)}),
		process:   system.NewProcessService(),
		exec:      system.NewExecService(system.ExecConfig{Enabled: cfg.EnableExec}),
		term: system.NewTerminalService(system.TerminalConfig{
			Enabled:     cfg.EnableExec,
			SudoEnabled: cfg.FSSudoEnabled,
			SudoAny:     cfg.FSSudoAny,
			SudoUsers:   cfg.FSSudoUsers,
		}),
		fw: system.NewFirewallService(system.FirewallConfig{
			Enabled:      cfg.EnableFW,
			DBPath:       cfg.FWDBPath,
			SudoPassword: sudoPasswordProvider(cfg.AuthStore),
		}),
	}, nil
}

func sudoPasswordProvider(store auth.Store) func(user string) (string, bool, error) {
	type sudoStore interface {
		GetUser(user string) (auth.UserInfo, bool, error)
		GetSudoPassword(user string) (string, bool, error)
	}
	st, ok := store.(sudoStore)
	if !ok {
		return nil
	}
	return func(user string) (string, bool, error) {
		info, ok, err := st.GetUser(user)
		if err != nil || !ok {
			return "", false, err
		}
		if strings.ToLower(strings.TrimSpace(info.Role)) != "admin" {
			return "", false, nil
		}
		return st.GetSudoPassword(user)
	}
}

func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()

	assetHandler := http.FileServer(http.FS(mustSub(ui.FS, "web/assets")))
	mux.Handle("/assets/", http.StripPrefix("/assets/", assetHandler))

	mux.HandleFunc("/login", s.auth.HandleLogin)
	mux.HandleFunc("/logout", s.auth.HandleLogout)

	mux.HandleFunc("/", s.requireHTMLAuth(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/" {
			http.NotFound(w, r)
			return
		}
		http.ServeFileFS(w, r, mustSub(ui.FS, "web"), "index.html")
	}))

	mux.Handle("/api/stats", s.requireAPIAuth(http.HandlerFunc(s.stats.HandleStats)))
	mux.Handle("/api/system/info", s.requireAPIAuth(http.HandlerFunc(s.info.HandleInfo)))
	mux.Handle("/api/system/autostart", s.requireAPIAuth(http.HandlerFunc(s.autostart.HandleAutostart)))
	mux.Handle("/api/processes", s.requireAPIAuth(http.HandlerFunc(s.process.HandleList)))
	mux.Handle("/api/processes/signal", s.requireAPIAuth(s.requireProcs(s.requireCSRF(http.HandlerFunc(s.process.HandleSignal)))))

	mux.Handle("/api/fs/list", s.requireAPIAuth(http.HandlerFunc(s.fs.HandleList)))
	mux.Handle("/api/fs/search", s.requireAPIAuth(http.HandlerFunc(s.fs.HandleSearch)))
	mux.Handle("/api/fs/read", s.requireAPIAuth(http.HandlerFunc(s.fs.HandleRead)))
	mux.Handle("/api/fs/download", s.requireAPIAuth(http.HandlerFunc(s.fs.HandleDownload)))
	mux.Handle("/api/fs/upload", s.requireAPIAuth(s.requireCSRF(http.HandlerFunc(s.fs.HandleUpload))))
	mux.Handle("/api/fs/identities", s.requireAPIAuth(http.HandlerFunc(s.fs.HandleIdentities)))
	mux.Handle("/api/fs/mkdir", s.requireAPIAuth(s.requireCSRF(http.HandlerFunc(s.fs.HandleMkdir))))
	mux.Handle("/api/fs/touch", s.requireAPIAuth(s.requireCSRF(http.HandlerFunc(s.fs.HandleTouch))))
	mux.Handle("/api/fs/write", s.requireAPIAuth(s.requireCSRF(http.HandlerFunc(s.fs.HandleWrite))))
	mux.Handle("/api/fs/rename", s.requireAPIAuth(s.requireCSRF(http.HandlerFunc(s.fs.HandleRename))))
	mux.Handle("/api/fs/delete", s.requireAPIAuth(s.requireCSRF(http.HandlerFunc(s.fs.HandleDelete))))

	mux.Handle("/api/exec", s.requireAPIAuth(s.requireExec(s.requireCSRF(http.HandlerFunc(s.exec.HandleRun)))))
	mux.Handle("/api/term/identities", s.requireAPIAuth(s.requireExec(http.HandlerFunc(s.term.HandleIdentities))))
	mux.Handle("/api/term/session", s.requireAPIAuth(s.requireExec(s.requireCSRF(http.HandlerFunc(s.term.HandleCreate)))))
	mux.Handle("/api/term/session/", s.requireAPIAuth(s.requireExec(s.requireCSRF(http.HandlerFunc(s.term.HandleSession)))))
	mux.Handle("/api/term/complete", s.requireAPIAuth(s.requireExec(http.HandlerFunc(s.term.HandleComplete))))
	mux.Handle("/api/firewall/status", s.requireAPIAuth(s.requireFW(http.HandlerFunc(s.fw.HandleStatus))))
	mux.Handle("/api/firewall/enabled", s.requireAPIAuth(s.requireFW(s.requireCSRF(http.HandlerFunc(s.fw.HandleEnabled)))))
	mux.Handle("/api/firewall/apply", s.requireAPIAuth(s.requireFW(s.requireCSRF(http.HandlerFunc(s.fw.HandleApply)))))
	mux.Handle("/api/firewall/rules", s.requireAPIAuth(s.requireFW(s.requireCSRF(http.HandlerFunc(s.fw.HandleRules)))))
	mux.Handle("/api/firewall/rules/", s.requireAPIAuth(s.requireFW(s.requireCSRF(http.HandlerFunc(s.fw.HandleRuleID)))))
	mux.Handle("/api/ports/usage", s.requireAPIAuth(s.requireFW(http.HandlerFunc(s.fw.HandlePortUsage))))
	mux.Handle("/api/admin/users", s.requireAPIAuth(s.requireAdmin(s.requireCSRF(http.HandlerFunc(s.HandleAdminUsers)))))
	mux.Handle("/api/admin/users/", s.requireAPIAuth(s.requireAdmin(s.requireCSRF(http.HandlerFunc(s.HandleAdminUserID)))))
	mux.Handle("/api/admin/config", s.requireAPIAuth(s.requireAdmin(s.requireCSRF(http.HandlerFunc(s.HandleAdminConfig)))))
	mux.Handle("/api/admin/action", s.requireAPIAuth(s.requireAdmin(s.requireCSRF(http.HandlerFunc(s.HandleAdminAction)))))
	mux.Handle("/api/admin/tls", s.requireAPIAuth(s.requireAdmin(s.requireCSRF(http.HandlerFunc(s.HandleAdminTLS)))))
	mux.Handle("/api/admin/autostart", s.requireAPIAuth(s.requireAdmin(s.requireCSRF(http.HandlerFunc(s.HandleAdminAutostart)))))
	mux.Handle("/api/admin/uninstall", s.requireAPIAuth(s.requireAdmin(s.requireCSRF(http.HandlerFunc(s.HandleAdminUninstall)))))
	mux.Handle("/api/admin/logs", s.requireAPIAuth(s.requireAdmin(http.HandlerFunc(s.HandleAdminLogs))))
	mux.Handle("/api/admin/update", s.requireAPIAuth(s.requireAdmin(s.requireCSRF(http.HandlerFunc(s.HandleAdminUpdate)))))
	mux.Handle("/api/admin/sudo", s.requireAPIAuth(s.requireAdmin(s.requireCSRF(http.HandlerFunc(s.HandleAdminSudo)))))
	mux.Handle("/api/me", s.requireAPIAuth(http.HandlerFunc(s.auth.HandleMe)))

	timeout := http.TimeoutHandler(mux, 60*time.Second, "request timeout")
	inner := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Long-lived terminal streams shouldn't be wrapped with TimeoutHandler.
		if strings.HasPrefix(r.URL.Path, "/api/term/") {
			mux.ServeHTTP(w, r)
			return
		}
		timeout.ServeHTTP(w, r)
	})

	basePath := strings.TrimSpace(s.cfg.BasePath)
	if basePath == "" || basePath == "/" {
		return inner
	}
	if !strings.HasPrefix(basePath, "/") {
		basePath = "/" + basePath
	}
	basePath = strings.TrimRight(basePath, "/")

	// Serve everything under basePath; return 404 for root and other paths.
	strip := http.StripPrefix(basePath, inner)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != basePath && !strings.HasPrefix(r.URL.Path, basePath+"/") {
			http.NotFound(w, r)
			return
		}
		if r.URL.Path == basePath {
			http.Redirect(w, r, basePath+"/", http.StatusFound)
			return
		}
		strip.ServeHTTP(w, r)
	})
}

func (s *Server) requireExec(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		c, ok := auth.ClaimsFromContext(r.Context())
		if !ok || !c.CanExec {
			http.Error(w, "forbidden", http.StatusForbidden)
			return
		}
		next.ServeHTTP(w, r)
	})
}

func (s *Server) requireFW(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		c, ok := auth.ClaimsFromContext(r.Context())
		if !ok || !c.CanFW {
			http.Error(w, "forbidden", http.StatusForbidden)
			return
		}
		next.ServeHTTP(w, r)
	})
}

func (s *Server) requireAdmin(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		c, ok := auth.ClaimsFromContext(r.Context())
		if !ok || strings.ToLower(strings.TrimSpace(c.Role)) != "admin" {
			http.Error(w, "forbidden", http.StatusForbidden)
			return
		}
		next.ServeHTTP(w, r)
	})
}

func (s *Server) requireProcs(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		c, ok := auth.ClaimsFromContext(r.Context())
		if !ok || !c.CanProcs {
			http.Error(w, "forbidden", http.StatusForbidden)
			return
		}
		next.ServeHTTP(w, r)
	})
}

func (s *Server) requireCSRF(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet || r.Method == http.MethodHead {
			next.ServeHTTP(w, r)
			return
		}
		token := r.Header.Get("X-Atlas-CSRF")
		if token == "" || token != s.auth.CSRFToken(r) {
			http.Error(w, "csrf token required", http.StatusForbidden)
			return
		}
		next.ServeHTTP(w, r)
	})
}

func (s *Server) requireAPIAuth(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !s.auth.IsAuthenticated(r) {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		if c, err := s.auth.Claims(r); err == nil {
			r = r.WithContext(auth.WithClaims(r.Context(), c))
		}
		w.Header().Set("Cache-Control", "no-store")
		next.ServeHTTP(w, r)
	})
}

func (s *Server) requireHTMLAuth(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.URL.Path, "/assets/") || strings.HasPrefix(r.URL.Path, "/login") {
			next(w, r)
			return
		}
		if !s.auth.IsAuthenticated(r) {
			http.Redirect(w, r, s.path("/login"), http.StatusFound)
			return
		}
		next(w, r)
	}
}

func (s *Server) path(p string) string {
	if p == "" {
		p = "/"
	}
	if !strings.HasPrefix(p, "/") {
		p = "/" + p
	}
	basePath := strings.TrimSpace(s.cfg.BasePath)
	if basePath == "" || basePath == "/" {
		return p
	}
	if !strings.HasPrefix(basePath, "/") {
		basePath = "/" + basePath
	}
	basePath = strings.TrimRight(basePath, "/")
	if p == "/" {
		return basePath + "/"
	}
	return basePath + p
}

func mustSub(fsys iofs.FS, dir string) iofs.FS {
	sub, err := iofs.Sub(fsys, dir)
	if err != nil {
		panic(err)
	}
	return sub
}
