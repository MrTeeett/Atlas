package app

import (
	"encoding/json"
	"io"
	"net/http"
	"strings"

	"github.com/MrTeeett/atlas/internal/auth"
)

type adminSudoResponse struct {
	User        string `json:"user"`
	HasPassword bool   `json:"has_password"`
}

type adminSudoRequest struct {
	Password string `json:"password,omitempty"`
	Clear    bool   `json:"clear,omitempty"`
}

func (s *Server) HandleAdminSudo(w http.ResponseWriter, r *http.Request) {
	st, err := s.adminStore()
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	claims, ok := auth.ClaimsFromContext(r.Context())
	if !ok || strings.TrimSpace(claims.User) == "" {
		http.Error(w, "missing user", http.StatusUnauthorized)
		return
	}
	user := strings.TrimSpace(claims.User)

	switch r.Method {
	case http.MethodGet:
		pass, ok, err := st.GetSudoPassword(user)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		writeJSON(w, adminSudoResponse{User: user, HasPassword: ok && pass != ""})
		return

	case http.MethodPost, http.MethodPut:
	default:
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}

	var req adminSudoRequest
	if err := json.NewDecoder(io.LimitReader(r.Body, 1<<20)).Decode(&req); err != nil {
		http.Error(w, "bad json", http.StatusBadRequest)
		return
	}
	pass := strings.TrimSpace(req.Password)
	if req.Clear || pass == "" {
		if !req.Clear && pass == "" {
			http.Error(w, "password is required", http.StatusBadRequest)
			return
		}
		if err := st.SetSudoPassword(user, ""); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		writeJSON(w, adminSudoResponse{User: user, HasPassword: false})
		return
	}

	if err := st.SetSudoPassword(user, pass); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	writeJSON(w, adminSudoResponse{User: user, HasPassword: true})
}
