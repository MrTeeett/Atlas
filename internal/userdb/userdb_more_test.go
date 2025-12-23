package userdb

import (
	"bytes"
	"path/filepath"
	"reflect"
	"testing"
)

func TestUserCRUDAndPermissions(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "atlas.users.db")
	masterKey := bytes.Repeat([]byte{0x22}, 32)

	s, err := Open(dbPath, masterKey)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}

	if err := s.UpsertUser("alice", "pw"); err != nil {
		t.Fatalf("UpsertUser: %v", err)
	}
	if err := s.SetPermissions("alice", "admin", true, true, true, true, false, []string{"root"}); err != nil {
		t.Fatalf("SetPermissions: %v", err)
	}

	info, ok, err := s.GetUser("alice")
	if err != nil {
		t.Fatalf("GetUser: %v", err)
	}
	if !ok {
		t.Fatalf("expected user")
	}
	if info.Role != "admin" || !info.CanExec || !info.CanProcs || !info.CanFW || !info.FSSudo || info.FSAny {
		t.Fatalf("unexpected perms: %#v", info)
	}
	if !reflect.DeepEqual(info.FSUsers, []string{"root"}) {
		t.Fatalf("unexpected fs_users: %#v", info.FSUsers)
	}

	users := s.ListUsers()
	if len(users) != 1 || users[0] != "alice" {
		t.Fatalf("ListUsers: %#v", users)
	}

	if err := s.DeleteUser("alice"); err != nil {
		t.Fatalf("DeleteUser: %v", err)
	}
	if s.HasAnyUsers() {
		t.Fatalf("expected empty after delete")
	}
}
