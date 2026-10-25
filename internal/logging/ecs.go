// Package logging writes Runwarden's logs as JSON lines in Elastic Common Schema (ECS), so
// Filebeat can ship them without custom parsing.
package logging

import (
	"context"
	"io"
	"log/slog"
	"strings"
	"time"
)

// ECSVersion is the ECS version the fields follow.
const ECSVersion = "8.11.0"

// Dataset of application logs; audit events use their own (see internal/audit).
const appDataset = "runwarden.app"

// ecsRoots are top-level keys that are already ECS fields (or groups of them). Any other key is
// a custom field and is moved under "runwarden.", as ECS recommends for custom fields.
var ecsRoots = map[string]bool{
	"@timestamp": true, "log.level": true, "message": true, "ecs.version": true,
	"event": true, "user": true, "source": true, "user_agent": true, "process": true,
	"error.message": true, "error": true, "service": true, "url": true, "http": true,
	"labels": true, "runwarden": true,
}

// NewECSHandler returns a handler writing ECS JSON lines to w.
func NewECSHandler(w io.Writer, version string, level slog.Leveler) slog.Handler {
	inner := slog.NewJSONHandler(w, &slog.HandlerOptions{Level: level, ReplaceAttr: replaceBuiltins})
	return &ecsHandler{inner: inner.WithAttrs([]slog.Attr{
		slog.String("ecs.version", ECSVersion),
		slog.Group("service", slog.String("name", "runwarden"), slog.String("version", version)),
	})}
}

type ecsHandler struct {
	inner slog.Handler
	// custom holds non-ECS attributes from With; they are merged with the record's own custom
	// attributes so each line has a single "runwarden" object (duplicate keys would be lost).
	custom []slog.Attr
}

func (h *ecsHandler) Enabled(ctx context.Context, l slog.Level) bool { return h.inner.Enabled(ctx, l) }

func (h *ecsHandler) Handle(ctx context.Context, r slog.Record) error {
	attrs := make([]slog.Attr, 0, r.NumAttrs()+len(h.custom)+1)
	attrs = append(attrs, h.custom...)
	r.Attrs(func(a slog.Attr) bool {
		attrs = append(attrs, a)
		return true
	})
	attrs = namespace(attrs)
	hasEvent := false
	for _, a := range attrs {
		if a.Key == "event" {
			hasEvent = true
		}
	}
	if !hasEvent {
		attrs = append(attrs, slog.Group("event", slog.String("dataset", appDataset)))
	}
	out := slog.NewRecord(r.Time, r.Level, r.Message, r.PC)
	out.AddAttrs(attrs...)
	return h.inner.Handle(ctx, out)
}

func (h *ecsHandler) WithAttrs(attrs []slog.Attr) slog.Handler {
	ecs, custom := split(attrs)
	return &ecsHandler{inner: h.inner.WithAttrs(ecs), custom: append(append([]slog.Attr(nil), h.custom...), custom...)}
}

func (h *ecsHandler) WithGroup(name string) slog.Handler {
	return &ecsHandler{inner: h.inner.WithGroup(name), custom: h.custom}
}

// split separates ECS attributes (errors mapped to error.message) from custom ones.
func split(attrs []slog.Attr) (ecs, custom []slog.Attr) {
	for _, a := range attrs {
		switch {
		case a.Key == "err" || a.Key == "error":
			ecs = append(ecs, slog.String("error.message", errorText(a.Value)))
		case ecsRoots[a.Key]:
			ecs = append(ecs, a)
		default:
			custom = append(custom, a)
		}
	}
	return ecs, custom
}

// namespace returns the ECS attributes plus one "runwarden" group with all custom ones.
func namespace(attrs []slog.Attr) []slog.Attr {
	ecs, custom := split(attrs)
	if len(custom) > 0 {
		ecs = append(ecs, slog.Attr{Key: "runwarden", Value: slog.GroupValue(custom...)})
	}
	return ecs
}

func errorText(v slog.Value) string {
	if err, ok := v.Any().(error); ok && err != nil {
		return err.Error()
	}
	return v.String()
}

// replaceBuiltins renames slog's built-in keys to their ECS names.
func replaceBuiltins(groups []string, a slog.Attr) slog.Attr {
	if len(groups) > 0 {
		return a
	}
	switch a.Key {
	case slog.TimeKey:
		return slog.String("@timestamp", a.Value.Time().UTC().Format(time.RFC3339Nano))
	case slog.LevelKey:
		return slog.String("log.level", strings.ToLower(a.Value.String()))
	case slog.MessageKey:
		a.Key = "message"
	}
	return a
}
