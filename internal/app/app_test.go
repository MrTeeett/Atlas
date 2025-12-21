package app

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/MrTeeett/atlas/internal/auth"
)

type testStore struct {
	passByUser map[string]string
}

func (s *testStore) Authenticate(user, pass string) (bool, error) {
	if s.passByUser == nil {
		return false, nil
	}
	want, ok := s.passByUser[strings.TrimSpace(user)]
	return ok && want == pass, nil
}

func (s *testStore) HasAnyUsers() bool { return len(s.passByUser) > 0 }

func (s *testStore) GetUser(user string) (auth.UserInfo, bool, error) {
	user = strings.TrimSpace(user)
	if user == "" {
		return auth.UserInfo{}, false, nil
	}
	if _, ok := s.passByUser[user]; !ok {
		return auth.UserInfo{}, false, nil
	}
	return auth.UserInfo{User: user, Role: "admin", CanExec: true, CanProcs: true, CanFW: true, FSSudo: true, FSAny: true}, true, nil
}

func TestBasePathGatesAllRoutes(t *testing.T) {
	t.Parallel()

	srv, err := New(Config{
		RootDir:    "/",
		BasePath:   "/x",
		AuthStore:  &testStore{passByUser: map[string]string{"admin": "ok"}},
		Secret:     []byte("0123456789abcdef0123456789abcdef"),
		FWDBPath:   "/tmp/fw.db",
		ConfigPath: "/tmp/atlas.json",
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	h := srv.Handler()

	// Without base path -> 404.
	r := httptest.NewRequest(http.MethodGet, "http://example/", nil)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if w.Code != http.StatusNotFound {
		t.Fatalf("expected 404, got %d", w.Code)
	}

	// /x -> redirect to /x/
	r = httptest.NewRequest(http.MethodGet, "http://example/x", nil)
	w = httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if w.Code != http.StatusFound || w.Header().Get("Location") != "/x/" {
		t.Fatalf("expected redirect to /x/, got status=%d loc=%q", w.Code, w.Header().Get("Location"))
	}

	// login page works under base path
	r = httptest.NewRequest(http.MethodGet, "http://example/x/login", nil)
	w = httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", w.Code)
	}
}

func TestLoginFlowThenAccessIndex(t *testing.T) {
	t.Parallel()

	srv, err := New(Config{
		RootDir:    "/",
		BasePath:   "/x",
		AuthStore:  &testStore{passByUser: map[string]string{"admin": "ok"}},
		Secret:     []byte("0123456789abcdef0123456789abcdef"),
		FWDBPath:   "/tmp/fw.db",
		ConfigPath: "/tmp/atlas.json",
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	h := srv.Handler()

	// Unauthed index redirects to /x/login
	r := httptest.NewRequest(http.MethodGet, "http://example/x/", nil)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if w.Code != http.StatusFound || w.Header().Get("Location") != "/x/login" {
		t.Fatalf("expected redirect to /x/login, got status=%d loc=%q", w.Code, w.Header().Get("Location"))
	}

	// Login
	form := url.Values{}
	form.Set("user", "admin")
	form.Set("pass", "ok")
	r = httptest.NewRequest(http.MethodPost, "http://example/x/login", strings.NewReader(form.Encode()))
	r.Header.Set("content-type", "application/x-www-form-urlencoded")
	w = httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if w.Code != http.StatusFound || w.Header().Get("Location") != "/x/" {
		t.Fatalf("expected redirect to /x/, got status=%d loc=%q", w.Code, w.Header().Get("Location"))
	}
	cookie := w.Header().Get("Set-Cookie")
	if !strings.Contains(cookie, "atlas_session=") {
		t.Fatalf("expected session cookie, got %q", cookie)
	}

	// Authed index returns HTML.
	r = httptest.NewRequest(http.MethodGet, "http://example/x/", nil)
	r.Header.Set("Cookie", strings.Split(cookie, ";")[0])
	w = httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", w.Code)
	}
	b, _ := io.ReadAll(w.Body)
	if !strings.Contains(string(b), "<title>Atlas</title>") {
		t.Fatalf("expected index html, body=%q", string(b))
	}
}

func TestCSRFRequiredForPost(t *testing.T) {
	t.Parallel()

	srv, err := New(Config{
		RootDir:    "/",
		BasePath:   "/x",
		AuthStore:  &testStore{passByUser: map[string]string{"admin": "ok"}},
		Secret:     []byte("0123456789abcdef0123456789abcdef"),
		FWDBPath:   "/tmp/fw.db",
		ConfigPath: "/tmp/atlas.json",
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	h := srv.Handler()

	// Login and capture cookie.
	form := url.Values{}
	form.Set("user", "admin")
	form.Set("pass", "ok")
	r := httptest.NewRequest(http.MethodPost, "http://example/x/login", strings.NewReader(form.Encode()))
	r.Header.Set("content-type", "application/x-www-form-urlencoded")
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	cookie := w.Header().Get("Set-Cookie")
	if cookie == "" {
		t.Fatalf("expected cookie")
	}
	cookieKV := strings.Split(cookie, ";")[0]

	// Fetch csrf token.
	r = httptest.NewRequest(http.MethodGet, "http://example/x/api/me", nil)
	r.Header.Set("Cookie", cookieKV)
	w = httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if w.Code != http.StatusOK {
		t.Fatalf("me status=%d body=%q", w.Code, w.Body.String())
	}
	var me struct {
		CSRF string `json:"csrf"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &me); err != nil {
		t.Fatalf("json: %v", err)
	}
	if me.CSRF == "" {
		t.Fatalf("expected csrf token")
	}

	// POST without CSRF -> 403.
	r = httptest.NewRequest(http.MethodPost, "http://example/x/api/exec", strings.NewReader(`{"command":"echo hi"}`))
	r.Header.Set("content-type", "application/json")
	r.Header.Set("Cookie", cookieKV)
	w = httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if w.Code != http.StatusForbidden {
		t.Fatalf("expected 403, got %d body=%q", w.Code, w.Body.String())
	}

	// POST with CSRF but lacking exec permission should still be forbidden by requireExec (our test user has CanExec=true,
	// but server ExecService is disabled by default, so it should not be 403 csrf).
	r = httptest.NewRequest(http.MethodPost, "http://example/x/api/exec", strings.NewReader(`{"command":"echo hi"}`))
	r.Header.Set("content-type", "application/json")
	r.Header.Set("Cookie", cookieKV)
	r.Header.Set("X-Atlas-CSRF", me.CSRF)
	w = httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if w.Code == http.StatusForbidden && strings.Contains(w.Body.String(), "csrf token required") {
		t.Fatalf("csrf should have passed, got %d body=%q", w.Code, w.Body.String())
	}
}
