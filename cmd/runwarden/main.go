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
	"syscall"
	"time"

	"github.com/catalinmuraru/runwarden/internal/api"
	"github.com/catalinmuraru/runwarden/internal/config"
	"github.com/catalinmuraru/runwarden/internal/executor"
	"github.com/catalinmuraru/runwarden/internal/history"
	"github.com/catalinmuraru/runwarden/internal/web"
)

func main() {
	if len(os.Args) > 1 && os.Args[1] == "check-config" {
		os.Exit(checkConfig(os.Args[2:], os.Stdout))
	}

	slog.SetDefault(slog.New(slog.NewJSONHandler(os.Stdout, nil)))
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
	ex := executor.New(executor.Limits{
		MaxConcurrent:  cfg.MaxConcurrentRuns,
		Timeout:        cfg.RunTimeout,
		KillGrace:      cfg.KillGrace,
		MaxOutputBytes: cfg.MaxOutputBytes,
	}, executor.EnvPolicy{})
	apiSrv := api.New(api.Options{
		BaseDir:        cfg.BaseDir,
		DataDir:        cfg.DataDir,
		MaxUploadBytes: cfg.MaxUploadBytes,
		AllowedOrigins: cfg.AllowedOrigins,
	}, ex, hist)
	apiSrv.SetCatalog(cat)

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	go reloadOnHangup(ctx, cfg, apiSrv)

	srv := &http.Server{
		Addr:              cfg.ListenAddr,
		Handler:           newMux(apiSrv),
		ReadHeaderTimeout: 10 * time.Second,
	}
	errc := make(chan error, 1)
	go func() {
		slog.Info("listening", "addr", cfg.ListenAddr, "runners", cfg.RunnersDir, "data", cfg.DataDir)
		errc <- srv.ListenAndServe()
	}()

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
	if err := srv.Shutdown(shutdownCtx); err != nil {
		return err
	}
	if err := <-errc; !errors.Is(err, http.ErrServerClosed) {
		return err
	}
	return nil
}

// loadCatalog loads the runner configs, logging problems; scripts with errors are left out.
func loadCatalog(cfg config.App) (*config.Catalog, error) {
	cat, err := config.LoadDir(cfg.RunnersDir, config.Options{BaseDir: cfg.BaseDir})
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
			if err != nil {
				slog.Error("reload failed; keeping the current configs", "err", err)
				continue
			}
			apiSrv.SetCatalog(cat)
		}
	}
}

func newMux(apiSrv *api.Server) *http.ServeMux {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", ok)
	mux.HandleFunc("GET /readyz", func(w http.ResponseWriter, r *http.Request) {
		if !apiSrv.Ready() {
			http.Error(w, "runner configs not loaded", http.StatusServiceUnavailable)
			return
		}
		ok(w, r)
	})
	apiSrv.Register(mux)
	mux.Handle("GET /", web.Handler())
	return mux
}

func ok(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	_, _ = w.Write([]byte("ok\n"))
}
