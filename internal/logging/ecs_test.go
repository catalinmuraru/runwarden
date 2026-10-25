package logging

import (
	"bytes"
	"encoding/json"
	"errors"
	"log/slog"
	"strings"
	"testing"
	"time"
)

func decode(t *testing.T, buf *bytes.Buffer) []map[string]any {
	t.Helper()
	var out []map[string]any
	for _, line := range strings.Split(strings.TrimSpace(buf.String()), "\n") {
		var m map[string]any
		if err := json.Unmarshal([]byte(line), &m); err != nil {
			t.Fatalf("not JSON: %q", line)
		}
		out = append(out, m)
	}
	return out
}

func TestECSHandler(t *testing.T) {
	var buf bytes.Buffer
	log := slog.New(NewECSHandler(&buf, "v1.2.3", slog.LevelDebug))
	log.With("component", "executor").Warn("run started", "run", "r1", "err", errors.New("boom"))
	log.Info("custom event", slog.Group("event", slog.String("dataset", "runwarden.audit")), slog.Group("user", slog.String("name", "alice")))

	lines := decode(t, &buf)
	first := lines[0]
	ts, _ := first["@timestamp"].(string)
	if _, err := time.Parse(time.RFC3339Nano, ts); err != nil || !strings.HasSuffix(ts, "Z") {
		t.Errorf("@timestamp = %q", ts)
	}
	want := map[string]any{"log.level": "warn", "message": "run started", "ecs.version": ECSVersion, "error.message": "boom"}
	for k, v := range want {
		if first[k] != v {
			t.Errorf("%s = %v, want %v", k, first[k], v)
		}
	}
	for _, gone := range []string{"time", "level", "msg", "err", "run", "component"} {
		if _, ok := first[gone]; ok {
			t.Errorf("non-ECS key %q present", gone)
		}
	}
	svc, _ := first["service"].(map[string]any)
	if svc["name"] != "runwarden" || svc["version"] != "v1.2.3" {
		t.Errorf("service = %v", svc)
	}
	custom, _ := first["runwarden"].(map[string]any)
	if custom["run"] != "r1" || custom["component"] != "executor" {
		t.Errorf("custom fields not under runwarden: %v", first)
	}
	if ev, _ := first["event"].(map[string]any); ev["dataset"] != "runwarden.app" {
		t.Errorf("app event.dataset = %v", first["event"])
	}

	second := lines[1]
	if ev, _ := second["event"].(map[string]any); ev["dataset"] != "runwarden.audit" {
		t.Errorf("explicit event group replaced: %v", second["event"])
	}
	if u, _ := second["user"].(map[string]any); u["name"] != "alice" {
		t.Errorf("ECS group moved: %v", second)
	}
}
