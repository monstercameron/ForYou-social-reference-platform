// Package rank implements the feed gate described in docs/design/feed-gate.md:
// an objective, reader-independent rubric over a verified post and a personal
// model learned only from the reader's own votes. It is pure, with no I/O, clock
// reads, or randomness. Its results are local to the reader and never network
// state, as required by docs/07-clients.md Scope.
package rank
