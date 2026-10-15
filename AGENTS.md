# AGENTS.md — Contributor & Agent Guidelines

Guidance for any human or AI agent working in this repository. These rules apply
to every commit, regardless of size.

## Commit conventions

- Use a clear **Conventional Commit** subject, kept concise and specific
  (format: `<type>(<optional scope>): <subject>`; types: `feat`, `fix`, `test`,
  `docs`, `chore`, `refactor`).
- Add a short commit **body** that explains the user-visible or architectural
  change **and** the relevant verification performed.
- Review the **staged diff** before committing (`git diff --staged`) and keep
  unrelated changes out of the commit.
- Do **not** amend, squash, rebase, or rewrite an existing commit unless the
  user explicitly requests it.

## Pre-commit checklist (every commit)

Run and pass all of the following before `git commit`:

1. `gofmt -l .` — must print nothing (fix formatting first)
2. `go vet ./...`
3. `go test -race -count=1 ./...` — matches the CI configuration
4. `go build ./...`

A commit that fails any of these on a clean checkout should not exist.

## Code standards

Rules that keep the codebase small, safe, and reviewable — each one is
verifiable, so treat a violation as a bug:

- **Stdlib only.** No third-party imports in any phase unless the maintainer
  explicitly opens the door; `go.mod` must stay dependency-free.
- **Idiomatic edge-first Go.** Constructors return errors; programming errors
  panic (documented at the call site); errors wrap with `%w` and a contextual
  prefix, checked with `errors.Is`.
- **No package-global mutable state.** Per-request state travels in the
  request context under empty-struct key types; per-process state lives on
  structs (`atomic.Uint64`, `sync.RWMutex` map guards) owned by the type.
- **Configurable components take functional options** (`WithXxx`), and
  behavior that depends on time depends on an injected clock — tests never
  sleep.
- **Safe by construction.** Concurrency changes must pass `go test -race`,
  never replay responses after headers are written, and keep goroutine
  lifecycles bounded by a context.
- **One responsibility per package file**, with a package doc comment that
  explains the design tradeoff, not just the mechanics.

## Atomic commits

- 1 commit = 1 small concept: the code together with its tests/docs.
- Commit as soon as a coherent slice is green — do not batch unrelated changes.
- If unrelated changes are discovered mid-work, set them aside (e.g. `git stash`
  or a separate commit) rather than blending them in.
- Commit messages describe *this* slice, not the whole day's work.

## Project-specific notes

- Pure Go, stdlib only — no third-party dependencies in Phase 0–2.
- `roadmap.local.md` is a local planning file and must never be committed
  (kept out via `.gitignore`).
- Milestones are tagged (`v0.1.0`, `v0.2.0`, ...) after verified working states.
- Agents using Codex or other tools: the project skill lives at
  `.opencode/skills/go-reverse-flow/SKILL.md` (Claude Code gets a pointer
  stub under `.claude/skills/`). Consult it before working on any package.
