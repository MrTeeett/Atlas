package userdb

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"
)

func TestStoreReloadIfChanged(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	dbPath := filepath.Join(dir, "atlas.users.db")

	masterKey := bytes.Repeat([]byte{0x11}, 32)

	s1, err := Open(dbPath, masterKey)
	if err != nil {
		t.Fatalf("Open s1: %v", err)
	}
	if s1.HasAnyUsers() {
		t.Fatalf("expected empty db")
	}

	s2, err := Open(dbPath, masterKey)
	if err != nil {
		t.Fatalf("Open s2: %v", err)
	}
	if err := s2.UpsertUser("admin", "pw"); err != nil {
		t.Fatalf("UpsertUser: %v", err)
	}

	// s1 should pick up on-disk changes without reopening.
	ok, err := s1.Authenticate("admin", "pw")
	if err != nil {
		t.Fatalf("Authenticate: %v", err)
	}
	if !ok {
		t.Fatalf("expected auth ok after reload")
	}

	// Deleting the file should be handled as empty db.
	if err := os.Remove(dbPath); err != nil {
		t.Fatalf("Remove: %v", err)
	}
	ok, err = s1.Authenticate("admin", "pw")
	if err != nil {
		t.Fatalf("Authenticate after delete: %v", err)
	}
	if ok {
		t.Fatalf("expected auth false after delete")
	}
}
