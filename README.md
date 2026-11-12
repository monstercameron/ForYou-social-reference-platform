# ForYou reference platform

[![CI](https://github.com/monstercameron/ForYou-social-reference-platform/actions/workflows/ci.yml/badge.svg)](https://github.com/monstercameron/ForYou-social-reference-platform/actions/workflows/ci.yml)
[![License: MIT](https://img.shields.io/badge/license-MIT-blue.svg)](LICENSE)
[![Specification 1.0](https://img.shields.io/badge/specification-1.0-3b3fe0.svg)](https://github.com/monstercameron/ForYou-Social-Ecosystem-Specification)

**The social network nobody owns.** [Project site](https://monstercameron.github.io/ForYou-social-reference-platform/) · [Specification](https://github.com/monstercameron/ForYou-Social-Ecosystem-Specification)

ForYou is an open protocol for a social network built on public, signed text posts. Authors hold their own signing key and choose where to publish. Anyone can read for free, and any application can verify the records. Posting costs about US$0.0001 per KiB, a fee set by the specification as an anti-spam hurdle and not by any provider.

This repository is the Go reference platform for release 1.0 of that protocol: one binary that runs every role (provider, replica, committee monitor, Foundation and client) and a server-rendered client application.

> **Development network only.** The platform runs the real protocol flow, but payments use a built-in faux chain that accepts every transfer, the dev keys are derived from a public seed, and the client stores your signing key unencrypted on its server. It has had no independent security review. Do not use real funds or real keys with it.

## What works

Each row is implemented and tested. The [specification](https://github.com/monstercameron/ForYou-Social-Ecosystem-Specification) is the source of truth; where the code disagrees with it, the code is wrong.

| Area | Specification | Packages |
| --- | --- | --- |
| Canonical JSON, strict Ed25519, `did:key`, signed messages, versions and replies | [Protocol](https://github.com/monstercameron/ForYou-Social-Ecosystem-Specification/blob/main/docs/02-protocol.md) | `canon`, `keys`, `message`, `content`, `wire` |
| Fee schedule 1, quotes, receipts, prepaid balances, deposits, withdrawals, signed account history | [Protocol](https://github.com/monstercameron/ForYou-Social-Ecosystem-Specification/blob/main/docs/02-protocol.md), [Economics](https://github.com/monstercameron/ForYou-Social-Ecosystem-Specification/blob/main/docs/03-economics.md) | `fee`, `funding`, `provider` |
| Settlement claims, committee witnesses and quorum, equivocation evidence | [Economics](https://github.com/monstercameron/ForYou-Social-Ecosystem-Specification/blob/main/docs/03-economics.md) | `settlement`, `committee`, `monitor` |
| Retention declarations, Lifetime Vault delivery and reads | [Economics](https://github.com/monstercameron/ForYou-Social-Ecosystem-Specification/blob/main/docs/03-economics.md) | `retention`, `foundation` |
| Quality probes and reports with served evidence, quarterly distribution, monitor disclosures | [Economics](https://github.com/monstercameron/ForYou-Social-Ecosystem-Specification/blob/main/docs/03-economics.md), [Quality](https://github.com/monstercameron/ForYou-Social-Ecosystem-Specification/blob/main/docs/09-quality.md) | `quality`, `distribution`, `monitor`, `foundation` |
| Notices, rights requests, appeals, restrictions, recovery checkpoints | [Compliance](https://github.com/monstercameron/ForYou-Social-Ecosystem-Specification/blob/main/docs/04-compliance.md) | `provider` |
| Public listings, incremental sync, mirrors, owner export, delegation | [Portability](https://github.com/monstercameron/ForYou-Social-Ecosystem-Specification/blob/main/docs/05-portability.md) | `discovery`, `replica`, `delegation` |
| Genesis, release keys, Foundation certificates, key succession | [Governance](https://github.com/monstercameron/ForYou-Social-Ecosystem-Specification/blob/main/docs/06-governance.md) | `governance`, `succession` |
| Display names | [Display names profile](https://github.com/monstercameron/ForYou-Social-Ecosystem-Specification/blob/main/docs/12-display-names-profile.md) | `names` |
| Verifying client library and the client application | [Clients](https://github.com/monstercameron/ForYou-Social-Ecosystem-Specification/blob/main/docs/07-clients.md) | `sdk`, `server` |
| Routes and the closed error bodies | [API catalog](https://github.com/monstercameron/ForYou-Social-Ecosystem-Specification/blob/main/docs/API-catalog.md) | `apierr` and each role |

The wire types are checked against the specification's JSON schemas, which are vendored under `internal/wire/schemas`.

### Known limits

- There is no adapter for a real chain. The faux chain does not simulate a reorganization, so a credited deposit is never frozen or reversed.
- Distribution statements and vault status follow real calendar quarters. On a fresh dev network the Ledger shows none yet; the end-to-end test in `cmd/foryou` moves the clock to cover that flow.
- The client renders no author avatars from the network.
- Only dev mode is exercised. Without `FORYOU_DEV` a role serves HTTPS and HTTP/3 with the certificate named by `FORYOU_TLS_CERT` and `FORYOU_TLS_KEY`, but that is not a supported deployment.

## Run the dev network

Requirements: Go 1.27 or later. Node.js is needed only to rebuild the stylesheet.

```sh
FORYOU_DEV=1 go run ./cmd/foryou -role devnet
```

This starts every role in one process on loopback and logs each address. Open the client at <http://127.0.0.1:8443>.

To fill the network with sample posts, run the seeder in a second terminal. It publishes through the real flow: it creates keys, funds balances on the faux chain, publishes, and waits for the committee to settle the claims.

```sh
FORYOU_DEV=1 go run ./cmd/foryou-seed
```

A first walk through the client:

1. **Identity**: create your key and confirm the three warnings.
2. **Wallet**: read the custody disclosure, request an invoice and pay it with the faux chain. The balance is credited once the provider signs the credit, and the account history lists it.
3. **New post**: write, get a quote for the exact bytes, confirm and publish. The receipt appears with the fee.
4. **Home**: the post is there, marked pending until the committee settles its claim. Reply, like, save and embed work in place.
5. **Ledger**: follow the fee of a post through its receipt, claim, witnesses and vault receipt, and read the fee schedules the release keys signed.
6. **Network**: the trust root, the committee, and the providers and monitors this client reads.
7. **Settings, then Your data**: everything the client keeps about you, with download and erase.

### Roles one at a time

Each role also runs as its own process:

```sh
FORYOU_DEV=1 go run ./cmd/foryou -role provider
```

Roles are `client`, `foundation`, `provider`, `replica`, `monitor` and `faux`. Configuration is by environment, and [.env.example](.env.example) lists the variables. The defaults are loopback ports 8443 to 8448 and data under `.local/data`.

## How it is built

- **One module, small packages.** Protocol rules live in packages with no HTTP in them, and each role is a thin service over those packages. See the [architecture decision](docs/adr/0003-architecture.md).
- **The client is server-rendered.** Go renders pages and fragments. [HTMLeX](https://github.com/monstercameron/HTMLeX), vendored under `internal/server/static/htmlex`, swaps fragments in place, and forms fall back to plain page loads without script.
- **The client verifies.** It reads through `internal/sdk`, which checks signatures, receipts, settlement proofs and the trust root before anything is shown as verified.
- **State is files** under each role's data directory. There is no database.

```text
cmd/foryou/           The role binary and the one-process dev network
cmd/foryou-seed/      Publishes sample posts through the real flow
cmd/devcert/          Local certificate generator
internal/<package>/   Protocol packages, the roles, the SDK and the client (server)
docs/adr/             Engineering decisions
docs/                 Project site and design concepts
```

## Develop

```sh
go build ./...
go test ./...
go vet ./...
```

Enable the hooks once per clone:

```sh
git config core.hooksPath .githooks
```

The pre-commit hook formats, vets and tests the packages you staged. The pre-push hook does the same for the whole module. CI runs the suite again with the race detector.

After changing a template or a file under `internal/server/styles`, rebuild the stylesheet. The generated file is committed and embedded in the binary.

```sh
npm ci
npm run build:css
```

Follow the [Go style decision](docs/adr/0001-go-style.md) and the [testing strategy](docs/adr/0002-testing-strategy.md): named subtests with literal expected values, no loops over cases, and comments only where the code cannot say it.

## Contributing

Use [issues](https://github.com/monstercameron/ForYou-social-reference-platform/issues) and pull requests. Questions about the protocol belong in the [specification repository](https://github.com/monstercameron/ForYou-Social-Ecosystem-Specification/issues).

## License

[MIT](LICENSE). The specification is MIT licensed as well.
