package logging

import (
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestInitOffAllowsEmptyFile(t *testing.T) {
	prev := slog.Default()
	t.Cleanup(func() { slog.SetDefault(prev) })

	closeFn, err := Init(Config{Level: "off", File: ""})
	if err != nil {
		t.Fatalf("Init(off): %v", err)
	}
	if err := closeFn(); err != nil {
		t.Fatalf("close: %v", err)
	}
}

func TestInitWritesToFile(t *testing.T) {
	prev := slog.Default()
	t.Cleanup(func() { slog.SetDefault(prev) })

	dir := t.TempDir()
	logPath := filepath.Join(dir, "atlas.log")

	closeFn, err := Init(Config{Level: "debug", File: logPath, Stdout: false})
	if err != nil {
		t.Fatalf("Init: %v", err)
	}
	t.Cleanup(func() { _ = closeFn() })

	slog.Info("hello", "k", "v")
	if err := closeFn(); err != nil {
		t.Fatalf("close: %v", err)
	}

	b, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	s := string(b)
	if !strings.Contains(s, "hello") {
		t.Fatalf("expected log to contain message, got: %q", s)
	}
	if !strings.Contains(s, "k=v") && !strings.Contains(s, `k="v"`) {
		t.Fatalf("expected log to contain attribute, got: %q", s)
	}
}

func TestParseLevelInvalid(t *testing.T) {
	if _, _, err := parseLevel("nope"); err == nil {
		t.Fatalf("expected error")
	}
}
