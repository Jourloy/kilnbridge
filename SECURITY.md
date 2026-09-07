# Security

Use GitHub's **Report a vulnerability** option in this repository's Security tab for
sensitive reports when available. Otherwise open an issue asking for a private contact
without including exploit details, credentials, or private source code.

Kilnbridge is a trusted build worker, not a sandbox. A valid agent key authorizes
executing project code as the signed-in Windows user. Project scripts, native build
steps, and dependencies can read that user's files and access the network. Use a
separate Windows account or dedicated PC for builds, and only submit trusted projects.

The remote API uses TLS with an explicitly pinned certificate and a random bearer key.
The local settings interface has a separate key and listens only on loopback. The
remote API cannot change local settings or reveal/rotate keys. Both interfaces reject
unexpected browser origins. Put the worker on a private network or VPN; do not expose
port 8743 directly to the public internet. There is no per-project identity, quota, or
multi-tenant isolation in this release.

On Windows, agent secrets and each queued job's MinIO credentials are encrypted with
DPAPI for the current user. These are not protected against code running as that same
user or an administrator. Logs redact the supplied MinIO credential strings; arbitrary
secrets printed by project code cannot be recognized automatically. Completed uploads
remove the encrypted job specification and source files. Upload failures retain the
specification so they can be retried; retention cleanup removes it with the job.

Snapshots omit ignored files, common caches, dotenv files, and common private-key
formats by default. `snapshotIncludes` is an explicit override for exact tracked source
files such as public test fixtures. This filter is not a secret scanner. Review what is
tracked in your repository before submission.

Application updates require an Ed25519 signature from the embedded publisher key,
matching version, SHA-256, and size. GitHub TLS alone cannot authorize a replacement.
The initial download still depends on trusting this repository and its publisher.
Application releases are not Authenticode signed; Windows may show SmartScreen on the
first launch. Update signatures do not grant Windows publisher reputation.

Non-Windows agent mode exists only for development. It stores secrets in permissioned
files without DPAPI and refuses real Windows jobs. macOS/Linux production use is the
sender CLI only.
