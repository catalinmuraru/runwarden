package executor

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"time"
)

// Limits for helper commands (dynamic values and defaults).
const (
	captureTimeout = 30 * time.Second
	captureMax     = 1 << 20
)

// Capture runs a short helper command, such as a script listing the allowed values of a
// parameter, and returns its stdout. It uses the same environment policy and process-group
// handling as runs, is not counted against the run limit, and is killed after 30 seconds.
// secrets are exported to it and masked in what it returns.
func (e *Executor) Capture(ctx context.Context, command []string, dir string, secrets map[string]string) ([]byte, error) {
	if len(command) == 0 {
		return nil, errors.New("executor: empty command")
	}
	ctx, cancel := context.WithTimeout(ctx, captureTimeout)
	defer cancel()

	// Helpers get a private TMPDIR and HOME too, removed afterwards.
	dir0, vars, err := e.env.prepareRunDir(fmt.Sprintf("values-%d", e.helperSeq.Add(1)))
	if err != nil {
		return nil, err
	}
	if dir0 != "" {
		defer func() { _ = os.RemoveAll(dir0) }()
	}
	env, _ := e.env.build(nil, secrets, vars)
	cmd := exec.CommandContext(ctx, command[0], command[1:]...) //nolint:gosec // argv from config, no shell
	cmd.Dir = dir
	cmd.Env = env
	if err := prepare(cmd); err != nil {
		return nil, err
	}
	cmd.Cancel = func() error {
		killGroup(cmd.Process.Pid)
		return nil
	}
	cmd.WaitDelay = time.Second

	stdout := &capped{max: captureMax}
	stderr := &capped{max: 4 << 10}
	cmd.Stdout, cmd.Stderr = stdout, stderr
	err = cmd.Run()
	if cmd.Process != nil {
		killGroup(cmd.Process.Pid) // background children of the helper
	}
	switch {
	case ctx.Err() != nil:
		return nil, fmt.Errorf("%s: timed out", command[0])
	case err != nil:
		msg := strings.TrimSpace(string(maskAll(stderr.Bytes(), secrets)))
		return nil, fmt.Errorf("%s: %w: %s", command[0], err, msg)
	case stdout.overflow:
		return nil, fmt.Errorf("%s: output exceeds %d bytes", command[0], captureMax)
	}
	return maskAll(stdout.Bytes(), secrets), nil
}

// maskAll replaces every secret value in b, longest first.
func maskAll(b []byte, secrets map[string]string) []byte {
	if len(secrets) == 0 {
		return b
	}
	m := newMasker(nil, nil)
	for _, v := range secrets {
		m.addLocked(v)
	}
	return m.replace(bytes.Clone(b))
}

// capped keeps the first max bytes written and discards the rest. The buffer is a named field,
// not embedded: an embedded bytes.Buffer would expose ReadFrom, which io.Copy prefers over
// Write, bypassing the cap.
type capped struct {
	buf      bytes.Buffer
	max      int
	overflow bool
}

func (c *capped) Write(p []byte) (int, error) {
	if room := c.max - c.buf.Len(); room < len(p) {
		c.overflow = true
		c.buf.Write(p[:max(room, 0)])
		return len(p), nil
	}
	return c.buf.Write(p)
}

func (c *capped) Bytes() []byte  { return c.buf.Bytes() }
func (c *capped) String() string { return c.buf.String() }
