package secrets

import (
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

// write creates dir/env/name with value.
func write(t *testing.T, dir, env, name, value string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Join(dir, env), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, env, name), []byte(value), 0o600); err != nil {
		t.Fatal(err)
	}
}

func setup(t *testing.T) Store {
	t.Helper()
	dir := t.TempDir()
	write(t, dir, "production", "DB_PASSWORD", "prod-db\n")
	write(t, dir, "development", "DB_PASSWORD", "dev-db")
	write(t, dir, Common, "PRIVATE_TOKEN", "gitlab-token\r\n")
	write(t, dir, "production", "lower_case", "ignored")
	// Kubernetes layout: the visible files are symlinks into a timestamped directory.
	data := filepath.Join(dir, "acceptance", "..2026_10_26")
	write(t, filepath.Join(dir, "acceptance"), "..2026_10_26", "DB_PASSWORD", "acc-db")
	if err := os.Symlink(data, filepath.Join(dir, "acceptance", "..data")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join("..data", "DB_PASSWORD"), filepath.Join(dir, "acceptance", "DB_PASSWORD")); err != nil {
		t.Fatal(err)
	}
	return Store{Dir: dir}
}

func TestResolve(t *testing.T) {
	s := setup(t)
	tests := []struct {
		name     string
		names    []string
		targets  []string
		sources  []string
		want     map[string]string
		wantErr  string
		missing  bool
		severalT bool
	}{
		{name: "target and common", names: []string{"DB_PASSWORD", "PRIVATE_TOKEN"}, targets: []string{"production"},
			want: map[string]string{"DB_PASSWORD": "prod-db", "PRIVATE_TOKEN": "gitlab-token"}},
		{name: "kubernetes symlinks", names: []string{"DB_PASSWORD"}, targets: []string{"acceptance"},
			want: map[string]string{"DB_PASSWORD": "acc-db"}},
		{name: "source with prefix", names: []string{"DB_PASSWORD", "PRIVATE_TOKEN"}, targets: []string{"development"}, sources: []string{"production"},
			want: map[string]string{"DB_PASSWORD": "dev-db", "SOURCE_DB_PASSWORD": "prod-db", "PRIVATE_TOKEN": "gitlab-token"}}, // common not repeated
		{name: "no environment: common only", names: []string{"PRIVATE_TOKEN"}, targets: []string{"-"},
			want: map[string]string{"PRIVATE_TOKEN": "gitlab-token"}},
		{name: "missing in the environment", names: []string{"DB_PASSWORD"}, targets: []string{"staging"},
			wantErr: "secret DB_PASSWORD is not available in staging", missing: true},
		{name: "missing without environment", names: []string{"DB_PASSWORD"},
			wantErr: "not available in the common secrets", missing: true},
		{name: "two targets", names: []string{"DB_PASSWORD"}, targets: []string{"development", "production"}, severalT: true},
		{name: "nothing declared", targets: []string{"development", "production"}, want: map[string]string{}},
		{name: "same environment twice", names: []string{"DB_PASSWORD"}, targets: []string{"production", "production"},
			want: map[string]string{"DB_PASSWORD": "prod-db"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			res, err := s.Resolve(tt.names, tt.targets, tt.sources)
			var missing *MissingError
			switch {
			case tt.severalT:
				if !errors.Is(err, ErrSeveralTargets) {
					t.Errorf("err = %v", err)
				}
			case tt.wantErr != "":
				if err == nil || !strings.Contains(err.Error(), tt.wantErr) || errors.As(err, &missing) != tt.missing {
					t.Errorf("err = %v, want %q", err, tt.wantErr)
				}
			case err != nil:
				t.Fatal(err)
			default:
				if len(res.Env) != len(tt.want) {
					t.Errorf("env = %v, want %v", res.Env, tt.want)
				}
				for k, v := range tt.want {
					if res.Env[k] != v {
						t.Errorf("%s = %q, want %q", k, res.Env[k], v)
					}
				}
				if len(res.Values()) != len(res.Env) || !slices.IsSorted(res.Names()) {
					t.Errorf("names/values = %v %v", res.Names(), res.Values())
				}
			}
		})
	}
}

func TestRotationAndLimits(t *testing.T) {
	s := setup(t)
	write(t, s.Dir, "production", "DB_PASSWORD", "rotated")
	res, err := s.Resolve([]string{"DB_PASSWORD"}, []string{"production"}, nil)
	if err != nil || res.Env["DB_PASSWORD"] != "rotated" {
		t.Errorf("after rotation = %v, %v", res.Env, err)
	}
	write(t, s.Dir, "production", "HUGE", strings.Repeat("x", maxSize+1))
	if _, err := s.Resolve([]string{"DB_PASSWORD"}, []string{"production"}, nil); err == nil || !strings.Contains(err.Error(), "larger") {
		t.Errorf("oversized secret: %v", err)
	}
	if _, err := s.Resolve([]string{"X"}, []string{"../etc"}, nil); err == nil || !strings.Contains(err.Error(), "invalid environment") {
		t.Errorf("path in environment: %v", err)
	}
	if res, err := (Store{}).Resolve(nil, []string{"production"}, nil); err != nil || len(res.Env) != 0 {
		t.Errorf("no store: %v %v", res, err)
	}
	if _, err := (Store{Dir: filepath.Join(t.TempDir(), "none")}).Resolve([]string{"X"}, nil, nil); err == nil {
		t.Error("missing directory resolved a secret")
	}
}

func TestInventory(t *testing.T) {
	s := setup(t)
	inv, err := s.Inventory()
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(inv["production"], []string{"DB_PASSWORD"}) || !slices.Equal(inv[Common], []string{"PRIVATE_TOKEN"}) ||
		!slices.Equal(inv["acceptance"], []string{"DB_PASSWORD"}) || len(inv) != 4 {
		t.Errorf("inventory = %v", inv)
	}
	if inv, err := (Store{Dir: filepath.Join(t.TempDir(), "none")}).Inventory(); err != nil || len(inv) != 0 {
		t.Errorf("missing dir = %v %v", inv, err)
	}
}
