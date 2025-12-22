package logging

import (
	"errors"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync"
)

type Config struct {
	Level  string
	File   string
	Stdout bool
}

var (
	mu     sync.Mutex
	closer io.Closer
)

func Init(cfg Config) (func() error, error) {
	level, enabled, err := parseLevel(cfg.Level)
	if err != nil {
		return nil, err
	}
	if !enabled {
		slog.SetDefault(slog.New(slog.NewTextHandler(io.Discard, &slog.HandlerOptions{Level: slog.LevelError})))
		return func() error { return nil }, nil
	}

	path := strings.TrimSpace(cfg.File)
	if path == "" {
		return nil, errors.New("log_file is empty")
	}
	path = filepath.Clean(path)

	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return nil, err
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o640)
	if err != nil {
		return nil, err
	}

	var w io.Writer = f
	if cfg.Stdout {
		w = io.MultiWriter(os.Stdout, f)
	}
	h := slog.NewTextHandler(w, &slog.HandlerOptions{Level: level})
	slog.SetDefault(slog.New(h))

	mu.Lock()
	prev := closer
	closer = f
	mu.Unlock()
	_ = prev

	return func() error {
		mu.Lock()
		c := closer
		if closer == f {
			closer = nil
		}
		mu.Unlock()
		if c != nil {
			return c.Close()
		}
		return nil
	}, nil
}

func parseLevel(s string) (lvl slog.Level, enabled bool, _ error) {
	s = strings.TrimSpace(strings.ToLower(s))
	switch s {
	case "", "info":
		return slog.LevelInfo, true, nil
	case "debug":
		return slog.LevelDebug, true, nil
	case "warn", "warning":
		return slog.LevelWarn, true, nil
	case "error":
		return slog.LevelError, true, nil
	case "off", "none", "disabled":
		return slog.LevelError, false, nil
	default:
		return slog.LevelInfo, true, errors.New("bad log_level (use debug/info/warn/error/off)")
	}
}
