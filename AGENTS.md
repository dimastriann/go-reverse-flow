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
3. `go test ./...`
4. `go build ./...`

A commit that fails any of these on a clean checkout should not exist.

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
