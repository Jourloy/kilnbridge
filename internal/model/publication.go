package model

import (
	"errors"
	"net/url"
	"regexp"
	"strings"
)

// Existing recipes carrying S3 settings keep publishing to S3. New recipes
// without storage settings finish locally and need only the agent connection.
func (s Spec) PublicationMode() string {
	if s.Publish != "" {
		return s.Publish
	}
	if s.Storage != (Storage{}) {
		return "s3"
	}
	return "local"
}

func (s *Spec) ValidatePublication() error {
	s.Publish = s.PublicationMode()
	switch s.Publish {
	case "local", "download", "s3", "github":
	default:
		return errors.New("publish must be local, download, s3, or github")
	}
	if s.Publish != "s3" && s.Storage != (Storage{}) {
		return errors.New("storage settings require publish: s3")
	}
	if s.Publish != "github" && s.GitHub != (GitHubRelease{}) {
		return errors.New("github settings require publish: github")
	}
	if s.Publish == "github" {
		if !ValidGitHubRepository(s.GitHub.Repository) || !ValidGitHubTag(s.GitHub.Tag) {
			return errors.New("github requires an owner/repository and an exact release tag")
		}
		if strings.TrimSpace(s.GitHub.Token) == "" || strings.ContainsAny(s.GitHub.Token, "\r\n\x00") {
			return errors.New("a GitHub token with repository Contents write permission is required")
		}
	}
	if s.Publish != "s3" {
		return nil
	}
	u, err := url.Parse(s.Storage.Endpoint)
	if err != nil || u.Scheme != "https" || u.Host == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" || (u.Path != "" && u.Path != "/") {
		return errors.New("storage.endpoint must be an HTTPS origin")
	}
	if !regexp.MustCompile(`^[a-z0-9][a-z0-9.-]{1,61}[a-z0-9]$`).MatchString(s.Storage.Bucket) || s.Storage.AccessKey == "" || s.Storage.SecretKey == "" {
		return errors.New("storage bucket and credentials are required")
	}
	if s.Storage.Region == "" {
		s.Storage.Region = "us-east-1"
	}
	if s.Storage.Prefix == "" {
		s.Storage.Prefix = "release-candidates"
	}
	if !Relative(s.Storage.Prefix) || s.Storage.Prefix == "." {
		return errors.New("storage.prefix must be a relative object prefix")
	}
	return nil
}

func ValidGitHubRepository(value string) bool {
	return regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9-]{0,38}/[A-Za-z0-9_][A-Za-z0-9_.-]{0,99}$`).MatchString(value)
}

func ValidGitHubTag(value string) bool {
	if value == "" || len(value) > 200 || value == "@" || strings.Contains(value, "..") || strings.Contains(value, "@{") || strings.ContainsAny(value, " ~^:?*[\\") || strings.HasSuffix(value, ".") {
		return false
	}
	for _, r := range value {
		if r < 32 || r == 127 {
			return false
		}
	}
	for _, part := range strings.Split(value, "/") {
		if part == "" || strings.HasPrefix(part, ".") || strings.HasSuffix(part, ".lock") {
			return false
		}
	}
	return true
}
