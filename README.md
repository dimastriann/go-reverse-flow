# 🧩 go-reverse-flow

[![CI](https://github.com/dimastriann/go-reverse-flow/actions/workflows/ci.yml/badge.svg)](https://github.com/dimastriann/go-reverse-flow/actions/workflows/ci.yml)
[![Release](https://github.com/dimastriann/go-reverse-flow/actions/workflows/release.yml/badge.svg)](https://github.com/dimastriann/go-reverse-flow/actions/workflows/release.yml)

A lightweight reverse proxy and round-robin load balancer built in pure Go (stdlib only), inspired by tools like Nginx.

This project demonstrates core backend and systems concepts: HTTP routing, concurrency, load balancing, health checking, and observability — grown incrementally through atomic commits.

## ✨ Features

| Status | Feature |
|---|---|
| ✅ v0.1.0 | 🔁 Reverse proxy (forward requests to backend servers) |
| ✅ v0.1.0 | ⚖️ Round-robin load balancing |
| ✅ v0.1.0 | ⚡ Concurrent request handling (goroutines) |
| ✅ v0.1.0 | 🛑 Graceful shutdown (SIGINT/SIGTERM drains in-flight requests) |
| ✅ v0.2.0 | ❤️ Health checks (probing goroutine, skip unhealthy, `503` when all down) |
| ✅ v0.2.0 | 📝 Structured request logging (method, path, backend, status, duration) |
| ✅ v0.3.0 | 📊 `/metrics` endpoint (per-backend counters + healthy gauges, Prometheus-scrapeable) |
| ✅ v0.3.0 | 🐳 Docker support (multi-stage `scratch` image + 3-backend `docker compose up` demo) |
| ✅ v0.3.0 | ⏳ Rate limiting (per-client token bucket, `429` + `Retry-After`) |
| ✅ v0.4.0 | 🔁 Retry-once on dead backend (idempotent requests only, never replays side effects) |
| ✅ v0.4.0 | ⚖️ Smooth weighted round-robin (`backend_weights`, nginx-style interleave) |
| ✅ v0.4.0 | 🗄️ TTL response cache (`X-Cache: hit/miss`, conservative eligibility) |
| ✅ v0.5.0 | 🛤️ Path-prefix routing (`routes`, longest-prefix, per-pool health/weights/retries) |
| ✅ v0.6.0 | 🔒 Cache hardening (credentials/Range bypass, partial-content rejection, bounded buffering + entry count) |
| ✅ v0.6.0 | 🌍 `LISTEN_ADDR` env override (file config stays portable across replicas) |
| ✅ v0.7.0 | 🧹 Cache invalidation endpoint (`POST /cache/purge`, 204 + `X-Purged-Entries`, `405` for other methods) |

## 🏗️ Architecture

```
Client → go-reverse-flow → Backend Servers
                 │
                 └─ health checker (probes every backend every 5s)
```

1. Client sends a request to the proxy (`:8080`)
2. The round-robin pointer walks **only over currently healthy backends**
3. A dead backend is skipped automatically (readers demote it within one probe interval); when *nothing* is up the proxy answers `503` instead of forwarding into the void
4. Every request produces one structured access-log line:

```
INFO request remote_addr=[::1]:51076 method=GET path=/index.html backend=http://localhost:8001 status=200 duration=13.346ms
```

Observability and defense come on the same listener:

- `GET /metrics` — Prometheus-style exposition: `grf_backend_requests_total{backend=...}` counters and `grf_backend_healthy{backend=...}` gauges (from the live checker)
- `POST /cache/purge` — invalidates every cached entry (only mounted while `cache.ttl` is positive); answers `204` with `X-Purged-Entries: <n>` and refuses other methods with `405`, so a stray GET or crawler cannot wipe the cache
- `rate_limiter` config — optional per-client token bucket: over-limit clients get `429` + computed `Retry-After`, still logged; `/metrics` and `/cache/purge` are never throttled

## 🛠️ Tech Stack

- Go 1.27+
- `net/http`, `net/http/httputil` (ReverseProxy), `log/slog` — no third-party deps

## 📦 Project Structure

```
.
├── cmd/proxy/          # Application entry point
├── internal/
│   ├── config/         # JSON config loader, validation, duration parsing
│   ├── balancer/       # Lock-free round-robin + smooth weighted picker
│   ├── proxy/          # httputil.ReverseProxy wiring, health-aware routing,
│   │                   #   retry-once for idempotent requests
│   ├── router/         # Longest-prefix path dispatch across pools
│   ├── health/         # Periodic probing, transitions, transition hooks
│   ├── logging/        # Access-log middleware (backend attribution via ctx)
│   ├── metrics/        # Prometheus-style counters + gauges, /metrics
│   ├── ratelimit/      # Per-client token bucket, 429 + Retry-After
│   └── cache/          # TTL response cache with X-Cache observability
├── config/             # Configuration files (JSON, incl. docker demo)
├── Dockerfile          # Multi-stage build on a scratch runtime image
├── docker-compose.yml  # One-command 3-backend demo stack
└── README.md
```

## ⚙️ Getting Started

### 1. Clone

```
git clone https://github.com/dimastriann/go-reverse-flow.git
cd go-reverse-flow
```

### 2. Start example backends

```
python3 -m http.server 8001
python3 -m http.server 8002
```

### 3. Run the proxy

```
go run ./cmd/proxy
```

Config lives in `config/config.json`:

```json
{
    "listen_addr": ":8080",
    "backends": [
        "http://localhost:3000",
        "http://localhost:4000"
    ],
    "routes": [
        {
            "prefix": "/api",
            "backends": ["http://localhost:5000"]
        }
    ],
    "backend_weights": [3, 1],
    "health": {
        "interval": "5s",
        "timeout": "1s"
    },
    "rate_limiter": {
        "rate": 0,
        "burst": 0
    },
    "cache": {
        "ttl": "0s"
    }
}
```

Knob semantics:

- `LISTEN_ADDR` — environment variable; when set non-empty it overrides the
  file config's `listen_addr`, so the same config file can serve different
  ports per replica. Unset/empty means the file value stands as-is
- `routes` — optional path-prefix routing: requests whose path starts with a
  route prefix go to that pool (longest prefix wins, path forwarded as-is),
  everything else uses the default `backends` pool. Each pool is
  health-aware, weighted, and retry-capable independently; a dead route pool
  answers `503` without touching other pools. Omit `routes` entirely to keep
  the simple single-pool behavior
- `backend_weights` — optional, aligned with `backends`; omitted means plain
  round-robin, `[3, 1]` gives the heavy node three picks out of four,
  interleaved smoothly (`a,a,b,a`, no bursts). Route-pool backends that have
  no configured weight join at weight 1
- `rate_limiter` — disabled when `rate` is 0; a positive `rate` (tokens per
  second, with an optional `burst` ceiling) throttles clients with `429`s
- `cache` — disabled when `ttl` is 0; a positive TTL caches successful
  anonymous, whole-representation responses only (GET/HEAD, no credentials,
  no `Range`, ≤1 MiB, at most 1024 entries) and stamps every proxied
  response with `X-Cache: hit` or `miss`. Responses that outgrow or bypass
  the cache stream or serve verbatim, unstamped

The composed request path is `access log → rate limiter → cache → router →
per-pool proxy`, with `/metrics` and the health checker outside the chain.

### 4. Test

```
curl http://localhost:8080
```

Responses alternate between the backend servers. Kill one backend with Ctrl+C
and the proxy quietly keeps serving through the survivor — bring it back on
the same port and it rejoins rotation automatically.

### One-command Docker demo

```
docker compose up
curl http://localhost:8080/index.html   # alternates across 3 backends
```

The stack runs the proxy plus three backends inside one Docker network, with
service-name DNS resolving the baked backend URLs automatically.

## 🗺️ Roadmap

`v0.1.0` round-robin proxy → `v0.2.0` health checks + logging → `v0.3.0` metrics + Docker + rate limiting → `v0.4.0` retry-once + weighted balancing + TTL cache → `v0.5.0` path-prefix routing → `v0.6.0` cache hardening + env override → `v0.7.0` cache invalidation endpoint → next: least-connections, Grafana dashboard JSON, YAML config / more env overrides.

## 📚 What I'm Learning

- How HTTP reverse proxies work under the hood
- Load balancing strategies (round-robin, smooth weighted, health-aware)
- Go concurrency (goroutines, atomic ops, mutexes, graceful shutdown)
- Path routing (longest-prefix dispatch, pool-scoped state, shared checkers)
- Health-check design (probing, state transitions, hot-path reads)
- Observability plumbing (structured logs vs scrapeable exposition)
- Throttling patterns (token buckets, deterministic clock injection)
- HTTP caching rules (what is safe to store, and why most things are not)
- Safe retries (pre-headers guard, idempotency, never replaying side effects)
- Packaging: Docker multi-stage builds, compose topologies, tag-driven releases

## 📄 License

MIT

## 🙌 Acknowledgements

Inspired by Nginx and real-world backend systems design.
