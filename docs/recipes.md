# Universal Windows recipes

Every job selects its recipe independently. The same Windows x64 agent restores,
tests, builds, packages and uploads different projects through the existing queue.
Existing recipes without `recipe` retain the Flutter pipeline. Upgrade both the
sender and agent before using the new recipe types.

Copy an example, set `project` and `workDir`, and submit it as usual:

```sh
./kilnbridge submit --source ./my-app --recipe tauri-windows.json --env agent.env
```

`workDir` is relative to the submitted source root, so a monorepo can use
`apps/desktop`. Use forward slashes. Each build runs in a fresh extracted snapshot.
Non-Flutter recipes require explicit `version` (such as `1.2.3`) and `buildNumber`
(1–9 digits). Match the project's own version: Tauri and Electron metadata is not
rewritten. .NET receives `-p:Version`. Flutter reads missing values from `pubspec.yaml`
and verifies they match the snapshot. `apiUrl` is optional and used by Flutter.

## Presets

| Recipe | Tools on the Windows agent | Pipeline / output |
| --- | --- | --- |
| [Flutter](../examples/flutter-windows.json) | Agent provisions Flutter, Git, C++ Build Tools, Windows SDK and Inno Setup | Locked pub get, optional codegen, flutter test, release build, runtime bundling, ZIP and optional Inno installer |
| [Tauri](../examples/tauri-windows.json) | Install Node/npm, Rust with `x86_64-pc-windows-msvc`, C++ Build Tools, Windows SDK and WebView2 | `npm ci`, `cargo test --locked`, `npm run tauri -- build --target x86_64-pc-windows-msvc`; ZIP of `src-tauri/target/x86_64-pc-windows-msvc/release/bundle` |
| [Electron](../examples/electron-windows.json) | Install Node/npm; native dependencies may need Python and C++ Build Tools | `npm ci`, `npm test`, `npm run build`, local `electron-builder --win --x64 --publish never`; ZIP of `dist` |
| [.NET](../examples/dotnet-windows.json) | Install the SDK required by the project / `global.json` | `dotnet restore --runtime win-x64`, `dotnet test`, self-contained `dotnet publish` to `publish` |
| [Custom](../examples/custom-windows.json) | Install the executables used by the recipe | Custom tests, then build commands, then ZIP of the required `outputDir` |

Restart the agent after installing tools or changing PATH. The **Prepare this
machine** action provisions Flutter tools; other recipes use installed SDKs and do
not trigger Flutter setup. Missing tools fail the job with a setup hint.

The Tauri preset expects a `tauri` npm script, `package-lock.json`, `Cargo.lock`, and
bundling enabled in Tauri configuration. Its default tests cover Rust; use
`testCommands` to add frontend tests. Configure `beforeBuildCommand` in Tauri for
frontend compilation. The Electron preset requires `electron-builder` in project
dependencies and `build` / `test` npm scripts. It disables electron-builder's own
publishing because Kilnbridge handles the upload. The .NET preset targets a directory
with one `.csproj`. Use custom commands for solutions requiring separate test and
publish projects. No preset silently skips missing tests.

References: [Tauri distribution](https://v2.tauri.app/distribute/),
[electron-builder CLI](https://www.electron.build/v26/docs/cli/),
[dotnet publish](https://learn.microsoft.com/en-us/dotnet/core/tools/dotnet-publish).

## Commands and tests

Custom `commands` and non-Flutter `testCommands` use executable and argument arrays:

```json
{
  "recipe": "custom",
  "project": "my-app",
  "workDir": "apps/desktop",
  "version": "1.2.3",
  "buildNumber": "1",
  "outputDir": "out",
  "testCommands": [{"run": "npm.cmd", "args": ["test"]}],
  "commands": [{"run": "npm.cmd", "args": ["run", "build"]}]
}
```

Commands execute sequentially from `workDir`. Paths containing slashes are resolved
against that directory; bare executable names use PATH. `.cmd` and `.bat` arguments
are quoted by the agent's existing Windows launcher. Shell operators are not
interpreted in argument arrays: invoke `powershell.exe -File scripts/build.ps1` for
a script. Scripts must propagate native command failures through their exit code.
The website's command editor accepts one PowerShell command per line and wraps each
in a separate process that propagates a nonzero native exit code.

For custom builds, put dependency installation in `prepareScripts` (project-relative
PowerShell files) if it must happen before `testCommands`. Presets provide their own
dependency restore. `testCommands` replaces preset tests; `skipTests: true` skips
them explicitly and is recorded in `manifest.json`. Custom recipes require at least
one test command unless tests are explicitly skipped. A command failure stops the
job before packaging or upload. Cancellation uses the same process-tree cleanup as
Flutter builds. Limit: 64 commands total, 128 arguments per command.

## Artifacts

For non-Flutter recipes, `outputDir` names a directory within `workDir`; it cannot
be `.` or escape the project. Electron and .NET pass this directory to their build
tool. Tauri users changing it must also configure the corresponding output location
in their project. The entire nonempty directory is packaged as
`<project>-<version>-windows-x64.zip`, preserving nested installers and other files.
Links and nonregular output files are rejected. Empty/missing output fails the job.

The ZIP, `SHA256SUMS.txt`, and `manifest.json` follow the existing S3 upload and retry
flow. The manifest records the selected recipe and test status. `installerScript`,
`flutterVersion`, `skipCodegen`, and `testPaths` apply only to Flutter. Other recipes
produce installers with their build tools and include them in the output ZIP.

Connection and storage credentials stay in `agent.env`, not in recipe examples.
As with Flutter preparation scripts, build commands run as the agent's user and
are intended for trusted source code; they are not an isolation boundary.
