# Releases and background updates

## Publisher setup

The official repository embeds its Ed25519 public key in `internal/update/release.pub`.
The corresponding private key must remain outside Git and be stored as the GitHub
Actions repository secret `UPDATE_SIGNING_KEY` (base64, one line). Keep an offline backup.
Losing this key means existing installations cannot trust new releases.

A fork must generate its own key **before its first distributed build**:

```sh
go run ./cmd/kilnbridge keygen
# Commit internal/update/release.pub, never .private/update.key.
gh secret set UPDATE_SIGNING_KEY < .private/update.key
```

The keygen command refuses to overwrite an existing private key. Do not regenerate
keys routinely. Changing the embedded key breaks the update chain for installations
that trust the old key. Key rotation needs a deliberately staged migration; v1 does
not implement an automatic signing-key rotation protocol. The access-key regeneration
button in settings rotates a completely separate API credential.

## Publish a release

1. Update `version` in `cmd/kilnbridge/main.go` and `docs/release-notes.md`.
2. Run the checks and merge the change to `main`.
3. Push a stable semantic-version tag, for example:

```sh
git tag -a v0.2.0 -m 'Kilnbridge 0.2.0'
git push origin v0.2.0
```

The release workflow reruns CI, including native Windows tests and a real Flutter
build. It cross-compiles Windows x64 and sender binaries for macOS/Linux, signs the
Windows update, and uploads the complete set to a draft GitHub release. The release
becomes public only after all assets are present. Do not reuse published tags.

Required Windows updater assets:

- `kilnbridge-windows-amd64.exe`
- `update.json`
- `update.json.sig`

`update.json` contains the stable version without `v`, exact asset name, file size,
and lowercase SHA-256. `update.json.sig` is a base64 Ed25519 signature of the exact
manifest bytes, including its trailing newline. `SHA256SUMS.txt` covers all distributed
binaries and update metadata; it is a convenience checksum list, not a replacement
for signature verification.

To sign a local release for verification:

```sh
go run ./cmd/kilnbridge sign-release \
  --binary dist/kilnbridge-windows-amd64.exe \
  --version 0.2.0 --out dist
```

The command uses `UPDATE_SIGNING_KEY` or the local `.private/update.key` and refuses a
key that does not match the embedded public key. Treat the signing secret as release
authority: protect repository write access and review workflow changes. Pull request
validation never receives this secret.

## Installation protocol

1. Query the configured repository's latest stable GitHub release. Ignore drafts,
   prereleases, equal versions, and downgrades.
2. Require assets under that repository's GitHub release path. Verify the signed
   metadata against the compiled public key, then hash and size-check the downloaded EXE.
3. Stage it in `updates/VERSION` without modifying the running application.
4. Wait for an empty queue, no running tool setup, and no settings-window heartbeat.
5. Copy the current trusted EXE as a temporary updater helper, stop accepting jobs, and
   shut down the worker normally.
6. The helper waits for the worker to exit, verifies the signature and binary again,
   renames the installed EXE to `.previous`, and copies the new executable into place.
7. Start the replacement in background mode with a random startup nonce. It acknowledges
   its version after opening its listeners. If that does not happen within a minute,
   terminate it, restore `.previous`, and restart the old worker.

The settings window sends heartbeats; the native host also keeps a heartbeat while
it is open, even when WebView2 throttles a hidden page. A normal update does not close
an open settings window or interrupt a build. Current configuration and DPAPI state
stay in the per-user data directory. Diagnostics go to `updates/last-error.txt` when
an installation fails. Updates require no UAC because the EXE is installed per user.

Rollback covers failed startup, not every possible regression after startup. A power
loss during executable replacement may require reopening a freshly downloaded EXE or
restoring `app/kilnbridge.exe.previous`. Keep data formats backwards compatible until
the old executable has been retired. There are no automatic destructive migrations.
The initial Windows download is not Authenticode signed; Ed25519 update signatures do
not suppress Windows SmartScreen for the first installation.
