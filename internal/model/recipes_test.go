package model

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func recipeSpec() Spec {
	return Spec{Project: "demo", Version: "1.2.3", BuildNumber: "4", SourceSHA: strings.Repeat("a", 40), ArchiveSHA256: strings.Repeat("b", 64), Storage: Storage{Endpoint: "https://storage.example.com", Bucket: "releases", AccessKey: "key", SecretKey: "secret"}}
}

func TestRecipeCompatibilityAndPresets(t *testing.T) {
	for _, recipe := range []string{"", "flutter", "tauri", "electron", "dotnet", "custom"} {
		t.Run(recipe, func(t *testing.T) {
			s := recipeSpec()
			s.Recipe = recipe
			if recipe == "custom" {
				s.Commands = []Command{{Run: "builder.exe"}}
				s.OutputDir = "release"
				s.SkipTests = true
			}
			if err := s.Validate(); err != nil {
				t.Fatal(err)
			}
			if recipe == "" || recipe == "flutter" {
				if s.Recipe != "flutter" || s.FlutterVersion != DefaultFlutterVersion {
					t.Fatal("legacy defaults lost")
				}
			} else {
				if s.FlutterVersion != "" {
					t.Fatal("non-Flutter recipe requires Flutter")
				}
				_, tests, build := s.RecipeCommands()
				if len(build) == 0 || (recipe != "custom" && len(tests) == 0) {
					t.Fatal("incomplete pipeline")
				}
				s.SkipTests = true
				_, tests, _ = s.RecipeCommands()
				if len(tests) != 0 {
					t.Fatal("skipTests ignored")
				}
			}
		})
	}
}

func TestRejectInvalidRecipes(t *testing.T) {
	for _, change := range []func(*Spec){
		func(s *Spec) { s.Recipe = "unknown" },
		func(s *Spec) { s.OutputDir = "../escape" },
		func(s *Spec) { s.OutputDir = "." },
		func(s *Spec) { s.OutputDir = "C:/escape" },
		func(s *Spec) { s.OutputDir = "build\\release" },
		func(s *Spec) { s.Commands = nil },
		func(s *Spec) { s.Commands[0].Run = " " },
		func(s *Spec) { s.Commands[0].Args = []string{"bad\x00arg"} },
		func(s *Spec) { s.SkipTests = false },
		func(s *Spec) { s.InstallerScript = "setup.iss" },
		func(s *Spec) { s.Recipe = "flutter" },
		func(s *Spec) { s.Recipe = "electron" },
	} {
		s := recipeSpec()
		s.Recipe = "custom"
		s.OutputDir = "out"
		s.Commands = []Command{{Run: "builder.exe"}}
		s.SkipTests = true
		change(&s)
		if err := s.Validate(); err == nil {
			t.Fatalf("accepted invalid recipe: %+v", s)
		}
	}
}

func TestExamplesMatchTheAgentContract(t *testing.T) {
	for _, file := range []string{"flutter-windows", "tauri-windows", "electron-windows", "dotnet-windows", "custom-windows", "custom-artifacts-windows"} {
		b, err := os.ReadFile(filepath.Join("..", "..", "examples", file+".json"))
		if err != nil {
			t.Fatal(err)
		}
		s := recipeSpec()
		if err = json.Unmarshal(b, &s); err != nil {
			t.Fatal(err)
		}
		if err = s.Validate(); err != nil {
			t.Fatalf("%s: %v", file, err)
		}
	}
}
