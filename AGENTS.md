# ForYou agent instructions

- Follow the user's request, inspect relevant project context, and keep changes focused.
- Favor clear, simple solutions and document meaningful assumptions and limitations.
- Preserve unrelated work. Do not modify or remove existing material outside the requested scope.
- Verify changes with checks appropriate to their risk and report failures or checks that could not run.
- Follow the [Go style decision](docs/adr/0001-go-style.md) when writing or reviewing Go code.
- Follow the [testing strategy](docs/adr/0002-testing-strategy.md) when writing or reviewing tests.
- Build client behavior the HTMLeX way: Go renders pages and fragments, and HTMLeX attributes on the markup do the interaction. Keep state on the server or in the URL. Do not add custom JavaScript for anything HTMLeX, HTML or CSS can do. Script is allowed only for a browser capability HTMLeX lacks, in a few lines with a comment naming that capability, and every interaction keeps a plain link or form fallback.
- Keep every client page on one frame. The page title sits 16px from the content column's left edge and 20px from its top (12px inset on phones), 24px bold, with a 13px muted subtitle 4px below it and 16px before the content. Reuse the shared components (`page-heading`, `feed-heading`, `component-card`, `button`, `segmented-control`) instead of new spacing, sizes or colors, and measure the positions in the DOM against Home before calling a page done.
- For client UI work, consult the [design concepts](docs/concepts/), including the light and dark feed references. Treat them as visual direction, not evidence that depicted features or privacy claims exist.
- Treat protocol, key handling and payments as security-sensitive. Never claim that passing tests establishes security or production readiness.
- Follow the [architecture decision](docs/adr/0003-architecture.md): package boundaries, layering, dev mode and the faux chain. Do not add a package, a dependency or a role without changing that decision first.
- Work from a GitHub issue. Change only the files the issue names as yours. If the issue cannot be done without touching another file, stop and report it instead of editing that file.
- Write self-documenting code. Give exported identifiers a doc comment and explain a non-obvious reason or a specification rule in one line where it matters. Do not narrate what the code does, do not leave commented-out code, and do not add banner or section comments.
- Do not commit generated CSS, binaries, `.local/` or anything under `node_modules/`. Do not commit at all when an issue says the integrator commits.
- Git hooks live in `.githooks` (`git config core.hooksPath .githooks`). Do not bypass them.

## Project context: the specification

This repository is the reference platform for the ForYou protocol. The protocol is defined in a separate repository, the [ForYou specification](https://github.com/monstercameron/ForYou-Social-Ecosystem-Specification), release 1.0. Read the relevant document there before working on protocol, payment, storage or client-verification code. A local checkout, when present, is usually a sibling directory named `ForYou-Social-Ecosystem-Specification`.

- The specification is the source of truth. If this repository's code, README or design concepts disagree with it, follow the specification and report the difference. Do not change protocol behavior here to something the specification does not say; propose a specification change instead.
- Where to look: [overview and goals](https://github.com/monstercameron/ForYou-Social-Ecosystem-Specification/blob/main/docs/01-overview.md), [protocol](https://github.com/monstercameron/ForYou-Social-Ecosystem-Specification/blob/main/docs/02-protocol.md) (signing, IDs, versions, replies, admission, Markdown and media rules), [economics](https://github.com/monstercameron/ForYou-Social-Ecosystem-Specification/blob/main/docs/03-economics.md) (fees, settlement, distribution, retention, vault), [portability](https://github.com/monstercameron/ForYou-Social-Ecosystem-Specification/blob/main/docs/05-portability.md) (public reads, sync), [governance](https://github.com/monstercameron/ForYou-Social-Ecosystem-Specification/blob/main/docs/06-governance.md), [clients](https://github.com/monstercameron/ForYou-Social-Ecosystem-Specification/blob/main/docs/07-clients.md), [API catalog](https://github.com/monstercameron/ForYou-Social-Ecosystem-Specification/blob/main/docs/API-catalog.md).
- Conformance material lives there too: `schemas/launch.schema.json`, `schemas/operations.schema.json`, `tests/security-vectors.json` and the executable model `tests/economics_model.py`. Test protocol code here against those vectors and schemas.

Facts that are easy to get wrong:

- The specification sets the fee, never a provider: 136 millisatoshis or 48,900,000,000 wei per started KiB under fee schedule 1. It is an anti-spam hurdle, not provider revenue.
- Providers settle the whole fee to the ForYou Foundation. The Foundation returns up to half per quarter, scaled by measured quality and proven retention. A committee of independent monitors (at least 5, quorum of more than two-thirds) decides settlement; the Foundation has no vote.
- Reading is free and needs no account. There is no protocol token.
- There is no delete message. An edit is a new signed post whose `supersedes` names the earlier version; a reply is a post whose `reply_to` names its parent.
- Content is `text/plain` or CommonMark 0.31.2 with no extensions. Raw HTML is literal text. Media is never stored: a post has up to 16 external media slots with SHA-256 hashes.
- Providers declare retention of at least one year. The Foundation's Lifetime Vault keeps settled posts with no scheduled expiry.
- The core is storage, admission and retrieval. Feeds, ranking, likes, follows and profiles are application features and are not protocol state.
- This repository is an infrastructure demo with mock data. It implements none of the above yet, so never describe it as conformant.
