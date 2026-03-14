# Agent Guidelines

## Core Principles

- **Do NOT maintain backward compatibility** unless explicitly requested. Break things boldly.
- **Keep this file under 20-30 lines of instructions.**

---

## Project Overview

**Project type:** CLI tool — local proxy server (Anthropic Messages API → Kiro backend)
**Primary language:** Go (requires `GOEXPERIMENT=jsonv2`)

---

## Commands

```bash
make build          # Build to dist/kirocc
make test           # go test -race ./...
make lint           # golangci-lint run
make run            # go run ./cmd/kirocc
make test-e2e       # E2E tests with real Kiro API (requires credentials)
```

---

## Code Conventions

- Follow the existing patterns in the codebase
- Prefer explicit over clever
- Delete dead code immediately

---

## Maintenance Notes

**Keep this file lean and current:**

1. **Review regularly** - stale instructions poison the agent's context
2. **CRITICAL: Keep total under 20-30 lines** - move detailed docs to separate files and reference them
3. **Update commands immediately** when workflows change
4. **Rewrite Architecture section** when major architectural changes occur
5. **Delete anything the agent can infer** from your code

**Remember:** Coding agents learn from your actual code. Only document what's truly non-obvious or critically important.
