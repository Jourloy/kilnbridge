package build

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/jourloy/kilnbridge/internal/model"
)

func (r Runner) buildRecipe(ctx context.Context, jobDir, work, id string, s model.Spec) ([]model.Artifact, error) {
	info, err := os.Stat(work)
	if err != nil || !info.IsDir() {
		return nil, errors.New("workDir does not exist in the source snapshot")
	}
	for _, script := range s.PrepareScripts {
		r.stage("Preparing " + filepath.Base(script))
		if err := r.run(ctx, work, "powershell.exe", "-NoProfile", "-NonInteractive", "-ExecutionPolicy", "Bypass", "-File", filepath.Join(work, filepath.FromSlash(script))); err != nil {
			return nil, err
		}
	}
	prepare, tests, commands := s.RecipeCommands()
	for _, phase := range []struct {
		name     string
		commands []model.Command
	}{{"Restoring dependencies", prepare}, {"Running tests", tests}, {"Building " + s.Recipe + " for Windows x64", commands}} {
		if len(phase.commands) == 0 {
			continue
		}
		r.stage(phase.name)
		for i, command := range phase.commands {
			file := command.Run
			if strings.ContainsAny(file, `/\`) && !filepath.IsAbs(file) {
				file = filepath.Join(work, filepath.FromSlash(file))
			}
			if err := r.run(ctx, work, file, command.Args...); err != nil {
				return nil, fmt.Errorf("%s, command %d (%s): %w; install the recipe's tools on the Windows agent and restart it after PATH changes", phase.name, i+1, command.Run, err)
			}
		}
	}
	if s.SkipTests {
		r.log("Tests were explicitly skipped; the manifest will record testStatus=skipped.")
	}
	if s.ArtifactPaths != nil {
		r.stage("Collecting selected artifacts")
		return collectArtifacts(ctx, jobDir, work, id, s)
	}
	return packageRecipe(jobDir, work, id, s, r.stage)
}

func packageRecipe(jobDir, work, id string, s model.Spec, stage func(string)) ([]model.Artifact, error) {
	release := filepath.Join(work, filepath.FromSlash(s.OutputDir))
	// Check every ancestor: a build command may have created a link or junction.
	for dir := release; dir != work; dir = filepath.Dir(dir) {
		if dir == filepath.Dir(dir) {
			return nil, errors.New("outputDir escapes workDir")
		}
		info, err := os.Lstat(dir)
		if err != nil {
			return nil, fmt.Errorf("missing outputDir %q: %w", s.OutputDir, err)
		}
		if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
			return nil, errors.New("outputDir and its parents must be real directories")
		}
	}
	files := 0
	err := filepath.WalkDir(release, func(_ string, entry os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.IsDir() {
			return nil
		}
		if !entry.Type().IsRegular() {
			return errors.New("build outputs must contain only regular files")
		}
		files++
		return nil
	})
	if err != nil {
		return nil, err
	}
	if files == 0 {
		return nil, errors.New("outputDir contains no build artifacts")
	}
	stage("Packaging release")
	out := filepath.Join(jobDir, "artifacts")
	if err := os.Mkdir(out, 0700); err != nil {
		return nil, err
	}
	if err := ZipDirectory(release, filepath.Join(out, s.Project+"-"+s.Version+"-windows-x64.zip")); err != nil {
		return nil, err
	}
	return Manifest(out, id, s)
}
