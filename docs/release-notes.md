A background Windows build agent for local release scripts.

- One Windows EXE with settings, managed build tools, and startup at sign-in.
- Local source ZIP delivery over pinned HTTPS; no worker Git account required.
- Flutter Windows releases with portable ZIP, optional Inno installer, and MinIO upload.
- Persistent build queue, logs, cancellation, and upload retry.
- Signed background updates with idle installation and startup rollback.
- Sender binaries for macOS, Linux, and Windows.

Download `kilnbridge-windows-amd64.exe` on the build PC, open it, and select **Prepare
this machine**. The first Microsoft C++ tools installation may require administrator
approval. Download the matching sender binary for your development computer.

See the README for recipes, the shared Android/Windows wrapper, and setup instructions.
