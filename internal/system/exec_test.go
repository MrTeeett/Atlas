package system

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestExecServiceDisabled(t *testing.T) {
	t.Parallel()
	s := NewExecService(ExecConfig{Enabled: false})
	req := httptest.NewRequest(http.MethodPost, "http://example/api/exec", bytes.NewReader([]byte(`{"command":"echo hi"}`)))
	rr := httptest.NewRecorder()
	s.HandleRun(rr, req)
	if rr.Code != http.StatusForbidden {
		t.Fatalf("expected 403, got %d", rr.Code)
	}
}

func TestExecServiceBadRequest(t *testing.T) {
	t.Parallel()
	s := NewExecService(ExecConfig{Enabled: true})

	req := httptest.NewRequest(http.MethodPost, "http://example/api/exec", bytes.NewReader([]byte(`{`)))
	rr := httptest.NewRecorder()
	s.HandleRun(rr, req)
	if rr.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d", rr.Code)
	}

	req = httptest.NewRequest(http.MethodPost, "http://example/api/exec", bytes.NewReader([]byte(`{"command":"  "}`)))
	rr = httptest.NewRecorder()
	s.HandleRun(rr, req)
	if rr.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d", rr.Code)
	}
}
