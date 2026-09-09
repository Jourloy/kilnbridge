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
  "sourceType": "git",
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

`sourceType` is `git`, `folder`, or `zip`. Omitted/empty means `git` for compatibility.
Every type requires `archiveSha256`, the 64-character lowercase SHA-256 of the exact ZIP
bytes. Git sources additionally require `sourceSha`, a 40-character lowercase Git commit,
and may set `sourceDirty: true`. This is caller-supplied provenance, not proof of a clean
checkout. Folder/ZIP sources omit `sourceSha` and `sourceDirty` (empty/false are accepted);
nonempty Git metadata for these types is rejected. No placeholder commit is needed.

A caller can create and transfer its own ZIP with `sourceType: "zip"` without Git on either
machine. Include the files required by the recipe, scripts, tests, and assets. Flutter
needs `pubspec.yaml`, `pubspec.lock` and its Windows project. `workDir` is relative to the
ZIP root; the agent does not strip wrapper folders or fetch source URLs. See [source
inputs](sources.md) for CLI and external-script examples. Optional `storage.sessionToken`
supports temporary S3 credentials.

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

The manifest includes `recipe` and records `testStatus`. By default, non-Flutter
output files are packaged into one ZIP. Every recipe also accepts `artifacts`:
an array of 1–64 exact files/directories relative to `workDir`, such as
`["dist/setup.msi", "bin/cli.exe", "lib", "reports"]`. This replaces automatic
packaging with individual uploads retaining the selected paths and original bytes.
Directories expand recursively. Custom recipes may omit `outputDir` in this mode.

An empty list, unsafe paths, overlaps (case-insensitive), and reserved root metadata
names are rejected at submission. Missing results, empty selected directories,
links, and special files fail collection after the build. Test failures stop the
job before artifact collection. Omit `artifacts` to retain existing packaging.
See [artifact selection](recipes.md#artifacts) for the full path contract.

Public job and manifest `artifacts` remain arrays of `{ "name": "lib/core.dll",
"sha256": "..." }`, one entry per file. S3 keys are `PREFIX/JOB_UUID/NAME`, including
nested directories. Checksums are uploaded after the files and `manifest.json`
last. A failed upload retains copies for the existing retry endpoint.

## Optional publication

`spec.publish` accepts `local`, `download`, `s3`, or `github`. When omitted, storage
settings preserve legacy S3 publication; without them, the job completes locally.
Only S3 jobs require `storage`. GitHub jobs require `github` with `repository`,
`tag`, `token`, and optional `draft` (default false). The sender injects credentials
from its environment; recipe JSON contains no token. Unused publisher credentials
and unknown modes are rejected. See [delivery behavior](publication.md).

After packaging, public jobs include `publish` and `result` metadata:

```json
{
  "result": {
    "name": "my-app-1.2.3-JOB_UUID.zip",
    "sha256": "HEX_SHA256",
    "size": 12345
  }
}
```

`GET /v1/jobs/{id}/result` streams this ZIP through the same authenticated, pinned
connection. The ZIP contains all artifacts and both metadata files. It is available
as soon as `result` is present, including during/after a failed publication. The
response supports byte ranges and includes Content-Length, attachment filename,
and a SHA-256 ETag. Verify the entire downloaded file against `result.sha256` and
`result.size`. A missing/expired job returns 404; a job with no prepared result or
full transfer capacity returns 409. There are at most four concurrent downloads.
Source archives and saved credentials have no download endpoint.

`download` is a sender instruction: the server retains files just as in `local`.
A waiting sender downloads the ZIP, while detached submissions require a later
`wait` or `download` call. All modes retain results for the configured period.
Successful local jobs report their agent-side artifact directory as `destination`;
S3 jobs report the S3 prefix and GitHub jobs report the release URL.

## Read and control jobs

| Request | Result |
| --- | --- |
| `GET /v1/health` | Name, app version, platform, and pause state |
| `GET /v1/jobs` | Array of retained public jobs, newest first |
| `GET /v1/jobs/{id}/result` | Authenticated result ZIP; size and SHA-256 are in the public job |
| `GET /v1/jobs/{id}` | `{ "job": {...}, "log": "..." }` with the latest 128 KiB of logs |
| `POST /v1/jobs/{id}/cancel` | Request cancellation; 200 `{ "ok": true }` |
| `POST /v1/jobs/{id}/retry-upload` | Queue an existing `upload_failed` job; 202 `{ "ok": true }` |

Job states are `queued`, `running`, `succeeded`, `failed`, `upload_failed`, and
`cancelled`. Only queued/running jobs are active. A completed build includes retained result metadata and artifact names/hashes.
Successful external publications also include their destination. Saved credentials never
appear in job JSON. An active cancellation stops the build process tree; poll until
a terminal state is observed. Cancellation does not remove already uploaded S3 objects.

Common errors: **400** invalid metadata/archive, **401** invalid key or browser origin,
**404** unknown job, **409** paused/full queue or invalid state transition, and **503**
non-Windows development worker. At most 20 active jobs are accepted; one runs at a time.

## Local settings API

The settings server is an implementation detail on an ephemeral loopback port. Its
separate admin key authorizes settings, key rotation, tool setup, and stopping an idle
agent. Do not use it from remote scripts or treat its routes as a stable public API.
