// Command proxy runs the go-reverse-flow reverse proxy: it loads the JSON
// config, starts the health checker, builds the round-robin proxy handler,
// and serves until Ctrl+C.
package main

import (
	"context"
	"errors"
	"flag"
	"log"
	"net/http"
	"net/url"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/dimastriann/go-reverse-flow/internal/balancer"
	"github.com/dimastriann/go-reverse-flow/internal/config"
	"github.com/dimastriann/go-reverse-flow/internal/health"
	"github.com/dimastriann/go-reverse-flow/internal/logging"
	"github.com/dimastriann/go-reverse-flow/internal/metrics"
	"github.com/dimastriann/go-reverse-flow/internal/proxy"
	"github.com/dimastriann/go-reverse-flow/internal/ratelimit"
)

const (
	// defaultConfigPath is used when -config is not given.
	defaultConfigPath = "config/config.json"

	// shutdownTimeout bounds how long in-flight requests may finish after Ctrl+C.
	shutdownTimeout = 10 * time.Second
)

func main() {
	cfgPath := flag.String("config", defaultConfigPath, "path to JSON config file")
	flag.Parse()

	cfg, err := config.Load(*cfgPath)
	if err != nil {
		log.Fatalf("config: %v", err)
	}

	backends := make([]*url.URL, len(cfg.Backends))
	for i, raw := range cfg.Backends {
		u, err := url.Parse(raw)
		if err != nil {
			log.Fatalf("config: backends[%d]: %v", i, err)
		}
		backends[i] = u
	}

	interval, probeTimeout := cfg.Health.Values()
	checker := health.New(cfg.Backends, probeTimeout, health.WithTransitionHook(func(backend string, up bool) {
		if up {
			log.Printf("health: backend %s is UP", backend)
			return
		}
		log.Printf("health: backend %s is DOWN", backend)
	}))

	scrape := metrics.New(metrics.WithStatusSource(checker.Statuses))
	handler, err := proxy.New(backends, &balancer.Balancer{},
		proxy.WithHealth(checker),
		proxy.WithMetrics(scrape),
	)
	if err != nil {
		log.Fatalf("proxy: %v", err)
	}
	accessLog := logging.New(handler) // one structured line per request
	chain := accessLog

	// Optional per-client throttling sits inside the access log so 429s are
	// still logged (order: accessLog -> rateLimiter -> proxy).
	if rate, burst := cfg.RateLimit.Values(); rate > 0 {
		chain = logging.New(ratelimit.New(handler, rate, burst))
		log.Printf("ratelimit: %.2f req/s per client, burst %d", rate, burst)
	}

	// /metrics serves the counter/gauge exposition; everything else is proxied.
	mux := http.NewServeMux()
	mux.Handle("/metrics", scrape.Handler())
	mux.Handle("/", chain)

	// ReadHeaderTimeout guards slowloris-style connections; other timeouts
	// stay at zero because proxied streams may legitimately be long-lived.
	srv := &http.Server{
		Addr:              cfg.ListenAddr,
		Handler:           mux,
		ReadHeaderTimeout: 5 * time.Second,
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	// Checker probes are killed exactly when the server context dies.
	checker.Start(ctx, interval)

	errCh := make(chan error, 1)
	go func() {
		log.Printf("proxy: listening on %s over %d backend(s)", cfg.ListenAddr, len(backends))
		for i, u := range backends {
			log.Printf("proxy: backends[%d] = %s", i, u)
		}
		log.Printf("health: probing every %s (timeout %s)", interval, probeTimeout)

		// Serve returns http.ErrServerClosed on graceful shutdown, which is
		// expected — anything else is fatal.
		if err := srv.ListenAndServe(); !errors.Is(err, http.ErrServerClosed) {
			errCh <- err
		}
		close(errCh)
	}()

	select {
	case err := <-errCh:
		if err != nil {
			log.Fatalf("server: %v", err)
		}
	case <-ctx.Done():
		log.Printf("shutting down: draining in-flight requests…")
		shCtx, cancel := context.WithTimeout(context.Background(), shutdownTimeout)
		defer cancel()
		if err := srv.Shutdown(shCtx); err != nil {
			log.Printf("graceful shutdown: %v", err)
		}
	}
}
