package main

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestProbes(t *testing.T) {
	mux := newMux()
	for _, path := range []string{"/healthz", "/readyz"} {
		t.Run(path, func(t *testing.T) {
			rec := httptest.NewRecorder()
			mux.ServeHTTP(rec, httptest.NewRequestWithContext(t.Context(), http.MethodGet, path, nil))
			if rec.Code != http.StatusOK {
				t.Fatalf("GET %s = %d, want %d", path, rec.Code, http.StatusOK)
			}
		})
	}
}

func TestProbesRejectPost(t *testing.T) {
	rec := httptest.NewRecorder()
	newMux().ServeHTTP(rec, httptest.NewRequestWithContext(t.Context(), http.MethodPost, "/healthz", nil))
	if rec.Code != http.StatusMethodNotAllowed {
		t.Fatalf("POST /healthz = %d, want %d", rec.Code, http.StatusMethodNotAllowed)
	}
}
