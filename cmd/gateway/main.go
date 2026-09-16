// Command gateway runs the CQU NetProbe central push gateway.
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

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promhttp"

	"github.com/tano/cqu-netprobe-gateway/internal/admin"
	"github.com/tano/cqu-netprobe-gateway/internal/api"
	"github.com/tano/cqu-netprobe-gateway/internal/config"
	"github.com/tano/cqu-netprobe-gateway/internal/latest"
	"github.com/tano/cqu-netprobe-gateway/internal/metrics"
	"github.com/tano/cqu-netprobe-gateway/internal/portal"
	"github.com/tano/cqu-netprobe-gateway/internal/store"
	"github.com/tano/cqu-netprobe-gateway/internal/webui"
)

// shutdownTimeout bounds how long in-flight requests may finish.
const shutdownTimeout = 15 * time.Second

// version is the release version, injected at build time with
// -ldflags="-X main.version=v1.2.3" (see Dockerfile). It stays "dev" for
// `go build` / `go run` from a working tree.
var version = "dev"

// Handler bundles both listeners' handlers with the state they share.
//
// Latest and Self are exported because the metrics collector must be given the
// *same* latest.Store the push handler writes to. Constructing a second store
// would yield a gateway that accepts pushes and exposes nothing — a silent
// failure, since neither component is individually broken.
type Handler struct {
	Push   http.Handler
	Admin  *admin.Server
	Portal *portal.Server
	Latest *latest.Store
	Self   *metrics.Self
}

// buildHandler assembles the push API and admin UI into one mux.
func buildHandler(cfg *config.Config, st *store.Store, reg prometheus.Registerer) (*Handler, error) {
	self := metrics.NewSelf(reg)
	latestStore := latest.New()
	limiter := api.NewLimiter(cfg.RateLimit, cfg.RateLimitBurst)

	push := api.NewServer(api.Deps{
		Store:   st,
		Latest:  latestStore,
		Self:    self,
		Limiter: limiter,
	})

	// The portal owns the one-shot slot store, so it is built first: the admin's
	// create and rotate flows mint into it and redirect to the portal's public
	// /token/{slot}, which is the only token page there is.
	portalSrv, err := portal.NewServer(portal.Deps{
		Store:   st,
		Config:  cfg,
		Limiter: portal.NewRegisterLimiter(cfg.RegisterLimit, cfg.RegisterLimitBurst),
	})
	if err != nil {
		return nil, fmt.Errorf("build portal: %w", err)
	}

	adminSrv, err := admin.NewServer(admin.Deps{
		Store:           st,
		Config:          cfg,
		Latest:          latestStore,
		Limiter:         limiter,
		OneShot:         portalSrv,
		OnlineThreshold: cfg.OnlineThreshold,
	})
	if err != nil {
		return nil, fmt.Errorf("build admin server: %w", err)
	}

	// One mux for the public listener, so push, admin and the public portal share
	// a port while /metrics stays on its own listener (design doc §22).
	mux := http.NewServeMux()
	mux.Handle("/api/v1/", push.Routes())
	mux.Handle("/admin", adminSrv.Routes())
	mux.Handle("/admin/", adminSrv.Routes())
	// The shared stylesheet, served once for both UIs.
	mux.Handle("GET /static/", webui.StaticHandler())
	// The public registration pages, including the one-shot token display.
	// Registered last for readability only: ServeMux prefers the more specific
	// pattern, so this does not shadow /admin/....
	mux.Handle("/", portalSrv.Routes())

	return &Handler{Push: mux, Admin: adminSrv, Portal: portalSrv, Latest: latestStore, Self: self}, nil
}

// metricsHandler builds the /metrics handler from its own registry.
func metricsHandler(reg *prometheus.Registry) http.Handler {
	return promhttp.HandlerFor(reg, promhttp.HandlerOpts{
		ErrorHandling: promhttp.ContinueOnError,
	})
}

func main() {
	if err := run(); err != nil {
		// Nothing sensitive reaches this path: errors are wrapped, never dumped.
		fmt.Fprintln(os.Stderr, "fatal:", err)
		os.Exit(1)
	}
}

func run() error {
	cfg, err := config.Load()
	if err != nil {
		return err
	}

	logger := slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: cfg.LogLevel}))
	slog.SetDefault(logger)

	if err := os.MkdirAll(cfg.DataDir, 0o750); err != nil {
		return fmt.Errorf("create data dir: %w", err)
	}

	st, err := store.Open(cfg.DBPath())
	if err != nil {
		return err
	}
	defer func() { _ = st.Close() }()

	reg := prometheus.NewRegistry()
	handler, err := buildHandler(cfg, st, reg)
	if err != nil {
		return err
	}

	collector := metrics.NewCollector(handler.Latest, st, cfg.OnlineThreshold, handler.Self)
	reg.MustRegister(collector)

	// Log a generated admin password exactly once, at startup, and never again.
	if pw, generated := handler.Admin.PasswordGeneration(); generated {
		logger.Warn("ADMIN_PASSWORD was not set; generated a random administrator password. "+
			"Store it now — it will not be shown again. Delete the admin_password_hash row "+
			"in the settings table and restart to generate a new one.",
			"username", cfg.AdminUsername, "password", pw)
	}

	// Both servers route net/http's own output — recovered handler panics, TLS
	// handshake failures, superfluous WriteHeader calls — through the JSON
	// handler at ERROR level. slog.SetDefault bridges the stdlib log package on
	// modern Go, but only at INFO, so a recovered panic would otherwise land at
	// the wrong severity (design §13).
	publicSrv := &http.Server{
		Addr:              cfg.ListenAddr,
		Handler:           handler.Push,
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       10 * time.Second,
		WriteTimeout:      15 * time.Second,
		IdleTimeout:       60 * time.Second,
		MaxHeaderBytes:    16 << 10,
		ErrorLog:          slog.NewLogLogger(logger.Handler(), slog.LevelError),
	}

	metricsMux := http.NewServeMux()
	metricsMux.Handle("GET /metrics", api.CIDRAllowlist(cfg.MetricsAllowedCIDRs, metricsHandler(reg)))
	metricsSrv := &http.Server{
		Addr:              cfg.MetricsAddr,
		Handler:           metricsMux,
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       10 * time.Second,
		WriteTimeout:      30 * time.Second,
		IdleTimeout:       60 * time.Second,
		MaxHeaderBytes:    16 << 10,
		ErrorLog:          slog.NewLogLogger(logger.Handler(), slog.LevelError),
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	errCh := make(chan error, 2)
	go func() {
		logger.Info("starting push listener", "addr", cfg.ListenAddr, "version", version)
		if err := publicSrv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			errCh <- fmt.Errorf("push listener: %w", err)
		}
	}()
	go func() {
		logger.Info("starting metrics listener", "addr", cfg.MetricsAddr,
			"allowed_cidrs", len(cfg.MetricsAllowedCIDRs))
		if err := metricsSrv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			errCh <- fmt.Errorf("metrics listener: %w", err)
		}
	}()

	select {
	case err := <-errCh:
		return err
	case <-ctx.Done():
		logger.Info("shutdown signal received")
	}

	shutdownCtx, cancel := context.WithTimeout(context.Background(), shutdownTimeout)
	defer cancel()

	var errs []error
	if err := publicSrv.Shutdown(shutdownCtx); err != nil {
		errs = append(errs, fmt.Errorf("shutdown push listener: %w", err))
	}
	if err := metricsSrv.Shutdown(shutdownCtx); err != nil {
		errs = append(errs, fmt.Errorf("shutdown metrics listener: %w", err))
	}
	logger.Info("stopped")
	return errors.Join(errs...)
}
