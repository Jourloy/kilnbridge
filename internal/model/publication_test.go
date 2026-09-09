package model

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestOptionalPublicationAndLegacyS3(t *testing.T) {
	for _, mode := range []string{"", "local", "download", "s3", "github"} {
		s := recipeSpec()
		s.Publish = mode
		if mode != "s3" {
			s.Storage = Storage{}
		}
		if mode == "github" {
			s.GitHub = GitHubRelease{Repository: "owner/repo", Tag: "releases/v1.2.3", Token: "synthetic-token", Draft: true}
		}
		if err := s.Validate(); err != nil {
			t.Fatal(mode, err)
		}
		if mode == "" && s.Publish != "local" {
			t.Fatal("storage-free default was lost")
		}
		if mode == "local" || mode == "download" {
			b, _ := json.Marshal(s)
			if strings.Contains(string(b), `"storage"`) || strings.Contains(string(b), `"github"`) {
				t.Fatal("unused credentials sent", string(b))
			}
		}
	}
	s := recipeSpec()
	if err := s.Validate(); err != nil || s.Publish != "s3" {
		t.Fatal("legacy S3 recipe changed", err)
	}
}

func TestRejectInvalidPublication(t *testing.T) {
	for _, s := range []Spec{
		{Publish: "ftp"}, {Publish: "s3"}, {Publish: "local", Storage: Storage{SecretKey: "secret"}},
		{Publish: "local", GitHub: GitHubRelease{Token: "secret"}},
		{Publish: "github", GitHub: GitHubRelease{Repository: "https://github.com/owner/repo", Tag: "v1", Token: "token"}},
		{Publish: "github", GitHub: GitHubRelease{Repository: "owner/repo", Tag: "../v1", Token: "token"}},
		{Publish: "github", GitHub: GitHubRelease{Repository: "owner/repo", Tag: "v1"}},
		{Publish: "github", GitHub: GitHubRelease{Repository: "owner/repo", Tag: "v1", Token: "bad\r\ntoken"}},
	} {
		if err := s.ValidatePublication(); err == nil {
			t.Fatalf("accepted %+v", s)
		}
	}
	for _, tag := range []string{"", "@", "..", "-bad.lock", "x//v1", "/v1", ".hidden", "v 1", "v1~2", "v1@{1}", "v1.", "v\x7f1", strings.Repeat("v", 201)} {
		if ValidGitHubTag(tag) {
			t.Fatal("accepted tag", tag)
		}
	}
}
