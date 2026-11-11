# ADR 0001: Adopt Google Go style

- **Status:** Accepted
- **Date:** 2026-11-11

## Context

ForYou needs consistent, readable Go code and a common reference for human and agent reviews. The Go formatter handles formatting but does not define every naming, error-handling, or API-design decision.

## Decision

Use the [Google Go Style Guide](https://google.github.io/styleguide/go/guide) as the project’s Go style baseline. Its [style decisions and best practices](https://google.github.io/styleguide/go/) provide supporting guidance, with the core guide taking precedence if they disagree. Apply `gofmt` to Go code and favor clarity and simplicity in the context of the project.

Repository-specific decisions may document justified exceptions. Apply the guide to new and changed code; do not churn unrelated code merely to enforce style. Correctness, security and explicit project requirements remain essential. Do not introduce a Google-specific library or tooling dependency merely because it appears in an example.

## Consequences

Code reviews have a documented style reference, and both Codex and Claude Code should consult it when creating or reviewing Go code. The existing CI checks formatting, tests and `go vet`; it does not automatically enforce every rule in the guide. Update this decision if the team adopts a different standard.
