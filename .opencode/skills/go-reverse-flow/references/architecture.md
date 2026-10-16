# Architecture — go-reverse-flow

Pure Go, stdlib only (module `github.com/dimastriann/go-reverse-flow`,
Go ≥ 1.24). No third-party dependencies anywhere — this is a portfolio piece
showing stdlib fluency, so the answer to "just use a library" is always no.

## Request path

```
client → http.Server (ReadHeaderTimeout only)
       → mux: /metrics → metrics handler
              /  → chain:
                   logging.New        (access log, slog)
                   → ratelimit.New    (per-client token bucket, 429)
                   → cache.New        (TTL response cache, GET/HEAD only)
                   → router.New       (longest-prefix path dispatch)
                   → proxy.Handler    (one per route pool; health-aware pick)
                                        → http.ReverseProxy per backend
```

Chain composition in `cmd/proxy/main.go:104-116`, innermost-first. The shared
`health.Checker` goroutine and `/metrics` sit outside the request chain.

## Packages (one responsibility each)

| Package | File(s) | Responsibility |
|---|---|---|
| `internal/config` | `config.go` | JSON load, `Duration` parsing ("5s" or seconds int), defaults, thorough `Validate()` (weights alignment, schemes, route rules, `AllBackends()`) |
| `internal/balancer` | `balancer.go`, `smooth.go` | Lock-free `atomic.Uint64` round-robin (`Next` panics on n≤0 = programming error); nginx-style smooth weighted RR with mutex-guarded URL-keyed state |
| `internal/proxy` | `proxy.go` | Core routing: one `httputil.ReverseProxy` per backend, health-aware pick over pool ∩ healthy set, 503 when empty, retry-once on dead wire for GET/HEAD guarded by `writeTracker` |
| `internal/health` | `health.go` | Probe goroutine (`select` on ctx.Done/ticker), `RWMutex` state map, `WithTransitionHook`, sorted stable `HealthyOf()` |
| `internal/logging` | `logging.go` | slog access log; inner handlers report chosen backend via `Info` holder in request context |
| `internal/metrics` | `metrics.go` | Hand-written Prometheus text exposition; `atomic.Uint64` counters, healthy gauges from `health.Statuses` (no prometheus client!) |
| `internal/ratelimit` | `ratelimit.go` | Per-client (RemoteAddr host) token bucket; 429 + computed `Retry-After`; injectable clock; lock-free pruning every 64th request |
| `internal/cache` | `cache.go` | TTL cache. Eligibility is deliberately conservative: 2xx only, known Content-Length ≤ 1 MiB, no Set-Cookie, no `Cache-Control: no-store`, no request `Accept-Encoding`. Stamps `X-Cache`; lazy expiry |
| `internal/router` | `router.go` | Longest-prefix, segment-boundary matching (`/api` matches `/api/users`, not `/april`), fallback handler. Deliberately "dumb": pools own health/weights/retries |
| `cmd/proxy` | `main.go` | Wiring only: flags, pool construction, middleware compose, `/metrics` mux, `signal.NotifyContext` graceful shutdown with 10 s drain |

## Extension points (where features usually attach)

- **New balancer policy** → `internal/balancer` (implement a `Pick`-
  compatible method or type), wire via a `proxy.Option` in `main.go`, add a
  config knob in `internal/config` + `Validate()`.
- **New middleware** → new `internal/<name>` package wrapping `http.Handler`,
  composed in `main.go` in the chain position with a comment justifying the
  order (order is behavior: throttled clients never reach the cache).
- **New config knob** → `internal/config` struct + unmarshal + `Validate()`
  test rows + `main.go` wiring.
- **New endpoint** → mount on the mux in `main.go` (only `/metrics` lives
  outside the proxy chain today). Health-style endpoints must decide their
  scope explicitly — the whole checker's set vs one route pool — and take a
  functional option (e.g. a `WithScope`-style `Option`) if scoped; route
  pools answer their own 503s, so a node-level verdict should say which
  traffic it speaks for.

## House idioms (match these or have a reason)

- Configurable components take functional options: `WithHealth`,
  `WithWeighted`, `WithMetrics`, `WithTransitionHook`, `WithClock`.
- Constructors return errors; programming errors panic (documented:
  `Balancer.Next` on n≤0). Sentinel errors at top of file (`errNoBackends…`).
- Errors wrap with `%w` and a contextual prefix, checked with `errors.Is`.
- Time: injectable clock interfaces so tests never sleep.
- Per-request state (attempt tracking, logging backend) lives in the request
  context under empty-struct key types — never package globals.
