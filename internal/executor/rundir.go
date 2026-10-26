package executor

import (
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
)

// prepareRunDir creates the private directory of one run (or helper call) under
// EnvPolicy.TempRoot and returns it with the variables pointing into it: TMPDIR always, and with
// PrivateHome also HOME, KUBECONFIG and AZURE_CONFIG_DIR, so credentials and contexts written by
// one run are invisible to every other. dir is empty when TempRoot is not set.
func (p EnvPolicy) prepareRunDir(name string) (dir string, vars map[string]string, err error) {
	if p.TempRoot == "" {
		return "", nil, nil
	}
	dir = filepath.Join(p.TempRoot, name)
	tmp := filepath.Join(dir, "tmp")
	if err := os.MkdirAll(tmp, 0o700); err != nil {
		return "", nil, fmt.Errorf("run temp dir: %w", err)
	}
	vars = map[string]string{"TMPDIR": tmp}
	if p.PrivateHome {
		home := filepath.Join(dir, "home")
		if err := os.Mkdir(home, 0o700); err != nil {
			_ = os.RemoveAll(dir)
			return "", nil, fmt.Errorf("run home: %w", err)
		}
		if p.HomeSkeleton != "" {
			if err := copySkeleton(p.HomeSkeleton, home); err != nil {
				_ = os.RemoveAll(dir)
				return "", nil, fmt.Errorf("run home skeleton: %w", err)
			}
		}
		vars["HOME"] = home
		vars["KUBECONFIG"] = filepath.Join(home, ".kube", "config")
		vars["AZURE_CONFIG_DIR"] = filepath.Join(home, ".azure")
	}
	return dir, vars, nil
}

// copySkeleton copies the regular files and directories of src into dst. Symlinks (to files or
// directories) are followed only when they stay inside src, as in Kubernetes ConfigMap volumes;
// entries whose name starts with ".." (ConfigMap internals) are skipped.
func copySkeleton(src, dst string) error {
	root, err := filepath.EvalSymlinks(src)
	if err != nil {
		return err
	}
	return copyTree(root, root, dst, 0)
}

func copyTree(root, from, to string, depth int) error {
	if depth > 16 {
		return nil // symlink loops
	}
	entries, err := os.ReadDir(from)
	if err != nil {
		return err
	}
	for _, d := range entries {
		if strings.HasPrefix(d.Name(), "..") {
			continue
		}
		path, target := filepath.Join(from, d.Name()), filepath.Join(to, d.Name())
		if d.Type()&fs.ModeSymlink != 0 {
			resolved, err := filepath.EvalSymlinks(path)
			if err != nil || !within(root, resolved) {
				continue // dangling, or pointing outside the skeleton
			}
			path = resolved
		}
		info, err := os.Stat(path)
		switch {
		case err != nil:
			continue
		case info.IsDir():
			if err := os.MkdirAll(target, 0o700); err != nil {
				return err
			}
			if err := copyTree(root, path, target, depth+1); err != nil {
				return err
			}
		case info.Mode().IsRegular():
			if err := copyRegular(path, target, info.Mode().Perm()&0o700|0o600); err != nil {
				return err
			}
		}
	}
	return nil
}

func within(root, path string) bool {
	rel, err := filepath.Rel(root, path)
	return err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}

func copyRegular(src, dst string, mode fs.FileMode) error {
	if err := os.MkdirAll(filepath.Dir(dst), 0o700); err != nil {
		return err
	}
	in, err := os.Open(src) //nolint:gosec // inside the operator-configured skeleton
	if err != nil {
		return err
	}
	defer func() { _ = in.Close() }()
	out, err := os.OpenFile(dst, os.O_CREATE|os.O_EXCL|os.O_WRONLY, mode) //nolint:gosec // inside the run's own directory
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		_ = out.Close()
		return err
	}
	return out.Close()
}
