package userdb

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/MrTeeett/atlas/internal/auth"
)

type Store struct {
	path string
	aead cipher.AEAD
	mu   sync.Mutex
	db   plainDB

	loadedModTime int64
	loadedSize    int64
}

type plainDB struct {
	Version int             `json:"version"`
	Users   map[string]User `json:"users"`
}

type User struct {
	SaltB64 string `json:"salt"`
	Iter    int    `json:"iter"`
	HashB64 string `json:"hash"`

	Role     string   `json:"role,omitempty"`
	CanExec  bool     `json:"can_exec,omitempty"`
	CanProcs bool     `json:"can_procs,omitempty"`
	CanFW    bool     `json:"can_fw,omitempty"`
	FSSudo   bool     `json:"fs_sudo,omitempty"`
	FSAny    bool     `json:"fs_any,omitempty"`
	FSUsers  []string `json:"fs_users,omitempty"`
}

type envelope struct {
	V     int    `json:"v"`
	Nonce string `json:"nonce"`
	Data  string `json:"data"`
}

func Open(path string, masterKey []byte) (*Store, error) {
	if len(masterKey) != 32 {
		return nil, errors.New("master key must be 32 bytes")
	}
	key := sha256.Sum256(append(append([]byte{}, masterKey...), []byte("atlas:userdb:v1")...))
	block, err := aes.NewCipher(key[:])
	if err != nil {
		return nil, err
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}
	s := &Store{
		path: filepath.Clean(path),
		aead: aead,
		db: plainDB{
			Version: 1,
			Users:   map[string]User{},
		},
	}
	if err := s.loadLocked(); err != nil {
		return nil, err
	}
	return s, nil
}

func (s *Store) Authenticate(user, pass string) (bool, error) {
	user = strings.TrimSpace(user)
	if user == "" {
		return false, nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.reloadIfChangedLocked(); err != nil {
		return false, err
	}
	rec, ok := s.db.Users[user]
	if !ok {
		return false, nil
	}
	salt, err := base64.RawStdEncoding.DecodeString(rec.SaltB64)
	if err != nil {
		return false, err
	}
	want, err := base64.RawStdEncoding.DecodeString(rec.HashB64)
	if err != nil {
		return false, err
	}
	iter := rec.Iter
	if iter < 10_000 {
		iter = 120_000
	}
	got := pbkdf2Key([]byte(pass), salt, iter, len(want))
	if subtle.ConstantTimeCompare(got, want) == 1 {
		return true, nil
	}
	return false, nil
}

type UserInfo = auth.UserInfo

func (s *Store) GetUser(user string) (UserInfo, bool, error) {
	user = strings.TrimSpace(user)
	if user == "" {
		return UserInfo{}, false, nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.reloadIfChangedLocked(); err != nil {
		return UserInfo{}, false, err
	}
	rec, ok := s.db.Users[user]
	if !ok {
		return UserInfo{}, false, nil
	}
	info := UserInfo{
		User:     user,
		Role:     rec.Role,
		CanExec:  rec.CanExec,
		CanProcs: rec.CanProcs,
		CanFW:    rec.CanFW,
		FSSudo:   rec.FSSudo,
		FSAny:    rec.FSAny,
		FSUsers:  append([]string{}, rec.FSUsers...),
	}
	if info.Role == "" {
		info.Role = "user"
	}
	return info, true, nil
}

func (s *Store) HasAnyUsers() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	_ = s.reloadIfChangedLocked()
	return len(s.db.Users) > 0
}

func (s *Store) UpsertUser(user, pass string) error {
	user = strings.TrimSpace(user)
	if user == "" {
		return errors.New("user is required")
	}
	if pass == "" {
		return errors.New("password is required")
	}
	salt := make([]byte, 16)
	if _, err := rand.Read(salt); err != nil {
		return err
	}
	iter := 120_000
	hash := pbkdf2Key([]byte(pass), salt, iter, 32)

	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.reloadIfChangedLocked(); err != nil {
		return err
	}
	if s.db.Users == nil {
		s.db.Users = map[string]User{}
	}
	prev := s.db.Users[user]
	s.db.Users[user] = User{
		SaltB64: base64.RawStdEncoding.EncodeToString(salt),
		Iter:    iter,
		HashB64: base64.RawStdEncoding.EncodeToString(hash),

		Role:     prev.Role,
		CanExec:  prev.CanExec,
		CanProcs: prev.CanProcs,
		CanFW:    prev.CanFW,
		FSSudo:   prev.FSSudo,
		FSAny:    prev.FSAny,
		FSUsers:  normalizeCSV(prev.FSUsers),
	}
	return s.saveLocked()
}

func (s *Store) SetPermissions(user string, role string, canExec bool, canProcs bool, canFW bool, fsSudo bool, fsAny bool, fsUsers []string) error {
	user = strings.TrimSpace(user)
	if user == "" {
		return errors.New("user is required")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.reloadIfChangedLocked(); err != nil {
		return err
	}
	rec, ok := s.db.Users[user]
	if !ok {
		return errors.New("user not found")
	}
	role = strings.TrimSpace(role)
	if role == "" {
		role = rec.Role
	}
	rec.Role = role
	rec.CanExec = canExec
	rec.CanProcs = canProcs
	rec.CanFW = canFW
	rec.FSSudo = fsSudo
	rec.FSAny = fsAny
	rec.FSUsers = normalizeCSV(fsUsers)
	s.db.Users[user] = rec
	return s.saveLocked()
}

func (s *Store) DeleteUser(user string) error {
	user = strings.TrimSpace(user)
	if user == "" {
		return errors.New("user is required")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.reloadIfChangedLocked(); err != nil {
		return err
	}
	delete(s.db.Users, user)
	return s.saveLocked()
}

func (s *Store) ListUsers() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	_ = s.reloadIfChangedLocked()
	var out []string
	for u := range s.db.Users {
		out = append(out, u)
	}
	return out
}

func (s *Store) loadLocked() error {
	st, err := os.Stat(s.path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			s.loadedModTime = 0
			s.loadedSize = 0
			return nil
		}
		return err
	}
	b, err := os.ReadFile(s.path)
	if err != nil {
		return err
	}
	var env envelope
	if err := json.Unmarshal(b, &env); err != nil {
		return err
	}
	if env.V != 1 {
		return errors.New("unsupported user db envelope version")
	}
	nonce, err := base64.RawStdEncoding.DecodeString(env.Nonce)
	if err != nil {
		return err
	}
	ct, err := base64.RawStdEncoding.DecodeString(env.Data)
	if err != nil {
		return err
	}
	pt, err := s.aead.Open(nil, nonce, ct, nil)
	if err != nil {
		return errors.New("cannot decrypt user db (wrong key?)")
	}
	var db plainDB
	if err := json.Unmarshal(pt, &db); err != nil {
		return err
	}
	if db.Users == nil {
		db.Users = map[string]User{}
	}
	for u, rec := range db.Users {
		rec.FSUsers = normalizeCSV(rec.FSUsers)
		if rec.Role == "" {
			rec.Role = "user"
		}
		db.Users[u] = rec
	}
	s.db = db
	s.loadedModTime = st.ModTime().UnixNano()
	s.loadedSize = st.Size()
	return nil
}

func (s *Store) saveLocked() error {
	if err := os.MkdirAll(filepath.Dir(s.path), 0o755); err != nil {
		return err
	}
	pt, err := json.Marshal(s.db)
	if err != nil {
		return err
	}
	nonce := make([]byte, s.aead.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return err
	}
	ct := s.aead.Seal(nil, nonce, pt, nil)
	env := envelope{
		V:     1,
		Nonce: base64.RawStdEncoding.EncodeToString(nonce),
		Data:  base64.RawStdEncoding.EncodeToString(ct),
	}
	b, err := json.MarshalIndent(env, "", "  ")
	if err != nil {
		return err
	}
	tmp := s.path + ".tmp"
	if err := os.WriteFile(tmp, append(b, '\n'), 0o600); err != nil {
		return err
	}
	if err := os.Rename(tmp, s.path); err != nil {
		return err
	}
	if st, err := os.Stat(s.path); err == nil {
		s.loadedModTime = st.ModTime().UnixNano()
		s.loadedSize = st.Size()
	} else {
		s.loadedModTime = time.Now().UnixNano()
		s.loadedSize = int64(len(b))
	}
	return nil
}

func normalizeCSV(in []string) []string {
	var out []string
	for _, s := range in {
		s = strings.TrimSpace(s)
		if s == "" {
			continue
		}
		out = append(out, s)
	}
	return out
}

func (s *Store) reloadIfChangedLocked() error {
	st, err := os.Stat(s.path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			if s.loadedModTime == 0 && s.loadedSize == 0 {
				return nil
			}
			s.db = plainDB{Version: 1, Users: map[string]User{}}
			s.loadedModTime = 0
			s.loadedSize = 0
			return nil
		}
		return err
	}
	mt := st.ModTime().UnixNano()
	sz := st.Size()
	if mt == s.loadedModTime && sz == s.loadedSize {
		return nil
	}
	return s.loadLocked()
}
