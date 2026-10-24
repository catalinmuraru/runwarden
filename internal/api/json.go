package api

import (
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strings"
)

const maxJSONBody = 1 << 20

type errorBody struct {
	Error   string   `json:"error"`
	Details []string `json:"details,omitempty"`
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	if err := json.NewEncoder(w).Encode(v); err != nil {
		slog.Debug("api: write response", "err", err)
	}
}

func writeError(w http.ResponseWriter, status int, msg string, details ...string) {
	writeJSON(w, status, errorBody{Error: msg, Details: details})
}

// decodeJSON reads a JSON body of at most 1 MiB, keeping numbers exact.
func decodeJSON(w http.ResponseWriter, r *http.Request, v any) error {
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxJSONBody))
	dec.UseNumber()
	if err := dec.Decode(v); err != nil {
		return fmt.Errorf("invalid JSON body: %w", err)
	}
	return nil
}

// errorList flattens errors.Join results into one message per error.
func errorList(err error) []string {
	var joined interface{ Unwrap() []error }
	if errors.As(err, &joined) {
		var out []string
		for _, e := range joined.Unwrap() {
			out = append(out, e.Error())
		}
		return out
	}
	return strings.Split(err.Error(), "\n")
}
