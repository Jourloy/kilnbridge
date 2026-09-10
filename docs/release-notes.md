Kilnbridge 0.2.0 adds more Windows build recipes, source inputs, and ways to
retrieve or publish build results.

- Build Tauri, Electron, .NET, and custom projects alongside Flutter, with per-job
  test and build commands.
- Submit Git working directories, ordinary folders, or prepared ZIP archives.
- Select output files and directories, including installers, libraries, CLI
  executables, and test reports, with checksums and a result manifest.
- Keep results on the agent, download a verified result ZIP to the sender, or
  publish to S3/MinIO or GitHub Releases. Storage credentials are optional for
  local builds and downloads.
- Open settings or stop the background agent from the Windows notification-area
  icon.

Update both the Windows agent and the sender CLI before using the new recipes,
source types, or delivery modes. Existing Flutter recipes remain supported, and
recipes with S3 settings keep publishing to S3. Existing settings, access keys,
build history, and prepared tools are preserved.

Download `kilnbridge-windows-amd64.exe` and open it on the build PC. Existing agents
can also receive this release through signed background updates. Non-Flutter
recipes use SDKs installed on the build PC; automatic tool setup covers Flutter.

The release includes Windows x64 and macOS/Linux sender binaries, SHA-256 checksums,
and signed Windows update metadata.
