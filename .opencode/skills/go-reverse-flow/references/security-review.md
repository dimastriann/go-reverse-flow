# HTTP / proxy security review — go-reverse-flow

Security work in this repo means knowing exactly which protections were
deliberately placed and why, so a change doesn't silently undo them (the
tradeoffs are documented in package docs — read them if you touch the
package). This doc is the inventory + checklist.

## Baseline protections — verify a change preserves them

| Protection | Where | Why it matters |
|---|---|---|
| Slowloris guard | `cmd/proxy/main.go` — `http.Server{ReadHeaderTimeout: 5s}` | Headers must arrive within 5 s. All other timeouts are deliberately zero: proxied streams may be long-lived; don't add blanket `WriteTimeout`/`IdleTimeout` without re-examining streaming behavior |
| Hop-by-hop sanitizing | `httputil.NewSingleHostReverseProxy` (stdlib) handles connection-scoped headers and appends X-Forwarded-For | Hand-rolled header handling is a classic hole; any change that bypasses `ReverseProxy` must re-establish this |
| Never replay after headers | `proxy.writeTracker` + `attemptState.final` | Retries are limited to GET/HEAD *before any byte reaches the client*; POST/PUT replays side-effect requests, header-after headers confuses clients/dupes responses |
| Cookie-poisoning blocked | `cache.storeable` — responses with `Set-Cookie` are never stored; only 2xx cached | A shared cache storing per-client cookies would serve one user's session to another |
| Response-uniqueness escape hatches | `cache.eligible` — client `Cache-Control: no-store` respected, `X-Cache-Bypass` honored, `Accept-Encoding` bypasses the store | The single-key scheme (`method + RequestURI`) cannot represent content variants; encoding requests bypass rather than risk serving the wrong flavor |
| Memory-bounded caching | `cache.maxEntryBytes` = 1 MiB, `Content-Length` known and matching, full buffering before store | Unbounded buffering on attacker-sized responses is a memory-exhaustion vector; the length/byte match also defeats chunked-truncation confusion |
| Honest rate limiting | `ratelimit` — per-client token bucket (RemoteAddr host), 429 + computed `Retry-After`, opportunistic in-path pruning | Throttled clients get early, cheap 429s; `Retry-After` prevents thundering herds; no background goroutine to starve |
| No redirect-following | health probes use `http.Client{Timeout}` default, status < 5xx = healthy | Backend can't bounce the checker (no retry loops across systems) |
| Minimal exposed surface | `/metrics` on the mux; everything else proxied; config validation rejects empty/scheme-broken backends | Endpoint list is finite and discoverable from `main.go` alone |

## Security review checklist

1. **New header handling?** Trace hop-by-hop vs end-to-end explicitly;
   never merge header sets you didn't clone (`cache` clones headers on
   store — same rule applies to anything buffering header maps).
2. **New keying scheme (cache/router/ratelimit)?** Enumerate the variants:
   method, encoding, cookie presence, normalized vs raw URI, case.
   A key that's too coarse leaks content across clients; too fine and the
   store turns into a memory DoS channel. Cache keys stick to
   `method + RequestURI` with explicit bypasses for the coarse cases.
3. **Unbounded buffers?** Anything accumulating client- or backend-controlled
   bytes must bound: cache body cap, Content-Length match, rate-limit map
   pruned opaquely. Header maps have a stdlib bound; bodies don't — cap them.
4. **Timing-log hygiene:** access logs must keep status/duration/remote —
   they're the incident forensics path — but should never log request
   bodies, cookies, or full querystrings into the access line.
5. **New endpoint?** Decide *explicitly*: auth? rate limit? method allowlist?
   `/metrics` is unauthenticated by design (ops scraping) but exposes
   backend topology — if a deployment is internet-facing, that's a config
   concern to surface in the report, not to paper over silently.
6. **Client IP source changes?** The limiter keys on `RemoteAddr` deliberately.
   Switching to `X-Forwarded-For` invites spoofing unless a trusted-proxy
   chain is enforced first; surface the tradeoff, don't just swap the key.
7. **Error paths don't leak.** Responses here stay terse (`backend
   unavailable`, `no healthy backend available`); config errors stay in
   startup `log.Fatalf` — detail for operators, not for HTTP responses.
8. **Fuzz the boundaries** with a quick test pass: empty/oversized/
   duplicate/malformed headers against anything new.

## Known accepted limitations (surface in reviews, don't "fix" silently)

- No TLS termination — the proxy serves plaintext; TLS terminates upstream
  or on a fronting LB. Anything else is a design decision, not a patch.
- `X-Forwarded-For` is appended by the stdlib but never filtered/verified —
  same trust model caveat as item 6.
- `/metrics` unauthenticated, revealing backend URLs and health state.
- Cache TTL expiry is lazy — expired entries stay resident until hit or
  overwritten; bounded by the 1 MiB cap and conservative eligibility, but
  worth remembering when sizing deployment memory.
