//go:build windows

package build

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/jourloy/kilnbridge/internal/model"
	"github.com/jourloy/kilnbridge/internal/platform"
)

// Opt in: downloads the actual toolchain and compiles a real Flutter application.
func TestWindowsFlutterSmoke(t *testing.T) {
	tools := os.Getenv("KILNBRIDGE_SMOKE_TOOLS")
	if tools == "" {
		t.Skip("set KILNBRIDGE_SMOKE_TOOLS to run the Windows toolchain smoke test")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 35*time.Minute)
	defer cancel()
	r := Runner{Tools: tools, Log: func(line string) { t.Log(line) }}
	if err := r.Prepare(ctx, model.DefaultFlutterVersion); err != nil {
		t.Fatal(err)
	}
	source := filepath.Join(t.TempDir(), "sample")
	flutter := filepath.Join(tools, "flutter", model.DefaultFlutterVersion, "flutter", "bin", "flutter.bat")
	if err := r.run(ctx, filepath.Dir(source), flutter, "create", "--project-name=kilnbridge_smoke", "--platforms=windows", source); err != nil {
		t.Fatal(err)
	}

	installer := `[Setup]
AppName=Kilnbridge Smoke
AppVersion={#MyAppVersion}
DefaultDirName={localappdata}\KilnbridgeSmoke
PrivilegesRequired=lowest
ArchitecturesAllowed=x64
ArchitecturesInstallIn64BitMode=x64
Uninstallable=no
[Files]
Source: "build\windows\x64\runner\Release\*"; DestDir: "{app}"; Flags: recursesubdirs createallsubdirs
`
	if err := os.WriteFile(filepath.Join(source, "smoke.iss"), []byte(installer), 0600); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{".git", ".dart_tool", "build"} {
		_ = os.RemoveAll(filepath.Join(source, name))
	}
	job := t.TempDir()
	archive := filepath.Join(job, "source.zip")
	if err := ZipDirectory(source, archive); err != nil {
		t.Fatal(err)
	}
	digest, err := FileSHA256(archive)
	if err != nil {
		t.Fatal(err)
	}
	spec := model.Spec{Project: "kilnbridge-smoke", WorkDir: ".", FlutterVersion: model.DefaultFlutterVersion, Version: "1.0.0", BuildNumber: "1", SourceSHA: strings.Repeat("a", 40), ArchiveSHA256: digest, APIURL: "https://example.com", SkipCodegen: true, InstallerScript: "smoke.iss"}
	artifacts, err := r.Build(ctx, job, platform.ID(), spec)
	if err != nil {
		t.Fatal(err)
	}
	if len(artifacts) != 2 {
		t.Fatalf("unexpected artifacts: %+v", artifacts)
	}
	exe := filepath.Join(job, "source", "build", "windows", "x64", "runner", "Release", "kilnbridge_smoke.exe")
	if stat, err := os.Stat(exe); err != nil || stat.Size() == 0 {
		t.Fatal("the Windows application was not produced")
	}
}
