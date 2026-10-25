//go:build linux

package executor

import (
	"fmt"
	"os"
	"os/exec"
	"strconv"
	"syscall"
	"unsafe"
)

// ptySupported reports whether scripts with requires_terminal get a pseudo-terminal.
const ptySupported = true

// Terminal size given to scripts (tools like ls and progress bars format for it).
const (
	ptyRows = 40
	ptyCols = 120
)

// openPTY opens a new pseudo-terminal pair: master (Runwarden's side, read for output and
// written for input) and tty (the script's terminal).
func openPTY() (master, tty *os.File, err error) {
	master, err = os.OpenFile("/dev/ptmx", os.O_RDWR|syscall.O_NOCTTY|syscall.O_CLOEXEC, 0)
	if err != nil {
		return nil, nil, fmt.Errorf("open pty: %w", err)
	}
	fail := func(err error) (*os.File, *os.File, error) {
		_ = master.Close()
		return nil, nil, fmt.Errorf("open pty: %w", err)
	}
	var unlock int32
	if err := ioctl(master, syscall.TIOCSPTLCK, &unlock); err != nil {
		return fail(err)
	}
	var n uint32
	if err := ioctl(master, syscall.TIOCGPTN, &n); err != nil {
		return fail(err)
	}
	tty, err = os.OpenFile("/dev/pts/"+strconv.FormatUint(uint64(n), 10), os.O_RDWR|syscall.O_NOCTTY|syscall.O_CLOEXEC, 0)
	if err != nil {
		return fail(err)
	}
	size := struct{ rows, cols, x, y uint16 }{ptyRows, ptyCols, 0, 0}
	if err := ioctl(tty, syscall.TIOCSWINSZ, &size); err != nil {
		_ = tty.Close()
		return fail(err)
	}
	return master, tty, nil
}

// ioctl runs an ioctl on f without switching it to blocking mode (as f.Fd() would), so reads
// keep honouring deadlines and Close. arg stays a typed pointer until the Syscall call
// expression itself, as the unsafe rules require, so the memory can't move in between.
func ioctl[T any](f *os.File, req uintptr, arg *T) error {
	conn, err := f.SyscallConn()
	if err != nil {
		return err
	}
	var errno syscall.Errno
	if err := conn.Control(func(fd uintptr) {
		// The kernel reads/writes the small struct arg points to; there is no safe API for it.
		_, _, errno = syscall.Syscall(syscall.SYS_IOCTL, fd, req, uintptr(unsafe.Pointer(arg))) //nolint:gosec // see above
	}); err != nil {
		return err
	}
	if errno != 0 {
		return errno
	}
	return nil
}

// preparePTY starts the script in its own session with the terminal as controlling terminal.
// The session leader's process group is its own, so stop and kill still reach every child.
func preparePTY(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true, Setctty: true, Ctty: 0}
}
