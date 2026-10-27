package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	"github.com/catalinmuraru/runwarden/internal/config"
	"github.com/catalinmuraru/runwarden/internal/executor"
	"github.com/catalinmuraru/runwarden/internal/kube"
	"github.com/catalinmuraru/runwarden/internal/remote"
	"github.com/catalinmuraru/runwarden/internal/secrets"
)

// runExecutor is `runwarden executor [--environment <name>]`: it dials out to the server and
// runs that environment's runs and dropdown helpers, holding the environment's secrets and
// kubeconfig. The server keeps history, audit, limits and access control.
func runExecutor(args []string, stderr io.Writer) int {
	fs := flag.NewFlagSet("executor", flag.ContinueOnError)
	fs.SetOutput(stderr)
	env := fs.String("environment", "", "environment this executor runs (default EXECUTOR_ENVIRONMENT)")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if err := executorMain(*env); err != nil {
		slog.Error("runwarden executor stopped", "err", err)
		return 1
	}
	return 0
}

func executorMain(environment string) error {
	cfg, err := config.LoadExecutor(os.Getenv, environment)
	if err != nil {
		return fmt.Errorf("configuration: %w", err)
	}
	x := cfg.Executor
	tempRoot := filepath.Join(cfg.DataDir, "tmp")
	uploads := filepath.Join(cfg.DataDir, "uploads")
	for _, dir := range []string{tempRoot, uploads} {
		_ = os.RemoveAll(dir) // leftovers of a previous process
		if err := os.MkdirAll(dir, 0o700); err != nil {
			return fmt.Errorf("data dir: %w", err)
		}
	}
	// No per-user limit here: the server applies it across all executors.
	ex := executor.New(executor.Limits{
		MaxConcurrent:  cfg.MaxConcurrentRuns,
		Timeout:        cfg.RunTimeout,
		KillGrace:      cfg.KillGrace,
		MaxOutputBytes: cfg.MaxOutputBytes,
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
	agent := &remote.Agent{
		URL:         x.ServerURL,
		Token:       x.Token,
		Environment: x.Environment,
		Pod:         x.Pod,
		Version:     version,
		Exec:        ex,
		Capacity:    cfg.MaxConcurrentRuns,
		Secrets:     secrets.Store{Dir: cfg.SecretsDir},
		Kube:        kube.Store{Dir: cfg.KubeDir},
		Isolation:   isolation,
		UploadDir:   uploads,
		// Uploaded files travel base64-encoded in the start message.
		ReadLimit: cfg.MaxUploadBytes*4/3 + 1<<20,
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	health := &http.Server{
		Addr:              cfg.HealthAddr,
		Handler:           newExecutorHealthMux(agent),
		ReadHeaderTimeout: 5 * time.Second,
	}
	errc := make(chan error, 2)
	go func() { errc <- health.ListenAndServe() }()
	go func() { errc <- agent.Run(ctx) }()
	slog.Info("executor starting", "environment", x.Environment, "pod", x.Pod, "server", x.ServerURL, "health", cfg.HealthAddr)

	select {
	case err := <-errc:
		if !errors.Is(err, context.Canceled) {
			stop()
			_ = health.Close()
			return err
		}
	case <-ctx.Done():
	}
	slog.Info("executor shutting down: stopping running scripts")
	runsCtx, cancel := context.WithTimeout(context.Background(), cfg.KillGrace+5*time.Second)
	defer cancel()
	if err := ex.Shutdown(runsCtx); err != nil {
		slog.Warn("some runs had to be killed", "err", err)
	}
	shutdownCtx, cancelHealth := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancelHealth()
	return health.Shutdown(shutdownCtx)
}

// newExecutorHealthMux: /readyz is ready while the executor is registered with the server.
func newExecutorHealthMux(agent *remote.Agent) *http.ServeMux {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", ok)
	mux.HandleFunc("GET /readyz", func(w http.ResponseWriter, _ *http.Request) {
		if !agent.Connected() {
			http.Error(w, "not connected to the server", http.StatusServiceUnavailable)
			return
		}
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		_, _ = w.Write([]byte("ok\nisolation: " + describeIsolation(agent.Isolation) + "\n"))
	})
	return mux
}
