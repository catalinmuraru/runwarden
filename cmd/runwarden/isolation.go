package main

import (
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"

	"github.com/catalinmuraru/runwarden/internal/config"
	"github.com/catalinmuraru/runwarden/internal/executor"
	"github.com/catalinmuraru/runwarden/internal/sandbox"
)

// Paths every sandboxed run may read: the system, without /tmp, /home, /var or /run (where
// the service account token lives). Runwarden's own data, secrets and kubeconfigs must not be
// below any of them; see checkIsolation.
var (
	sandboxRead  = []string{"/usr", "/bin", "/sbin", "/lib", "/lib32", "/lib64", "/libx32", "/etc", "/proc", "/sys", "/dev/zero", "/dev/random", "/dev/urandom"}
	sandboxWrite = []string{"/dev/null", "/dev/full", "/dev/tty"}
)

// newSandbox decides whether runs are sandboxed (ISOLATION) and returns the sandbox, or nil.
// tempRoot holds the run directories.
// With "required" every reason for not sandboxing is an error; with "auto" it is a warning.
func newSandbox(cfg config.App, tempRoot string) (*executor.Sandbox, sandbox.Status, error) {
	st := sandbox.Status{Mode: cfg.Isolation}
	if cfg.Isolation == config.IsolationOff {
		st.Reason = "ISOLATION=off"
		slog.Warn("isolation: runs are not sandboxed and can read each other's files and Runwarden's data", "reason", st.Reason)
		return nil, st, nil
	}
	detected := sandbox.Detect()
	detected.Mode = cfg.Isolation
	st = detected
	sb := &executor.Sandbox{
		Read:     append(append(append([]string(nil), sandboxRead...), cfg.BaseDir), cfg.IsolationRead...),
		Write:    append(append([]string(nil), sandboxWrite...), cfg.IsolationWrite...),
		MaxProcs: uint64(cfg.RunMaxProcs), //nolint:gosec // validated >= 0
	}
	var err error
	if st.Active {
		if sb.Exe, err = os.Executable(); err != nil {
			st.Active, st.Reason = false, "own executable: "+err.Error()
		}
	}
	if st.Active {
		if err := checkIsolation(cfg, append(append([]string(nil), sb.Read...), sb.Write...)); err != nil {
			st.Active, st.Reason = false, err.Error()
		}
	}
	if st.Active {
		if err := sb.SelfTest(cfg.BaseDir, tempRoot, cfg.WorkspaceDir); err != nil {
			st.Active, st.Reason = false, err.Error()
		}
	}
	if !st.Active {
		if cfg.Isolation == config.IsolationRequired {
			return nil, st, fmt.Errorf("ISOLATION=required: %s", st.Reason)
		}
		slog.Warn("isolation: runs are not sandboxed and can read each other's files and Runwarden's data", "reason", st.Reason)
		return nil, st, nil
	}
	slog.Info("isolation: runs are sandboxed with Landlock", "abi", st.ABI, "signal_scoping", st.SignalScoping)
	if !st.SignalScoping {
		slog.Warn("isolation: Landlock ABI below 6, so a run can still signal another run's processes", "abi", st.ABI)
	}
	return sb, st, nil
}

// describeIsolation is the one-line isolation state on the readiness probe.
func describeIsolation(st sandbox.Status) string {
	if !st.Active {
		return fmt.Sprintf("not sandboxed (ISOLATION=%s): %s", st.Mode, st.Reason)
	}
	if st.SignalScoping {
		return fmt.Sprintf("sandboxed (Landlock ABI %d, signals scoped)", st.ABI)
	}
	return fmt.Sprintf("sandboxed (Landlock ABI %d, signals not scoped)", st.ABI)
}

// checkIsolation refuses a sandbox that would hand runs Runwarden's data, secrets, kubeconfigs
// or the workspace root: none of them may be below a path every run can access, nor contain
// one. Landlock has no deny rules, so this is the only protection against such a layout.
func checkIsolation(cfg config.App, allowed []string) error {
	private := map[string]string{"DATA_DIR": cfg.DataDir, "SECRETS_DIR": cfg.SecretsDir, "KUBE_DIR": cfg.KubeDir}
	if cfg.WorkspaceDir != "" {
		private["WORKSPACE_DIR"] = cfg.WorkspaceDir
	}
	var errs []error
	for name, dir := range private {
		dir = resolve(dir)
		for _, a := range allowed {
			if r := resolve(a); sandbox.Within(r, dir) || sandbox.Within(dir, r) {
				errs = append(errs, fmt.Errorf("%s %s overlaps %s, which every run can access", name, dir, a))
			}
		}
	}
	return errors.Join(errs...)
}

// resolve follows symlinks where the path exists, so /bin → /usr/bin compares correctly.
func resolve(p string) string {
	if r, err := filepath.EvalSymlinks(p); err == nil {
		return r
	}
	return filepath.Clean(p)
}
