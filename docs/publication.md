# Optional publication and downloads

A build needs only the agent URL, key, and certificate fingerprint. Storage accounts
and publication credentials are optional. Every completed build retains its selected
files and a downloadable result ZIP on the Windows agent.

Set `publish` in the recipe, or override it with `submit --publish MODE`:

| Mode | Behavior | Credentials |
| --- | --- | --- |
| `local` | Finish after saving files and a result ZIP on the agent | Agent connection only |
| `download` | Save on the agent; a waiting sender downloads and verifies the ZIP | Agent connection only |
| `s3` | Upload individual files, checksums, and manifest to S3/MinIO | `MINIO_*` settings |
| `github` | Upload the result ZIP as a GitHub release asset | GitHub repository, tag, and token |

An omitted `publish` defaults to `local` when no storage settings are supplied.
Existing recipes with S3 settings retain S3 publication. An explicit `local` or
`download` ignores ambient S3 credentials in the sender's environment. Only required
credentials are forwarded. Upgrade both sender and agent before using these modes.

## Build and download without storage

Copy the three connection values from the Windows app into `agent.env`:

```sh
./kilnbridge submit --source ./my-app --recipe custom-windows.json \
  --publish download --env agent.env
```

The ZIP filename contains the project, version, and job UUID. Use `--output ./result.zip`
to select another filename. Existing files are never overwritten. Transfers stream
to disk, verify the size and SHA-256 from authenticated job metadata, and remove a
partial file on failure. The archive contains the selected files, `SHA256SUMS.txt`,
and `manifest.json`, preserving nested paths and original file contents.

Download any retained result later, including after a failed external publication:

```sh
./kilnbridge download --job JOB_UUID --env agent.env --output ./result.zip
./kilnbridge wait --job JOB_UUID --env agent.env --download
```

`--download` on `submit` or `wait` also downloads externally published results.
`publish: "download"` downloads automatically when `submit` or `wait` finishes.
`--detach` returns immediately; it does not download in the background. Reconnect
with `wait` or use `download` once packaging finishes. A download failure leaves the
agent's completed build intact. If external publication failed, `wait --download`
saves the result but still returns a nonzero exit code for the publication failure.

Files live in `jobs/JOB_UUID/artifacts/`; the ZIP is `jobs/JOB_UUID/result.zip`.
Retention is configurable from 1–90 days, default 7, counted from the last job status
update. Active downloads prevent cleanup, shutdown, and automatic updates from
interrupting their transfer. Build history shows the download command. Older jobs
created before this feature may have no transfer ZIP.

## S3 / MinIO

Set `publish: "s3"` and fill the `MINIO_*` values in `agent.env`. The bucket must
exist and the endpoint must use HTTPS. Files keep their paths below
`PREFIX/JOB_UUID/`; checksums follow the artifacts and `manifest.json` is uploaded
last. `retry-upload` reuses retained files and credentials without rebuilding.

## GitHub Releases

Add this configuration to any recipe:

```json
{
  "publish": "github",
  "github": {
    "repository": "owner/repository",
    "tag": "v1.2.3",
    "draft": true
  }
}
```

Set `KILNBRIDGE_GITHUB_TOKEN` in `agent.env`. `GITHUB_TOKEN` and `GH_TOKEN` are
fallbacks, read only for GitHub publication. The token needs repository Contents
write permission; GitHub may require additional permissions when creating tags
that change workflows. Keep the token out of recipe JSON and source control.

The agent finds the release by tag, including drafts, or creates a new draft.
An existing Git tag is reused. A new tag requires the snapshot's source commit in
that GitHub repository; otherwise publication fails. The agent never substitutes
the default branch for a missing source commit. Dirty snapshots remain recorded
in the manifest.

The website selects `draft: true` by default. With `draft: false` (also the API
default when omitted), the agent publishes only after the ZIP upload succeeds.
It does not change GitHub's latest-release selection. A public release cannot
be turned back into a draft; use `draft: false` to attach to it. Immutable releases
cannot receive new assets.

Each job gets a unique `<project>-<version>-<job-UUID>.zip` asset, preserving
directories and avoiding basename collisions. GitHub assets must be smaller than
2 GiB; use S3 or local downloads for larger results. Retry reuses an asset with a
matching remote SHA-256. An empty `starter` asset from an interrupted upload is
replaced; existing assets with different content are kept and reported as conflicts.
A failed upload leaves a new release in draft.

This publisher targets github.com. API behavior follows GitHub's
[release documentation](https://docs.github.com/en/rest/releases/releases) and
[release asset documentation](https://docs.github.com/en/rest/releases/assets).
The test suite simulates this API without creating real releases.

## Failures and credentials

Build failures do not publish a result. After packaging, a publication failure
becomes `upload_failed`; the ZIP remains available through `download`.
`retry-upload` retries the selected external publisher. Credentials stay encrypted
on Windows until success or retention cleanup and are redacted from logs.
Successful local jobs also remove source files and saved job settings. Downloads
require the same agent key and certificate pin as submissions; artifact URLs on
the agent are not public.
