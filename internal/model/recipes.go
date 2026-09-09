package model

import (
	"errors"
	"fmt"
	"strings"
)

// An omitted recipe preserves the original Flutter contract.
func (s *Spec) ValidateRecipe() error {
	if err := s.ValidateArtifacts(); err != nil {
		return err
	}
	if s.Recipe == "" {
		s.Recipe = "flutter"
	}
	switch s.Recipe {
	case "flutter":
		if len(s.Commands) > 0 || len(s.TestCommands) > 0 || s.OutputDir != "" {
			return errors.New("Flutter uses its built-in pipeline; use the custom recipe for commands or outputDir")
		}
		return nil
	case "tauri", "electron", "dotnet", "custom":
	default:
		return fmt.Errorf("unknown recipe: %q", s.Recipe)
	}
	if s.FlutterVersion != "" || s.SkipCodegen || len(s.TestPaths) > 0 || s.InstallerScript != "" {
		return errors.New("flutterVersion, skipCodegen, testPaths and installerScript are Flutter-only fields")
	}
	if s.Recipe == "custom" && len(s.Commands) == 0 {
		return errors.New("custom recipes require at least one command")
	}
	if s.Recipe != "custom" && len(s.Commands) > 0 {
		return errors.New("use the custom recipe to replace built-in build commands")
	}
	if s.Recipe == "custom" && !s.SkipTests && len(s.TestCommands) == 0 {
		return errors.New("custom recipes require testCommands or an explicit skipTests: true")
	}
	if s.OutputDir == "" {
		switch s.Recipe {
		case "tauri":
			s.OutputDir = "src-tauri/target/x86_64-pc-windows-msvc/release/bundle"
		case "electron":
			s.OutputDir = "dist"
		case "dotnet":
			s.OutputDir = "publish"
		}
	}
	needsOutputDir := s.Recipe != "custom" || s.ArtifactPaths == nil || s.OutputDir != ""
	if needsOutputDir && (!Relative(s.OutputDir) || s.OutputDir == "." || strings.HasPrefix(s.OutputDir, "-") || strings.ContainsAny(s.OutputDir, `<>"|?*`)) {
		return errors.New("outputDir must name a directory inside workDir, using forward slashes")
	}
	if len(s.Commands)+len(s.TestCommands) > 64 {
		return errors.New("a recipe may contain at most 64 commands")
	}
	for _, c := range append(append([]Command{}, s.Commands...), s.TestCommands...) {
		if strings.TrimSpace(c.Run) == "" || strings.ContainsAny(c.Run, "\x00\r\n") || len(c.Run) > 1024 || len(c.Args) > 128 {
			return errors.New("each command requires an executable and at most 128 arguments")
		}
		for _, arg := range c.Args {
			if strings.ContainsRune(arg, '\x00') || len(arg) > 8192 {
				return errors.New("command arguments must not contain NUL or exceed 8192 bytes")
			}
		}
	}
	return nil
}

// RecipeCommands returns preparation, tests, and build commands separately so a
// failed test stops packaging and skipTests always describes what actually ran.
func (s Spec) RecipeCommands() (prepare, tests, build []Command) {
	switch s.Recipe {
	case "tauri":
		prepare = []Command{{Run: "npm.cmd", Args: []string{"ci"}}}
		tests = []Command{{Run: "cargo.exe", Args: []string{"test", "--locked", "--manifest-path", "src-tauri/Cargo.toml", "--target", "x86_64-pc-windows-msvc"}}}
		build = []Command{{Run: "npm.cmd", Args: []string{"run", "tauri", "--", "build", "--target", "x86_64-pc-windows-msvc"}}}
	case "electron":
		prepare = []Command{{Run: "npm.cmd", Args: []string{"ci"}}}
		tests = []Command{{Run: "npm.cmd", Args: []string{"test"}}}
		build = []Command{{Run: "npm.cmd", Args: []string{"run", "build"}}, {Run: "node_modules/.bin/electron-builder.cmd", Args: []string{"--win", "--x64", "--publish", "never", "--config.directories.output=" + s.OutputDir}}}
	case "dotnet":
		prepare = []Command{{Run: "dotnet.exe", Args: []string{"restore", "--runtime", "win-x64"}}}
		tests = []Command{{Run: "dotnet.exe", Args: []string{"test", "--configuration", "Release"}}}
		build = []Command{{Run: "dotnet.exe", Args: []string{"publish", "--configuration", "Release", "--runtime", "win-x64", "--self-contained", "true", "--no-restore", "--output", s.OutputDir, "-p:Version=" + s.Version}}}
	case "custom":
		build = s.Commands
	}
	if len(s.TestCommands) > 0 {
		tests = s.TestCommands
	}
	if s.SkipTests {
		tests = nil
	}
	return
}
