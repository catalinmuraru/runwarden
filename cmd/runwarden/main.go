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
	"slices"
	"strings"
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
	"github.com/catalinmuraru/runwarden/internal/kube"
	"github.com/catalinmuraru/runwarden/internal/logging"
	"github.com/catalinmuraru/runwarden/internal/policy"
	"github.com/catalinmuraru/runwarden/internal/sandbox"
	"github.com/catalinmuraru/runwarden/internal/secrets"
	"github.com/catalinmuraru/runwarden/internal/settings"
	"github.com/catalinmuraru/runwarden/internal/web"
)

// version is set at build time (-ldflags "-X main.version=v1.2.3").
var version = "dev"

func main() {
	if len(os.Args) > 1 {
		switch os.Args[1] {
		case "sandbox-exec": // hidden: the executor starts sandboxed runs through it
			os.Exit(sandbox.Main(os.Args[2:]))
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
	warnSharedSecrets(os.Environ())
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
		Workspace:    cfg.WorkspaceDir,
	})
	sb, isolation, err := newSandbox(cfg, tempRoot)
	if err != nil {
		return fmt.Errorf("configuration: %w", err)
	}
	if sb != nil {
		ex.WithSandbox(sb)
	}
	access, err := newAccess(cfg)
	if err != nil {
		return err
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	authH, sessions, err := newAuth(ctx, cfg, access, clientIP)
	if err != nil {
		return err
	}
	appSettings, err := settings.Open(cfg.DataDir)
	if err != nil {
		return fmt.Errorf("settings: %w", err)
	}
	opts := api.Options{
		Isolation:      isolation,
		BaseDir:        cfg.BaseDir,
		DataDir:        cfg.DataDir,
		MaxUploadBytes: cfg.MaxUploadBytes,
		AllowedOrigins: cfg.AllowedOrigins,
		ClientIP:       clientIP,
		Authenticate:   authH.Middleware,
		Access:         access,
		Activity:       acts,
		Secrets:        secrets.Store{Dir: cfg.SecretsDir},
		Kube:           kube.Store{Dir: cfg.KubeDir},
		Settings:       appSettings,

		ValuesMaxPerUser: cfg.ValuesMaxPerUser,
		ValuesMaxGlobal:  cfg.ValuesMaxGlobal,
		ValuesCacheTTL:   cfg.ValuesCacheTTL,
	}
	flushers := []flusher{acts}
	if sessions != nil {
		opts.Sessions = sessions
		flushers = append(flushers, sessions)
		defer func() {
			if err := sessions.Flush(); err != nil {
				slog.Error("auth: save sessions", "err", err)
			}
		}()
	}
	apiSrv := api.New(opts, ex, hist)
	apiSrv.SetCatalog(cat)
	go reloadOnHangup(ctx, cfg, apiSrv)
	go cleanHistory(ctx, cfg, hist, acts)
	go flushEveryMinute(ctx, flushers...)
	go auditExpiredExceptions(ctx, access.Store)

	srv := &http.Server{
		Addr:              cfg.ListenAddr,
		Handler:           newMux(apiSrv, authH),
		ReadHeaderTimeout: 10 * time.Second,
	}
	// Probes get their own listener, which the Service and ingress do not expose.
	health := &http.Server{
		Addr:              cfg.HealthAddr,
		Handler:           newHealthMux(apiSrv, isolation),
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

// newAccess opens the access policy on the data volume (created from the configured
// environments the first time).
func newAccess(cfg config.App) (*authz.Authorizer, error) {
	for _, w := range cfg.Access.Warnings {
		slog.Warn("configuration", "problem", w)
	}
	envs := make([]policy.Environment, len(cfg.Access.Environments))
	for i, e := range cfg.Access.Environments {
		envs[i] = policy.Environment{Name: e.Name, Match: e.Match}
	}
	store, err := policy.Open(cfg.DataDir, policy.Options{
		Environments:       envs,
		UnknownEnvironment: cfg.Access.UnknownEnvironment,
		MaxExceptionDays:   cfg.Access.MaxExceptionDays,
		// Without the built-in administrator nobody could give the devops role back.
		KeepDevops: !cfg.Auth.InitialAdmin && !cfg.Auth.NoAuth,
	})
	if err != nil {
		return nil, fmt.Errorf("access policy: %w", err)
	}
	if devops := store.Snapshot().DevopsUsers(); len(devops) == 0 && !cfg.Auth.InitialAdmin && !cfg.Auth.NoAuth {
		slog.Warn("access policy: nobody has the devops role, so nobody can open the Admin area; " +
			"set INITIAL_ADMIN=true, sign in as the built-in administrator and give the devops role to the first user")
	}
	return &authz.Authorizer{Store: store, Everyone: cfg.Auth.NoAuth}, nil
}

// auditExpiredExceptions writes an audit event when an access exception expires.
func auditExpiredExceptions(ctx context.Context, store *policy.Store) {
	tick := time.NewTicker(time.Minute)
	defer tick.Stop()
	for {
		expired, err := store.Sweep()
		if err != nil {
			slog.Error("access policy: sweep expired exceptions", "err", err)
		}
		for _, e := range expired {
			audit.Log(ctx, audit.Event{Action: audit.PolicyExpiry, Category: "configuration", Type: "change", Success: true,
				PolicyObject: "exception " + e.ID, PolicyChange: "expired",
				Message: fmt.Sprintf("access exception %s of %s expired (reason: %s)", e.ID, e.User, e.Reason)})
		}
		select {
		case <-ctx.Done():
			return
		case <-tick.C:
		}
	}
}

// newAuth builds the sign-in handlers for the enabled methods and the built-in administrator.
// The session store is nil when sign-in is disabled.
func newAuth(ctx context.Context, cfg config.App, access *authz.Authorizer, clientIP clientip.Resolver) (*auth.Handler, *auth.Store, error) {
	for _, w := range cfg.Auth.Warnings {
		slog.Warn("configuration", "problem", w)
	}
	opts := auth.Options{
		NoAuth:         cfg.Auth.NoAuth,
		AllowedOrigins: cfg.AllowedOrigins,
		ClientIP:       clientIP,
		IsAdmin:        access.IsAdmin,
		Roles:          access.Roles,

		MaxFailuresPerIP:  cfg.Auth.MaxFailuresPerIP,
		MaxFailuresGlobal: cfg.Auth.MaxFailuresGlobal,
	}
	if cfg.Auth.NoAuth {
		slog.Warn("AUTH_METHODS=none: authentication is disabled and every visitor is an admin; use only for local development")
		return auth.New(opts), nil, nil
	}

	cookies, err := auth.NewSessions(cfg.Auth.SessionKey, cfg.Auth.SessionTTL, cfg.Auth.CookieSecure)
	if err != nil {
		return nil, nil, err
	}
	store, err := auth.OpenStore(cfg.DataDir, cfg.Auth.SessionTTL)
	if err != nil {
		return nil, nil, fmt.Errorf("sessions: %w", err)
	}
	opts.Cookies, opts.Store = cookies, store
	callback := cfg.Auth.PublicURL + "/auth/callback"
	for _, id := range cfg.Auth.Methods {
		switch id {
		case config.AuthGitLab, config.AuthOIDC:
			oc := cfg.Auth.OIDC
			if id == config.AuthGitLab {
				oc = cfg.Auth.GitLab
			}
			p, err := auth.NewOIDC(ctx, oc, callback, cfg.Auth.PublicURL)
			if err != nil {
				return nil, nil, err
			}
			opts.Methods = append(opts.Methods, auth.Method{ID: id, Label: oc.Label, Redirect: p, RequiredGroup: oc.RequiredGroup})
		case config.AuthLDAP:
			opts.Methods = append(opts.Methods, auth.Method{ID: id, Label: cfg.Auth.LDAP.Label, Password: auth.NewLDAP(cfg.Auth.LDAP)})
		}
	}

	if cfg.Auth.InitialAdmin {
		admin, path, err := auth.EnableLocalAdmin(cfg.DataDir)
		if err != nil {
			return nil, nil, fmt.Errorf("initial admin: %w", err)
		}
		opts.Methods = append(opts.Methods, auth.Method{ID: auth.MethodLocal, Label: "Administrator", Password: admin})
		slog.Warn("initial admin enabled: sign in as the built-in administrator with the password in the file; disable INITIAL_ADMIN once devops users are set up",
			"user", auth.LocalAdminName, "password_file", path)
	} else {
		if err := auth.DisableLocalAdmin(cfg.DataDir); err != nil {
			return nil, nil, fmt.Errorf("initial admin: %w", err)
		}
		if n := store.RevokeWhere(func(u auth.User) bool { return u.Method == auth.MethodLocal }); n > 0 {
			slog.Info("initial admin disabled: its sessions were ended", "sessions", n)
		}
	}
	slog.Info("authentication enabled", "methods", cfg.Auth.Methods, "initial_admin", cfg.Auth.InitialAdmin)
	return auth.New(opts), store, nil
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

// flusher saves state kept in memory (users index, session last-seen times).
type flusher interface{ Flush() error }

// flushEveryMinute saves the flushers' state every minute.
func flushEveryMinute(ctx context.Context, fs ...flusher) {
	tick := time.NewTicker(time.Minute)
	defer tick.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-tick.C:
			for _, f := range fs {
				if err := f.Flush(); err != nil {
					slog.Error("save state", "err", err)
				}
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
	mux.Handle("GET /", web.Handler(authH.SignedIn))
	return mux
}

// newHealthMux serves the liveness and readiness probes. The readiness body also says how runs
// are isolated (ISOLATION=required never gets this far without a sandbox).
func newHealthMux(apiSrv *api.Server, isolation sandbox.Status) *http.ServeMux {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", ok)
	mux.HandleFunc("GET /readyz", func(w http.ResponseWriter, _ *http.Request) {
		if !apiSrv.Ready() {
			http.Error(w, "runner configs not loaded", http.StatusServiceUnavailable)
			return
		}
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		_, _ = w.Write([]byte("ok\nisolation: " + describeIsolation(isolation) + "\n"))
	})
	return mux
}

func ok(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	_, _ = w.Write([]byte("ok\n"))
}

// warnSharedSecrets warns about RUNNER_ENV_* variables that look like secrets: they reach every
// script in every environment. Secrets belong in SECRETS_DIR, per environment.
func warnSharedSecrets(environ []string) {
	var names []string
	for _, kv := range environ {
		name, _, _ := strings.Cut(kv, "=")
		rest, ok := strings.CutPrefix(name, "RUNNER_ENV_")
		if !ok {
			continue
		}
		for _, hint := range []string{"TOKEN", "PASSWORD", "PASSWD", "SECRET", "KEY", "CREDENTIAL"} {
			if strings.Contains(rest, hint) {
				names = append(names, name)
				break
			}
		}
	}
	if len(names) > 0 {
		slices.Sort(names)
		slog.Warn("these RUNNER_ENV_ variables look like secrets and reach every script in every environment; "+
			"move them to the secrets of an environment (SECRETS_DIR) and declare them in the runner configs", "variables", names)
	}
}
