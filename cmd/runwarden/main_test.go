package main

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/catalinmuraru/runwarden/internal/api"
	"github.com/catalinmuraru/runwarden/internal/config"
)

func get(t *testing.T, h http.Handler, method, path string) int {
	t.Helper()
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequestWithContext(t.Context(), method, path, nil))
	return rec.Code
}

func TestProbes(t *testing.T) {
	apiSrv := api.New(api.Options{}, nil, nil)
	mux := newMux(apiSrv)

	if code := get(t, mux, http.MethodGet, "/healthz"); code != http.StatusOK {
		t.Errorf("GET /healthz = %d, want 200", code)
	}
	if code := get(t, mux, http.MethodGet, "/readyz"); code != http.StatusServiceUnavailable {
		t.Errorf("GET /readyz before configs load = %d, want 503", code)
	}
	apiSrv.SetCatalog(&config.Catalog{})
	if code := get(t, mux, http.MethodGet, "/readyz"); code != http.StatusOK {
		t.Errorf("GET /readyz after configs load = %d, want 200", code)
	}
	if code := get(t, mux, http.MethodPost, "/healthz"); code != http.StatusMethodNotAllowed {
		t.Errorf("POST /healthz = %d, want 405", code)
	}
	if code := get(t, mux, http.MethodGet, "/api/nope"); code != http.StatusNotFound {
		t.Errorf("GET /api/nope = %d, want 404", code)
	}
}
