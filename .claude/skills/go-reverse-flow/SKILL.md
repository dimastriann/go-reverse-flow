---
name: go-reverse-flow
description: "go-reverse-flow repository workflow for github.com/dimastriann/go-reverse-flow — pure-Go reverse proxy. Use when any agent task touches this repo: adding a feature (balancer, cache, ratelimit, router, health, metrics, proxy, config), refactoring, reviewing concurrency changes (goroutines, atomics, mutexes), reviewing HTTP/proxy security, writing/running tests, or preparing commits. Enforces the repo's stdlib-only rule, atomic Conventional Commits, and the strict gate: gofmt, vet, test -race, build before anything lands."
license: MIT
---

# go-reverse-flow — pointer stub

The canonical skill lives one directory level up in the repo:

Read and follow `.opencode/skills/go-reverse-flow/SKILL.md` (same repo,
path relative to the repo root), including whatever reference files under
`.opencode/skills/go-reverse-flow/references/` it points to for the task
at hand.

This stub exists so Claude Code (which only scans `.claude/skills/`)
discovers the skill; opencode reads the canonical copy directly. Keep this
file a pure pointer — the SKILL.md content is maintained in exactly one
place to prevent drift.
