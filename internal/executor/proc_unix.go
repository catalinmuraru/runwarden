//go:build unix

package executor

import (
	"errors"
	"os"
	"os/exec"
	"syscall"
)

// prepare puts the script in its own process group, so signals reach all of its children.
func prepare(cmd *exec.Cmd) error {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	return nil
}

// terminateGroup sends SIGTERM to the process group led by pid.
func terminateGroup(pid int) { signalGroup(pid, syscall.SIGTERM) }

// killGroup sends SIGKILL to the process group led by pid.
func killGroup(pid int) { signalGroup(pid, syscall.SIGKILL) }

func signalGroup(pid int, sig syscall.Signal) {
	if pid <= 0 {
		return
	}
	if err := syscall.Kill(-pid, sig); err != nil && !errors.Is(err, syscall.ESRCH) {
		logger().Warn("signal process group", "pid", pid, "signal", sig.String(), "err", err)
	}
}

// exitCode reports the exit status, using the shell convention 128+n for signal n.
func exitCode(state *os.ProcessState) int {
	if ws, ok := state.Sys().(syscall.WaitStatus); ok && ws.Signaled() {
		return 128 + int(ws.Signal())
	}
	return state.ExitCode()
}
