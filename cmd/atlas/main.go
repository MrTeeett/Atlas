package main

import (
	"context"
	"crypto/sha256"
	"crypto/tls"
	"flag"
	"log"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/MrTeeett/atlas/internal/app"
	"github.com/MrTeeett/atlas/internal/cli"
	"github.com/MrTeeett/atlas/internal/config"
	filesvc "github.com/MrTeeett/atlas/internal/fs"
	"github.com/MrTeeett/atlas/internal/userdb"
)

func main() {
	if len(os.Args) > 1 && os.Args[1] == "fs-helper" {
		os.Exit(filesvc.RunHelper(os.Args[2:]))
	}

	var configPath string
	flag.StringVar(&configPath, "config", envDefault("ATLAS_CONFIG", "atlas.json"), "config path")

	var listenAddr string
	flag.StringVar(&listenAddr, "listen", "", "listen address (overrides config)")
	flag.Parse()

	// User management CLI:
	// atlas user add|del|passwd|list -config atlas.json -user ... [-pass ...]
	if flag.NArg() > 0 && flag.Arg(0) == "user" {
		code, err := cli.RunUserCLI(configPath, flag.Args()[1:])
		if err != nil {
			log.Printf("user: %v", err)
			os.Exit(1)
		}
		os.Exit(code)
	}

	fileCfg, err := config.Load(configPath)
	if err != nil {
		log.Fatalf("load config %s: %v", configPath, err)
	}
	if listenAddr == "" {
		listenAddr = fileCfg.Listen
	}

	certFile := resolveRelativeToConfigDir(configPath, fileCfg.TLSCertFile)
	keyFile := resolveRelativeToConfigDir(configPath, fileCfg.TLSKeyFile)
	tlsEnabled := strings.TrimSpace(certFile) != "" && strings.TrimSpace(keyFile) != ""

	masterKey, err := config.EnsureMasterKeyFile(fileCfg.MasterKeyFile)
	if err != nil {
		log.Fatalf("master key: %v", err)
	}
	sessionSecret := sha256.Sum256(append(append([]byte{}, masterKey...), []byte("atlas:session:v1")...))

	store, err := userdb.Open(fileCfg.UserDBPath, masterKey)
	if err != nil {
		log.Fatalf("user db: %v", err)
	}
	if !store.HasAnyUsers() {
		log.Printf("WARNING: no users in %s; create one via: atlas -config %s user add -user admin -pass <pass>", fileCfg.UserDBPath, configPath)
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
		CookieSecure:       fileCfg.CookieSecure || tlsEnabled,
		EnableExec:         fileCfg.EnableExec,
		EnableFW:           fileCfg.EnableFW,
		FWDBPath:           fileCfg.FWDBPath,
		ConfigPath:         configPath,
		ServiceName:        fileCfg.ServiceName,
		EnableAdminActions: fileCfg.EnableAdminActions,
	}

	srv, err := app.New(cfg)
	if err != nil {
		log.Fatalf("init app: %v", err)
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

		scheme := "http"
		if tlsEnabled {
			scheme = "https"
			httpServer.TLSConfig = &tls.Config{MinVersion: tls.VersionTLS12}
		}
		log.Printf("listening on %s://%s%s", scheme, listenAddr, basePath)
		if basePath != "" {
			log.Printf("login: %s://%s%s/login", scheme, listenAddr, basePath)
		} else {
			log.Printf("login: %s://%s/login", scheme, listenAddr)
		}
		var err error
		if tlsEnabled {
			err = httpServer.ListenAndServeTLS(certFile, keyFile)
		} else {
			err = httpServer.ListenAndServe()
		}
		if err != nil && err != http.ErrServerClosed {
			log.Fatalf("server: %v", err)
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
