# Remote API v1

The default endpoint is `https://WINDOWS_PC:8743`. Every request, including health,
requires `Authorization: Bearer AGENT_KEY`. Verify the SHA-256 of the peer's DER leaf
certificate against the fingerprint from Connection **during the TLS handshake,
before sending HTTP headers or files**. Do not disable TLS verification without this
replacement check. The official sender implements this in `internal/client`.

The remote API rejects browser `Origin` headers, follows no redirects, exposes no
settings routes, and returns JSON errors of the form `{"error":"message"}`. It is
intended for scripts on a trusted LAN or VPN.

## Submit an archive

`POST /v1/jobs` takes `multipart/form-data` with exactly two ordered parts:

1. `spec`: UTF-8 JSON, at most 64 KiB.
2. `archive`: a ZIP file, at most 2 GiB.

Example metadata (replace all values with those for the actual snapshot):

```json
{
  "project": "my-app",
  "workDir": ".",
  "flutterVersion": "3.41.6",
  "version": "1.2.3",
  "buildNumber": "45",
  "sourceSha": "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
  "sourceDirty": false,
  "archiveSha256": "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb",
  "apiUrl": "https://api.example.com",
  "skipCodegen": true,
  "skipTests": false,
  "storage": {
    "endpoint": "https://minio.example.com",
    "bucket": "releases",
    "region": "us-east-1",
    "prefix": "release-candidates",
    "accessKey": "YOUR_ACCESS_KEY",
    "secretKey": "YOUR_SECRET_KEY"
  }
}
```

`archiveSha256` is the SHA-256 of the exact ZIP bytes. `sourceSha` is the local source
commit; it is metadata supplied by the trusted caller, not proof of a clean checkout.
The sender computes it locally and records dirty snapshots explicitly. Optional
`storage.sessionToken` supports temporary S3 credentials.

A caller can create and transfer its own ZIP without Git on either machine. The source
must include the files required by the selected recipe, preparation scripts, tests,
and required assets. Flutter needs `pubspec.yaml`, `pubspec.lock` and its Windows project. No credentials or source URLs are fetched by the
worker. See the README for optional recipe fields.

Successful submission returns **202** and a public job object:

```json
{
  "id": "eb6f948b-6392-4d85-b747-f383bb9744e2",
  "project": "my-app",
  "version": "1.2.3",
  "status": "queued",
  "stage": "Queued",
  "createdAt": "2026-09-07T10:00:00Z",
  "updatedAt": "2026-09-07T10:00:00Z"
}
```

The request is complete only after the archive hash is checked and the job is saved.
There is no idempotency key in v1. If a connection drops before receiving the response,
check `GET /v1/jobs` before resubmitting to avoid duplicates.

## Universal recipes

`spec.recipe` selects `flutter` (the backward-compatible default), `tauri`,
`electron`, `dotnet`, or `custom`. Non-Flutter recipes require `version` and
`buildNumber`; they do not require `pubspec.yaml`, `apiUrl`, or a Flutter SDK.
`outputDir` is relative to `workDir`. Custom `commands` and optional non-Flutter
`testCommands` are arrays of `{ "run": "executable", "args": ["argument"] }`.
Unknown recipe names, empty custom commands, unsafe output paths and Flutter-only
fields on other recipes are rejected. Custom recipes must provide tests or
explicitly set `skipTests: true`. See [recipe documentation](recipes.md).

The manifest includes `recipe` and records `testStatus`. Non-Flutter output
files (including nested installers) are packaged into one ZIP, then uploaded
with checksums and a manifest through the same job lifecycle.

## Read and control jobs

| Request | Result |
| --- | --- |
| `GET /v1/health` | Name, app version, platform, and pause state |
| `GET /v1/jobs` | Array of retained public jobs, newest first |
| `GET /v1/jobs/{id}` | `{ "job": {...}, "log": "..." }` with the latest 128 KiB of logs |
| `POST /v1/jobs/{id}/cancel` | Request cancellation; 200 `{ "ok": true }` |
| `POST /v1/jobs/{id}/retry-upload` | Queue an existing `upload_failed` job; 202 `{ "ok": true }` |

Job states are `queued`, `running`, `succeeded`, `failed`, `upload_failed`, and
`cancelled`. Only queued/running jobs are active. A successful job includes its
`s3://bucket/prefix/id/` destination and artifact names/hashes. Saved credentials never
appear in job JSON. An active cancellation stops the build process tree; poll until
a terminal state is observed. Cancellation does not remove already uploaded S3 objects.

Common errors: **400** invalid metadata/archive, **401** invalid key or browser origin,
**404** unknown job, **409** paused/full queue or invalid state transition, and **503**
non-Windows development worker. At most 20 active jobs are accepted; one runs at a time.

## Local settings API

The settings server is an implementation detail on an ephemeral loopback port. Its
separate admin key authorizes settings, key rotation, tool setup, and stopping an idle
agent. Do not use it from remote scripts or treat its routes as a stable public API.
