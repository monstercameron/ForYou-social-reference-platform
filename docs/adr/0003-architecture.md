# ADR 0003: Reference platform architecture and dev network

- **Status:** Accepted
- **Date:** 2026-11-11

## Context

The platform must express every feature of [specification release 1.0](https://github.com/monstercameron/ForYou-Social-Ecosystem-Specification) in running code, built by many contributors working in parallel. That needs fixed package boundaries, one owner per file, and a way to run the whole network on one machine without real money.

## Decision

### The specification is the contract

Wire objects, field names, limits, status codes and error names come from the specification documents and its two JSON schemas (`schemas/launch.schema.json`, `schemas/operations.schema.json`). Protocol code is tested against the specification's vectors and examples (`tests/security-vectors.json`, `tests/strict-ed25519-vectors.json`, `examples/*.json`). A local checkout is expected at `../ForYou-Social-Ecosystem-Specification`. Vectors a test needs are copied into the package's `testdata/` with a note of their source path.

### Packages

Lower layers never import higher ones. `internal/wire` holds data only; rules live in the package that owns them.

| Layer | Package | Owns |
| --- | --- | --- |
| 0 | `internal/canon` | RFC 8785 canonical JSON |
| 0 | `internal/keys` | Strict Ed25519, `did:key`, the signing input `P(label, obj)`, signature encoding |
| 0 | `internal/wire` | Go types for every wire object in the two schemas, with JSON tags and structural validation |
| 0 | `internal/apierr` | The closed `PublicReadError` body and the status catalog |
| 1 | `internal/message` | Intent, `message_id`, post validation, size budget, replies, versions, fork selection |
| 1 | `internal/content` | CommonMark parsing without extensions, link, reference and media plans, external URL rules, safe HTML output |
| 1 | `internal/fee` | Fee schedules, release approvals, fee calculation, quotes |
| 1 | `internal/settlement` | Leaves, the Merkle sum tree, claims, the link rule, witness, refusal and hold objects, proof verification, receipt states |
| 1 | `internal/governance` | Release key sets and updates, Foundation key certificates and closures, the Foundation log, the genesis manifest |
| 1 | `internal/retention` | Retention declarations and `retain_until` |
| 1 | `internal/succession` | `KeySuccession` |
| 1 | `internal/delegation` | Delegations, authorization epochs, revocations, service challenges and request authentication |
| 1 | `internal/discovery` | Listing filters, cursors, `DiscoveryPage`, `ChangesPage` |
| 1 | `internal/quality` | Quality reports and the reference score |
| 1 | `internal/distribution` | Distribution policy and the quarterly statement calculation |
| 1 | `internal/faux` | The faux chain: ledger, HTTP API and Go client |
| 1 | `internal/names` | The optional display names profile: `NameRecord`, name rules, current-record selection |
| 1 | `internal/store` | Storage interfaces and the in-memory implementation with a JSON journal |
| 2 | `internal/devnet` | Deterministic dev identities and the dev genesis |
| 2 | `internal/funding` | Deposit invoices, credits, balances, custody cap, withdrawals |
| 2 | `internal/committee` | One monitor's settlement logic: audit, one witness per sequence, catching up, following, handover and holds, equivocation |
| 3 | `internal/provider` | The provider HTTP service |
| 3 | `internal/foundation` | The Foundation and Lifetime Vault HTTP service |
| 3 | `internal/monitor` | The committee HTTP service and its scheduler |
| 3 | `internal/replica` | Sync from a provider and public reads |
| 3 | `internal/sdk` | Go client for every service, with the full verification a client must do |
| 4 | `internal/server` | The client web application |
| any | `internal/transport`, `cmd/foryou` | Listeners and the role runner |
| any | `cmd/foryou-seed` | Publishes demo content to a running dev network through the real flow |

### Roles and ports

One binary runs one role per process: `client` 8443, `foundation` 8444 (also the vault), `provider` 8445, `replica` 8446, `monitor` 8447, `faux` 8448. `-role devnet` runs all six in one process for local work. The `monitor` process hosts the whole dev committee; each member is served under `/monitors/{n}/`.

### Dev mode

Dev mode is the only supported mode today. It is on when `FORYOU_DEV=1`, and `devnet` implies it.

- **Faux payments.** The faux chain stands in for Bitcoin and Ethereum. It accepts every transfer anyone submits, with no authentication and no real value, and confirms it at once. Everything downstream is the production flow: deposit invoices, observed credits, balances, quotes, atomic debit, receipts, settlement deposits with their commitment, claims, committee witnesses, proofs, distribution and vault delivery. The faux chain is the only component that bypasses reality.
- **Deterministic identities.** `internal/devnet` derives every dev key (five release keys, the Foundation key, five monitor keys, the provider key, the replica key) from fixed labels, and builds the dev genesis from them. Every role therefore computes the same trust root with no shared files. Dev keys are public knowledge and worthless.
- **Transport.** In dev mode a role serves plain HTTP on a loopback address and refuses any other address, because browsers and tools reject a self-signed certificate. Roles call each other over loopback. Outside dev mode TLS stays mandatory.
- **Author keys.** The client holds a dev author key per browser profile on the server, under `.local/`. This is dev custody only and the interface says so.
- **Storage.** State is held in memory and journaled as JSON under `.local/data/{role}/`, so a restart keeps it. PostgreSQL remains a readiness check only.

Dev mode never weakens verification. Signatures, IDs, fees, proofs and limits are checked exactly as the specification says.

### Parallel work

Work is split into GitHub issues grouped in waves. An issue depends only on earlier waves, names the files it owns, and touches no others. Shared wiring files (`cmd/foryou/main.go`, each service's route table) are changed only by the issue that owns them or by the integrator. Generated CSS (`internal/server/static/tailwind.css`) is rebuilt by the integrator, not committed by feature work.

## Consequences

The platform can run a complete network on one machine and exercise the production flow without funds. It is not a production deployment: custody, real chains, PostgreSQL storage, and operations hardening are later work. Package boundaries are fixed; changing one needs an update to this decision.
