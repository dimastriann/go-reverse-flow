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
| 📋 planned | 🐳 Docker support |
| 📋 planned | ⏳ Rate limiting |

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

## 🛠️ Tech Stack

- Go 1.27+
- `net/http`, `net/http/httputil` (ReverseProxy), `log/slog` — no third-party deps

## 📦 Project Structure

```
.
├── cmd/proxy/          # Application entry point
├── internal/
│   ├── config/         # JSON config loader, validation, duration parsing
│   ├── balancer/       # Lock-free round-robin picker (atomic counter)
│   ├── proxy/          # httputil.ReverseProxy wiring + health-aware routing
│   ├── health/         # Periodic probing, transitions, transition hooks
│   └── logging/        # Access-log middleware (backend attribution via ctx)
├── config/             # Configuration files (JSON)
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
        "http://localhost:8001",
        "http://localhost:8002"
    ],
    "health": {
        "interval": "5s",
        "timeout": "1s"
    }
}
```

### 4. Test

```
curl http://localhost:8080
```

Responses alternate between the backend servers. Kill one backend with Ctrl+C
and the proxy quietly keeps serving through the survivor — bring it back on
the same port and it rejoins rotation automatically.

## 🗺️ Roadmap

`v0.1.0` reverse proxy + round-robin → `v0.2.0` health checks + access logging → next: `/metrics`, Docker demo, rate limiting.

## 📚 What I'm Learning

- How HTTP reverse proxies work under the hood
- Load balancing strategies
- Go concurrency (goroutines, atomic ops, mutexes, graceful shutdown)
- Health-check design (probing, state transitions, hot-path reads)
- Structuring and shipping a real-world Go project incrementally

## 📄 License

MIT

## 🙌 Acknowledgements

Inspired by Nginx and real-world backend systems design.
