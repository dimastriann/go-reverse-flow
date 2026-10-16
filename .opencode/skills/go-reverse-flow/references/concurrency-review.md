# Concurrency review — go-reverse-flow

The request path is hot: every middleware runs concurrently for every
request while the health checker mutates shared state in the background.
This checklist exists because the repo already pays careful attention here;
a casual change can quietly break a guarantee a past phase proved with
race tests.

## Baseline guarantees — verify a change doesn't undo them

| Guarantee | Where | What to check |
|---|---|---|
| Lock-free round-robin on the hot path | `balancer.Balancer` — `atomic.Uint64` counter, `int(Add(1)-1) % n` | New picking logic shouldn't add locks the hot path pays for unless contention data justifies it. `Next` panics on `n <= 0` by design (programming error) — don't convert it to an error-return unless the contract expands |
| URL-keyed weighted state surviving demotions | `balancer.SmoothWeighted` — `sync.Mutex` + `map[string]*cw`; unknown candidates default to weight 1 | State must stay keyed by URL, not slice position. Reads of state maps return copies (`Weights`), never aliased internals |
| Sorted, stable healthy snapshots | `health.Checker.HealthyOf` sorts; `proxy.healthyInPool` keeps the order (stable subsequence) | Anything that feeds the round-robin counter must present a stable order between calls, or the counter's parity drifts |
| Never replay after headers | `proxy.writeTracker` marks `written` in `WriteHeader`/`Write`; `attemptState.final` forces the honest 502 | New retry/replay logic must respect both flags. Headers already gone ⇒ no second attempt, no side-effecting replay ever |
| Request-scoped attempt state | `proxy.attemptState` via `attemptKey{}` (empty-struct context key) in the request context | No package globals for per-request state. Handlers receive state from the caller via ctx, and degrade honestly (502) when called outside the machinery |
| Checker goroutine lifecycle | `health.Checker.Start(ctx, interval)` — `select` on `ctx.Done()` / ticker | Any new background goroutine must be killed by a context, never an implicit forever-loop. Shuttle the server's ctx down (`signal.NotifyContext` in `main`) |
| Hook never runs under the lock | `Checker.onTransition` fires after `mu.Unlock()` in `sweep` | New hook/callback patterns must not invoke callers while holding a guard — reentrant calls deadlock |
| Clock injection for anything time-based | `cache` and `ratelimit` take `WithClock`; health relies on interval+timeout | New timing-sensitive logic needs an injectable clock so tests stay instant and deterministic; tests never `time.Sleep` for correctness |

## Review checklist (work through in order)

1. **Type the shared state.** For each field touched by 2+ goroutines: owned
   by what? `atomic.Uint64`/`atomic.Bool` for single words, `sync.Mutex` for
   compound invariants (smoothRR's map), `sync.RWMutex` where request paths
   are read-mostly (checker map). A mix is fine; an unguarded field isn't.
2. **Copy on read.** Exported accessors return fresh maps (`Statuses`,
   `Weights`) — callers can't race on iteration. New accessors must too.
3. **One-shot state resets in `New`.** All backends start healthy in the
   checker so the first sweep's transitions are authoritative — if you add
   optional state, initialize it in the constructor, not lazily.
4. **Map iteration randomization.** Never feed a balancer pick from an
   unsorted map range; snapshot-and-sort first (this bug class already
   burned `HealthyOf` once — see C10).
5. **Reader/writer layers.** ResponseWriter decorators (`writeTracker`,
   logging's `recordingWriter`) must forward `WriteHeader`, `Write`,
   `Flush`, and `Hijacker`/`CloseNotify` casts as needed — a wrapper
   the proxy passes through must not silently drop interface assertions.
6. **Retry fairness.** Alternate-backend selection excludes the just-failed
   URL and only picks from the pool's healthy set — any new retry path keeps
   that exclusion and the GET/HEAD-only rule.
7. **Sit the test down.** Concurrency features ship with race-covered tests:
   goroutine hammering (`sync.WaitGroup`), per-goroutine local tallies merged
   under a mutex, exact distribution bounds (`TestNextConcurrentDistribution`,
   `TestConcurrentRequests`, `TestSmoothConcurrency` are the reference
   style). A new concurrent behavior without such a test is an incomplete PR.
8. **Run what CI runs.** `go test -race -count=1 ./...` on a machine with
   cgo; if the local host can't (Windows without gcc), say so in the commit
   body and rely on the ubuntu runner — but never skip mentioning it.
9. **Candidate-set contracts of the pickers.** Both pickers carry implicit
   input contracts any caller must preserve — enumerate them in reviews:
   `SmoothWeighted.Pick` assumes *distinct* candidate URLs (a duplicate
   double-bumps its `current` while `total` is added twice but subtracted
   once — state stays consistent, shares skew); unknown URLs enter the
   state map permanently as weight-1 ghosts (bounded in this repo because
   candidates come from config-cardinal pools, but unbounded for a library
   caller feeding arbitrary URLs); weights are `int` with no magnitude cap,
   so absurd configs can overflow the sharing arithmetic (defined wrap, no
   panic). Current callers derive candidates from `HealthyOf`-derived,
   deduplicated pool slices, so the contracts hold by construction — when
   you add a new caller, re-prove it, or pin the contract in a doc comment
   and a test instead of relying on reachability.
10. **Single-word state wants atomics; compound invariants want locks.**
    A counter advanced by one `Add` (`balancer.Balancer`) is lock-free; the
    moment two fields or a map+field must change together
    (`SmoothWeighted.Pick`) a mutex is the correct tool — removing a lock
    because "the parts are already atomic" is a data race (e.g. a metrics
    map's lazy counter insertion vs its reads; see the metrics pattern:
    lock the map, not the counters).
