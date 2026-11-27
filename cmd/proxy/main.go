// Command proxy runs the go-reverse-flow reverse proxy: it loads the JSON
// or YAML config, starts the health checker, builds the round-robin proxy
// handler, and serves until Ctrl+C. The admin listener can apply new
// configurations at runtime: the routing table is rebuilt and swapped
// atomically, and a failing candidate never touches the live one.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log"
	"net/http"
	"net/url"
	"os"
	"os/signal"
	"runtime"
	"strings"
	"sync/atomic"
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

	// version is reported by the admin status API.
	version = "v0.11.0"
)

// appRuntime is one immutable configuration generation: everything built
// from a validated config. Swapping generations is an atomic pointer store;
// in-flight requests finish against the old generation, whose checker stops
// only after the swap.
type appRuntime struct {
	cfg         *config.Config
	checker     *health.Checker
	proxy       http.Handler // cache + ratelimit + routed pools chain
	cacheLayer  *cache.Cache // nil when caching is disabled
	stopChecker context.CancelFunc
}

// parsePool converts raw backend URLs; unknown entries are a config error.
func parsePool(raws []string) ([]*url.URL, error) {
	pool := make([]*url.URL, len(raws))
	for i, raw := range raws {
		u, err := url.Parse(raw)
		if err != nil {
			return nil, fmt.Errorf("backend %s: %w", raw, err)
		}
		pool[i] = u
	}
	return pool, nil
}

func main() {
	cfgPath := flag.String("config", defaultConfigPath, "path to JSON or YAML config file")
	flag.Parse()

	cfg, err := config.Load(*cfgPath)
	if err != nil {
		log.Fatalf("config: %v", err)
	}
	// Env overrides beat the file config so the same file can serve
	// different ports/credentials per replica — unset means the file wins.
	cfg.ApplyEnvOverride(os.Getenv)

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	startedAt := time.Now()
	scrape := metrics.New() // shared across generations: counters survive reloads

	buildRuntime := func(next *config.Config) (*appRuntime, error) {
		pool, err := parsePool(next.Backends)
		if err != nil {
			return nil, err
		}

		interval, probeTimeout := next.Health.Values()
		checker := health.New(next.AllBackends(), probeTimeout, health.WithTransitionHook(func(backend string, up bool) {
			if up {
				log.Printf("health: backend %s is UP", backend)
				return
			}
			log.Printf("health: backend %s is DOWN", backend)
		}))

		poolOpts := []proxy.Option{proxy.WithHealth(checker), proxy.WithMetrics(scrape)}
		if len(next.Weights) > 0 {
			weights := make(map[string]int, len(next.Backends))
			for i, w := range next.Weights {
				weights[next.Backends[i]] = w
			}
			smooth, err := balancer.NewSmoothWeighted(weights)
			if err != nil {
				return nil, fmt.Errorf("balancer: %w", err)
			}
			poolOpts = append(poolOpts, proxy.WithWeighted(smooth))
		}

		buildPool := func(poolURLs []*url.URL, label string) *proxy.Handler {
			h, err := proxy.New(poolURLs, &balancer.Balancer{}, poolOpts...)
			if err != nil {
				log.Fatalf("proxy: %s pool: %v", label, err)
			}
			return h
		}

		defaultPool := buildPool(pool, "default")
		var routed http.Handler = defaultPool
		if len(next.Routes) > 0 {
			pools := make(map[string]http.Handler, len(next.Routes))
			for _, r := range next.Routes {
				routePool, err := parsePool(r.Backends)
				if err != nil {
					return nil, fmt.Errorf("route %s: %w", r.Prefix, err)
				}
				pools[r.Prefix] = buildPool(routePool, r.Prefix)
			}
			routed = router.New(defaultPool, pools)
		}

		proxied := http.Handler(routed)
		var cacheLayer *cache.Cache
		if ttl := next.Cache.Value(); ttl > 0 {
			cacheLayer, proxied = cache.NewCache(proxied, ttl)
		}
		if rate, burst := next.RateLimit.Values(); rate > 0 {
			proxied = ratelimit.New(proxied, rate, burst)
		}

		rt := &appRuntime{
			cfg:        next,
			checker:    checker,
			proxy:      proxied,
			cacheLayer: cacheLayer,
		}

		// The checker starts only after every other build step succeeded.
		checkerCtx, cancelChecker := context.WithCancel(ctx)
		checker.Start(checkerCtx, interval)
		rt.stopChecker = cancelChecker
		return rt, nil
	}

	first, err := buildRuntime(cfg)
	if err != nil {
		log.Fatalf("runtime: %v", err)
	}
	var current atomic.Pointer[appRuntime]
	current.Store(first)

	// apply swaps in a new generation after full validation. The proxy
	// listen address and admin credentials cannot change at runtime (they
	// belong to listeners that are already bound) and are rejected with a
	// clear message; everything else — backends, weights, routes, health,
	// cache, rate limiting — rebuilds live.
	apply := func(next *config.Config) error {
		prev := current.Load()
		if next.ListenAddr != prev.cfg.ListenAddr {
			return fmt.Errorf("listen_addr change (%s -> %s) requires a restart", prev.cfg.ListenAddr, next.ListenAddr)
		}
		if next.Admin != prev.cfg.Admin {
			return fmt.Errorf("admin settings changes require a restart")
		}
		rt, err := buildRuntime(next)
		if err != nil {
			return err
		}
		old := current.Swap(rt)
		if old != nil && old.stopChecker != nil {
			old.stopChecker()
		}
		log.Printf("config: applied new generation (%d backend(s), %d route(s))", len(next.Backends), len(next.Routes))
		return nil
	}

	// persist writes the config to disk in the file's own format, keeping a
	// .bak of the previous content, so the file stays the source of truth.
	persist := func(next *config.Config) error {
		var data []byte
		var err error
		lower := strings.ToLower(*cfgPath)
		switch {
		case strings.HasSuffix(lower, ".yaml"), strings.HasSuffix(lower, ".yml"):
			data, err = next.YAML()
		default:
			data, err = next.JSON()
		}
		if err != nil {
			return fmt.Errorf("render config: %w", err)
		}

		old, err := os.ReadFile(*cfgPath)
		if err == nil {
			if err := os.WriteFile(*cfgPath+".bak", old, 0o600); err != nil {
				return fmt.Errorf("write backup: %w", err)
			}
		}
		tmp := *cfgPath + ".tmp"
		if err := os.WriteFile(tmp, data, 0o600); err != nil {
			return fmt.Errorf("write temp config: %w", err)
		}
		if err := os.Rename(tmp, *cfgPath); err != nil {
			return fmt.Errorf("replace config: %w", err)
		}
		return nil
	}

	// The proxy listener keeps one stable handler across generations: it
	// delegates to whichever generation is current. The access ring is a
	// long-lived resident of that stable layer, so history survives reloads.
	ring := logging.NewRing(200)
	accessLog := logging.New(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		current.Load().proxy.ServeHTTP(w, r)
	}), logging.WithRing(ring))

	adminListen, adminUser, adminPass, adminEnabled := cfg.Admin.Values()
	adminStatus := func() admin.Status {
		rt := current.Load()
		requests := scrape.Snapshot()
		statuses := rt.checker.Statuses()
		urls := rt.cfg.AllBackends()
		backends := make([]admin.BackendStatus, 0, len(urls))
		for _, raw := range urls {
			backends = append(backends, admin.BackendStatus{
				URL:      raw,
				Healthy:  statuses[raw],
				Requests: requests[raw],
			})
		}
		rate, burst := rt.cfg.RateLimit.Values()
		return admin.Status{
			Version:     version,
			GoVersion:   runtime.Version(),
			Uptime:      time.Since(startedAt).Round(time.Second).String(),
			Listen:      rt.cfg.ListenAddr,
			AdminListen: adminListen,
			StartedAt:   startedAt,
			Backends:    backends,
			CacheTTL:    rt.cfg.Cache.Value().String(),
			RateLimit:   rate,
			RateBurst:   burst,
		}
	}

	// Optional admin surface on its own listener: never cached, throttled,
	// or routed; dies with the same signal context as the proxy. /api/status
	// requires Basic Auth; the embedded dashboard at / is static and
	// credential-free (it only reads, and prompts for the API credentials).
	if adminEnabled {
		adminAPI := admin.New(adminUser, adminPass, adminStatus,
			admin.WithConfigSource(func() any { return current.Load().cfg }),
			admin.WithLogTail(func(limit int) any { return ring.Tail(limit) }),
			admin.WithApply(func(body []byte) error {
				next, err := config.Parse(body)
				if err != nil {
					return err
				}
				if err := apply(next); err != nil {
					return err
				}
				return persist(next)
			}),
			admin.WithReload(func() error {
				reloaded, err := config.Load(*cfgPath)
				if err != nil {
					return err
				}
				reloaded.ApplyEnvOverride(os.Getenv)
				return apply(reloaded)
			}),
		)
		adminMux := http.NewServeMux()
		adminMux.Handle("/api/status", adminAPI.Handler())
		adminMux.Handle("/api/config", adminAPI.Handler())
		adminMux.Handle("/api/reload", adminAPI.Handler())
		adminMux.Handle("/api/logs", adminAPI.Handler())
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

	// /metrics serves the exposition (shared collector, counters survive
	// reloads); /cache/purge clears whichever generation is current.
	mux := http.NewServeMux()
	mux.Handle("/metrics", scrape.Handler())
	mux.HandleFunc("/cache/purge", func(w http.ResponseWriter, r *http.Request) {
		rt := current.Load()
		if rt.cacheLayer == nil {
			http.Error(w, "cache disabled", http.StatusNotFound)
			return
		}
		rt.cacheLayer.PurgeHandler().ServeHTTP(w, r)
	})
	mux.Handle("/", accessLog)

	// ReadHeaderTimeout guards slowloris-style connections; other timeouts
	// stay at zero because proxied streams may legitimately be long-lived.
	srv := &http.Server{
		Addr:              cfg.ListenAddr,
		Handler:           mux,
		ReadHeaderTimeout: 5 * time.Second,
	}

	errCh := make(chan error, 1)
	go func() {
		log.Printf("proxy: listening on %s over %d backend(s)", cfg.ListenAddr, len(cfg.AllBackends()))
		for i, u := range cfg.AllBackends() {
			log.Printf("proxy: backends[%d] = %s", i, u)
		}
		interval, probeTimeout := cfg.Health.Values()
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
