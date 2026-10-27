// Package sandbox confines a run with Landlock: the run and everything it starts may only
// read the paths of its policy and write its own directories, cannot ptrace processes outside
// its domain (so other runs' /proc/<pid>/environ and memory stay closed), and on kernels with
// Landlock ABI 6 or later cannot signal them either. It needs no privileges.
//
// Go cannot run code between fork and exec, so the executor starts the run as
// `runwarden sandbox-exec -- <script> <args>` with the policy in RUNWARDEN_SANDBOX; Main applies
// it and execs the script in the same process (same PID, process group and terminal).
package sandbox

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"syscall"
)

// EnvVar carries the encoded Policy to the trampoline, which removes it before the exec.
const EnvVar = "RUNWARDEN_SANDBOX"

// Policy is what one sandboxed process may access. Paths that don't exist are skipped.
type Policy struct {
	Read     []string `json:"read,omitempty"`      // read and execute, recursively
	Write    []string `json:"write,omitempty"`     // every access, recursively
	MaxProcs uint64   `json:"max_procs,omitempty"` // RLIMIT_NPROC; 0 leaves it unchanged
}

// Encode returns the policy as the value of EnvVar.
func (p Policy) Encode() string {
	b, _ := json.Marshal(p) // only strings and numbers: cannot fail
	return string(b)
}

// Status describes the isolation available to runs.
type Status struct {
	Mode          string `json:"mode"`             // ISOLATION: auto, required or off
	Active        bool   `json:"active"`           // runs are sandboxed
	ABI           int    `json:"abi"`              // Landlock ABI version; 0 when unavailable
	SignalScoping bool   `json:"signal_scoping"`   // ABI >= 6: runs cannot signal processes outside their sandbox
	Reason        string `json:"reason,omitempty"` // why runs are not sandboxed, when !Active
}

// ErrUnavailable is returned when the kernel offers no Landlock.
var ErrUnavailable = errors.New("landlock is not available")

// Detect reports whether runs can be sandboxed. A process running as root is not isolated:
// with its capabilities a run can still read other runs' /proc/<pid>/environ.
func Detect() Status {
	abi, err := probe()
	st := Status{ABI: abi, SignalScoping: abi >= 6}
	switch {
	case err != nil:
		st.ABI, st.SignalScoping = 0, false
		st.Reason = err.Error()
	case os.Geteuid() == 0:
		st.Reason = "runwarden runs as root, which Landlock does not isolate"
	default:
		st.Active = true
	}
	return st
}

// Within reports whether path is dir or below it (lexically, after cleaning).
func Within(dir, path string) bool {
	rel, err := filepath.Rel(filepath.Clean(dir), filepath.Clean(path))
	return err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}

// Main is the `sandbox-exec` subcommand: args are `-- <command> <args...>`. It applies the
// policy from EnvVar to itself and replaces itself with the command. It returns an exit code
// only when that fails (126: sandbox, 127: command).
func Main(args []string) int {
	// Landlock and no_new_privs apply to the calling thread; Go may otherwise move this
	// goroutine to another thread between restricting and exec'ing.
	runtime.LockOSThread()
	if len(args) < 2 || args[0] != "--" {
		fmt.Fprintln(os.Stderr, "usage: runwarden sandbox-exec -- <command> [args...]")
		return 2
	}
	argv := args[1:]
	raw, ok := os.LookupEnv(EnvVar)
	if !ok {
		fmt.Fprintln(os.Stderr, "runwarden sandbox: no policy in", EnvVar)
		return 126
	}
	var p Policy
	if err := json.Unmarshal([]byte(raw), &p); err != nil {
		fmt.Fprintln(os.Stderr, "runwarden sandbox: bad policy:", err)
		return 126
	}
	_ = os.Unsetenv(EnvVar)
	if err := apply(p); err != nil {
		fmt.Fprintln(os.Stderr, "runwarden sandbox:", err)
		return 126
	}
	// Commands without a slash are looked up in the script's PATH, as exec.Command does.
	path, err := exec.LookPath(argv[0])
	if err != nil && !errors.Is(err, exec.ErrDot) {
		fmt.Fprintln(os.Stderr, "runwarden sandbox:", err)
		return 127
	}
	err = syscall.Exec(path, argv, os.Environ()) //nolint:gosec // argv from a validated config, no shell
	fmt.Fprintf(os.Stderr, "runwarden sandbox: exec %s: %v\n", argv[0], err)
	return 127
}
