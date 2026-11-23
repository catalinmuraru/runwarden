package config

import (
	"regexp"
	"testing"

	"github.com/catalinmuraru/runwarden/internal/params"
)

func TestScriptHash(t *testing.T) {
	newScript := func() *Script {
		return &Script{
			Name: "deploy", Command: []string{"/opt/deploy.sh", "--fixed"}, WorkingDirectory: "/srv",
			Secrets: []string{"TOKEN"}, Kubernetes: true, RequiresTerminal: true,
			RedactPatterns: []*regexp.Regexp{regexp.MustCompile(`pw=\S+`)},
			Parameters: []*params.Parameter{
				{Name: "Env", Type: params.TypeList, ValuesScript: "list-envs ${Region}", EnvVar: "TARGET"},
				{Name: "Region", Default: params.Scalar("eu")},
			},
		}
	}
	base := newScript().Hash()
	if base != newScript().Hash() || len(base) != 64 {
		t.Fatalf("hash not deterministic: %s", base)
	}
	// Descriptions and grouping do not shape a run.
	s := newScript()
	s.Description, s.Group = "other", "other"
	if s.Hash() != base {
		t.Error("description changed the hash")
	}
	for name, change := range map[string]func(*Script){
		"command":     func(s *Script) { s.Command[0] = "/bin/sh" },
		"fixed arg":   func(s *Script) { s.Command = append(s.Command, "-x") },
		"dir":         func(s *Script) { s.WorkingDirectory = "/" },
		"secrets":     func(s *Script) { s.Secrets = append(s.Secrets, "OTHER") },
		"kubernetes":  func(s *Script) { s.Kubernetes = false },
		"terminal":    func(s *Script) { s.RequiresTerminal = false },
		"redact":      func(s *Script) { s.RedactPatterns = nil },
		"helper":      func(s *Script) { s.Parameters[0].ValuesScript = "sh -c id" },
		"env name":    func(s *Script) { s.Parameters[0].EnvVar = "LD_PRELOAD" },
		"default":     func(s *Script) { s.Parameters[1].Default = params.Scalar("us") },
		"pass as":     func(s *Script) { s.Parameters[1].PassAs = params.PassEnv },
		"new param":   func(s *Script) { s.Parameters = append(s.Parameters, &params.Parameter{Name: "x"}) },
		"param order": func(s *Script) { s.Parameters[0], s.Parameters[1] = s.Parameters[1], s.Parameters[0] },
	} {
		s := newScript()
		change(s)
		if s.Hash() == base {
			t.Errorf("%s: hash unchanged", name)
		}
	}
}

func TestExpandProgram(t *testing.T) {
	lookup := func(name string) (string, bool) {
		return map[string]string{"Env": "a b;c", "Tool": "/bin/sh"}[name], name != "Missing"
	}
	prog, args, err := ExpandProgram(`list.sh --env ${Env} "x ${Env}"`, lookup)
	if err != nil || prog != "list.sh" || len(args) != 3 || args[1] != "a b;c" || args[2] != "x a b;c" {
		t.Errorf("got %q %q %v", prog, args, err)
	}
	for _, cmd := range []string{"${Tool} list", "bin/${Tool}", "", "list.sh ${Missing}"} {
		if _, _, err := ExpandProgram(cmd, lookup); err == nil {
			t.Errorf("%q: no error", cmd)
		}
	}
	if got := ResolveProgram("/base", "scripts/list.sh"); got != "/base/scripts/list.sh" {
		t.Errorf("relative program: %s", got)
	}
	for _, p := range []string{"/abs/list.sh", "ls"} {
		if got := ResolveProgram("/base", p); got != p {
			t.Errorf("%s resolved to %s", p, got)
		}
	}
}
