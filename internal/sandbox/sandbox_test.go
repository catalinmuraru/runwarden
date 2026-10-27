package sandbox

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// The test binary doubles as the trampoline, as runwarden does.
func TestMain(m *testing.M) {
	if len(os.Args) > 1 && os.Args[1] == "sandbox-exec" {
		os.Exit(Main(os.Args[2:]))
	}
	os.Exit(m.Run())
}

func needLandlock(t *testing.T) {
	t.Helper()
	if runtime.GOOS != "linux" {
		t.Skip("Landlock is Linux only")
	}
	if _, err := probe(); err != nil {
		t.Skip(err)
	}
}

var system = []string{"/usr", "/bin", "/lib", "/lib64", "/etc"}

// sandboxed runs argv through the trampoline with p and returns its output and exit code.
func sandboxed(t *testing.T, p *Policy, argv ...string) (string, int) {
	t.Helper()
	cmd := exec.CommandContext(context.Background(), os.Args[0], append([]string{"sandbox-exec", "--"}, argv...)...) //nolint:gosec // test binary
	cmd.Env = []string{"PATH=/usr/bin:/bin"}
	if p != nil {
		cmd.Env = append(cmd.Env, EnvVar+"="+p.Encode())
	}
	out, err := cmd.CombinedOutput()
	code := 0
	var ee *exec.ExitError
	if errors.As(err, &ee) {
		code = ee.ExitCode()
	} else if err != nil {
		t.Fatal(err)
	}
	return string(out), code
}

func TestPolicy(t *testing.T) {
	needLandlock(t)
	dir := t.TempDir()
	ro, rw, hidden := filepath.Join(dir, "ro"), filepath.Join(dir, "rw"), filepath.Join(dir, "hidden")
	for _, d := range []string{ro, rw, hidden} {
		if err := os.Mkdir(d, 0o750); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(d, "f"), []byte("content of "+filepath.Base(d)), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	p := &Policy{Read: append(system, ro, filepath.Join(dir, "missing")), Write: []string{rw, "/dev/null"}}
	tests := []struct {
		name string
		argv []string
		ok   bool
	}{
		{"read allowed", []string{"cat", filepath.Join(ro, "f")}, true},
		{"read hidden", []string{"cat", filepath.Join(hidden, "f")}, false},
		{"list parent", []string{"ls", dir}, false},
		{"write read-only", []string{"touch", filepath.Join(ro, "new")}, false},
		{"write writable", []string{"sh", "-c", "echo x > " + filepath.Join(rw, "new") + " && cat " + filepath.Join(rw, "new")}, true},
		{"write /dev/null", []string{"sh", "-c", "echo x > /dev/null"}, true},
		{"environment cleaned", []string{"sh", "-c", "test -z \"$" + EnvVar + "\""}, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			out, code := sandboxed(t, p, tt.argv...)
			if (code == 0) != tt.ok {
				t.Errorf("exit %d, want ok=%v; output: %s", code, tt.ok, out)
			}
		})
	}
}

func TestMainErrors(t *testing.T) {
	needLandlock(t)
	if out, code := sandboxed(t, nil, "true"); code != 126 || !strings.Contains(out, "no policy") {
		t.Errorf("no policy: exit %d, %s", code, out)
	}
	p := &Policy{Read: system}
	if out, code := sandboxed(t, p, "no-such-command-here"); code != 127 {
		t.Errorf("missing command: exit %d, %s", code, out)
	}
	if out, code := sandboxed(t, &Policy{Read: []string{"/proc"}}, "/bin/true"); code != 127 {
		t.Errorf("command outside the policy: exit %d, %s", code, out)
	}
}

func TestDetect(t *testing.T) {
	st := Detect()
	if st.ABI == 0 && (st.Active || st.Reason == "") {
		t.Errorf("no Landlock but %+v", st)
	}
	if st.Active && os.Geteuid() == 0 {
		t.Error("root reported as isolated")
	}
	if st.SignalScoping != (st.ABI >= 6) {
		t.Errorf("signal scoping %v with ABI %d", st.SignalScoping, st.ABI)
	}
}

func TestWithin(t *testing.T) {
	tests := []struct {
		dir, path string
		want      bool
	}{
		{"/etc", "/etc", true},
		{"/etc", "/etc/runwarden/secrets", true},
		{"/etc/", "/etc/x/../y", true},
		{"/etc", "/etcetera", false},
		{"/var/lib/runwarden", "/var/lib", false},
		{"/", "/anything", true},
	}
	for _, tt := range tests {
		if got := Within(tt.dir, tt.path); got != tt.want {
			t.Errorf("Within(%q, %q) = %v", tt.dir, tt.path, got)
		}
	}
}
