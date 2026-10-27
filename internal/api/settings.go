package api

import (
	"fmt"
	"log/slog"
	"net/http"

	"github.com/catalinmuraru/runwarden/internal/audit"
	"github.com/catalinmuraru/runwarden/internal/settings"
)

type uiSettings struct {
	Theme string `json:"theme"`
}

// getUISettings is public: the sign-in page needs the theme, and it holds nothing sensitive.
func (s *Server) getUISettings(w http.ResponseWriter, _ *http.Request) {
	theme := settings.ThemeSystem
	if s.opts.Settings != nil {
		theme = s.opts.Settings.Get().Theme
	}
	writeJSON(w, http.StatusOK, uiSettings{Theme: theme})
}

func (s *Server) putUISettings(w http.ResponseWriter, r *http.Request) {
	if s.opts.Settings == nil {
		writeError(w, http.StatusNotFound, "not found")
		return
	}
	var body uiSettings
	if err := decodeJSON(w, r, &body); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	if !settings.ValidTheme(body.Theme) {
		writeError(w, http.StatusBadRequest, "theme must be system, light or dark")
		return
	}
	before := s.opts.Settings.Get().Theme
	if err := s.opts.Settings.SetTheme(body.Theme); err != nil {
		slog.Error("save settings", "err", err)
		writeError(w, http.StatusInternalServerError, "could not save the settings")
		return
	}
	s.record(r, audit.Event{Action: audit.SettingsChange, Category: "configuration", Type: "change", Success: true,
		Message: fmt.Sprintf("settings: theme: %s → %s", before, body.Theme)})
	writeJSON(w, http.StatusOK, uiSettings{Theme: body.Theme})
}
