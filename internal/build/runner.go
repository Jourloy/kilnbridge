package build

import (
	"bufio"
	"context"
	_ "embed"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/jourloy/kilnbridge/internal/model"
	"github.com/jourloy/kilnbridge/internal/platform"
)

//go:embed bootstrap.ps1
var bootstrap []byte

//go:embed bundle-runtime.ps1
var bundleRuntime []byte

type Runner struct {
	Tools string
	Log   func(string)
	Stage func(string)
}

func (r Runner) log(s string) {
	if r.Log != nil {
		r.Log(s)
	}
}
func (r Runner) stage(s string) {
	if r.Stage != nil {
		r.Stage(s)
	}
	r.log(s)
}

func (r Runner) Prepare(ctx context.Context, version string) error {
	if runtime.GOOS != "windows" {
		return errors.New("Windows build tools can only be prepared on Windows")
	}
	if err := os.MkdirAll(r.Tools, 0700); err != nil {
		return err
	}
	script := filepath.Join(r.Tools, "bootstrap.ps1")
	if err := platform.AtomicWrite(script, bootstrap, 0600); err != nil {
		return err
	}
	return r.run(ctx, r.Tools, "powershell.exe", "-NoProfile", "-NonInteractive", "-ExecutionPolicy", "Bypass", "-File", script, "-ToolsDirectory", r.Tools, "-FlutterVersion", version)
}

func (r Runner) run(ctx context.Context, dir, file string, args ...string) error {
	cmd := platform.ShellCommand(ctx, file, args...)
	cmd.Dir = dir
	cmd.Env = append(platform.ChildEnvironment(), "PUB_CACHE="+filepath.Join(r.Tools, "pub-cache"), "CI=true", "FLUTTER_SUPPRESS_ANALYTICS=true")
	if runtime.GOOS == "windows" {
		cmd.Env = append(cmd.Env, "PATH="+filepath.Join(r.Tools, "git", "cmd")+string(os.PathListSeparator)+os.Getenv("PATH"))
	}
	return RunCommand(ctx, cmd, r.Log)
}

// RunCommand cancels the complete build process tree, drains both streams, and
// bounds individual log records so malformed tool output cannot consume RAM.
func RunCommand(ctx context.Context, cmd *exec.Cmd, log func(string)) error {
	platform.PrepareCommand(cmd)
	cmd.WaitDelay = 10 * time.Second
	reader, writer := io.Pipe()
	cmd.Stdout = writer
	cmd.Stderr = writer
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		br := bufio.NewReaderSize(reader, 16384)
		for {
			line, err := br.ReadSlice('\n')
			if len(line) > 0 && log != nil {
				log(strings.TrimRight(string(line), "\r\n"))
			}
			if err != nil && err != bufio.ErrBufferFull {
				break
			}
		}
	}()
	err := cmd.Run()
	_ = writer.Close()
	wg.Wait()
	_ = reader.Close()
	if ctx.Err() != nil {
		return ctx.Err()
	}
	if err != nil {
		return fmt.Errorf("%s failed: %w", filepath.Base(cmd.Path), err)
	}
	return nil
}

func (r Runner) Build(ctx context.Context, jobDir, id string, s model.Spec) ([]model.Artifact, error) {
	if err := s.ValidateRecipe(); err != nil {
		return nil, err
	}
	if s.Recipe == "flutter" {
		r.stage("Preparing Flutter tools")
		if s.FlutterVersion == "" {
			s.FlutterVersion = model.DefaultFlutterVersion
		}
		if err := r.Prepare(ctx, s.FlutterVersion); err != nil {
			return nil, err
		}
	}
	r.stage("Extracting source snapshot")
	archive := filepath.Join(jobDir, "source.zip")
	digest, err := FileSHA256(archive)
	if err != nil {
		return nil, err
	}
	if digest != s.ArchiveSHA256 {
		return nil, errors.New("source archive SHA-256 mismatch")
	}
	source := filepath.Join(jobDir, "source")
	if err = Extract(archive, source); err != nil {
		return nil, err
	}
	work := filepath.Join(source, filepath.FromSlash(s.WorkDir))
	if s.Recipe != "flutter" {
		return r.buildRecipe(ctx, jobDir, work, id, s)
	}
	b, err := os.ReadFile(filepath.Join(work, "pubspec.yaml"))
	if err != nil {
		return nil, err
	}
	if !hasVersion(string(b), s.Version+"+"+s.BuildNumber) {
		return nil, errors.New("pubspec.yaml version does not match the submitted release metadata")
	}
	flutter := filepath.Join(r.Tools, "flutter", s.FlutterVersion, "flutter", "bin", "flutter.bat")
	dart := filepath.Join(filepath.Dir(flutter), "dart.bat")
	for _, script := range s.PrepareScripts {
		r.stage("Preparing " + filepath.Base(script))
		if err = r.run(ctx, work, "powershell.exe", "-NoProfile", "-NonInteractive", "-ExecutionPolicy", "Bypass", "-File", filepath.Join(work, filepath.FromSlash(script))); err != nil {
			return nil, err
		}
	}
	r.stage("Restoring locked dependencies")
	if err = r.run(ctx, work, flutter, "pub", "get", "--enforce-lockfile"); err != nil {
		return nil, err
	}
	if !s.SkipCodegen {
		r.stage("Generating Dart sources")
		if err = r.run(ctx, work, dart, "run", "build_runner", "build", "--delete-conflicting-outputs"); err != nil {
			return nil, err
		}
	}
	if !s.SkipTests {
		r.stage("Running tests")
		args := append([]string{"test"}, s.TestPaths...)
		if err = r.run(ctx, work, flutter, args...); err != nil {
			return nil, err
		}
	} else {
		r.log("Tests were explicitly skipped; the manifest will record testStatus=skipped.")
	}
	r.stage("Building Windows x64")
	if err = r.run(ctx, work, flutter, "build", "windows", "--release", "--build-name="+s.Version, "--build-number="+s.BuildNumber, "--dart-define=API_URL="+s.APIURL); err != nil {
		return nil, err
	}
	release := filepath.Join(work, "build", "windows", "x64", "runner", "Release")
	exe, err := filepath.Glob(filepath.Join(release, "*.exe"))
	if err != nil || len(exe) == 0 {
		return nil, errors.New("Flutter did not produce a Windows executable")
	}
	out := filepath.Join(jobDir, "artifacts")
	if err = os.Mkdir(out, 0700); err != nil {
		return nil, err
	}
	r.stage("Packaging release")
	runtimeScript := filepath.Join(r.Tools, "bundle-runtime.ps1")
	if err = platform.AtomicWrite(runtimeScript, bundleRuntime, 0600); err != nil {
		return nil, err
	}
	if err = r.run(ctx, work, "powershell.exe", "-NoProfile", "-NonInteractive", "-ExecutionPolicy", "Bypass", "-File", runtimeScript, "-Destination", release); err != nil {
		return nil, err
	}
	base := s.Project + "-" + s.Version + "-windows-x64"
	if err = ZipDirectory(release, filepath.Join(out, base+".zip")); err != nil {
		return nil, err
	}
	if s.InstallerScript != "" {
		if err = r.run(ctx, work, filepath.Join(r.Tools, "inno", "ISCC.exe"), "/DMyAppVersion="+s.Version, "/O"+out, "/F"+base+"-setup", filepath.Join(work, filepath.FromSlash(s.InstallerScript))); err != nil {
			return nil, err
		}
		if info, e := os.Stat(filepath.Join(out, base+"-setup.exe")); e != nil || info.Size() == 0 {
			return nil, errors.New("Inno Setup did not produce the installer")
		}
	}
	return Manifest(out, id, s)
}

func hasVersion(pubspec, expected string) bool {
	for _, line := range strings.Split(pubspec, "\n") {
		if strings.HasPrefix(line, "version:") {
			value := strings.TrimSpace(strings.SplitN(strings.TrimPrefix(line, "version:"), "#", 2)[0])
			return strings.Trim(value, "\"'") == expected
		}
	}
	return false
}

func Manifest(out, id string, s model.Spec) ([]model.Artifact, error) {
	entries, err := os.ReadDir(out)
	if err != nil {
		return nil, err
	}
	var artifacts []model.Artifact
	for _, entry := range entries {
		if entry.IsDir() || (!strings.HasSuffix(entry.Name(), ".zip") && !strings.HasSuffix(entry.Name(), ".exe")) {
			continue
		}
		digest, err := FileSHA256(filepath.Join(out, entry.Name()))
		if err != nil {
			return nil, err
		}
		artifacts = append(artifacts, model.Artifact{Name: entry.Name(), SHA256: digest})
	}
	if len(artifacts) == 0 {
		return nil, errors.New("release contains no artifacts")
	}
	sort.Slice(artifacts, func(i, j int) bool { return artifacts[i].Name < artifacts[j].Name })
	var sums strings.Builder
	for _, a := range artifacts {
		fmt.Fprintf(&sums, "%s  %s\n", a.SHA256, a.Name)
	}
	if err = platform.AtomicWrite(filepath.Join(out, "SHA256SUMS.txt"), []byte(sums.String()), 0600); err != nil {
		return nil, err
	}
	testStatus := "success"
	if s.SkipTests {
		testStatus = "skipped"
	}
	recipe := s.Recipe
	if recipe == "" {
		recipe = "flutter"
	}
	m := map[string]any{"recipe": recipe, "buildId": id, "sourceSha": s.SourceSHA, "sourceDirty": s.SourceDirty, "sourceArchiveSha256": s.ArchiveSHA256, "version": s.Version, "buildNumber": s.BuildNumber, "platform": "windows", "platforms": map[string]string{"windows": "success", "android": "skipped"}, "testStatus": testStatus, "artifacts": artifacts}
	b, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return nil, err
	}
	err = platform.AtomicWrite(filepath.Join(out, "manifest.json"), append(b, '\n'), 0600)
	return artifacts, err
}
