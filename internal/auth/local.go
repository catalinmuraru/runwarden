package auth

import (
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
)

// The built-in administrator (INITIAL_ADMIN=true) signs in with a generated password. Its name
// contains "@", which GitLab user names cannot, and other methods may not use it (see
// Handler.startSession), so it never shares runs or rights with a real account.
const (
	MethodLocal       = "local"
	LocalAdminName    = "admin@local"
	adminPasswordFile = "initial-admin-password"
)

// LocalAdmin checks the built-in administrator's password. Only its SHA-256 is kept in memory;
// the password itself is random and long, so a slow hash adds nothing.
type LocalAdmin struct {
	digest [32]byte
}

// EnableLocalAdmin generates a new password, writes it to dataDir/initial-admin-password (0600)
// and returns the checker and the file's path. The password is never logged.
func EnableLocalAdmin(dataDir string) (*LocalAdmin, string, error) {
	password := randomString() // 256 random bits, 43 characters
	path := filepath.Join(dataDir, adminPasswordFile)
	if err := os.MkdirAll(dataDir, 0o750); err != nil {
		return nil, "", err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, []byte(password+"\n"), 0o600); err != nil {
		return nil, "", err
	}
	if err := os.Rename(tmp, path); err != nil {
		return nil, "", err
	}
	return &LocalAdmin{digest: sha256.Sum256([]byte(password))}, path, nil
}

// DisableLocalAdmin removes the password file, if any.
func DisableLocalAdmin(dataDir string) error {
	err := os.Remove(filepath.Join(dataDir, adminPasswordFile))
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	return err
}

// Authenticate implements PasswordProvider. The user name is ignored: there is one account.
func (l *LocalAdmin) Authenticate(_ context.Context, _, password string) (User, error) {
	got := sha256.Sum256([]byte(password))
	if subtle.ConstantTimeCompare(got[:], l.digest[:]) != 1 {
		return User{}, ErrInvalidCredentials
	}
	return User{Name: LocalAdminName, Method: MethodLocal}, nil
}
