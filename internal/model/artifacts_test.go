package model

import "testing"

func TestArtifactListsWorkWithEveryRecipeAndAllowCustomWithoutOutputDir(t *testing.T) {
	for _, recipe := range []string{"", "flutter", "tauri", "electron", "dotnet", "custom"} {
		s := recipeSpec()
		s.Recipe = recipe
		s.ArtifactPaths = []string{"dist/setup.msi", "dist/app.zip", "bin/cli", "lib", "reports"}
		if recipe == "custom" {
			s.Commands = []Command{{Run: "builder.exe"}}
			s.SkipTests = true
		}
		if err := s.Validate(); err != nil {
			t.Fatalf("%s: %v", recipe, err)
		}
		if recipe == "custom" && s.OutputDir != "" {
			t.Fatal("explicit artifacts require no outputDir")
		}
	}
}

func TestArtifactListRejectsAmbiguousOrOverlappingPaths(t *testing.T) {
	badLists := [][]string{{}, {"."}, {".."}, {"../out"}, {"/out"}, {"C:/out"}, {"out\\file"}, {"out/"}, {"out/*"}, {"out/a?"}, {"out/file:stream"}, {"out/CON.txt"}, {"out/LPT²"}, {"out./file"}, {"out /file"}, {"file\x01.txt"}, {"manifest.json"}, {"SHA256SUMS.TXT"}, {"Manifest.json/nested"}, {"lib", "LIB"}, {"lib", "lib/a.dll"}, {"lib/a.dll", "LIB"}}
	for _, paths := range badLists {
		if err := (Spec{ArtifactPaths: paths}).ValidateArtifacts(); err == nil {
			t.Fatalf("accepted %q", paths)
		}
	}
	if err := (Spec{ArtifactPaths: nil}).ValidateArtifacts(); err != nil {
		t.Fatal("legacy recipe rejected:", err)
	}
	if err := (Spec{ArtifactPaths: []string{"reports/manifest.json", "reports/SHA256SUMS.txt", "setup.msi", "app.tar.gz", "my cli", "-tool"}}).ValidateArtifacts(); err != nil {
		t.Fatal(err)
	}
	if err := (Spec{ArtifactPaths: make([]string, 65)}).ValidateArtifacts(); err == nil {
		t.Fatal("unbounded selection accepted")
	}
}
