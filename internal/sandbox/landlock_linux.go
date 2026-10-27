//go:build linux

package sandbox

import (
	"errors"
	"fmt"
	"syscall"
	"unsafe"
)

// Landlock syscalls (the same numbers on every architecture) and constants from
// include/uapi/linux/landlock.h.
const (
	sysCreateRuleset = 444
	sysAddRule       = 445
	sysRestrictSelf  = 446

	createRulesetVersion = 1 << 0
	rulePathBeneath      = 1
	prSetNoNewPrivs      = 38
	oPath                = 0x200000 // O_PATH, not exported by package syscall

	scopeAbstractUnixSocket = 1 << 0
	scopeSignal             = 1 << 1
)

// Filesystem access rights.
const (
	fsExecute = 1 << iota
	fsWriteFile
	fsReadFile
	fsReadDir
	fsRemoveDir
	fsRemoveFile
	fsMakeChar
	fsMakeDir
	fsMakeReg
	fsMakeSock
	fsMakeFifo
	fsMakeBlock
	fsMakeSym
	fsRefer    // ABI 2
	fsTruncate // ABI 3
	fsIoctlDev // ABI 5

	fsRead     = fsExecute | fsReadFile | fsReadDir
	fsFileOnly = fsExecute | fsWriteFile | fsReadFile | fsTruncate | fsIoctlDev // rights a rule on a file may carry
)

type rulesetAttr struct {
	handledFS  uint64
	handledNet uint64 // ABI 4
	scoped     uint64 // ABI 6
}

// pathBeneathAttr is packed in C (12 bytes); Go's layout has the same first 12 bytes.
type pathBeneathAttr struct {
	allowed  uint64
	parentFD int32
}

func probe() (int, error) {
	abi, _, errno := syscall.Syscall(sysCreateRuleset, 0, 0, createRulesetVersion)
	switch {
	case errno == syscall.ENOSYS || errno == syscall.EOPNOTSUPP:
		return 0, fmt.Errorf("%w (kernel without Landlock, or Landlock not enabled at boot)", ErrUnavailable)
	case errno == syscall.EPERM:
		return 0, fmt.Errorf("%w (blocked by seccomp)", ErrUnavailable)
	case errno != 0:
		return 0, fmt.Errorf("%w: %w", ErrUnavailable, errno)
	}
	return int(abi), nil
}

// handled returns the filesystem rights the given ABI knows; all of them are denied unless a
// rule allows them. On ABI 1 (no REFER) moving or linking a file to another directory always
// fails inside the sandbox.
func handled(abi int) uint64 {
	h := uint64(fsExecute | fsWriteFile | fsReadFile | fsReadDir | fsRemoveDir | fsRemoveFile |
		fsMakeChar | fsMakeDir | fsMakeReg | fsMakeSock | fsMakeFifo | fsMakeBlock | fsMakeSym)
	if abi >= 2 {
		h |= fsRefer
	}
	if abi >= 3 {
		h |= fsTruncate
	}
	if abi >= 5 {
		h |= fsIoctlDev
	}
	return h
}

func apply(p Policy) error {
	abi, err := probe()
	if err != nil {
		return err
	}
	h := handled(abi)
	attr := rulesetAttr{handledFS: h}
	size := unsafe.Sizeof(attr.handledFS)
	switch {
	case abi >= 6:
		attr.scoped = scopeAbstractUnixSocket | scopeSignal
		size = unsafe.Sizeof(attr)
	case abi >= 4:
		size = unsafe.Offsetof(attr.scoped)
	}
	// The kernel reads the small struct attr points to; there is no safe API for it.
	rs, _, errno := syscall.Syscall(sysCreateRuleset, uintptr(unsafe.Pointer(&attr)), size, 0) //nolint:gosec // see above
	if errno != 0 {
		return fmt.Errorf("landlock create ruleset: %w", errno)
	}
	defer func() { _ = syscall.Close(int(rs)) }()
	for _, path := range p.Read {
		if err := addRule(int(rs), path, fsRead&h); err != nil {
			return err
		}
	}
	for _, path := range p.Write {
		if err := addRule(int(rs), path, h); err != nil {
			return err
		}
	}
	if p.MaxProcs > 0 {
		lim := syscall.Rlimit{Cur: p.MaxProcs, Max: p.MaxProcs}
		if err := syscall.Setrlimit(rlimitNproc, &lim); err != nil {
			return fmt.Errorf("RLIMIT_NPROC: %w", err)
		}
	}
	if _, _, errno := syscall.RawSyscall6(syscall.SYS_PRCTL, prSetNoNewPrivs, 1, 0, 0, 0, 0); errno != 0 {
		return fmt.Errorf("no_new_privs: %w", errno)
	}
	if _, _, errno := syscall.Syscall(sysRestrictSelf, rs, 0, 0); errno != 0 {
		return fmt.Errorf("landlock restrict self: %w", errno)
	}
	return nil
}

const rlimitNproc = 6 // RLIMIT_NPROC, not exported by package syscall

// addRule allows access beneath path; a path that doesn't exist is skipped.
func addRule(rs int, path string, access uint64) error {
	fd, err := syscall.Open(path, oPath|syscall.O_CLOEXEC, 0)
	if errors.Is(err, syscall.ENOENT) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("landlock rule %s: %w", path, err)
	}
	defer func() { _ = syscall.Close(fd) }()
	var st syscall.Stat_t
	if err := syscall.Fstat(fd, &st); err != nil {
		return fmt.Errorf("landlock rule %s: %w", path, err)
	}
	if st.Mode&syscall.S_IFMT != syscall.S_IFDIR {
		access &= fsFileOnly
	}
	attr := pathBeneathAttr{allowed: access, parentFD: int32(fd)} //nolint:gosec // fds fit in int32
	if _, _, errno := syscall.Syscall6(sysAddRule, uintptr(rs), rulePathBeneath,
		uintptr(unsafe.Pointer(&attr)), 0, 0, 0); errno != 0 { //nolint:gosec // the kernel reads attr; no safe API
		return fmt.Errorf("landlock rule %s: %w", path, errno)
	}
	return nil
}
