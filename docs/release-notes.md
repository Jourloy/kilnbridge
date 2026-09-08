Fix a Windows startup issue where double-clicking the downloaded EXE could leave
Kilnbridge running with its settings window hidden.

- Launch the installed settings window visibly while keeping the build worker in
  the background.
- Add a Windows regression test that checks the actual process startup flags.
- Document how to open the installed app directly if using an older launcher.

Download `kilnbridge-windows-amd64.exe` and open it on the build PC. Existing settings,
access keys, build history, and prepared tools are preserved. Existing agents can
also receive this release through signed background updates.

The release includes Windows x64 and macOS/Linux sender binaries, SHA-256 checksums,
and signed Windows update metadata.
