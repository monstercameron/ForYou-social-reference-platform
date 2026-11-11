---
name: go-code-review
description: Review Go changes or a Go repository for actionable correctness, security, concurrency, and maintainability issues using Go tools. Use when asked to review code or a pull request, not for routine implementation.
---

# Go code review

Review the requested scope without changing application code. Identify the comparison base when reviewing changes; inspect staged, unstaged and untracked files as appropriate. If there is no base commit or the scope is unclear, inspect the requested files and state the limitation. Follow project instructions and the Go style decision in `docs/adr/0001-go-style.md`.

Use `go test ./...` and `go vet ./...` when practical; run focused tests for the affected behavior and inspect `gofmt -l` without rewriting files. For concurrent paths, use `go test -race ./...` when supported and useful. The Go race detector is not supported natively on Windows ARM64; report the gap or use an available supported environment. If already installed, use `govulncheck ./...` for relevant dependency or security reviews, and `staticcheck ./...` for additional static analysis. Do not silently install tools or alter dependencies; report missing tools and checks that could not run. Adapt checks to the scope and environment.

Trace changed behavior through callers and tests. Prioritize concrete failure modes: error handling, cancellation and timeouts, goroutine lifecycle, shared state, input bounds, HTTP and HTML safety, authorization, cryptographic verification, persistence and transaction boundaries. For ForYou-specific code, examine exact signed bytes, key custody, trust boundaries, idempotency, payment accounting, and untrusted media or links when touched. Treat tool output as leads and verify the actual impact before reporting.

Report actionable findings first, ordered by severity, each with a precise file and line, trigger, consequence, and suggested direction. Distinguish confirmed issues from assumptions, and do not turn style preferences into defects without an agreed standard or practical impact. Summarize checks run and relevant gaps; say explicitly when no actionable findings were found. Do not claim a clean review proves security.
