package auth

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"html/template"
	"net/http"
	"strings"
	"time"
)

type Config struct {
	Store        Store
	Secret       []byte
	CookieSecure bool
	BasePath     string
}

type Auth struct {
	cfg      Config
	basePath string
}

type Store interface {
	Authenticate(user, pass string) (bool, error)
	HasAnyUsers() bool
	GetUser(user string) (UserInfo, bool, error)
}

type UserInfo struct {
	User     string
	Role     string
	CanExec  bool
	CanProcs bool
	CanFW    bool
	FSSudo   bool
	FSAny    bool
	FSUsers  []string
}

type Claims struct {
	UserInfo
}

type claimsKey struct{}

func WithClaims(ctx context.Context, c Claims) context.Context {
	return context.WithValue(ctx, claimsKey{}, c)
}

func ClaimsFromContext(ctx context.Context) (Claims, bool) {
	v := ctx.Value(claimsKey{})
	c, ok := v.(Claims)
	return c, ok
}

type session struct {
	User string `json:"u"`
	Exp  int64  `json:"e"`
	CSRF string `json:"c"`
}

const cookieName = "atlas_session"

func New(cfg Config) *Auth {
	a := &Auth{cfg: cfg}
	a.basePath = normalizeBasePath(cfg.BasePath)
	return a
}

func (a *Auth) IsAuthenticated(r *http.Request) bool {
	sess, err := a.readSession(r)
	return err == nil && sess.Exp > time.Now().Unix()
}

func (a *Auth) CSRFToken(r *http.Request) string {
	sess, err := a.readSession(r)
	if err != nil {
		return ""
	}
	return sess.CSRF
}

func (a *Auth) Username(r *http.Request) (string, error) {
	sess, err := a.readSession(r)
	if err != nil {
		return "", err
	}
	if sess.Exp <= time.Now().Unix() {
		return "", errors.New("expired")
	}
	return sess.User, nil
}

func (a *Auth) Claims(r *http.Request) (Claims, error) {
	if a.cfg.Store == nil {
		return Claims{}, errors.New("auth store is not configured")
	}
	user, err := a.Username(r)
	if err != nil {
		return Claims{}, err
	}
	info, ok, err := a.cfg.Store.GetUser(user)
	if err != nil {
		return Claims{}, err
	}
	if !ok {
		return Claims{}, errors.New("user not found")
	}
	return Claims{UserInfo: info}, nil
}

func (a *Auth) HandleMe(w http.ResponseWriter, r *http.Request) {
	sess, err := a.readSession(r)
	if err != nil || sess.Exp <= time.Now().Unix() {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	resp := map[string]any{
		"user": sess.User,
		"csrf": sess.CSRF,
		"exp":  sess.Exp,
	}
	if a.cfg.Store != nil {
		if info, ok, _ := a.cfg.Store.GetUser(sess.User); ok {
			resp["role"] = info.Role
			resp["can_exec"] = info.CanExec
			resp["can_procs"] = info.CanProcs
			resp["can_firewall"] = info.CanFW
			resp["fs_sudo"] = info.FSSudo
		}
	}
	_ = json.NewEncoder(w).Encode(resp)
}

func (a *Auth) HandleLogin(w http.ResponseWriter, r *http.Request) {
	lang := loginLang(r)
	i18n := loginI18n(lang)

	switch r.Method {
	case http.MethodGet:
		a.writeLoginPage(w, http.StatusOK, loginPageData{Lang: lang, T: i18n})
		return
	case http.MethodPost:
	default:
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}

	if err := r.ParseForm(); err != nil {
		msg := "bad form"
		if lang == "ru" {
			msg = "некорректная форма"
		}
		a.writeLoginPage(w, http.StatusBadRequest, loginPageData{Lang: lang, T: i18n, Error: msg})
		return
	}
	user := r.Form.Get("user")
	pass := r.Form.Get("pass")
	if a.cfg.Store == nil {
		http.Error(w, "auth store is not configured", http.StatusInternalServerError)
		return
	}
	ok, err := a.cfg.Store.Authenticate(user, pass)
	if err != nil {
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	if !ok {
		msg := "invalid credentials"
		if lang == "ru" {
			msg = "неверные учётные данные"
		}
		a.writeLoginPage(w, http.StatusUnauthorized, loginPageData{Lang: lang, T: i18n, Error: msg, User: user})
		return
	}

	csrf, err := randomHex(16)
	if err != nil {
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}

	sess := session{
		User: user,
		Exp:  time.Now().Add(24 * time.Hour).Unix(),
		CSRF: csrf,
	}
	value, err := a.seal(sess)
	if err != nil {
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}

	http.SetCookie(w, &http.Cookie{
		Name:     cookieName,
		Value:    value,
		Path:     a.basePath,
		HttpOnly: true,
		SameSite: http.SameSiteStrictMode,
		Secure:   a.cfg.CookieSecure,
		Expires:  time.Unix(sess.Exp, 0),
	})
	http.Redirect(w, r, a.path("/"), http.StatusFound)
}

type loginPageData struct {
	Lang  string
	Error string
	User  string
	T     loginPageI18n
}

type loginPageI18n struct {
	Title     string
	Heading   string
	UserLabel string
	PassLabel string
	Submit    string
	Hint      string
}

var loginTpl = template.Must(template.New("login").Parse(loginHTML))

func loginLang(r *http.Request) string {
	q := strings.ToLower(strings.TrimSpace(r.URL.Query().Get("lang")))
	if q == "ru" || q == "en" {
		return q
	}
	al := strings.ToLower(r.Header.Get("Accept-Language"))
	if strings.HasPrefix(al, "ru") || strings.Contains(al, "ru-") || strings.Contains(al, "ru,") {
		return "ru"
	}
	return "en"
}

func loginI18n(lang string) loginPageI18n {
	if lang == "ru" {
		return loginPageI18n{
			Title:     "Atlas — Вход",
			Heading:   "Atlas",
			UserLabel: "Пользователь",
			PassLabel: "Пароль",
			Submit:    "Войти",
			Hint:      "Учётные данные хранятся в зашифрованной базе пользователей.",
		}
	}
	return loginPageI18n{
		Title:     "Atlas — Login",
		Heading:   "Atlas",
		UserLabel: "User",
		PassLabel: "Password",
		Submit:    "Sign in",
		Hint:      "Credentials are stored in the encrypted user database.",
	}
}

func (a *Auth) writeLoginPage(w http.ResponseWriter, status int, data loginPageData) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(status)
	var buf bytes.Buffer
	_ = loginTpl.Execute(&buf, data)
	_, _ = w.Write(buf.Bytes())
}

func (a *Auth) HandleLogout(w http.ResponseWriter, r *http.Request) {
	http.SetCookie(w, &http.Cookie{
		Name:     cookieName,
		Value:    "",
		Path:     a.basePath,
		HttpOnly: true,
		SameSite: http.SameSiteStrictMode,
		Secure:   a.cfg.CookieSecure,
		Expires:  time.Unix(0, 0),
		MaxAge:   -1,
	})
	http.Redirect(w, r, a.path("/login"), http.StatusFound)
}

func (a *Auth) readSession(r *http.Request) (session, error) {
	c, err := r.Cookie(cookieName)
	if err != nil {
		return session{}, err
	}
	return a.unseal(c.Value)
}

func (a *Auth) seal(sess session) (string, error) {
	b, err := json.Marshal(sess)
	if err != nil {
		return "", err
	}
	payload := base64.RawURLEncoding.EncodeToString(b)
	sig := a.sign([]byte(payload))
	return payload + "." + sig, nil
}

func (a *Auth) unseal(value string) (session, error) {
	parts := strings.Split(value, ".")
	if len(parts) != 2 {
		return session{}, errors.New("invalid session")
	}
	payload, sig := parts[0], parts[1]
	if !hmac.Equal([]byte(sig), []byte(a.sign([]byte(payload)))) {
		return session{}, errors.New("invalid signature")
	}
	raw, err := base64.RawURLEncoding.DecodeString(payload)
	if err != nil {
		return session{}, err
	}
	var sess session
	if err := json.Unmarshal(raw, &sess); err != nil {
		return session{}, err
	}
	return sess, nil
}

func (a *Auth) sign(data []byte) string {
	m := hmac.New(sha256.New, a.cfg.Secret)
	_, _ = m.Write(data)
	return base64.RawURLEncoding.EncodeToString(m.Sum(nil))
}

func randomHex(nBytes int) (string, error) {
	b := make([]byte, nBytes)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return hex.EncodeToString(b), nil
}

func (a *Auth) path(p string) string {
	if p == "" {
		p = "/"
	}
	if !strings.HasPrefix(p, "/") {
		p = "/" + p
	}
	if a.basePath == "/" {
		return p
	}
	if p == "/" {
		return a.basePath + "/"
	}
	return a.basePath + p
}

func normalizeBasePath(p string) string {
	p = strings.TrimSpace(p)
	if p == "" || p == "/" {
		return "/"
	}
	if !strings.HasPrefix(p, "/") {
		p = "/" + p
	}
	p = strings.TrimRight(p, "/")
	if p == "" {
		return "/"
	}
	return p
}

const loginHTML = `<!doctype html>
<html lang="{{.Lang}}">
<head>
  <meta charset="utf-8"/>
  <meta name="viewport" content="width=device-width,initial-scale=1"/>
  <title>{{.T.Title}}</title>
  <style>
    *{box-sizing:border-box;}
    body{font-family:system-ui,-apple-system,Segoe UI,Roboto,Ubuntu; background:#0b1220; color:#e7eefc; display:flex; min-height:100vh; align-items:center; justify-content:center;}
    .card{background:#101a30; border:1px solid #223155; border-radius:14px; padding:18px; width:min(420px,92vw);}
    h1{font-size:18px; margin:0 0 12px;}
    label{display:block; font-size:12px; color:#b7c3dc; margin:10px 0 6px;}
    input{display:block; width:100%; margin:0; padding:10px 12px; height:42px; border-radius:10px; border:1px solid #2a3b63; background:#0b1220; color:#e7eefc; font:inherit; font-size:14px; line-height:20px; appearance:none; -webkit-appearance:none;}
    button{margin-top:14px; width:100%; padding:10px 12px; border:0; border-radius:10px; background:#4f7cff; color:white; font-weight:600; cursor:pointer;}
    .hint{margin-top:10px; font-size:12px; color:#9fb0d1;}
    .err{margin:10px 0 0; padding:10px 12px; border-radius:10px; border:1px solid #5a2030; background:#2a1120; color:#ffb6c1; font-size:13px;}
  </style>
</head>
<body>
  <form class="card" method="post" action="">
    <h1>{{.T.Heading}}</h1>
    {{if .Error}}<div class="err">{{.Error}}</div>{{end}}
    <label for="user">{{.T.UserLabel}}</label>
    <input id="user" name="user" autocomplete="username" value="{{.User}}" />
    <label for="pass">{{.T.PassLabel}}</label>
    <input id="pass" name="pass" type="password" autocomplete="current-password" />
    <button type="submit">{{.T.Submit}}</button>
    <div class="hint">{{.T.Hint}}</div>
  </form>
</body>
</html>`
