# ForYou agent instructions

- Follow the user's request, inspect relevant project context, and keep changes focused.
- Favor clear, simple solutions and document meaningful assumptions and limitations.
- Preserve unrelated work. Do not modify or remove existing material outside the requested scope.
- Verify changes with checks appropriate to their risk and report failures or checks that could not run.
- Follow the [Go style decision](docs/adr/0001-go-style.md) when writing or reviewing Go code.
- Follow the [testing strategy](docs/adr/0002-testing-strategy.md) when writing or reviewing tests.
- For client UI work, consult the [design concepts](docs/concepts/), including the light and dark feed references. Treat them as visual direction, not evidence that depicted features or privacy claims exist.
- Treat protocol, key handling and payments as security-sensitive. Never claim that passing tests establishes security or production readiness.
