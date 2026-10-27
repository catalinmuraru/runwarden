//go:build linux

package executor

import (
	"context"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"

	"github.com/catalinmuraru/runwarden/internal/config"
	"github.com/catalinmuraru/runwarden/internal/sandbox"
)

// The test binary doubles as the trampoline, as runwarden does.
func TestMain(m *testing.M) {
	if len(os.Args) > 1 && os.Args[1] == "sandbox-exec" {
		os.Exit(sandbox.Main(os.Args[2:]))
	}
	os.Exit(m.Run())
}

func needLandlock(t *testing.T) sandbox.Status {
	t.Helper()
	st := sandbox.Detect()
	if st.ABI == 0 {
		t.Skip(st.Reason)
	}
	return st
}

// testSandbox returns a sandbox for runs of the scripts it copies from testdata into a local
// temporary directory: Landlock rules don't match on every filesystem (e.g. WSL's /mnt/*, see
// Sandbox.SelfTest), and the repository may live on one.
func testSandbox(t *testing.T) (*Sandbox, func(file string) *config.Script) {
	t.Helper()
	dir := t.TempDir()
	files, err := filepath.Glob("testdata/*.sh")
	if err != nil {
		t.Fatal(err)
	}
	for _, f := range files {
		data, err := os.ReadFile(f) //nolint:gosec // test fixtures
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, filepath.Base(f)), data, 0o755); err != nil { //nolint:gosec // executable fixture
			t.Fatal(err)
		}
	}
	sb := &Sandbox{
		Exe:   os.Args[0],
		Read:  []string{"/usr", "/bin", "/lib", "/lib64", "/etc", "/proc", dir},
		Write: []string{"/dev/null", "/dev/tty"},
	}
	return sb, func(file string) *config.Script {
		return &config.Script{Name: file, Command: []string{filepath.Join(dir, file)}, WorkingDirectory: t.TempDir()}
	}
}

// isolationSetup lays out Runwarden's data, secrets and workspaces under one base directory.
func isolationSetup(t *testing.T) (base string, env EnvPolicy) {
	t.Helper()
	base = t.TempDir()
	for path, content := range map[string]string{ //nolint:gosec // test fixtures, not credentials
		"data/history.jsonl":       "every user's output",
		"secrets/prod/DB_PASSWORD": "prod-db-pw",
		"ws/shared/tool":           "team tool\n",
	} {
		p := filepath.Join(base, path)
		if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	return base, EnvPolicy{TempRoot: filepath.Join(base, "data", "tmp"), PrivateHome: true, Workspace: filepath.Join(base, "ws")}
}

// attack starts bob's run with a secret, then alice's run trying to reach it, and returns
// alice's results by attack name.
func attack(t *testing.T, e *Executor, script func(string) *config.Script, base string) map[string]string {
	t.Helper()
	victim := mustStart(t, e, Request{Script: script("victim.sh"), User: "bob", Secrets: map[string]string{"BOB_SECRET": "bob-env-secret"}}) //nolint:gosec // test value
	waitOutput(t, victim, "ready")
	pid := pidFrom(t, outputOf(victim), "pid")
	home := filepath.Join(e.env.TempRoot, victim.ID, "home")

	s := script("attack.sh")
	s.Command = append(s.Command, strconv.Itoa(pid), home, base)
	r := mustStart(t, e, Request{Script: s, User: "alice"})
	waitDone(t, r)
	out := outputOf(r)
	if !strings.Contains(out, "attacks done") {
		t.Fatalf("attack script did not finish:\n%s", out)
	}
	got := map[string]string{}
	for _, m := range regexp.MustCompile(`attack:([a-z-]+)=(\w+)`).FindAllStringSubmatch(out, -1) {
		got[m[1]] = m[2]
	}
	victim.Stop()
	if res := waitDone(t, victim); res.Status != StatusStopped {
		t.Errorf("sandboxed victim: status %s after Stop, want stopped", res.Status)
	}
	return got
}

var mustWork = []string{"own-home", "own-tmp", "own-workspace", "shared-read", "process-substitution", "own-children"}

func TestIsolationBlocksOtherRuns(t *testing.T) {
	st := needLandlock(t)
	base, env := isolationSetup(t)
	sb, script := testSandbox(t)
	got := attack(t, New(Limits{}, env).WithSandbox(sb), script, base)

	blocked := []string{"other-home", "data-dir", "secrets", "other-workspace-read", "other-workspace-write", "shared-write"}
	if os.Geteuid() != 0 { // root's capabilities get past the ptrace check (see sandbox.Detect)
		blocked = append(blocked, "other-environ", "other-mem")
	}
	if st.SignalScoping {
		blocked = append(blocked, "other-signal")
	}
	for _, name := range blocked {
		if got[name] != "blocked" {
			t.Errorf("%s: %q, want blocked", name, got[name])
		}
	}
	for _, name := range mustWork {
		if got[name] != "open" {
			t.Errorf("%s: %q, want open", name, got[name])
		}
	}
}

// Without a sandbox the same attacks succeed: the test above proves the sandbox, not the layout.
func TestIsolationOff(t *testing.T) {
	base, env := isolationSetup(t)
	_, script := testSandbox(t)
	got := attack(t, New(Limits{}, env), script, base)
	for _, name := range append([]string{"other-home", "other-environ", "other-signal", "data-dir", "secrets", "other-workspace-read", "other-workspace-write", "shared-write"}, mustWork...) {
		if got[name] != "open" {
			t.Errorf("%s: %q, want open", name, got[name])
		}
	}
}

func TestSandboxedTerminal(t *testing.T) {
	needLandlock(t)
	sb, script := testSandbox(t)
	s := script("tty_check.sh")
	s.RequiresTerminal = true
	r := mustStart(t, New(Limits{}, EnvPolicy{TempRoot: t.TempDir(), PrivateHome: true}).WithSandbox(sb), Request{Script: s})
	if res := waitDone(t, r); res.ExitCode != 0 {
		t.Fatalf("exit %d:\n%s", res.ExitCode, outputOf(r))
	}
	out := outputOf(r)
	for _, want := range []string{"/dev/pts/", "40 120", "via /dev/tty", "terminal ok"} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q in:\n%s", want, out)
		}
	}
}

func TestSandboxedHelper(t *testing.T) {
	needLandlock(t)
	base, env := isolationSetup(t)
	sb, script := testSandbox(t)
	e := New(Limits{}, env).WithSandbox(sb)
	out, err := e.Capture(context.Background(), []string{script("helper_probe.sh").Command[0], filepath.Join(base, "data", "history.jsonl")}, t.TempDir(), nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if got := string(out); got != "team tool\ndata:blocked\n" {
		t.Errorf("helper output %q", got)
	}
}

func TestWorkspaceName(t *testing.T) {
	tests := map[string]string{
		"Alice":         "alice",
		"bob.smith-2_x": "bob.smith-2_x",
		"":              "anonymous",
	}
	for user, want := range tests {
		if got := WorkspaceName(user); got != want {
			t.Errorf("WorkspaceName(%q) = %q, want %q", user, got, want)
		}
	}
	seen := map[string]string{}
	for _, user := range []string{"a/b", "a_b", "a b", "..", ".", "shared", "Shared", "DOMAIN\\alice", "x@example.com"} {
		got := WorkspaceName(user)
		if strings.ContainsAny(got, "/\\ @") || got == "." || got == ".." || got == SharedWorkspace || got == "" {
			t.Errorf("WorkspaceName(%q) = %q: not a safe directory name", user, got)
		}
		if prev, ok := seen[got]; ok && !strings.EqualFold(prev, user) {
			t.Errorf("%q and %q share workspace %q", prev, user, got)
		}
		seen[got] = user
	}
}

func TestWorkspaceEnv(t *testing.T) {
	root := t.TempDir()
	e := New(Limits{}, EnvPolicy{Workspace: root})
	r := mustStart(t, e, Request{Script: testScript(t, "args.sh"), User: "Alice"})
	waitDone(t, r)
	out := outputOf(r)
	for _, want := range []string{"env:RUNWARDEN_WORKSPACE=" + filepath.Join(root, "alice"), "env:RUNWARDEN_SHARED=" + filepath.Join(root, "shared")} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q in:\n%s", want, out)
		}
	}
	info, err := os.Stat(filepath.Join(root, "alice"))
	if err != nil || info.Mode().Perm() != 0o700 {
		t.Errorf("workspace dir: %v %v", info, err)
	}
}

func TestSelfTest(t *testing.T) {
	needLandlock(t)
	sb, _ := testSandbox(t)
	root := t.TempDir()
	if err := sb.SelfTest("/usr", root); err != nil {
		t.Fatalf("self-test on a local directory: %v", err)
	}
	if entries, _ := os.ReadDir(root); len(entries) != 0 {
		t.Errorf("probe directory left behind: %v", entries)
	}
	// A write root every run can already read fails: the root must stay hidden.
	sb.Read = append(sb.Read, root)
	if err := sb.SelfTest("/usr", root); err == nil || !strings.Contains(err.Error(), "is readable") {
		t.Errorf("readable root: %v", err)
	}
}
