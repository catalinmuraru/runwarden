// Command runwarden serves a web UI to run scripts with parameters, live output and history.
package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	"github.com/catalinmuraru/runwarden/internal/activity"
	"github.com/catalinmuraru/runwarden/internal/api"
	"github.com/catalinmuraru/runwarden/internal/audit"
	"github.com/catalinmuraru/runwarden/internal/auth"
	"github.com/catalinmuraru/runwarden/internal/authz"
	"github.com/catalinmuraru/runwarden/internal/clientip"
	"github.com/catalinmuraru/runwarden/internal/config"
	"github.com/catalinmuraru/runwarden/internal/executor"
	"github.com/catalinmuraru/runwarden/internal/history"
	"github.com/catalinmuraru/runwarden/internal/logging"
	"github.com/catalinmuraru/runwarden/internal/web"
)

// version is set at build time (-ldflags "-X main.version=v1.2.3").
var version = "dev"

func main() {
	if len(os.Args) > 1 {
		switch os.Args[1] {
		case "check-config":
			os.Exit(checkConfig(os.Args[2:], os.Stdout))
		case "version", "--version":
			fmt.Println(version)
			return
		}
	}

	slog.SetDefault(slog.New(logging.NewECSHandler(os.Stdout, version, slog.LevelInfo)))
	if err := run(); err != nil {
		slog.Error("runwarden stopped", "err", err)
		os.Exit(1)
	}
}

func run() error {
	cfg, err := config.LoadApp(os.Getenv)
	if err != nil {
		return fmt.Errorf("configuration: %w", err)
	}
	cat, err := loadCatalog(cfg)
	if err != nil {
		return err
	}
	hist, err := history.Open(cfg.DataDir)
	if err != nil {
		return fmt.Errorf("history: %w", err)
	}
	acts, err := activity.Open(cfg.DataDir, cfg.Auth.SessionTTL)
	if err != nil {
		return fmt.Errorf("activity: %w", err)
	}
	acts.MaxDayBytes = cfg.ActivityMaxDay
	audit.SetSink(acts.Add)
	defer func() {
		if err := acts.Flush(); err != nil {
			slog.Error("activity: save users", "err", err)
		}
	}()
	clientIP, err := clientip.New(cfg.TrustedProxies)
	if err != nil {
		return fmt.Errorf("configuration: %w", err)
	}
	// Per-run temp dirs live here; leftovers from a previous process are removed.
	tempRoot := filepath.Join(cfg.DataDir, "tmp")
	_ = os.RemoveAll(tempRoot)
	if err := os.MkdirAll(tempRoot, 0o700); err != nil {
		return fmt.Errorf("temp dir: %w", err)
	}
	ex := executor.New(executor.Limits{
		MaxConcurrent:  cfg.MaxConcurrentRuns,
		Timeout:        cfg.RunTimeout,
		KillGrace:      cfg.KillGrace,
		MaxOutputBytes: cfg.MaxOutputBytes,
		MaxPerUser:     cfg.MaxRunsPerUser,
	}, executor.EnvPolicy{
		TempRoot:     tempRoot,
		PrivateHome:  cfg.RunHome == config.RunHomePerRun,
		HomeSkeleton: cfg.HomeSkeleton,
	})
	policy := authz.Policy{
		AdminUsers:  cfg.Auth.AdminUsers,
		AdminGroups: cfg.Auth.AdminGroups,
		Everyone:    cfg.Auth.Type == config.AuthNone,
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	var apiSrv *api.Server
	authH, err := newAuth(ctx, cfg, policy, clientIP, func() []string { return apiSrv.ReferencedGroups() })
	if err != nil {
		return err
	}
	apiSrv = api.New(api.Options{
		BaseDir:        cfg.BaseDir,
		DataDir:        cfg.DataDir,
		MaxUploadBytes: cfg.MaxUploadBytes,
		AllowedOrigins: cfg.AllowedOrigins,
		ClientIP:       clientIP,
		Authenticate:   authH.Middleware,
		Policy:         policy,
		Activity:       acts,

		ValuesMaxPerUser: cfg.ValuesMaxPerUser,
		ValuesMaxGlobal:  cfg.ValuesMaxGlobal,
		ValuesCacheTTL:   cfg.ValuesCacheTTL,
	}, ex, hist)
	apiSrv.SetCatalog(cat)
	go reloadOnHangup(ctx, cfg, apiSrv)
	go cleanHistory(ctx, cfg, hist, acts)
	go flushActivity(ctx, acts)

	srv := &http.Server{
		Addr:              cfg.ListenAddr,
		Handler:           newMux(apiSrv, authH),
		ReadHeaderTimeout: 10 * time.Second,
	}
	// Probes get their own listener, which the Service and ingress do not expose.
	health := &http.Server{
		Addr:              cfg.HealthAddr,
		Handler:           newHealthMux(apiSrv),
		ReadHeaderTimeout: 5 * time.Second,
	}
	errc := make(chan error, 2)
	go func() {
		slog.Info("listening", "addr", cfg.ListenAddr, "health", cfg.HealthAddr, "runners", cfg.RunnersDir, "data", cfg.DataDir)
		errc <- srv.ListenAndServe()
	}()
	go func() { errc <- health.ListenAndServe() }()

	select {
	case err := <-errc:
		return err
	case <-ctx.Done():
	}

	slog.Info("shutting down: stopping running scripts")
	runsCtx, cancelRuns := context.WithTimeout(context.Background(), cfg.KillGrace+5*time.Second)
	defer cancelRuns()
	if err := ex.Shutdown(runsCtx); err != nil {
		slog.Warn("some runs had to be killed", "err", err)
	}
	hist.Wait()

	shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	_ = health.Shutdown(shutdownCtx)
	if err := srv.Shutdown(shutdownCtx); err != nil {
		return err
	}
	for range 2 {
		if err := <-errc; !errors.Is(err, http.ErrServerClosed) {
			return err
		}
	}
	return nil
}

// newAuth builds the login handlers for the configured AUTH_TYPE.
func newAuth(ctx context.Context, cfg config.App, policy authz.Policy, clientIP clientip.Resolver, relevantGroups func() []string) (*auth.Handler, error) {
	opts := auth.Options{
		Type:           cfg.Auth.Type,
		AllowedOrigins: cfg.AllowedOrigins,
		ClientIP:       clientIP,
		IsAdmin:        policy.IsAdmin,
		RelevantGroups: relevantGroups,

		MaxFailuresPerIP:  cfg.Auth.MaxFailuresPerIP,
		MaxFailuresGlobal: cfg.Auth.MaxFailuresGlobal,
	}
	if cfg.Auth.Type == config.AuthNone {
		slog.Warn("AUTH_TYPE=none: authentication is disabled and every visitor is an admin; use only for local development")
		return auth.New(opts), nil
	}

	sessions, err := auth.NewSessions(cfg.Auth.SessionKey, cfg.Auth.SessionTTL, cfg.Auth.CookieSecure)
	if err != nil {
		return nil, err
	}
	opts.Sessions = sessions
	switch cfg.Auth.Type {
	case config.AuthOIDC, config.AuthGitLab:
		p, err := auth.NewOIDC(ctx, cfg.Auth.OIDC, cfg.Auth.PublicURL+"/auth/callback", cfg.Auth.PublicURL)
		if err != nil {
			return nil, err
		}
		opts.Redirect = p
	case config.AuthLDAP:
		opts.Password = auth.NewLDAP(cfg.Auth.LDAP)
	}
	slog.Info("authentication enabled", "type", cfg.Auth.Type)
	return auth.New(opts), nil
}

// loadCatalog loads the runner configs, logging problems; scripts with errors are left out.
func loadCatalog(cfg config.App) (*config.Catalog, error) {
	cat, err := config.LoadDir(cfg.RunnersDir, config.Options{BaseDir: cfg.BaseDir, MaxTimeout: cfg.RunTimeoutMax})
	if err != nil {
		return nil, fmt.Errorf("runners: %w", err)
	}
	for _, w := range cat.Warnings {
		slog.Warn("runner config", "file", w.File, "problem", w.Msg)
	}
	for _, e := range cat.Errors {
		slog.Error("runner config skipped", "file", e.File, "problem", e.Msg)
	}
	slog.Info("runner configs loaded", "scripts", len(cat.Scripts), "errors", len(cat.Errors))
	return cat, nil
}

// reloadOnHangup reloads the runner configs on SIGHUP.
func reloadOnHangup(ctx context.Context, cfg config.App, apiSrv *api.Server) {
	hup := make(chan os.Signal, 1)
	signal.Notify(hup, syscall.SIGHUP)
	defer signal.Stop(hup)
	for {
		select {
		case <-ctx.Done():
			return
		case <-hup:
			cat, err := loadCatalog(cfg)
			e := audit.Event{Action: audit.ConfigReload, Category: "configuration", Type: "change", Success: err == nil}
			if err != nil {
				slog.Error("reload failed; keeping the current configs", "err", err)
				e.Reason, e.Message = err.Error(), "runner config reload failed"
			} else {
				apiSrv.SetCatalog(cat)
				e.Count, e.Message = len(cat.Scripts), fmt.Sprintf("runner configs reloaded: %d scripts, %d errors", len(cat.Scripts), len(cat.Errors))
			}
			audit.Log(ctx, e)
		}
	}
}

// cleanHistory applies the retention settings at startup and then every hour.
func cleanHistory(ctx context.Context, cfg config.App, hist *history.Store, acts *activity.Store) {
	if cfg.HistoryMaxAge == 0 && cfg.HistoryMaxRuns == 0 {
		return
	}
	tick := time.NewTicker(time.Hour)
	defer tick.Stop()
	for {
		n, err := hist.Cleanup(cfg.HistoryMaxAge, cfg.HistoryMaxRuns, time.Now())
		if err != nil {
			slog.Error("history cleanup", "err", err)
		}
		if n > 0 {
			audit.Log(ctx, audit.Event{Action: audit.HistoryCleanup, Category: "file", Type: "deletion", Success: true,
				Count: n, Message: fmt.Sprintf("history cleanup removed %d runs", n)})
		}
		if _, err := acts.Cleanup(cfg.HistoryMaxAge, time.Now()); err != nil {
			slog.Error("activity cleanup", "err", err)
		}
		select {
		case <-ctx.Done():
			return
		case <-tick.C:
		}
	}
}

// flushActivity saves the users index every minute.
func flushActivity(ctx context.Context, acts *activity.Store) {
	tick := time.NewTicker(time.Minute)
	defer tick.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-tick.C:
			if err := acts.Flush(); err != nil {
				slog.Error("activity: save users", "err", err)
			}
		}
	}
}

func newMux(apiSrv *api.Server, authH *auth.Handler) *http.ServeMux {
	mux := http.NewServeMux()
	authH.Register(mux)
	// Probes live on HEALTH_ADDR only; here they are plain 404s rather than the UI's index page.
	for _, p := range []string{"GET /healthz", "GET /readyz"} {
		mux.HandleFunc(p, http.NotFound)
	}
	apiSrv.Register(mux)
	mux.Handle("GET /", web.Handler())
	return mux
}

// newHealthMux serves the liveness and readiness probes.
func newHealthMux(apiSrv *api.Server) *http.ServeMux {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", ok)
	mux.HandleFunc("GET /readyz", func(w http.ResponseWriter, r *http.Request) {
		if !apiSrv.Ready() {
			http.Error(w, "runner configs not loaded", http.StatusServiceUnavailable)
			return
		}
		ok(w, r)
	})
	return mux
}

func ok(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	_, _ = w.Write([]byte("ok\n"))
}
