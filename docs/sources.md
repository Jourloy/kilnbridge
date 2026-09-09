# Source inputs

The sender accepts three explicit source types for every recipe. `--source-type git`
is the default, keeping existing commands compatible. Both CLI and agent must be updated
for folder and ZIP submissions. The source path is local to the sending machine; the
agent receives a frozen ZIP and never clones a repository or reads that local path.

| Source type | `--source` | Selection and provenance |
| --- | --- | --- |
| `git` | Local Git working directory, including a project subdirectory | Tracked and unignored files; records the local commit and dirty state |
| `folder` | Ordinary directory, with or without `.git` | Recursive files and directories; no Git executable or commit required |
| `zip` | ZIP prepared by any external tool or script | Sends exactly the producer's bytes; no Git executable or commit required |

```sh
./kilnbridge submit --source-type git --source ./my-app \
  --recipe custom-windows.json --env agent.env

./kilnbridge submit --source-type folder --source './my project' \
  --recipe custom-windows.json --env agent.env

./kilnbridge submit --source-type zip --source ./prepared-source.zip \
  --recipe custom-windows.json --env agent.env
```

Select `workDir` relative to the **archive root**, using forward slashes. If the ZIP
contains `my-app/scripts/build.ps1`, use `workDir: "my-app"` to run `scripts/build.ps1`.
No top-level directory is stripped automatically. Flutter's omitted version/build number
are inferred from `workDir/pubspec.yaml` in the frozen ZIP, consistently for every input.
Other recipes specify `version` and `buildNumber` in JSON.

## Folder and Git snapshots

The sender excludes `.git`, dependency/build caches (`node_modules`, `.dart_tool`,
`build`, `dist`, `out`, and others), dotenv files, and common signing-key formats.
Folder mode does **not** apply `.gitignore`: use Git mode for Git ignore rules, or
prepare a staging directory/ZIP with exactly the required files.

`snapshotIncludes` contains exact paths relative to the source root, not `workDir`.
It can include a filtered file or a Git-ignored script:

```json
{
  "snapshotIncludes": ["scripts/generated-build.ps1", "test/fixtures/key.pem"]
}
```

Each explicit include must exist and be a regular file; directories and globs are
not accepted. An ignored/untracked include marks a Git snapshot dirty. Git submissions
with local changes require `--allow-dirty`; folder and ZIP submissions have no Git
dirty state. Links, special files, and Git submodules are rejected. Empty directories
are preserved in folder mode. Snapshot output and its JSON sidecar are excluded when
written inside the source directory. A failed snapshot preserves an existing output.
Keep input files unchanged until snapshot creation finishes.

## External preparation and build scripts

A preparation script runs on the **sender** before Kilnbridge. It can export a project
from another VCS, copy generated sources, or add shared scripts to a staging directory:

```sh
# Your script creates ./prepared with project files and scripts/build.ps1.
./prepare-source.sh ./prepared
./kilnbridge submit --source-type folder --source ./prepared \
  --recipe custom-windows.json --env agent.env --publish download
```

For an existing ZIP producer, call it first, then submit with `--source-type zip`.
Kilnbridge does not execute local preparation scripts implicitly. Build/test commands
from the recipe execute on the **Windows agent** from `workDir`. Put required external
scripts inside the source tree/ZIP and call them in a custom recipe, for example:

```json
{
  "recipe": "custom",
  "project": "scripted-project",
  "workDir": ".",
  "version": "1.2.3",
  "buildNumber": "1",
  "publish": "download",
  "commands": [
    {"run": "powershell.exe", "args": ["-NoProfile", "-NonInteractive", "-ExecutionPolicy", "Bypass", "-File", "scripts/build.ps1"]}
  ],
  "testCommands": [
    {"run": "powershell.exe", "args": ["-NoProfile", "-NonInteractive", "-ExecutionPolicy", "Bypass", "-File", "scripts/test.ps1"]}
  ],
  "artifacts": ["dist", "reports"]
}
```

The test script runs before the build script. Each must return a nonzero exit code on
failure. Both selected output directories must contain results after the build. Paths
outside the transferred sources are not automatically copied to the agent.

## Prepared ZIPs and metadata

The sender copies ZIP input to a private temporary file and validates it before upload.
It preserves the bytes, root layout, and all producer-selected files. Snapshot filters
are not applied; `snapshotIncludes` with ZIP input is rejected rather than ignored.
ZIPs must be at most 2 GiB, contain 1–100000 entries, and expand to at most 8 GiB.
Validation checks CRC/size, unsafe Windows paths, symlinks/special files, duplicate
names, and file/directory conflicts. A ZIP cannot overwrite itself through `--output`.

To inspect a source package without an agent connection:

```sh
./kilnbridge snapshot --source-type folder --source ./prepared \
  --recipe custom-windows.json --output ./source.zip
```

`--recipe` is optional here and supplies `snapshotIncludes`. The command writes the ZIP
and `source.zip.json` with `sourceType` and `archiveSha256`; Git snapshots also carry
`sourceSha` and, when applicable, `sourceDirty`. `snapshot` can also copy/check a ZIP.
For direct API callers, set `sourceType: "folder"` or `"zip"`, omit Git fields, and send
the SHA-256 of the exact ZIP bytes. Missing `sourceType` means `git` and requires a commit.
Result manifests record `sourceType` and `sourceArchiveSha256`; Git commit/dirty fields
appear only for Git sources. See the [API contract](api.md).

All four [delivery modes](publication.md) work with all source types. Folder/ZIP builds
need an existing GitHub tag or release when publishing to GitHub: there is no source
commit from which to create a new tag. Local saving, downloads and S3 need no Git metadata.
