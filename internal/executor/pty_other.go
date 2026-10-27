//go:build !linux

package executor

import (
	"errors"
	"os"
	"os/exec"
)

// ptySupported is false outside Linux: scripts run with pipes even when they request a terminal.
const ptySupported = false

func openPTY(int, int) (*os.File, *os.File, error) {
	return nil, nil, errors.New("pty: not supported")
}

func setSize(*os.File, int, int) error { return nil }

func preparePTY(*exec.Cmd) {}
