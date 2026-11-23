// Package compat keeps installs made under the project's former name working for one release:
// legacy environment variables, default directories and the executor header are still accepted,
// with a deprecation warning logged once per name. Remove in v0.6.
package compat

import (
	"log/slog"
	"os"
	"strings"
	"sync"
)

const (
	// Prefix is the prefix of the variables Ward sets and reads.
	Prefix = "WARD_"
	// LegacyPrefix is the former prefix, still read and exported in v0.5.
	LegacyPrefix = "RUNWARDEN_"
	// LegacyEnvironmentHeader is the former name of the executor environment header.
	LegacyEnvironmentHeader = "X-Runwarden-Environment"
)

var warned sync.Map

// warn logs a deprecation once per key.
func warn(key, msg string, args ...any) {
	if _, loaded := warned.LoadOrStore(key, true); loaded {
		return
	}
	slog.Warn(msg, args...)
}

// Legacy returns the former name of a WARD_ variable, or "" for any other name.
func Legacy(name string) string {
	if rest, ok := strings.CutPrefix(name, Prefix); ok && rest != "" {
		return LegacyPrefix + rest
	}
	return ""
}

// LookupEnv reads the WARD_ variable name with lookup; if it is unset, it falls back to the
// RUNWARDEN_ name and logs a deprecation warning once.
func LookupEnv(lookup func(string) (string, bool), name string) (string, bool) {
	if v, ok := lookup(name); ok {
		return v, true
	}
	old := Legacy(name)
	if old == "" {
		return "", false
	}
	v, ok := lookup(old)
	if ok {
		warn("env:"+old, "deprecated environment variable; rename it", "old", old, "new", name)
	}
	return v, ok
}

// Getenv is LookupEnv on the process environment.
func Getenv(name string) string {
	v, _ := LookupEnv(os.LookupEnv, name)
	return v
}

// ExportBoth adds the other name of every WARD_ or RUNWARDEN_ variable in vars that lacks it,
// so scripts written for either name keep working in v0.5. Existing entries are kept.
func ExportBoth(vars map[string]string) {
	add := map[string]string{}
	for k, v := range vars {
		var other string
		if rest, ok := strings.CutPrefix(k, Prefix); ok && rest != "" {
			other = LegacyPrefix + rest
		} else if rest, ok := strings.CutPrefix(k, LegacyPrefix); ok && rest != "" {
			other = Prefix + rest
		}
		if _, set := vars[other]; other != "" && !set {
			add[other] = v
		}
	}
	for k, v := range add {
		vars[k] = v
	}
}

// Dir returns dir, or legacy when dir does not exist but legacy does (an install laid out under
// the former name); the fallback logs a deprecation warning once.
func Dir(dir, legacy string, exists func(string) bool) string {
	if exists(dir) || !exists(legacy) {
		return dir
	}
	warn("dir:"+legacy, "deprecated default directory; move it", "old", legacy, "new", dir)
	return legacy
}

// DirExists reports whether path is an existing directory.
func DirExists(path string) bool {
	fi, err := os.Stat(path)
	return err == nil && fi.IsDir()
}

// Header returns the value of header name in get, falling back to legacy with a warning.
func Header(get func(string) string, name, legacy string) string {
	if v := get(name); v != "" {
		return v
	}
	v := get(legacy)
	if v != "" {
		warn("header:"+legacy, "executor sends a deprecated header; upgrade it", "old", legacy, "new", name)
	}
	return v
}
