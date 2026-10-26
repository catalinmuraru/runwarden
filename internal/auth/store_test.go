package auth

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestStore(t *testing.T) {
	dir := t.TempDir()
	s, err := OpenStore(dir, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 10, 26, 9, 0, 0, 0, time.UTC)
	s.now = func() time.Time { return now }

	alice, _ := s.Create(User{Name: "alice", Groups: []string{"ops"}, Method: "gitlab"})
	alice2, _ := s.Create(User{Name: "Alice", Method: "ldap"})
	bob, _ := s.Create(User{Name: "bob", Method: "gitlab"})
	if u, ok := s.Get(alice); !ok || u.Name != "alice" || u.Method != "gitlab" || len(u.Groups) != 1 {
		t.Fatalf("get = %+v %v", u, ok)
	}
	if _, ok := s.Get("forged"); ok {
		t.Error("unknown token accepted")
	}

	data, _ := os.ReadFile(filepath.Join(dir, sessionsFile)) //nolint:gosec // test temp dir
	if strings.Contains(string(data), alice) || !strings.Contains(string(data), hashToken(alice)) {
		t.Error("the file must hold token hashes, never tokens")
	}
	if info, _ := os.Stat(filepath.Join(dir, sessionsFile)); info.Mode().Perm() != 0o600 {
		t.Errorf("file mode = %v", info.Mode().Perm())
	}

	sum := s.Summary()
	if sum["alice"].Count != 2 || len(sum["alice"].Methods) != 2 || sum["bob"].Count != 1 {
		t.Errorf("summary = %+v", sum)
	}

	// Sessions survive a restart.
	s2, _ := OpenStore(dir, time.Hour)
	s2.now = s.now
	if _, ok := s2.Get(bob); !ok {
		t.Error("session lost on reopen")
	}

	if u, ok := s.Revoke(alice2); !ok || u.Method != "ldap" {
		t.Errorf("revoke = %+v %v", u, ok)
	}
	if _, ok := s.Get(alice2); ok {
		t.Error("revoked session still valid")
	}
	if n := s.RevokeUser("ALICE"); n != 1 {
		t.Errorf("RevokeUser = %d, want 1", n)
	}
	if _, ok := s.Get(alice); ok {
		t.Error("session valid after RevokeUser")
	}

	now = now.Add(2 * time.Hour)
	if _, ok := s.Get(bob); ok {
		t.Error("expired session accepted")
	}
	if err := s.save(); err != nil {
		t.Fatal(err)
	}
	if len(s.sessions) != 0 {
		t.Errorf("expired sessions not pruned: %d", len(s.sessions))
	}
}

func TestLocalAdminPassword(t *testing.T) {
	dir := t.TempDir()
	admin, path, err := EnableLocalAdmin(dir)
	if err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(path)
	if err != nil || info.Mode().Perm() != 0o600 {
		t.Fatalf("password file: %v %v", info, err)
	}
	data, _ := os.ReadFile(path) //nolint:gosec // test temp dir
	password := strings.TrimSpace(string(data))
	if len(password) < 40 {
		t.Errorf("password too short: %d characters", len(password))
	}
	if u, err := admin.Authenticate(t.Context(), "anything", password); err != nil || u.Name != LocalAdminName || u.Method != MethodLocal {
		t.Errorf("good password: %+v %v", u, err)
	}
	if _, err := admin.Authenticate(t.Context(), LocalAdminName, password+"x"); !errors.Is(err, ErrInvalidCredentials) {
		t.Errorf("wrong password: %v", err)
	}

	again, _, _ := EnableLocalAdmin(dir) // a new password at every start
	if _, err := again.Authenticate(t.Context(), "", password); err == nil {
		t.Error("the old password still works after a restart")
	}
	if err := DisableLocalAdmin(dir); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Errorf("password file not removed: %v", err)
	}
	if err := DisableLocalAdmin(dir); err != nil {
		t.Errorf("disabling twice: %v", err)
	}
}
