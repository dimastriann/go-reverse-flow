# 🧩 go-reverse-flow

A lightweight reverse proxy and round-robin load balancer built in pure Go (stdlib only), inspired by tools like Nginx.

This project demonstrates core backend and systems concepts: HTTP routing, concurrency, load balancing, and health checking — grown incrementally through atomic commits.

## ✨ Features

| Status | Feature |
|---|---|
| ✅ v0.1.0 | 🔁 Reverse proxy (forward requests to backend servers) |
| ✅ v0.1.0 | ⚖️ Round-robin load balancing |
| ✅ v0.1.0 | ⚡ Concurrent request handling (goroutines) |
| ✅ v0.1.0 | 🛑 Graceful shutdown (SIGINT/SIGTERM drains in-flight requests) |
| 📋 planned | ❤️ Health checks (skip unhealthy backends) |
| 📋 planned | 📝 Request logging |
| 📋 planned | 🐳 Docker support |

## 🏗️ Architecture

```
Client → go-reverse-flow → Backend Servers
```

1. Client sends a request to the proxy (`:8080`)
2. Proxy selects the next healthy backend (round-robin)
3. Request is forwarded; response is streamed back to the client

## 🛠️ Tech Stack

- Go 1.27+
- `net/http`, `net/http/httputil` (ReverseProxy) — no third-party deps

## 📦 Project Structure

```
.
├── cmd/proxy/        # Application entry point
├── internal/         # Core logic (config, balancer, proxy, health, logging)
├── config/           # Configuration files (JSON)
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

### 4. Test

```
curl http://localhost:8080
```

Responses alternate between the backend servers.

## 🗺️ Roadmap

See the build order in the project roadmap: MVP → health checks + logging → metrics/Docker/rate limiting (backlog).

## 📚 What I'm Learning

- How HTTP reverse proxies work under the hood
- Load balancing strategies
- Go concurrency (goroutines, atomic ops, mutexes, graceful shutdown)
- Structuring and shipping a real-world Go project incrementally

## 📄 License

MIT

## 🙌 Acknowledgements

Inspired by Nginx and real-world backend systems design.
