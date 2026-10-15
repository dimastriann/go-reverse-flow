# Testing & verification — go-reverse-flow

Standard-library `testing` only — no assertion libraries, no testify, no
golden files. Tests are the repo's proof, not decoration: every `feat`
commit ships tests for the concept it introduces, and CI runs the whole
suite with the race detector.

## House test idioms (match these)

- **`t.Helper()` on every fixture.** Helpers like `newBackend`,
  `newHandler`, `writeFixture` (config), `doGet`, `waitHealthy` all
  follow the same shape: name the setup, `t.Helper()`, `t.Fatalf` on
  construction failure, return closers or use `t.Cleanup`/`defer`.
- **httptest everywhere.** Backends are `httptest.NewServer` handlers that
  respond with their name (see `proxy_test.go` `newBackend`); requests are
  `httptest.NewRequest` into `httptest.NewRecorder` for unit-level checks,
  or a real `httptest.Server` + client for round trips.
- **Table-driven when variants exist.** `tests := []struct{ name string;
  … }` + `t.Run(tt.name, …)` (`config_test.go` `TestValidate` rows,
  `health_test.go` `TestProbeClassification`). Plain sequential asserts
  (`t.Fatalf` early, `t.Errorf` accumulate) are fine for single behaviors
  — don't force tables where one case exists.
- **Pin drift bounds exactly.** Concurrency tests hammer with
  `sync.WaitGroup`, tally per-goroutine locally, merge under a mutex, and
  assert exact counts (`TestRoundRobinAlternation`: 3n requests, each
  backend hit exactly n times).
- **Deterministic time.** Anything time-dependent injects a clock:
  `cache.WithClock`, `ratelimit.WithClock`; tests advance a `fakeClock`
  instead of sleeping. Health-driven tests either poll with tight bounds
  (`waitHealthy`: 20 ms polls, 2 s deadline) or use unswept checkers for
  stable retry paths.
- **Error contracts checked with `errors.Is`** against sentinel errors
  (`errNoBackends` and friends), not string matching.
- **Comments explain why.** Tests carry the contract language ("pins the
  contract HealthyOf promises to the proxy's round-robin parity",
  "proof that every replay path ran exactly once"). Copy that register.

## Regression discipline

- Fixing a bug ⇒ add the failing test first (or with the fix), named after
  the behavior, in the same commit.
- Concurrency changes always get a race-covered test; anything touching a
  previously-proven invariant (the `writeTracker` guard before touching
  replay logic, `HealthyOf` ordering before touching routing) re-runs
  the test that proved it.
- Leak checks: a goroutine-lifecycle change (checker sweep, serve loops)
  must come with a test that cancels the context and proves the goroutine
  is gone (channel-close or `WaitGroup` observation), not just that the
  happy path works.
- Live drills recorded in `roadmap.local.md` complement, but never
  replace, test coverage.

## The gate (run before every commit)

```
gofmt -l .                     → empty output
go vet ./...                   → no diagnostics
go test -race -count=1 ./...   → all green  (CI runs this)
go build ./...                 → success
```

- `_count=1` matters: Go caches passing runs, and a stale cache can mask
  a broken build tag or a test-order dependency.
- Hosts without cgo (Windows without a C compiler) can't run `-race`
  locally; run `go test -count=1 ./...` there and state the race
  delegation to CI in the commit body. Never silently skip mentioning it.
- Failing to fix, not skipping: a red gate is a bug report; treat it as
  the first task, not a failing-lint to ignore.
