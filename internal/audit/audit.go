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
	SessionRevoke  = "session-revoke"
	PolicyChange   = "policy-change"
	PolicyExpiry   = "exception-expired"
	SettingsChange = "settings-change"
)

// Event is one audited action.
type Event struct {
	Action   string // one of the actions above
	Category string // ECS event.category: process, authentication, configuration, file
	Type     string // ECS event.type: start, end, change, deletion, info
	Success  bool
	Reason   string // why it failed, or how a run ended
	Message  string

	User       string
	Roles      []string // Runwarden roles (ECS user.roles)
	Groups     []string // groups from the identity provider: shown, never grant rights
	AuthMethod string   // how the user signed in: gitlab, oidc, ldap or local
	SourceIP   string
	UserAgent  string

	RunID    string
	Script   string
	Status   string
	Values   map[string]any // masked parameter values
	Args     []string       // masked arguments
	ExitCode *int
	Duration time.Duration
	Count    int // e.g. runs removed by a cleanup

	Environments []string // environments a run touches
	Secrets      []string // names (never values) of the secrets a run received
	PolicyObject string   // what a policy change is about, e.g. "rule 1a2b3c4d"
	PolicyChange string   // short description of the change
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
		if len(e.Roles) > 0 {
			user = append(user, slog.Any("roles", e.Roles))
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
	var authAttrs []any
	if e.AuthMethod != "" {
		authAttrs = append(authAttrs, slog.String("method", e.AuthMethod))
	}
	if len(e.Groups) > 0 {
		authAttrs = append(authAttrs, slog.Any("groups", e.Groups))
	}
	if len(authAttrs) > 0 {
		custom = append(custom, slog.Group("auth", authAttrs...))
	}
	if len(e.Secrets) > 0 {
		custom = append(custom, slog.Any("secrets", e.Secrets))
	}
	if len(e.Environments) > 0 {
		custom = append(custom, slog.Any("environments", e.Environments))
	}
	if e.PolicyObject != "" || e.PolicyChange != "" {
		custom = append(custom, slog.Group("policy", slog.String("object", e.PolicyObject), slog.String("change", e.PolicyChange)))
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
			User: e.User, Roles: e.Roles, Groups: e.Groups, AuthMethod: e.AuthMethod, SourceIP: e.SourceIP, UserAgent: e.UserAgent,
			RunID: e.RunID, Script: e.Script, Status: e.Status, Values: e.Values, Args: e.Args,
			ExitCode: e.ExitCode, DurationMS: e.Duration.Milliseconds(), Count: e.Count,
			Environments: e.Environments, Secrets: e.Secrets, PolicyObject: e.PolicyObject, PolicyChange: e.PolicyChange,
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
	Roles      []string       `json:"roles,omitempty"`
	Groups     []string       `json:"groups,omitempty"`
	AuthMethod string         `json:"auth_method,omitempty"`
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

	Environments []string `json:"environments,omitempty"`
	Secrets      []string `json:"secrets,omitempty"`
	PolicyObject string   `json:"policy_object,omitempty"`
	PolicyChange string   `json:"policy_change,omitempty"`
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
