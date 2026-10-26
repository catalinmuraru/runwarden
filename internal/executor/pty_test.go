//go:build linux

package executor

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/catalinmuraru/runwarden/internal/params"
)

func terminalScript(t *testing.T, file string, ps ...*params.Parameter) Request {
	t.Helper()
	s := testScript(t, file, ps...)
	s.RequiresTerminal = true
	return Request{Script: s}
}

func TestTerminalPrompt(t *testing.T) {
	r := mustStart(t, New(Limits{}, EnvPolicy{}), terminalScript(t, "tty_prompt.sh"))
	if !r.Terminal {
		t.Fatal("run not on a terminal")
	}
	waitOutput(t, r, "Continue? ") // read -p only prints its prompt on a terminal
	if err := r.WriteInput("yes", false); err != nil {
		t.Fatal(err)
	}
	res := waitDone(t, r)
	out := outputOf(r)
	if res.Status != StatusFinished || !strings.Contains(out, "/dev/pts/") || !strings.Contains(out, "answer=yes") {
		t.Errorf("result %+v, output:\n%s", res, out)
	}
}

func TestTerminalSecretInputMasked(t *testing.T) {
	r := mustStart(t, New(Limits{}, EnvPolicy{}), terminalScript(t, "tty_secret.sh"))
	waitOutput(t, r, "Password: ")
	if err := r.WriteInput("hunter2", true); err != nil {
		t.Fatal(err)
	}
	waitDone(t, r)
	out := outputOf(r)
	if strings.Contains(out, "hunter2") || !strings.Contains(out, "got 7 chars") || !strings.Contains(out, params.Mask) {
		t.Errorf("terminal echo of secret input not masked:\n%s", out)
	}
}

func TestTerminalStopKillsGroup(t *testing.T) {
	r := mustStart(t, New(Limits{}, EnvPolicy{}), terminalScript(t, "group.sh"))
	waitOutput(t, r, "self=")
	out := outputOf(r)
	child, self := pidFrom(t, out, "child"), pidFrom(t, out, "self")
	r.Stop()
	if res := waitDone(t, r); res.Status != StatusStopped {
		t.Errorf("status = %s", res.Status)
	}
	waitGone(t, self)
	waitGone(t, child)
}

func TestTerminalCloseInputSendsEOF(t *testing.T) {
	r := mustStart(t, New(Limits{}, EnvPolicy{}), terminalScript(t, "read_eof.sh"))
	time.Sleep(100 * time.Millisecond) // let cat start reading
	if err := r.CloseInput(); err != nil {
		t.Fatal(err)
	}
	waitOutput(t, r, "eof reached")
}

func TestPerRunTempDir(t *testing.T) {
	root := t.TempDir()
	r := mustStart(t, New(Limits{}, EnvPolicy{TempRoot: root}), Request{Script: testScript(t, "tmpdir.sh")})
	waitDone(t, r)
	want := filepath.Join(root, r.ID)
	if out := outputOf(r); !strings.Contains(out, "tmp="+filepath.Join(want, "tmp")+"\n") || !strings.Contains(out, "writable") {
		t.Errorf("output:\n%s", out)
	}
	if _, err := os.Stat(want); !os.IsNotExist(err) {
		t.Errorf("temp dir not removed: %v", err)
	}
}

func TestRedactPatterns(t *testing.T) {
	s := testScript(t, "print_secrets.sh")
	s.RedactPatterns = []*regexp.Regexp{regexp.MustCompile(`ghp_[A-Za-z0-9]+`)}
	r := mustStart(t, New(Limits{}, EnvPolicy{}), Request{Script: s})
	waitDone(t, r)
	out := outputOf(r)
	if strings.Contains(out, "ghp_") || !strings.Contains(out, "token=******\nsecond token=******\nharmless line") {
		t.Errorf("not redacted:\n%s", out)
	}
}

// A secure value makes the masker hold back the end of the output; a prompt without a newline
// must still appear once the output goes quiet.
func TestIdleFlushReleasesPrompt(t *testing.T) {
	s := testScript(t, "prompt.sh", &params.Parameter{Name: "token", Secure: true})
	r := mustStart(t, New(Limits{}, EnvPolicy{}), Request{Script: s, Values: map[string]params.Value{
		"token": params.Scalar("a-rather-long-secret-value-0123456789"),
	}})
	start := time.Now()
	waitOutput(t, r, "Continue? ")
	if d := time.Since(start); d > 3*time.Second {
		t.Errorf("prompt took %v to appear", d)
	}
	_ = r.WriteInput("y", false)
	_ = r.WriteInput("z", false)
	waitDone(t, r)
}
