package config

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

// App is the server configuration, read from environment variables.
type App struct {
	ListenAddr        string        // LISTEN_ADDR, default ":8080"
	RunnersDir        string        // RUNNERS_DIR, default "./runners"
	BaseDir           string        // BASE_DIR, default working dir; resolves relative paths in configs
	DataDir           string        // DATA_DIR, default "./data"; history and uploads
	MaxConcurrentRuns int           // MAX_CONCURRENT_RUNS, default 10
	RunTimeout        time.Duration // RUN_TIMEOUT, default 60m
	KillGrace         time.Duration // KILL_GRACE, default 10s
	MaxOutputBytes    int64         // MAX_OUTPUT_BYTES, default 10Mi
	MaxUploadBytes    int64         // MAX_UPLOAD_BYTES, default 100Mi
	AllowedOrigins    []string      // ALLOWED_ORIGINS, comma-separated hosts allowed for WebSockets
}

// LoadApp reads the configuration through getenv (usually os.Getenv). All problems are
// reported together.
func LoadApp(getenv func(string) string) (App, error) {
	wd, err := os.Getwd()
	if err != nil {
		return App{}, err
	}
	var errs []error
	str := func(name, def string) string {
		if v := strings.TrimSpace(getenv(name)); v != "" {
			return v
		}
		return def
	}
	dir := func(name, def string) string {
		p, err := filepath.Abs(str(name, def))
		if err != nil {
			errs = append(errs, fmt.Errorf("%s: %w", name, err))
		}
		return p
	}
	num := func(name string, def int64, parse func(string) (int64, error)) int64 {
		raw := str(name, "")
		if raw == "" {
			return def
		}
		v, err := parse(raw)
		if err != nil || v <= 0 {
			errs = append(errs, fmt.Errorf("%s: invalid value %q", name, raw))
			return def
		}
		return v
	}
	dur := func(name string, def time.Duration) time.Duration {
		return time.Duration(num(name, int64(def), func(s string) (int64, error) {
			d, err := time.ParseDuration(s)
			return int64(d), err
		}))
	}
	integer := func(s string) (int64, error) { return strconv.ParseInt(s, 10, 64) }

	app := App{
		ListenAddr:        str("LISTEN_ADDR", ":8080"),
		RunnersDir:        dir("RUNNERS_DIR", "runners"),
		BaseDir:           dir("BASE_DIR", wd),
		DataDir:           dir("DATA_DIR", "data"),
		MaxConcurrentRuns: int(num("MAX_CONCURRENT_RUNS", 10, integer)),
		RunTimeout:        dur("RUN_TIMEOUT", 60*time.Minute),
		KillGrace:         dur("KILL_GRACE", 10*time.Second),
		MaxOutputBytes:    num("MAX_OUTPUT_BYTES", 10<<20, parseBytes),
		MaxUploadBytes:    num("MAX_UPLOAD_BYTES", 100<<20, parseBytes),
	}
	for _, o := range strings.Split(getenv("ALLOWED_ORIGINS"), ",") {
		if o = strings.TrimSpace(o); o != "" {
			app.AllowedOrigins = append(app.AllowedOrigins, o)
		}
	}
	return app, errors.Join(errs...)
}

// parseBytes accepts a byte count with an optional binary suffix: 512, 64Ki, 10Mi, 1Gi.
func parseBytes(s string) (int64, error) {
	mult := int64(1)
	for suffix, m := range map[string]int64{"Ki": 1 << 10, "Mi": 1 << 20, "Gi": 1 << 30} {
		if rest, ok := strings.CutSuffix(s, suffix); ok {
			s, mult = rest, m
			break
		}
	}
	n, err := strconv.ParseInt(strings.TrimSpace(s), 10, 64)
	if err != nil {
		return 0, err
	}
	return n * mult, nil
}
