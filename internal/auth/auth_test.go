package auth

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"
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

func (s *testStore) GetUser(user string) (UserInfo, bool, error) {
	user = strings.TrimSpace(user)
	if user == "" {
		return UserInfo{}, false, nil
	}
	if _, ok := s.passByUser[user]; !ok {
		return UserInfo{}, false, nil
	}
	return UserInfo{User: user, Role: "admin", CanExec: true, CanProcs: true, CanFW: true, FSSudo: true, FSAny: true}, true, nil
}

func TestLoginInvalidShowsFormAndError(t *testing.T) {
	t.Parallel()

	a := New(Config{
		Store:    &testStore{passByUser: map[string]string{"admin": "ok"}},
		Secret:   []byte("0123456789abcdef"),
		BasePath: "/x",
	})

	form := url.Values{}
	form.Set("user", "admin")
	form.Set("pass", "bad")
	req := httptest.NewRequest(http.MethodPost, "http://example/x/login", strings.NewReader(form.Encode()))
	req.Header.Set("content-type", "application/x-www-form-urlencoded")
	rr := httptest.NewRecorder()

	a.HandleLogin(rr, req)

	if rr.Code != http.StatusUnauthorized {
		t.Fatalf("status: got %d", rr.Code)
	}
	body := rr.Body.String()
	if !strings.Contains(body, "invalid credentials") {
		t.Fatalf("expected invalid credentials message, body=%q", body)
	}
	if !strings.Contains(body, `value="admin"`) {
		t.Fatalf("expected user preserved, body=%q", body)
	}
	if !strings.Contains(body, "<form") {
		t.Fatalf("expected login form, body=%q", body)
	}
}

func TestLoginValidSetsCookieAndRedirectsWithBasePath(t *testing.T) {
	t.Parallel()

	a := New(Config{
		Store:    &testStore{passByUser: map[string]string{"admin": "ok"}},
		Secret:   []byte("0123456789abcdef"),
		BasePath: "/x",
	})

	form := url.Values{}
	form.Set("user", "admin")
	form.Set("pass", "ok")
	req := httptest.NewRequest(http.MethodPost, "http://example/x/login", strings.NewReader(form.Encode()))
	req.Header.Set("content-type", "application/x-www-form-urlencoded")
	rr := httptest.NewRecorder()

	a.HandleLogin(rr, req)

	if rr.Code != http.StatusFound {
		t.Fatalf("status: got %d", rr.Code)
	}
	if loc := rr.Header().Get("Location"); loc != "/x/" {
		t.Fatalf("expected redirect /x/, got %q", loc)
	}
	setCookie := rr.Header().Get("Set-Cookie")
	if !strings.Contains(setCookie, "atlas_session=") {
		t.Fatalf("expected session cookie, got %q", setCookie)
	}
	if !strings.Contains(setCookie, "Path=/x") {
		t.Fatalf("expected cookie Path=/x, got %q", setCookie)
	}
}

func TestLogoutClearsCookieAndRedirectsWithBasePath(t *testing.T) {
	t.Parallel()

	a := New(Config{
		Store:    &testStore{passByUser: map[string]string{"admin": "ok"}},
		Secret:   []byte("0123456789abcdef"),
		BasePath: "/x",
	})

	req := httptest.NewRequest(http.MethodPost, "http://example/x/logout", nil)
	rr := httptest.NewRecorder()

	a.HandleLogout(rr, req)

	if rr.Code != http.StatusFound {
		t.Fatalf("status: got %d", rr.Code)
	}
	if loc := rr.Header().Get("Location"); loc != "/x/login" {
		t.Fatalf("expected redirect /x/login, got %q", loc)
	}
	setCookie := rr.Header().Get("Set-Cookie")
	if !strings.Contains(setCookie, "atlas_session=") || !(strings.Contains(setCookie, "Max-Age=0") || strings.Contains(setCookie, "Max-Age=-1")) {
		t.Fatalf("expected cookie cleared, got %q", setCookie)
	}
	if !strings.Contains(setCookie, "Path=/x") {
		t.Fatalf("expected cookie Path=/x, got %q", setCookie)
	}
}

func TestSealUnsealRoundtripAndTamper(t *testing.T) {
	t.Parallel()

	a := New(Config{
		Store:    &testStore{passByUser: map[string]string{"admin": "ok"}},
		Secret:   []byte("0123456789abcdef"),
		BasePath: "/x",
	})

	sess := session{User: "admin", Exp: time.Now().Add(time.Hour).Unix(), CSRF: "csrf"}
	val, err := a.seal(sess)
	if err != nil {
		t.Fatalf("seal: %v", err)
	}
	got, err := a.unseal(val)
	if err != nil {
		t.Fatalf("unseal: %v", err)
	}
	if got.User != sess.User || got.CSRF != sess.CSRF || got.Exp != sess.Exp {
		t.Fatalf("roundtrip mismatch: got=%#v want=%#v", got, sess)
	}

	// Tamper with signature.
	parts := strings.Split(val, ".")
	if len(parts) != 2 {
		t.Fatalf("unexpected sealed format")
	}
	bad := parts[0] + ".AAAA"
	if _, err := a.unseal(bad); err == nil {
		t.Fatalf("expected error for tampered signature")
	}
}
