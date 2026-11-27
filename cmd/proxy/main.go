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

	"github.com/dimastriann/go-reverse-flow/internal/admin"
	"github.com/dimastriann/go-reverse-flow/internal/balancer"
	"github.com/dimastriann/go-reverse-flow/internal/cache"
	"github.com/dimastriann/go-reverse-flow/internal/config"
	"github.com/dimastriann/go-reverse-flow/internal/health"
	"github.com/dimastriann/go-reverse-flow/internal/logging"
	"github.com/dimastriann/go-reverse-flow/internal/metrics"
	"github.com/dimastriann/go-reverse-flow/internal/proxy"
	"github.com/dimastriann/go-reverse-flow/internal/ratelimit"
	"github.com/dimastriann/go-reverse-flow/internal/router"
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
	// Env overrides beat the file config so the same file can serve
	// different ports/credentials per replica — unset means the file wins.
	cfg.ApplyEnvOverride(os.Getenv)

	allBackends := cfg.AllBackends()
	parsed := make(map[string]*url.URL, len(allBackends))
	for _, raw := range allBackends {
		u, err := url.Parse(raw)
		if err != nil {
			log.Fatalf("config: backend %s: %v", raw, err)
		}
		parsed[raw] = u
	}

	interval, probeTimeout := cfg.Health.Values()
	checker := health.New(allBackends, probeTimeout, health.WithTransitionHook(func(backend string, up bool) {
		if up {
			log.Printf("health: backend %s is UP", backend)
			return
		}
		log.Printf("health: backend %s is DOWN", backend)
	}))

	scrape := metrics.New(metrics.WithStatusSource(checker.Statuses))
	proxyOpts := []proxy.Option{proxy.WithHealth(checker), proxy.WithMetrics(scrape)}
	if len(cfg.Weights) > 0 {
		weights := make(map[string]int, len(cfg.Backends))
		for i, w := range cfg.Weights {
			weights[cfg.Backends[i]] = w
		}
		smooth, err := balancer.NewSmoothWeighted(weights)
		if err != nil {
			log.Fatalf("balancer: %v", err)
		}
		proxyOpts = append(proxyOpts, proxy.WithWeighted(smooth))
	}

	// One proxy handler per pool: the default pool plus one per route. They
	// share the checker, metrics, and weighted picker, but keep independent
	// round-robin pointers so pool traffic never disturbs pool parity.
	buildPool := func(rawURLs []string, label string) *proxy.Handler {
		pool := make([]*url.URL, len(rawURLs))
		for i, raw := range rawURLs {
			pool[i] = parsed[raw]
		}
		h, err := proxy.New(pool, &balancer.Balancer{}, proxyOpts...)
		if err != nil {
			log.Fatalf("proxy: %s pool: %v", label, err)
		}
		return h
	}

	defaultPool := buildPool(cfg.Backends, "default")
	var routed http.Handler = defaultPool
	if len(cfg.Routes) > 0 {
		pools := make(map[string]http.Handler, len(cfg.Routes))
		for _, r := range cfg.Routes {
			pools[r.Prefix] = buildPool(r.Backends, r.Prefix)
			log.Printf("router: %s -> %d backend(s)", r.Prefix, len(r.Backends))
		}
		routed = router.New(defaultPool, pools)
	}
	// Compose the proxy chain innermost-first: cache sits under the rate
	// limiter (throttled clients never reach the cache), and the access log
	// outermost so 429s and cache hits/misses are all logged.
	proxied := http.Handler(routed)
	var cacheLayer *cache.Cache
	if ttl := cfg.Cache.Value(); ttl > 0 {
		cacheLayer, proxied = cache.NewCache(proxied, ttl)
		log.Printf("cache: %s ttl for cacheable GET/HEAD responses", ttl)
	}
	rate, burst := cfg.RateLimit.Values()
	if rate > 0 {
		proxied = ratelimit.New(proxied, rate, burst)
		log.Printf("ratelimit: %.2f req/s per client, burst %d", rate, burst)
	}
	chain := logging.New(proxied)

	if len(cfg.Weights) > 0 {
		log.Printf("balancer: smooth weighted %v over %d backend(s)", cfg.Weights, len(cfg.Backends))
	}

	// /metrics serves the exposition and /cache/purge the invalidation
	// endpoint (while the cache is enabled); everything else is proxied.
	mux := http.NewServeMux()
	mux.Handle("/metrics", scrape.Handler())
	if cacheLayer != nil {
		mux.Handle("/cache/purge", cacheLayer.PurgeHandler())
		log.Printf("cache: POST /cache/purge invalidates every entry")
	}
	mux.Handle("/", chain)

	// Optional admin surface on its own listener: never cached, throttled,
	// or routed; dies with the same signal context as the proxy. /api/status
	// requires Basic Auth; the embedded dashboard at / is static and
	// credential-free (it only reads, and prompts for the API credentials).
	adminListen, adminUser, adminPass, adminEnabled := cfg.Admin.Values()
	if adminEnabled {
		startedAt := time.Now()
		statusSource := func() admin.Status {
			requests := scrape.Snapshot()
			statuses := checker.Statuses()
			backends := make([]admin.BackendStatus, 0, len(allBackends))
			for _, raw := range allBackends {
				backends = append(backends, admin.BackendStatus{
					URL:      raw,
					Healthy:  statuses[raw],
					Requests: requests[raw],
				})
			}
			return admin.Status{
				Version:     "v0.9.0",
				Uptime:      time.Since(startedAt).Round(time.Second).String(),
				Listen:      cfg.ListenAddr,
				AdminListen: adminListen,
				StartedAt:   startedAt,
				Backends:    backends,
				CacheTTL:    cfg.Cache.Value().String(),
				RateLimit:   rate,
				RateBurst:   burst,
			}
		}
		adminAPI := admin.New(adminUser, adminPass, statusSource)
		adminMux := http.NewServeMux()
		adminMux.Handle("/api/status", adminAPI.Handler())
		adminMux.Handle("/", admin.UIHandler())
		adminSrv := &http.Server{
			Addr:              adminListen,
			Handler:           adminMux,
			ReadHeaderTimeout: 5 * time.Second,
		}
		go func() {
			log.Printf("admin: listening on %s (user %s)", adminListen, adminUser)
			if err := adminSrv.ListenAndServe(); !errors.Is(err, http.ErrServerClosed) {
				log.Printf("admin: %v", err)
			}
		}()
		defer func() {
			shCtx, cancel := context.WithTimeout(context.Background(), shutdownTimeout)
			defer cancel()
			_ = adminSrv.Shutdown(shCtx)
		}()
	} else {
		log.Printf("admin: disabled (set admin.password or ADMIN_PASSWORD to enable)")
	}

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
		log.Printf("proxy: listening on %s over %d backend(s)", cfg.ListenAddr, len(allBackends))
		for i, u := range allBackends {
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
