---
name: go-reverse-flow
description: "go-reverse-flow repository workflow for github.com/dimastriann/go-reverse-flow — pure-Go reverse proxy. Use when any agent task touches this repo: adding a feature (balancer, cache, ratelimit, router, health, metrics, proxy, config), refactoring, reviewing concurrency changes (goroutines, atomics, mutexes), reviewing HTTP/proxy security, writing/running tests, or preparing commits. Enforces the repo's stdlib-only rule, atomic Conventional Commits, and the strict gate: gofmt, vet, test -race, build before anything lands."
license: MIT
---

# go-reverse-flow — repository workflow

A lightweight reverse proxy + load balancer in pure Go, stdlib only.
Every task in this repo follows the same path:

**task → repo conventions → package work → strict verification gate**

`AGENTS.md` at the repo root is binding for every task. The package docs and
file header comments explain the design tradeoffs — read the package you are
touching before editing it.

## Orientation (every task, first)

1. Read `AGENTS.md` (conventions, commit rules) and the target package.
2. Run `git log --oneline -5` to see the current slice of work and message style.
3. `roadmap.local.md` records plans and progress, but it is **gitignored —
   never commit it**.
4. Architecture map (what talks to what, and extension points):
   read `references/architecture.md`. It tells you which package owns which
   responsibility so changes land in the right place.

## Task routing

| Task type | Reference to load |
|---|---|
| Feature development | `references/architecture.md` + `references/testing.md` |
| Concurrency change or review | `references/concurrency-review.md` |
| HTTP / proxy security work | `references/security-review.md` |
| Tests & verification only | `references/testing.md` |

For coupled changes (e.g. a new balancer option = feat + review + tests), load
what applies; when in doubt, read both the feature and the relevant review
reference — a few hundred lines is cheaper than a wrong guard on a shared state
map.

## 1. Feature development

Work inside the package's single responsibility — one file per concern, one
behavior per commit.

1. Find the owning package via the architecture reference; if the change
   spans wiring, add it in `cmd/proxy/main.go` and keep packages blind to it.
2. Follow in-repo idioms: functional `Option` pattern for configurables,
   wrapped errors (`fmt.Errorf("...: %w", err)`), `http.Handler` middleware
   wrapping, context-cancelled goroutines, injectable clocks for time.
3. Write the test alongside the code (see `references/testing.md` for the
   repo's test idioms: table-driven variants, `t.Helper()`, httptest servers,
   race-covered concurrency tests).
4. No third-party dependencies, period — if a design needs one, stop and
   propose a stdlib approach instead.

## 2. Concurrency review

Any change touching shared state, goroutines, channels, or the request hot
path gets the concurrency checklist in
`references/concurrency-review.md`. The repo already pays careful attention
here: a lock-free atomic round-robin counter, `RWMutex`-guarded checker state,
per-attempt state in the request context, and a `writeTracker` that forbids
replaying headers already sent. Changes must not weaken any of these; if they
touch the same code, the checklist tells you exactly what to verify.

## 3. HTTP / proxy security

Security-relevant work (headers, timeouts, caching eligibility, rate limiting,
new endpoints) gets `references/security-review.md`. It lists what this proxy
already deliberately does (ReadHeaderTimeout as slowloris guard, no caching of
Set-Cookie responses, retry replay protection) so a change doesn't
accidentally undo what a past phase put in place — and it draws the
surface-vs-fix boundary explicitly: documented *taste* tradeoffs are surfaced
in the report, but an actual exploitable bug is fixed now with a regression
test in an atomic `fix(scope)` commit, even when the root cause is a
documented rule.

## 4. Test & verification (always, no exceptions)

Before finishing any task, run and pass the full gate in order:

```
gofmt -l .                    → empty output
go vet ./...                  → no diagnostics
go test -race -count=1 ./...  → all green
go build ./...                → success
```

This mirrors `.github/workflows/ci.yml` — a task is not done when the agent
says so, it is done when CI would pass. On hosts without cgo (Windows without
a C compiler) run the suite without `-race` and let the ubuntu runner enforce
it — but say so in the commit body. Details in `references/testing.md`.

Then:

5. `git diff --staged` — review your staged work; keep unrelated changes out.
6. Commit atomic (one concept + its tests) with a
   Conventional Commit subject (`feat|fix|test|docs|chore|refactor(scope): …`)
   and a short body explaining the user-visible change and the verification
   performed. Never amend/squash/rebase unless explicitly asked.
7. Test conventions live in `references/testing.md` — write tests in the
   shape CI and existing reviewers expect.

## Report format

End every task with a short status:

```
## Summary
- What changed and why (user-visible behavior first)
- Files touched (paths)
- Gate: gofmt OK | vet OK | test -race OK | build OK
- Proposed commit subject/body (or the commit you made)
- Anything deferred (and where it's tracked, e.g. roadmap.local.md backlog)
```

Keep round-trips to the user focused on decisions, not narration. Rules live
in `AGENTS.md`; this skill tells you how to apply them package by package.
