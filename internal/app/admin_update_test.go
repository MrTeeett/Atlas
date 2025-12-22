package app

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/MrTeeett/atlas/internal/buildinfo"
)

func TestResolveUpdateChannelAuto(t *testing.T) {
	oldCh, oldVer := buildinfo.Channel, buildinfo.Version
	t.Cleanup(func() { buildinfo.Channel, buildinfo.Version = oldCh, oldVer })

	buildinfo.Channel = "dev"
	buildinfo.Version = "dev"
	if got := resolveUpdateChannel("auto"); got != "dev" {
		t.Fatalf("expected dev, got %q", got)
	}

	buildinfo.Channel = "stable"
	buildinfo.Version = "v0.1.0"
	if got := resolveUpdateChannel("auto"); got != "stable" {
		t.Fatalf("expected stable, got %q", got)
	}
}

func TestIsValidRepo(t *testing.T) {
	ok := []string{"MrTeeett/Atlas", "a/b", "A0-_./b0-_."}
	for _, s := range ok {
		if !isValidRepo(s) {
			t.Fatalf("expected valid repo: %q", s)
		}
	}
	bad := []string{"", "a", "a/", "/b", "a/b/c", "a b/c", "a/ b", "a/б"}
	for _, s := range bad {
		if isValidRepo(s) {
			t.Fatalf("expected invalid repo: %q", s)
		}
	}
}

func TestChecksumForFile(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "SHA256SUMS.txt")
	body := "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa  atlas_dev_linux_amd64.tar.gz\n" +
		"bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb  other\n"
	if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	sum, err := checksumForFile(p, "atlas_dev_linux_amd64.tar.gz")
	if err != nil {
		t.Fatalf("checksumForFile: %v", err)
	}
	if sum != strings.Repeat("a", 64) {
		t.Fatalf("unexpected sum: %q", sum)
	}
}

func TestExtractTarGzFile(t *testing.T) {
	dir := t.TempDir()
	archive := filepath.Join(dir, "a.tar.gz")
	out := filepath.Join(dir, "atlas.bin")

	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	writeFile := func(name string, data []byte) {
		h := &tar.Header{Name: name, Mode: 0o755, Size: int64(len(data))}
		if err := tw.WriteHeader(h); err != nil {
			t.Fatalf("WriteHeader: %v", err)
		}
		if _, err := tw.Write(data); err != nil {
			t.Fatalf("Write: %v", err)
		}
	}
	writeFile("atlas", []byte("#!/bin/sh\necho ok\n"))
	writeFile("atlas.json", []byte("{}\n"))
	if err := tw.Close(); err != nil {
		t.Fatalf("tar close: %v", err)
	}
	if err := gz.Close(); err != nil {
		t.Fatalf("gz close: %v", err)
	}
	if err := os.WriteFile(archive, buf.Bytes(), 0o600); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	if err := extractTarGzFile(archive, "atlas", out); err != nil {
		t.Fatalf("extractTarGzFile: %v", err)
	}
	b, err := os.ReadFile(out)
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	if !bytes.Contains(b, []byte("echo ok")) {
		t.Fatalf("unexpected output: %q", string(b))
	}
}

