//go:build unix

package executor

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func homeScript(t *testing.T, context, sleep string) *Request {
	t.Helper()
	s := testScript(t, "home.sh")
	s.Command = append(s.Command, context, sleep)
	return &Request{Script: s}
}

func TestPrivateHomePerRun(t *testing.T) {
	root, skel := t.TempDir(), t.TempDir()
	if err := os.WriteFile(filepath.Join(skel, ".gitconfig"), []byte("[user]\n\tname = runwarden\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	secret := filepath.Join(t.TempDir(), "secret")
	_ = os.WriteFile(secret, []byte("x"), 0o600)
	_ = os.Symlink(secret, filepath.Join(skel, "outside")) // must not be copied
	// ConfigMap volume layout: files are symlinks into a ..data directory.
	_ = os.MkdirAll(filepath.Join(skel, "..2026_10_26", ".ssh"), 0o750)
	_ = os.WriteFile(filepath.Join(skel, "..2026_10_26", ".ssh", "known_hosts"), []byte("host key\n"), 0o600)
	_ = os.Symlink("..2026_10_26", filepath.Join(skel, "..data"))
	_ = os.Symlink(filepath.Join("..data", ".ssh"), filepath.Join(skel, ".ssh"))

	e := New(Limits{}, EnvPolicy{TempRoot: root, PrivateHome: true, HomeSkeleton: skel})
	dev := mustStart(t, e, *homeScript(t, "dev", "0.5"))
	prod := mustStart(t, e, *homeScript(t, "prod", "0"))
	waitDone(t, prod)
	waitDone(t, dev)

	for _, c := range []struct {
		r   *Run
		ctx string
	}{{dev, "dev"}, {prod, "prod"}} {
		out := outputOf(c.r)
		home := filepath.Join(root, c.r.ID, "home")
		for _, want := range []string{"home=" + home, "kubeconfig=" + filepath.Join(home, ".kube", "config"),
			"azure=" + filepath.Join(home, ".azure"), "tmp=" + filepath.Join(root, c.r.ID, "tmp"),
			"name = runwarden", "kube=context: " + c.ctx} {
			if !strings.Contains(out, want) {
				t.Errorf("run %s: missing %q in\n%s", c.ctx, want, out)
			}
		}
		if strings.Contains(out, "symlink followed outside") {
			t.Errorf("symlink outside the skeleton was copied")
		}
		if _, err := os.Stat(filepath.Join(root, c.r.ID)); !os.IsNotExist(err) {
			t.Errorf("run dir not removed: %v", err)
		}
	}
	if err := copySkeleton(skel, t.TempDir()); err != nil {
		t.Fatal(err)
	}
	dst := t.TempDir()
	_ = copySkeleton(skel, dst)
	b, err := os.ReadFile(filepath.Join(dst, ".ssh", "known_hosts")) //nolint:gosec // test temp dir
	if err != nil || string(b) != "host key\n" {
		t.Errorf("ConfigMap-style symlinks not followed: %q %v", b, err)
	}
	if _, err := os.Stat(filepath.Join(dst, "..data")); !os.IsNotExist(err) {
		t.Errorf("..data copied: %v", err)
	}
}

func TestSharedHomeKeepsRunwardenHome(t *testing.T) {
	t.Setenv("HOME", "/home/shared")
	e := New(Limits{}, EnvPolicy{TempRoot: t.TempDir()})
	r := mustStart(t, e, *homeScript(t, "x", "0"))
	waitDone(t, r)
	if out := outputOf(r); !strings.Contains(out, "home=/home/shared\n") || !strings.Contains(out, "kubeconfig=\n") {
		t.Errorf("shared HOME changed:\n%s", out)
	}
}

func TestCapturePrivateHome(t *testing.T) {
	root := t.TempDir()
	e := New(Limits{}, EnvPolicy{TempRoot: root, PrivateHome: true})
	script, _ := filepath.Abs("testdata/home.sh")
	out, err := e.Capture(context.Background(), []string{script, "helper", "0"}, t.TempDir(), nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(out), "home="+root+"/values-") || !strings.Contains(string(out), "kube=context: helper") {
		t.Errorf("helper output:\n%s", out)
	}
	if entries, _ := os.ReadDir(root); len(entries) != 0 {
		t.Errorf("helper dir not removed: %v", entries)
	}
}

func kubeRequest(t *testing.T, sleep string, kube *Kube) Request {
	t.Helper()
	s := testScript(t, "kube.sh")
	s.Command = append(s.Command, sleep)
	return Request{Script: s, Kube: kube}
}

func TestKubeconfigPerRun(t *testing.T) {
	root, skel := t.TempDir(), t.TempDir()
	// A kubeconfig from the skeleton is replaced, not kept.
	_ = os.MkdirAll(filepath.Join(skel, ".kube"), 0o700)
	_ = os.WriteFile(filepath.Join(skel, ".kube", "config"), []byte("skeleton"), 0o600)
	e := New(Limits{}, EnvPolicy{TempRoot: root, PrivateHome: true, HomeSkeleton: skel})
	dev := mustStart(t, e, kubeRequest(t, "0.5", &Kube{Config: []byte("dev-config"),
		Env: map[string]string{"RUNWARDEN_KUBE_CONTEXT": "development"}}))
	prod := mustStart(t, e, kubeRequest(t, "0", &Kube{Config: []byte("prod-config"),
		Env: map[string]string{"RUNWARDEN_KUBE_CONTEXT": "production", "RUNWARDEN_KUBE_SOURCE_CONTEXT": "development"}}))
	plain := mustStart(t, e, kubeRequest(t, "0", nil))
	waitDone(t, prod)
	waitDone(t, dev)
	waitDone(t, plain)

	for r, want := range map[*Run]string{
		dev:   "context=development source=unset\nmode=600\nconfig=dev-config\n",
		prod:  "context=production source=development\nmode=600\nconfig=prod-config\n",
		plain: "context= source=unset\nmode=600\nconfig=skeleton\n",
	} {
		if out := outputOf(r); !strings.Contains(out, want) {
			t.Errorf("run %s: want %q in\n%s", r.ID, want, out)
		}
	}
}

func TestKubeconfigNeedsPrivateHome(t *testing.T) {
	kube := &Kube{Config: []byte("x")}
	for _, p := range []EnvPolicy{{}, {TempRoot: t.TempDir()}} {
		e := New(Limits{}, p)
		if _, err := e.Start(kubeRequest(t, "0", kube)); !errors.Is(err, ErrNoPrivateHome) {
			t.Errorf("%+v: Start = %v, want ErrNoPrivateHome", p, err)
		}
		if _, err := e.Capture(t.Context(), []string{"true"}, t.TempDir(), nil, kube); !errors.Is(err, ErrNoPrivateHome) {
			t.Errorf("%+v: Capture = %v, want ErrNoPrivateHome", p, err)
		}
	}
}

func TestCaptureKubeconfig(t *testing.T) {
	e := New(Limits{}, EnvPolicy{TempRoot: t.TempDir(), PrivateHome: true})
	script, _ := filepath.Abs("testdata/kube.sh")
	out, err := e.Capture(t.Context(), []string{script, "0"}, t.TempDir(), nil,
		&Kube{Config: []byte("helper-config"), Env: map[string]string{"RUNWARDEN_KUBE_CONTEXT": "acceptance"}})
	if err != nil {
		t.Fatal(err)
	}
	if want := "context=acceptance source=unset\nmode=600\nconfig=helper-config\n"; string(out) != want {
		t.Errorf("helper output %q, want %q", out, want)
	}
}

func TestMaxRunsPerUser(t *testing.T) {
	e := New(Limits{MaxPerUser: 1}, EnvPolicy{})
	req := func(user string) Request {
		return Request{Script: testScript(t, "sleep.sh"), User: user}
	}
	first := mustStart(t, e, req("alice"))
	if _, err := e.Start(req("ALICE")); !errors.Is(err, ErrUserBusy) {
		t.Fatalf("second run for alice: %v, want ErrUserBusy", err)
	}
	other := mustStart(t, e, req("bob"))
	if got := e.RunningFor("Alice"); len(got) != 1 || got[0] != first.ID {
		t.Errorf("RunningFor = %v", got)
	}
	first.Kill()
	waitDone(t, first)
	again := mustStart(t, e, req("alice")) // the slot is released when the run ends
	again.Kill()
	other.Kill()

	unlimited := New(Limits{}, EnvPolicy{})
	mustStart(t, unlimited, req("carol"))
	mustStart(t, unlimited, req("carol"))
}
