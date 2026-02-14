package main

import (
	"context"
	"crypto/sha256"
	"crypto/tls"
	"flag"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/MrTeeett/atlas/internal/app"
	"github.com/MrTeeett/atlas/internal/cli"
	"github.com/MrTeeett/atlas/internal/config"
	filesvc "github.com/MrTeeett/atlas/internal/fs"
	"github.com/MrTeeett/atlas/internal/logging"
	"github.com/MrTeeett/atlas/internal/userdb"
)

var execCommand = exec.Command

func main() {
	if len(os.Args) > 1 && os.Args[1] == "fs-helper" {
		os.Exit(filesvc.RunHelper(os.Args[2:]))
	}

	var configPath string
	flag.StringVar(&configPath, "config", envDefault("ATLAS_CONFIG", "atlas.json"), "config path")

	var listenAddr string
	flag.StringVar(&listenAddr, "listen", "", "listen address (overrides config)")

	var foreground bool
	flag.BoolVar(&foreground, "foreground", false, "run in foreground (don't detach)")

	var daemonChild bool
	flag.BoolVar(&daemonChild, "daemon-child", false, "internal")
	flag.Parse()

	// User management CLI:
	// atlas user add|del|passwd|list -config atlas.json -user ... [-pass ...]
	if flag.NArg() > 0 && flag.Arg(0) == "user" {
		code, err := cli.RunUserCLI(configPath, flag.Args()[1:])
		if err != nil {
			fmt.Fprintf(os.Stderr, "user: %v\n", err)
			os.Exit(1)
		}
		os.Exit(code)
	}

	fileCfg, err := config.Load(configPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "load config %s: %v\n", configPath, err)
		os.Exit(1)
	}
	if listenAddr == "" {
		listenAddr = fileCfg.Listen
	}

	// Detach early (only when launched from a TTY) so the terminal remains usable.
	if fileCfg.Daemonize && !foreground && !daemonChild && isTerminal(os.Stdout.Fd()) {
		if err := daemonizeSelf(); err != nil {
			fmt.Fprintf(os.Stderr, "daemonize: %v\n", err)
			os.Exit(1)
		}
		return
	}

	tlsInfo, err := ensureTLSBootstrap(configPath, listenAddr, &fileCfg)
	if err != nil {
		fmt.Fprintf(os.Stderr, "tls bootstrap: %v\n", err)
		os.Exit(1)
	}

	logFile := resolveRelativeToConfigDir(configPath, fileCfg.LogFile)
	closeLogs, err := logging.Init(logging.Config{Level: fileCfg.LogLevel, File: logFile, Stdout: fileCfg.LogStdout})
	if err != nil {
		fmt.Fprintf(os.Stderr, "logging: %v\n", err)
		os.Exit(1)
	}
	defer func() { _ = closeLogs() }()

	masterKey, err := config.EnsureMasterKeyFile(fileCfg.MasterKeyFile)
	if err != nil {
		slog.Error("master key", "err", err)
		os.Exit(1)
	}
	sessionSecret := sha256.Sum256(append(append([]byte{}, masterKey...), []byte("atlas:session:v1")...))

	store, err := userdb.Open(fileCfg.UserDBPath, masterKey)
	if err != nil {
		slog.Error("user db", "err", err)
		os.Exit(1)
	}
	if !store.HasAnyUsers() {
		slog.Warn("no users; create one via: atlas -config <cfg> user add -user admin -pass <pass>", "user_db_path", fileCfg.UserDBPath, "config", configPath)
	}

	cfg := app.Config{
		ListenAddr:         listenAddr,
		RootDir:            fileCfg.Root,
		BasePath:           fileCfg.BasePath,
		AuthStore:          store,
		Secret:             sessionSecret[:],
		FSSudoEnabled:      fileCfg.FSSudo,
		FSSudoAny:          len(fileCfg.FSUsers) == 1 && fileCfg.FSUsers[0] == "*",
		FSSudoUsers:        fileCfg.FSUsers,
		CookieSecure:       true,
		EnableExec:         fileCfg.EnableExec,
		EnableFW:           fileCfg.EnableFW,
		FWDBPath:           fileCfg.FWDBPath,
		ConfigPath:         configPath,
		ServiceName:        fileCfg.ServiceName,
		EnableAdminActions: fileCfg.EnableAdminActions,
		LogPath:            logFile,
		LogLevel:           fileCfg.LogLevel,
	}

	srv, err := app.New(cfg)
	if err != nil {
		slog.Error("init app", "err", err)
		os.Exit(1)
	}

	httpServer := &http.Server{
		Addr:              listenAddr,
		Handler:           srv.Handler(),
		ReadHeaderTimeout: 5 * time.Second,
	}

	go func() {
		basePath := strings.TrimRight(strings.TrimSpace(fileCfg.BasePath), "/")
		if basePath == "" || basePath == "/" {
			basePath = ""
		}

		scheme := "https"
		httpServer.TLSConfig = &tls.Config{MinVersion: tls.VersionTLS12}
		logging.InfoOrDebug("listening", "url", fmt.Sprintf("%s://%s%s", scheme, listenAddr, basePath))
		logging.InfoOrDebug("login", "url", fmt.Sprintf("%s://%s%s/login", scheme, listenAddr, basePath))
		if tlsInfo.Generated {
			slog.Warn("using auto-generated self-signed TLS certificate", "cert", tlsInfo.CertFile, "key", tlsInfo.KeyFile)
		}
		err := httpServer.ListenAndServeTLS(tlsInfo.CertFile, tlsInfo.KeyFile)
		if err != nil && err != http.ErrServerClosed {
			slog.Error("server", "err", err)
			os.Exit(1)
		}
	}()

	stop := make(chan os.Signal, 1)
	signal.Notify(stop, syscall.SIGINT, syscall.SIGTERM)
	<-stop

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	_ = httpServer.Shutdown(ctx)
}

func envDefault(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}

func resolveRelativeToConfigDir(configPath, p string) string {
	p = strings.TrimSpace(p)
	if p == "" {
		return ""
	}
	if filepath.IsAbs(p) {
		return filepath.Clean(p)
	}
	return filepath.Join(filepath.Dir(filepath.Clean(configPath)), p)
}

func daemonizeSelf() error {
	args := make([]string, 0, len(os.Args)+1)
	args = append(args, os.Args[1:]...)
	args = append(args, "-daemon-child")
	cmd := execCommand(os.Args[0], args...)
	devNull, err := os.OpenFile(os.DevNull, os.O_RDWR, 0)
	if err != nil {
		return err
	}
	defer devNull.Close()
	cmd.Stdin = devNull
	cmd.Stdout = devNull
	cmd.Stderr = devNull
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	return cmd.Start()
}
