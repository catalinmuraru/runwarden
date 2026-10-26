// Package audit records who did what and with which result, as ECS events
// (event.dataset "runwarden.audit") in the application log. Secure values are always masked by
// the caller; passwords never reach this package.
package audit

import (
	"context"
	"encoding/json"
	"log/slog"
	"sync/atomic"
	"time"
)

// Dataset is the ECS event.dataset of audit events.
const Dataset = "runwarden.audit"

// Actions.
const (
	RunStart       = "run-start"
	RunEnd         = "run-end"
	RunInput       = "run-input"
	RunStop        = "run-stop"
	RunKill        = "run-kill"
	Login          = "login"
	Logout         = "logout"
	ConfigReload   = "config-reload"
	HistoryCleanup = "history-cleanup"
)

// Event is one audited action.
type Event struct {
	Action   string // one of the actions above
	Category string // ECS event.category: process, authentication, configuration, file
	Type     string // ECS event.type: start, end, change, deletion, info
	Success  bool
	Reason   string // why it failed, or how a run ended
	Message  string

	User      string
	Groups    []string
	SourceIP  string
	UserAgent string

	RunID    string
	Script   string
	Status   string
	Values   map[string]any // masked parameter values
	Args     []string       // masked arguments
	ExitCode *int
	Duration time.Duration
	Count    int // e.g. runs removed by a cleanup
}

// Log writes e as one ECS event.
func Log(ctx context.Context, e Event) {
	outcome, level := "success", slog.LevelInfo
	if !e.Success {
		outcome, level = "failure", slog.LevelWarn
	}
	event := []any{
		slog.String("kind", "event"),
		slog.Any("category", []string{e.Category}),
		slog.Any("type", []string{e.Type}),
		slog.String("action", e.Action),
		slog.String("outcome", outcome),
		slog.String("dataset", Dataset),
	}
	if e.Reason != "" {
		event = append(event, slog.String("reason", e.Reason))
	}
	if e.Duration > 0 {
		event = append(event, slog.Int64("duration", e.Duration.Nanoseconds()))
	}
	attrs := []slog.Attr{slog.Group("event", event...)}

	if e.User != "" {
		user := []any{slog.String("name", e.User)}
		if len(e.Groups) > 0 {
			user = append(user, slog.Any("roles", e.Groups))
		}
		attrs = append(attrs, slog.Group("user", user...))
	}
	if e.SourceIP != "" {
		attrs = append(attrs, slog.Group("source", slog.String("ip", e.SourceIP)))
	}
	if e.UserAgent != "" {
		attrs = append(attrs, slog.Group("user_agent", slog.String("original", e.UserAgent)))
	}

	var process []any
	if e.ExitCode != nil {
		process = append(process, slog.Int("exit_code", *e.ExitCode))
	}
	if e.Args != nil {
		process = append(process, slog.Any("args", e.Args))
	}
	if len(process) > 0 {
		attrs = append(attrs, slog.Group("process", process...))
	}

	var run []any
	if e.RunID != "" {
		run = append(run, slog.String("id", e.RunID))
	}
	if e.Status != "" {
		run = append(run, slog.String("status", e.Status))
	}
	if e.Values != nil {
		// One string field: parameter names vary per script, and as separate fields they would
		// create an unbounded, type-conflicting mapping in Elasticsearch.
		if b, err := json.Marshal(e.Values); err == nil {
			run = append(run, slog.String("parameters", string(b)))
		}
	}
	var custom []any
	if len(run) > 0 {
		custom = append(custom, slog.Group("run", run...))
	}
	if e.Script != "" {
		custom = append(custom, slog.Group("script", slog.String("name", e.Script)))
	}
	if e.Count > 0 {
		custom = append(custom, slog.Int("count", e.Count))
	}
	if len(custom) > 0 {
		attrs = append(attrs, slog.Group("runwarden", custom...))
	}

	msg := e.Message
	if msg == "" {
		msg = e.Action + " " + outcome
	}
	slog.Default().LogAttrs(ctx, level, msg, attrs...)

	if fn := sink.Load(); fn != nil {
		(*fn)(Record{
			Time: time.Now().UTC(), Action: e.Action, Outcome: outcome, Reason: e.Reason, Message: msg,
			User: e.User, Groups: e.Groups, SourceIP: e.SourceIP, UserAgent: e.UserAgent,
			RunID: e.RunID, Script: e.Script, Status: e.Status, Values: e.Values, Args: e.Args,
			ExitCode: e.ExitCode, DurationMS: e.Duration.Milliseconds(), Count: e.Count,
		})
	}
}

// Record is an audit event as kept by the in-app activity log (see SetSink).
type Record struct {
	Time       time.Time      `json:"time"`
	Action     string         `json:"action"`
	Outcome    string         `json:"outcome"`
	Reason     string         `json:"reason,omitempty"`
	Message    string         `json:"message,omitempty"`
	User       string         `json:"user,omitempty"`
	Groups     []string       `json:"groups,omitempty"`
	SourceIP   string         `json:"source_ip,omitempty"`
	UserAgent  string         `json:"user_agent,omitempty"`
	RunID      string         `json:"run_id,omitempty"`
	Script     string         `json:"script,omitempty"`
	Status     string         `json:"status,omitempty"`
	Values     map[string]any `json:"values,omitempty"`
	Args       []string       `json:"args,omitempty"`
	ExitCode   *int           `json:"exit_code,omitempty"`
	DurationMS int64          `json:"duration_ms,omitempty"`
	Count      int            `json:"count,omitempty"`
}

var sink atomic.Pointer[func(Record)]

// SetSink also hands every event to fn (nil stops it). fn must not block for long.
func SetSink(fn func(Record)) {
	if fn == nil {
		sink.Store(nil)
		return
	}
	sink.Store(&fn)
}
