// Command proxy runs the go-reverse-flow reverse proxy: it loads the JSON
// config, builds the round-robin proxy handler, and serves until Ctrl+C.
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
	"syscall"
	"time"

	"github.com/dimastriann/go-reverse-flow/internal/balancer"
	"github.com/dimastriann/go-reverse-flow/internal/config"
	"github.com/dimastriann/go-reverse-flow/internal/proxy"
)

// defaultConfigPath is used when -config is not given. Running `go run ./cmd/proxy`
// from the repo root uses ./config/config.json.
const defaultConfigPath = "config/config.json"

// shutdownTimeout bounds how long in-flight requests may finish after Ctrl+C.
const shutdownTimeout = 10 * time.Second

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

	handler, err := proxy.New(backends, &balancer.Balancer{})
	if err != nil {
		log.Fatalf("proxy: %v", err)
	}

	// ReadHeaderTimeout guards slowloris-style connections; other timeouts
	// stay at zero because proxied streams may legitimately be long-lived.
	srv := &http.Server{
		Addr:              cfg.ListenAddr,
		Handler:           handler,
		ReadHeaderTimeout: 5 * time.Second,
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	errCh := make(chan error, 1)
	go func() {
		log.Printf("proxy: listening on %s over %d backend(s)", cfg.ListenAddr, len(backends))
		for i, u := range backends {
			log.Printf("proxy: backends[%d] = %s", i, u)
		}
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
		fmt.Fprintln(os.Stderr, "shutting down: waiting for in-flight requests…")
		shCtx, cancel := context.WithTimeout(context.Background(), shutdownTimeout)
		defer cancel()
		if err := srv.Shutdown(shCtx); err != nil {
			log.Printf("graceful shutdown: %v", err)
		}
	}
}
