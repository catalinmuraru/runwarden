package config

import (
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/catalinmuraru/runwarden/internal/params"
)

// repoRoot is the base dir for the example runners, whose paths are relative to the repo root.
func repoRoot(t *testing.T) string {
	t.Helper()
	root, err := filepath.Abs("../..")
	if err != nil {
		t.Fatal(err)
	}
	return root
}

func loadExamples(t *testing.T) *Catalog {
	t.Helper()
	root := repoRoot(t)
	cat, err := LoadDir(filepath.Join(root, "examples", "runners"), Options{BaseDir: root})
	if err != nil {
		t.Fatal(err)
	}
	if len(cat.Errors) > 0 {
		t.Fatalf("errors loading examples: %v", cat.Errors)
	}
	return cat
}

func scriptNames(c *Catalog) []string {
	var names []string
	for _, s := range c.Scripts {
		names = append(names, s.Name)
	}
	return names
}

func mustScript(t *testing.T, c *Catalog, name string) *Script {
	t.Helper()
	s := c.Script(name)
	if s == nil {
		t.Fatalf("script %q not loaded; have %q", name, scriptNames(c))
	}
	return s
}

func mustParam(t *testing.T, s *Script, name string) *params.Parameter {
	t.Helper()
	p := s.Parameter(name)
	if p == nil {
		t.Fatalf("%s: parameter %q missing", s.Name, name)
	}
	return p
}

func TestLoadExamples(t *testing.T) {
	cat := loadExamples(t)
	root := repoRoot(t)

	want := []string{"Backup Database", "Check Credentials", "Disk Usage Report", "Restart Service", "🗂️ Rotate Logs"}
	if got := scriptNames(cat); !slices.Equal(got, want) {
		t.Fatalf("scripts = %q, want %q (hidden include target must not be listed)", got, want)
	}
	if len(cat.Warnings) != 1 || !strings.Contains(cat.Warnings[0].Msg, `"Dry run" is also defined in include`) {
		t.Errorf("warnings = %v, want exactly the duplicate include parameter", cat.Warnings)
	}

	t.Run("backup database", func(t *testing.T) {
		s := mustScript(t, cat, "Backup Database")
		if want := filepath.Join(root, "examples/scripts/print_args.sh"); !slices.Equal(s.Command, []string{want}) {
			t.Errorf("command = %q, want %q", s.Command, want)
		}
		if s.WorkingDirectory != root {
			t.Errorf("working dir = %q, want %q", s.WorkingDirectory, root)
		}
		if !slices.Equal(s.Tags, []string{"dba", "devops"}) || s.EnvironmentParameters != nil {
			t.Errorf("tags = %q, environment parameters = %v", s.Tags, s.EnvironmentParameters)
		}
		level := mustParam(t, s, "Compression level")
		if level.Min == nil || *level.Min != 1 || level.Max == nil || *level.Max != 9 {
			t.Errorf("min/max = %v/%v, want 1/9 (max given as a string)", level.Min, level.Max)
		}
		if level.Default.Str != "6" || !level.SameArgParam {
			t.Errorf("compression level = %+v", level)
		}
		if v := mustParam(t, s, "Verify after backup"); !v.NoValue || v.Default.Kind != params.KindFlag || !v.Default.Flag {
			t.Errorf("verify flag = %+v", v)
		}
		if v := mustParam(t, s, "Tables"); v.ArgMode != params.ArgRepeatParam {
			t.Errorf("tables mode = %q", v.ArgMode)
		}
		if v := mustParam(t, s, "Label"); v.Regex == nil || v.Regex.MatchString("UPPER") || !v.Regex.MatchString("ok-1") {
			t.Errorf("label regex = %v", v.Regex)
		}
		if v := mustParam(t, s, "Storage token"); v.PassAs != params.PassEnv || v.EnvVar != "STORAGE_TOKEN" || !v.Secure {
			t.Errorf("storage token = %+v", v)
		}
		if v := mustParam(t, s, "Tool version"); !v.Constant || v.Default.Str != "2.4.1" {
			t.Errorf("tool version = %+v", v)
		}
	})

	t.Run("rotate logs: positional, string booleans, legacy option", func(t *testing.T) {
		s := mustScript(t, cat, "🗂️ Rotate Logs")
		if s.RequiresTerminal {
			t.Error(`requires_terminal "false" parsed as true`)
		}
		if v := mustParam(t, s, "Service"); !v.Required || v.Type != params.TypeText || v.Param != "" {
			t.Errorf("service = %+v", v)
		}
		if v := mustParam(t, s, "Instances"); v.Secure || v.Separator != " " || v.ValuesScript == "" {
			t.Errorf("instances = %+v", v)
		}
		if v := mustParam(t, s, "Log files"); v.ArgMode != params.ArgPerValue {
			t.Errorf("legacy multiple_arguments mode = %q, want %q", v.ArgMode, params.ArgPerValue)
		}
		archive := mustParam(t, s, "Archive")
		if !archive.FileRecursive || archive.FileType != params.FileTypeDir || archive.FileDir != filepath.Join(root, "examples/archives") {
			t.Errorf("archive = %+v", archive)
		}
		if v := mustParam(t, s, "Keep days"); v.PassAs != params.PassStdin || v.Default.Str != "14" {
			t.Errorf("keep days = %+v", v)
		}
	})

	t.Run("disk usage report: no parameters", func(t *testing.T) {
		s := mustScript(t, cat, "Disk Usage Report")
		if len(s.Parameters) != 0 || !s.Scheduling || !s.RequiresTerminal || len(s.OutputFiles) != 1 {
			t.Errorf("script = %+v", s)
		}
	})

	t.Run("restart service: include merged, own definition wins", func(t *testing.T) {
		s := mustScript(t, cat, "Restart Service")
		var names []string
		for _, p := range s.Parameters {
			names = append(names, p.Name)
		}
		if want := []string{"Service", "Dry run", "Timeout seconds"}; !slices.Equal(names, want) {
			t.Fatalf("parameters = %q, want %q", names, want)
		}
		if v := mustParam(t, s, "Dry run"); v.Param != "--dry-run" || v.NoValue {
			t.Errorf("dry run came from the include: %+v", v)
		}
		if !strings.HasPrefix(s.Description, "Restarts a service") {
			t.Errorf("description overwritten by include: %q", s.Description)
		}
	})
}

func TestLoadErrorsDoNotBlockOtherFiles(t *testing.T) {
	dir := t.TempDir()
	files := map[string]string{ //nolint:gosec // test fixtures, no credentials
		"good.json":           `{"name": "Good", "script_path": "run.sh"}`,
		"templated.json":      `{"name": "Templated", "script_path": "run.sh", "include": "${Env}.json"}`,
		"hidden.json":         `{"hidden": "true", "parameters": [{"name": "x", "type": "nonsense"}]}`,
		"not_json.txt":        `ignored`,
		"sub/nested.json":     `{"name": "Nested", "script_path": "run.sh"}`,
		"bad_json.json":       `{`,
		"bad_bool.json":       `{"name": "B", "script_path": "run.sh", "parameters": [{"name": "x", "required": 5}]}`,
		"no_default.json":     `{"name": "C", "script_path": "run.sh", "parameters": [{"name": "x", "constant": true}]}`,
		"unknown_type.json":   `{"name": "D", "script_path": "run.sh", "parameters": [{"name": "x", "type": "integer"}, {"name": "y", "type": "IPv4"}]}`,
		"zz_duplicate.json":   `{"name": "Good", "script_path": "other.sh"}`, // loads after good.json
		"bad_regex.json":      `{"name": "E", "script_path": "run.sh", "parameters": [{"name": "x", "regex": {"pattern": "(?=x)"}}]}`,
		"no_script.json":      `{"name": "F"}`,
		"no_file_dir.json":    `{"name": "G", "script_path": "run.sh", "parameters": [{"name": "x", "type": "server_file"}]}`,
		"dup_param.json":      `{"name": "H", "script_path": "run.sh", "parameters": [{"name": "x"}, {"name": "x"}]}`,
		"bad_mode.json":       `{"name": "I", "script_path": "run.sh", "parameters": [{"name": "x", "multiselect_argument_type": "weird"}]}`,
		"bad_quote.json":      `{"name": "J", "script_path": "'run.sh"}`,
		"bad_redact.json":     `{"name": "M", "script_path": "run.sh", "redact_patterns": ["(?=x)"]}`,
		"redact.json":         `{"name": "N", "script_path": "run.sh", "redact_patterns": ["ghp_[A-Za-z0-9]+"]}`,
		"shell_values.json":   `{"name": "K", "script_path": "run.sh", "parameters": [{"name": "x", "type": "list", "values": {"script": "ls /var | grep log"}}]}`,
		"unknown_passas.json": `{"name": "L", "script_path": "run.sh", "parameters": [{"name": "x", "pass_as": "telepathy"}]}`,
	}
	for name, content := range files {
		path := filepath.Join(dir, name)
		if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
	}

	cat, err := LoadDir(dir, Options{BaseDir: dir})
	if err != nil {
		t.Fatal(err)
	}

	if got, want := scriptNames(cat), []string{"D", "Good", "K", "L", "N", "Nested", "Templated"}; !slices.Equal(got, want) {
		t.Errorf("scripts = %q, want %q", got, want)
	}
	failed := map[string]bool{}
	for _, e := range cat.Errors {
		failed[filepath.Base(e.File)] = true
	}
	for _, name := range []string{"bad_json.json", "bad_bool.json", "no_default.json",
		"zz_duplicate.json", "bad_regex.json", "no_script.json", "no_file_dir.json", "dup_param.json",
		"bad_mode.json", "bad_quote.json", "bad_redact.json"} {
		if !failed[name] {
			t.Errorf("expected an error for %s; errors: %v", name, cat.Errors)
		}
	}
	if len(cat.Errors) != 11 {
		t.Errorf("got %d errors, want 11: %v", len(cat.Errors), cat.Errors)
	}

	warned := map[string]bool{}
	for _, w := range cat.Warnings {
		warned[filepath.Base(w.File)] = true
	}
	for _, name := range []string{"templated.json", "shell_values.json", "unknown_passas.json", "unknown_type.json"} {
		if !warned[name] {
			t.Errorf("expected a warning for %s; warnings: %v", name, cat.Warnings)
		}
	}
	if s := cat.Script("D"); s == nil || s.Parameters[0].Type != params.TypeText || s.Parameters[1].Type != params.TypeIP4 {
		t.Errorf("unknown type must become text and IPv4 must become ip4: %+v", s)
	}
	if s := cat.Script("Good"); s == nil || filepath.Base(s.Command[0]) != "run.sh" {
		t.Errorf("first definition of a duplicate name must win: %+v", s)
	}
	if s := cat.Script("Templated"); s == nil || !slices.Equal(s.IncludeTemplates, []string{"${Env}.json"}) {
		t.Errorf("include template not recorded: %+v", s)
	}
}

func TestTimeout(t *testing.T) {
	dir := t.TempDir()
	files := map[string]string{
		"none.json":    `{"name": "None", "script_path": "run.sh"}`,
		"string.json":  `{"name": "String", "script_path": "run.sh", "timeout": "4h"}`,
		"minutes.json": `{"name": "Minutes", "script_path": "run.sh", "timeout": 90}`,
		"quoted.json":  `{"name": "Quoted", "script_path": "run.sh", "timeout": "45"}`,
		"bad.json":     `{"name": "Bad", "script_path": "run.sh", "timeout": "soon"}`,
		"zero.json":    `{"name": "Zero", "script_path": "run.sh", "timeout": 0}`,
		"huge.json":    `{"name": "Huge", "script_path": "run.sh", "timeout": "3h"}`,
	}
	for name, content := range files {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	cat, err := LoadDir(dir, Options{BaseDir: dir, MaxTimeout: 4 * time.Hour})
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]time.Duration{"None": 0, "String": 4 * time.Hour, "Minutes": 90 * time.Minute, "Quoted": 45 * time.Minute, "Huge": 3 * time.Hour}
	for name, d := range want {
		if s := cat.Script(name); s == nil || s.Timeout != d {
			t.Errorf("%s: timeout = %+v, want %s", name, s, d)
		}
	}
	if len(cat.Errors) != 2 {
		t.Errorf("errors = %v, want bad.json and zero.json", cat.Errors)
	}

	cat, _ = LoadDir(dir, Options{BaseDir: dir, MaxTimeout: 2 * time.Hour})
	if cat.Script("Huge") != nil || cat.Script("String") != nil {
		t.Error("timeouts above the maximum must be refused")
	}
}

func TestAccessFields(t *testing.T) {
	dir := t.TempDir()
	param := func(name string) string { return `{"name": "` + name + `", "type": "list", "values": ["dev", "prod"]}` }
	files := map[string]string{
		"copy.json": `{"name": "Copy", "script_path": "run.sh", "tags": ["Developer", "devops", "developer"],
			"environment_parameters": {"From": "source", "To": ""},
			"parameters": [` + param("From") + `, ` + param("To") + `]}`,
		"single_tag.json": `{"name": "Single", "script_path": "run.sh", "tags": "devops"}`,
		"legacy.json":     `{"name": "Legacy", "script_path": "run.sh", "allowed_users": ["@ops"], "admin_users": "bob"}`,
		"undeclared.json": `{"name": "Undeclared", "script_path": "run.sh", "parameters": [` + param("K8S_NAMESPACE") + `]}`,
		"bad_tag.json":    `{"name": "BadTag", "script_path": "run.sh", "tags": ["two words"]}`,
		"bad_use.json":    `{"name": "BadUse", "script_path": "run.sh", "environment_parameters": {"Env": "both"}, "parameters": [` + param("Env") + `]}`,
		"unknown.json":    `{"name": "Unknown", "script_path": "run.sh", "environment_parameters": {"Env": "target"}}`,
		"upload.json": `{"name": "Upload", "script_path": "run.sh", "environment_parameters": {"F": "target"},
			"parameters": [{"name": "F", "type": "file_upload"}]}`,
	}
	for name, content := range files {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	cat, err := LoadDir(dir, Options{BaseDir: dir})
	if err != nil {
		t.Fatal(err)
	}
	s := cat.Script("Copy")
	if s == nil || !slices.Equal(s.Tags, []string{"Developer", "devops"}) || !s.HasTag("DEVELOPER") ||
		!maps.Equal(s.EnvironmentParameters, map[string]string{"From": EnvSource, "To": EnvTarget}) {
		t.Errorf("copy = %+v", s)
	}
	if s := cat.Script("Single"); s == nil || !slices.Equal(s.Tags, []string{"devops"}) {
		t.Errorf("single tag = %+v", s)
	}
	failed := map[string]bool{}
	for _, e := range cat.Errors {
		failed[filepath.Base(e.File)] = true
	}
	if len(cat.Errors) != 4 || !failed["bad_tag.json"] || !failed["bad_use.json"] || !failed["unknown.json"] || !failed["upload.json"] {
		t.Errorf("errors = %v", cat.Errors)
	}
	warnings := map[string]string{}
	for _, w := range cat.Warnings {
		warnings[filepath.Base(w.File)] += w.Msg
	}
	if len(warnings) != 2 || !strings.Contains(warnings["legacy.json"], "ignored") || !strings.Contains(warnings["undeclared.json"], "K8S_NAMESPACE") {
		t.Errorf("warnings = %v", cat.Warnings)
	}
}

func TestLoadDirMissing(t *testing.T) {
	if _, err := LoadDir(filepath.Join(t.TempDir(), "nope"), Options{}); err == nil {
		t.Error("expected an error for a missing directory")
	}
}

func TestSplitCommand(t *testing.T) {
	tests := []struct {
		in      string
		want    []string
		wantErr bool
	}{
		{in: "run.sh", want: []string{"run.sh"}},
		{in: "  /bin/bash   conf/x.sh  ", want: []string{"/bin/bash", "conf/x.sh"}},
		{in: `'a b' "c \"d\"" e\ f`, want: []string{"a b", `c "d"`, "e f"}},
		{in: `x ''`, want: []string{"x", ""}},
		{in: `'unterminated`, wantErr: true},
		{in: `trailing\`, wantErr: true},
		{in: "   ", wantErr: true},
	}
	for _, tt := range tests {
		got, err := splitCommand(tt.in)
		if (err != nil) != tt.wantErr {
			t.Errorf("splitCommand(%q) error = %v, wantErr %v", tt.in, err, tt.wantErr)
			continue
		}
		if !tt.wantErr && !slices.Equal(got, tt.want) {
			t.Errorf("splitCommand(%q) = %q, want %q", tt.in, got, tt.want)
		}
	}
}

// TestCompat loads an external runners directory and fails on any error. It is opt-in:
//
//	RUNWARDEN_CONFIG_DIR=/path/to/runners [RUNWARDEN_CONFIG_BASE_DIR=/path] go test ./internal/config -run Compat -v
func TestCompat(t *testing.T) {
	dir := os.Getenv("RUNWARDEN_CONFIG_DIR")
	if dir == "" {
		t.Skip("RUNWARDEN_CONFIG_DIR not set")
	}
	cat, err := LoadDir(dir, Options{BaseDir: os.Getenv("RUNWARDEN_CONFIG_BASE_DIR")})
	if err != nil {
		t.Fatal(err)
	}
	for _, w := range cat.Warnings {
		t.Logf("warning: %s", w)
	}
	for _, e := range cat.Errors {
		t.Errorf("error: %s", e)
	}
	t.Logf("%d scripts loaded", len(cat.Scripts))
}

func TestSecretsField(t *testing.T) {
	dir := t.TempDir()
	env := `"environment_parameters": {"Env": "target"}, "parameters": [{"name": "Env", "type": "list", "values": ["dev"]}]`
	files := map[string]string{
		"ok.json":       `{"name": "Ok", "script_path": "run.sh", "secrets": ["PRIVATE_TOKEN", " DB_PASSWORD ", "PRIVATE_TOKEN"], ` + env + `}`,
		"single.json":   `{"name": "Single", "script_path": "run.sh", "secrets": "CF_API_TOKEN"}`,
		"lower.json":    `{"name": "Lower", "script_path": "run.sh", "secrets": ["token"]}`,
		"reserved.json": `{"name": "Reserved", "script_path": "run.sh", "secrets": ["PATH"]}`,
		"prefix.json":   `{"name": "Prefix", "script_path": "run.sh", "secrets": ["SOURCE_DB"]}`,
		"ld.json":       `{"name": "Ld", "script_path": "run.sh", "secrets": ["LD_PRELOAD"]}`,
	}
	for name, content := range files {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	cat, err := LoadDir(dir, Options{BaseDir: dir})
	if err != nil {
		t.Fatal(err)
	}
	if s := cat.Script("Ok"); s == nil || !slices.Equal(s.Secrets, []string{"PRIVATE_TOKEN", "DB_PASSWORD"}) {
		t.Errorf("ok = %+v", s)
	}
	if s := cat.Script("Single"); s == nil || !slices.Equal(s.Secrets, []string{"CF_API_TOKEN"}) {
		t.Errorf("single = %+v", s)
	}
	if len(cat.Errors) != 4 {
		t.Errorf("errors = %v", cat.Errors)
	}
	if len(cat.Warnings) != 1 || !strings.Contains(cat.Warnings[0].Msg, "only the common secrets") || filepath.Base(cat.Warnings[0].File) != "single.json" {
		t.Errorf("warnings = %v", cat.Warnings)
	}
}

func TestKubernetesField(t *testing.T) {
	dir := t.TempDir()
	env := `"parameters": [{"name": "Env", "type": "list", "values": ["dev"]}]`
	files := map[string]string{
		"ok.json":     `{"name": "Ok", "script_path": "run.sh", "kubernetes": true, "environment_parameters": {"Env": "target"}, ` + env + `}`,
		"off.json":    `{"name": "Off", "script_path": "run.sh"}`,
		"noenv.json":  `{"name": "NoEnv", "script_path": "run.sh", "kubernetes": true}`,
		"source.json": `{"name": "Source", "script_path": "run.sh", "kubernetes": true, "environment_parameters": {"Env": "source"}, ` + env + `}`,
	}
	for name, content := range files {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	cat, err := LoadDir(dir, Options{BaseDir: dir})
	if err != nil {
		t.Fatal(err)
	}
	if s := cat.Script("Ok"); s == nil || !s.Kubernetes {
		t.Errorf("ok = %+v", s)
	}
	if s := cat.Script("Off"); s == nil || s.Kubernetes {
		t.Errorf("off = %+v", s)
	}
	if len(cat.Errors) != 2 || !strings.Contains(cat.Errors[0].Msg, "needs a target environment parameter") {
		t.Errorf("errors = %v", cat.Errors)
	}
}
