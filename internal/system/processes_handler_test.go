package system

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestProcessHandleSignalValidation(t *testing.T) {
	t.Parallel()

	s := NewProcessService()

	// Bad JSON
	req := httptest.NewRequest(http.MethodPost, "http://example/api/processes/signal", bytes.NewReader([]byte("{")))
	rr := httptest.NewRecorder()
	s.HandleSignal(rr, req)
	if rr.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d", rr.Code)
	}

	// Missing pids
	req = httptest.NewRequest(http.MethodPost, "http://example/api/processes/signal", bytes.NewReader([]byte(`{"signal":"TERM"}`)))
	rr = httptest.NewRecorder()
	s.HandleSignal(rr, req)
	if rr.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d", rr.Code)
	}

	// Unknown signal
	req = httptest.NewRequest(http.MethodPost, "http://example/api/processes/signal", bytes.NewReader([]byte(`{"pid":123,"signal":"NOPE"}`)))
	rr = httptest.NewRecorder()
	s.HandleSignal(rr, req)
	if rr.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d", rr.Code)
	}
}
