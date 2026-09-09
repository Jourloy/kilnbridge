package model

import (
	"errors"
	"fmt"
	"strings"
)

// Root metadata names are owned by Kilnbridge. The same names inside a report
// directory are ordinary user artifacts.
func ArtifactMetadataPath(name string) bool {
	first, _, _ := strings.Cut(name, "/")
	return strings.EqualFold(first, "manifest.json") || strings.EqualFold(first, "SHA256SUMS.txt")
}

func (s Spec) ValidateArtifacts() error {
	if s.ArtifactPaths == nil {
		return nil
	}
	if len(s.ArtifactPaths) == 0 || len(s.ArtifactPaths) > 64 {
		return errors.New("artifacts must contain 1–64 explicit file or directory paths")
	}
	for i, name := range s.ArtifactPaths {
		if !SafeRelativeName(name) || ArtifactMetadataPath(name) {
			return fmt.Errorf("invalid artifact path %q: use an exact path inside workDir; root manifest.json and SHA256SUMS.txt are reserved", name)
		}
		key := strings.ToLower(name)
		for _, previous := range s.ArtifactPaths[:i] {
			other := strings.ToLower(previous)
			if key == other || strings.HasPrefix(key, other+"/") || strings.HasPrefix(other, key+"/") {
				return fmt.Errorf("artifact paths overlap: %q and %q", previous, name)
			}
		}
	}
	return nil
}

// Windows normalizes device names, trailing dots and spaces on every filesystem.
// Use the same portable path contract for snapshots and build results.
func SafeRelativeName(name string) bool {
	if !Relative(name) || name == "." {
		return false
	}
	for _, part := range strings.Split(name, "/") {
		if strings.TrimRight(part, ". ") != part || strings.ContainsAny(part, `<>"|?*`) {
			return false
		}
		for _, r := range part {
			if r < 32 {
				return false
			}
		}
		base := strings.ToUpper(strings.SplitN(part, ".", 2)[0])
		if base == "CON" || base == "PRN" || base == "AUX" || base == "NUL" || base == "CONIN$" || base == "CONOUT$" {
			return false
		}
		runes := []rune(base)
		if len(runes) == 4 && (strings.HasPrefix(base, "COM") || strings.HasPrefix(base, "LPT")) && strings.ContainsRune("123456789¹²³", runes[3]) {
			return false
		}
	}
	return true
}
