package model

import (
	"errors"
	"regexp"
)

// Missing sourceType preserves the original Git API contract.
func (s Spec) SourceKind() string {
	if s.SourceType == "" {
		return "git"
	}
	return s.SourceType
}

func (s *Spec) ValidateSource() error {
	s.SourceType = s.SourceKind()
	switch s.SourceType {
	case "git":
		if !regexp.MustCompile(`^[a-f0-9]{40}$`).MatchString(s.SourceSHA) {
			return errors.New("Git sources require a 40-character hexadecimal sourceSha")
		}
	case "folder", "zip":
		if s.SourceSHA != "" || s.SourceDirty {
			return errors.New("folder and ZIP sources must not claim Git commit or dirty metadata")
		}
	default:
		return errors.New("sourceType must be git, folder or zip")
	}
	if !SHA256Pattern.MatchString(s.ArchiveSHA256) {
		return errors.New("archiveSha256 must be a 64-character hexadecimal digest")
	}
	return nil
}
