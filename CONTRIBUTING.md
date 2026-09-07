# Contributing to Kilnbridge

Issues and pull requests are welcome. Keep code, documentation, and UI text in English.
For larger changes, describe the problem in an issue before proposing a new protocol or build recipe.

Use the Go version in `go.mod`. Run `gofmt -w cmd internal`, `go vet ./...`, and
`go test -race ./...`. Windows-specific behavior must also pass on Windows;
cross-compilation alone does not validate DPAPI, process cancellation, or the updater.
The CI workflow runs the Windows executable and an actual Flutter build as well.

Avoid adding an external runtime to the Windows application. Keep the embedded settings
UI usable without internet access, keyboard accessible, and consistent with
[the design notes](docs/design.md). Preview servers should run only during testing and
must be stopped afterwards.

Never commit a real `.env`, signing key, build snapshot, or credential. Use synthetic
fixtures in tests. Update the API documentation when a client-visible field changes.
The agent executes trusted project code; do not describe extraction checks as a sandbox.

Commit titles use one leading emoji and an English imperative, for example:
`✨ Add a build recipe` or `🔨 Fix upload cancellation`. Keep titles within 72 characters.
Explain the reason for a change in the body when it is not obvious.

By contributing, you agree to license your contribution under the MIT license.
