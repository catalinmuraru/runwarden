package executor

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"

	"github.com/catalinmuraru/runwarden/internal/sandbox"
)

// Sandbox confines runs and helpers with Landlock (package sandbox): each one may read Read,
// write Write plus its own run directory and its user's workspace, and nothing else.
type Sandbox struct {
	Exe      string   // binary with the sandbox-exec subcommand (runwarden itself)
	Read     []string // read and execute for every run: system directories, scripts
	Write    []string // full access for every run: /dev/null, /dev/tty, operator extras
	MaxProcs uint64   // RLIMIT_NPROC per run (counted per uid, so a guard, not a quota); 0: unchanged
}

// WithSandbox makes e start every run and helper inside s. Call it before the first run.
func (e *Executor) WithSandbox(s *Sandbox) *Executor {
	e.sandbox = s
	return e
}

// wrap returns the program, arguments and environment that start name with args inside the
// sandbox with the extra read and write paths, or all unchanged when there is no sandbox. The
// program stays apart from the arguments so it never depends on user input. The trampoline
// keeps the PID, so process groups, the terminal and stop/kill work as without it.
func (e *Executor) wrap(name string, args, env, read, write []string) (string, []string, []string) {
	s := e.sandbox
	if s == nil {
		return name, args, env
	}
	p := sandbox.Policy{
		Read:     append(slices.Clone(s.Read), nonEmpty(read)...),
		Write:    append(slices.Clone(s.Write), nonEmpty(write)...),
		MaxProcs: s.MaxProcs,
	}
	return s.Exe, append([]string{"sandbox-exec", "--", name}, args...), append(env, sandbox.EnvVar+"="+p.Encode())
}

// selfTestScript must read $1, write into $2 (a new directory below $3) and fail to list $3.
const selfTestScript = `ls "$1" >/dev/null || exit 1
echo ok > "$2/check" || { echo "cannot write below $3" >&2; exit 1; }
if ls "$3" >/dev/null 2>&1; then echo "$3 is readable" >&2; exit 1; fi`

// SelfTest starts a sandboxed shell that must read readDir and write into a new directory
// below each of writeRoots (run directories, workspaces) without seeing the root itself. It
// catches filesystems on which Landlock rules for subdirectories never match (e.g. 9p, whose
// inodes change between lookups): runs would fail there. The probe directories are removed.
func (s *Sandbox) SelfTest(readDir string, writeRoots ...string) error {
	for _, root := range nonEmpty(writeRoots) {
		probe, err := os.MkdirTemp(root, ".isolation-check-")
		if err != nil {
			return fmt.Errorf("isolation self-test: %w", err)
		}
		p := sandbox.Policy{Read: s.Read, Write: append(slices.Clone(s.Write), probe)}
		cmd := exec.Command(s.Exe, "sandbox-exec", "--", "/bin/sh", "-c", selfTestScript, "sh", readDir, probe, root) //nolint:gosec,noctx // fixed script
		cmd.Env = []string{"PATH=" + DefaultPath, sandbox.EnvVar + "=" + p.Encode()}
		out, err := cmd.CombinedOutput()
		_ = os.RemoveAll(probe)
		if err != nil {
			return fmt.Errorf("isolation self-test in %s failed (%w): %s; Landlock may not support this filesystem",
				root, err, strings.TrimSpace(string(out)))
		}
	}
	return nil
}

func nonEmpty(paths []string) []string {
	return slices.DeleteFunc(slices.Clone(paths), func(p string) bool { return p == "" })
}

// SharedWorkspace is the directory below EnvPolicy.Workspace that every run may read.
const SharedWorkspace = "shared"

// workspace is where a run keeps files between runs: own is writable by the user's runs only,
// shared is read-only. Both are empty without EnvPolicy.Workspace.
type workspace struct {
	own, shared string
	vars        map[string]string
}

// prepareWorkspace returns the workspace of user, creating it on first use. Helpers (user "")
// get only the shared directory; a run without a signed-in user gets "anonymous".
func (p EnvPolicy) prepareWorkspace(user string, helper bool) (workspace, error) {
	if p.Workspace == "" {
		return workspace{}, nil
	}
	ws := workspace{shared: filepath.Join(p.Workspace, SharedWorkspace), vars: map[string]string{}}
	if err := os.MkdirAll(ws.shared, 0o755); err != nil { //nolint:gosec // read by every run on purpose
		return workspace{}, fmt.Errorf("shared workspace: %w", err)
	}
	ws.vars["RUNWARDEN_SHARED"] = ws.shared
	if helper {
		return ws, nil
	}
	ws.own = filepath.Join(p.Workspace, WorkspaceName(user))
	if err := os.Mkdir(ws.own, 0o700); err != nil && !os.IsExist(err) {
		return workspace{}, fmt.Errorf("workspace: %w", err)
	}
	ws.vars["RUNWARDEN_WORKSPACE"] = ws.own
	return ws, nil
}

// WorkspaceName is the directory name of user's workspace: the lower-case name when it only
// has letters, digits, '.', '-' and '_'; otherwise the name with those other characters replaced
// and a hash suffix, so two users never share a directory. "" is "anonymous", and the name
// never collides with the shared directory.
func WorkspaceName(user string) string {
	name := strings.ToLower(user)
	if name == "" {
		return "anonymous"
	}
	safe := strings.Map(func(r rune) rune {
		if r >= 'a' && r <= 'z' || r >= '0' && r <= '9' || r == '.' || r == '-' || r == '_' {
			return r
		}
		return '_'
	}, name)
	if safe == name && name != SharedWorkspace && strings.Trim(name, ".") != "" {
		return name
	}
	sum := sha256.Sum256([]byte(name))
	return strings.Trim(safe, ".") + "-" + hex.EncodeToString(sum[:4])
}
