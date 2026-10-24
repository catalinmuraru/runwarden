//go:build !unix

package executor

import (
	"errors"
	"os"
	"os/exec"
)

// ErrUnsupported is returned on platforms without process groups.
var ErrUnsupported = errors.New("executor: running scripts requires a unix system")

func prepare(*exec.Cmd) error { return ErrUnsupported }

func terminateGroup(int) {}

func killGroup(int) {}

func exitCode(state *os.ProcessState) int { return state.ExitCode() }
