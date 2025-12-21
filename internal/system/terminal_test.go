package system

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/MrTeeett/atlas/internal/auth"
)

func TestTerminalDisabledCreateForbidden(t *testing.T) {
	t.Parallel()

	s := NewTerminalService(TerminalConfig{Enabled: false})
	req := httptest.NewRequest(http.MethodPost, "http://example/api/term/session", bytes.NewReader([]byte(`{"as":"self","cols":80,"rows":24}`)))
	rr := httptest.NewRecorder()
	s.HandleCreate(rr, req)
	if rr.Code != http.StatusForbidden {
		t.Fatalf("expected 403, got %d", rr.Code)
	}
}

func TestTerminalIdentitiesNoSudoWithoutClaims(t *testing.T) {
	t.Parallel()

	s := NewTerminalService(TerminalConfig{Enabled: true, SudoEnabled: true})
	s.sudoPath = "" // force no sudo

	req := httptest.NewRequest(http.MethodGet, "http://example/api/term/identities", nil)
	rr := httptest.NewRecorder()
	s.HandleIdentities(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rr.Code)
	}
	var resp identitiesResponse
	if err := json.Unmarshal(rr.Body.Bytes(), &resp); err != nil {
		t.Fatalf("json: %v", err)
	}
	if len(resp.Identities) != 1 || resp.Identities[0].ID != "self" {
		t.Fatalf("identities=%#v", resp.Identities)
	}
}

func TestTerminalAllowedSudoUsersIntersection(t *testing.T) {
	t.Parallel()

	s := NewTerminalService(TerminalConfig{Enabled: true, SudoEnabled: true, SudoAny: false, SudoUsers: []string{"root", "daemon"}})
	claims := auth.Claims{UserInfo: auth.UserInfo{FSSudo: true, FSAny: false, FSUsers: []string{"root", "nobody"}}}
	got := s.allowedSudoUsers(claims)
	if len(got) != 1 || got[0] != "root" {
		t.Fatalf("got=%#v", got)
	}

	// SudoAny + FSAny + explicit list.
	s2 := NewTerminalService(TerminalConfig{Enabled: true, SudoEnabled: true, SudoAny: true})
	claims2 := auth.Claims{UserInfo: auth.UserInfo{FSSudo: true, FSAny: false, FSUsers: []string{"a", "b"}}}
	got2 := s2.allowedSudoUsers(claims2)
	if len(got2) != 2 || got2[0] != "a" || got2[1] != "b" {
		t.Fatalf("got2=%#v", got2)
	}

	// HandleIdentities should include allowed users when claims allow.
	s3 := NewTerminalService(TerminalConfig{Enabled: true, SudoEnabled: true, SudoAny: false, SudoUsers: []string{"root"}})
	s3.sudoPath = "/bin/sudo"
	ctx := auth.WithClaims(context.Background(), claims)
	req := httptest.NewRequest(http.MethodGet, "http://example/api/term/identities", nil).WithContext(ctx)
	rr := httptest.NewRecorder()
	s3.HandleIdentities(rr, req)
	var resp identitiesResponse
	_ = json.Unmarshal(rr.Body.Bytes(), &resp)
	if len(resp.Identities) < 2 {
		t.Fatalf("identities=%#v", resp.Identities)
	}
}

func TestTerminalValidateAs(t *testing.T) {
	t.Parallel()

	s := NewTerminalService(TerminalConfig{Enabled: true, SudoEnabled: true, SudoAny: false, SudoUsers: []string{"root"}})

	// No claims -> forbidden.
	req := httptest.NewRequest(http.MethodPost, "http://example/api/term/session", nil)
	if err := s.validateAs(req, "root"); err == nil {
		t.Fatalf("expected error without claims")
	}

	// Claims but no FSSudo -> forbidden.
	ctx := auth.WithClaims(context.Background(), auth.Claims{UserInfo: auth.UserInfo{FSSudo: false}})
	req = req.WithContext(ctx)
	if err := s.validateAs(req, "root"); err == nil {
		t.Fatalf("expected error without FSSudo")
	}

	// Claims allow root.
	ctx = auth.WithClaims(context.Background(), auth.Claims{UserInfo: auth.UserInfo{FSSudo: true, FSAny: true}})
	req = httptest.NewRequest(http.MethodPost, "http://example/api/term/session", nil).WithContext(ctx)
	if err := s.validateAs(req, "root"); err != nil {
		t.Fatalf("expected ok, got %v", err)
	}
}
