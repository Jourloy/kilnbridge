package model

import "testing"

func TestSourceMetadataForEveryRecipe(t *testing.T) {
	for _, recipe := range []string{"flutter", "tauri", "electron", "dotnet", "custom"} {
		for _, kind := range []string{"", "git", "folder", "zip"} {
			s := recipeSpec()
			s.Recipe, s.SourceType = recipe, kind
			if kind == "folder" || kind == "zip" {
				s.SourceSHA = ""
			}
			if recipe == "custom" {
				s.Commands = []Command{{Run: "builder"}}
				s.OutputDir = "release"
				s.SkipTests = true
			}
			if err := s.Validate(); err != nil {
				t.Fatalf("%s/%s: %v", recipe, kind, err)
			}
			if s.SourceType == "" {
				t.Fatal("missing legacy default")
			}
		}
	}
}

func TestRejectFalseSourceProvenance(t *testing.T) {
	for _, change := range []func(*Spec){
		func(s *Spec) { s.SourceType = "unknown" },
		func(s *Spec) { s.SourceSHA = "" },
		func(s *Spec) { s.SourceType = "folder" },
		func(s *Spec) { s.SourceType = "zip"; s.SourceSHA = ""; s.SourceDirty = true },
		func(s *Spec) { s.SourceType = "folder"; s.SourceSHA = ""; s.ArchiveSHA256 = "" },
	} {
		s := recipeSpec()
		change(&s)
		if err := s.Validate(); err == nil {
			t.Fatalf("accepted %+v", s)
		}
	}
}
