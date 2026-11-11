# ADR 0002: Focused testing strategy

- **Status:** Accepted
- **Date:** 2026-11-11

## Context

Tests should give confidence in important behavior without creating a large, repetitive suite that is expensive to understand and maintain. ForYou has security-sensitive boundaries, so compactness cannot come at the cost of meaningful coverage.

## Decision

Write the smallest set of tests that establishes the behavior and risks of the change. Each test must have a clear purpose, independently specified expected results, and a meaningful failure when that behavior is broken. Tests pass when the requirement is met; do not weaken assertions, skip failures, or change expected values solely to obtain a green result. When fixing a defect, use a regression test that detects the defect when practical.

- Cover a representative success case and distinct important failures or boundaries when relevant. Add security, accounting, persistence, concurrency and protocol interoperability cases where the change makes them necessary. Do not duplicate the same check at multiple layers without a distinct reason.
- Prefer Go's standard `testing` package. Parameterize cases that exercise the same behavior with different inputs and expected outputs. Give each case a descriptive name and useful failure messages, and keep cases independent.
- Do not write loops in tests, including loops over parameterized cases. Use explicitly invoked named subtests and a small helper receiving inputs and expected values. Do not add a dependency solely to hide a loop or avoid this rule. Separate tests when setup or assertions differ enough that sharing obscures behavior.
- Assert observable behavior and contracts, not private implementation details. Derive expected values from known examples, independent vectors or the stated contract, not by recomputing them with the production logic being tested.
- Keep tests deterministic, isolated and fast by default. Avoid real funds, external networks, wall-clock sleeps, shared mutable state and order dependence. Use temporary directories and controlled clocks or fakes only when they improve meaningful verification; clean up resources. Do not build elaborate test frameworks or mock what can be tested simply.
- Use unit tests for focused behavior and selective integration or end-to-end tests where boundaries and wiring matter. Use fuzzing, race detection and interoperability vectors when justified by risk. Do not chase an arbitrary coverage percentage or add tests only to increase the count.
- Review and consolidate redundant or obsolete tests when changing related behavior. Retain distinct regression and security coverage; do not delete unrelated tests just to reduce the suite.

For example, each case is explicit while the shared assertion is parameterized:

```go
func TestExample(t *testing.T) {
    check := func(t *testing.T, input, want string) {
        t.Helper()
        if got := Example(input); got != want {
            t.Errorf("Example(%q) = %q; want %q", input, got, want)
        }
    }
    t.Run("first case", func(t *testing.T) { check(t, "input-a", "result-a") })
    t.Run("second case", func(t *testing.T) { check(t, "input-b", "result-b") })
}
```

Apply this decision to new and changed tests; update older tests when the related behavior is next changed. Do not rewrite unrelated tests solely to conform.

## Verification and tooling

Run relevant focused tests during development and `go test ./...` and `go vet ./...` before completion when practical. Use `gofmt` and the existing CI checks. Run race tests where supported for relevant concurrent behavior; Windows ARM64 does not support the Go race detector natively, so report that gap or use an available supported environment. Report failed, skipped or unavailable checks and avoid claiming that passing tests prove the absence of defects.

## Consequences

Tests should remain readable, purposeful and inexpensive to maintain. Explicit cases require more lines than conventional loop-based Go tables, but preserve the no-loop rule and clear failures. Some changes need no new test when existing coverage already proves the behavior; high-risk changes may require more cases and independent evidence. Review tests for meaningful assertions and risk coverage rather than test count.

## References

- [Go: Test comments](https://go.dev/wiki/TestComments)
- [Go: Fuzzing](https://go.dev/doc/security/fuzz/)
