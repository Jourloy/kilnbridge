package model

import (
	"errors"
	"fmt"
	"net/url"
	"path"
	"regexp"
	"strings"
	"time"
)

const DefaultFlutterVersion = "3.41.6"

type Storage struct {
	Endpoint     string `json:"endpoint"`
	Bucket       string `json:"bucket"`
	Region       string `json:"region"`
	AccessKey    string `json:"accessKey"`
	SecretKey    string `json:"secretKey"`
	SessionToken string `json:"sessionToken,omitempty"`
	Prefix       string `json:"prefix,omitempty"`
}

type Command struct {
	Run  string   `json:"run"`
	Args []string `json:"args,omitempty"`
}

type Spec struct {
	Recipe           string    `json:"recipe,omitempty"`
	Commands         []Command `json:"commands,omitempty"`
	TestCommands     []Command `json:"testCommands,omitempty"`
	OutputDir        string    `json:"outputDir,omitempty"`
	SnapshotIncludes []string  `json:"snapshotIncludes,omitempty"`
	Project          string    `json:"project"`
	WorkDir          string    `json:"workDir"`
	FlutterVersion   string    `json:"flutterVersion"`
	Version          string    `json:"version"`
	BuildNumber      string    `json:"buildNumber"`
	SourceSHA        string    `json:"sourceSha"`
	SourceDirty      bool      `json:"sourceDirty"`
	ArchiveSHA256    string    `json:"archiveSha256"`
	APIURL           string    `json:"apiUrl"`
	TestPaths        []string  `json:"testPaths,omitempty"`
	SkipTests        bool      `json:"skipTests"`
	SkipCodegen      bool      `json:"skipCodegen,omitempty"`
	PrepareScripts   []string  `json:"prepareScripts,omitempty"`
	InstallerScript  string    `json:"installerScript,omitempty"`
	Storage          Storage   `json:"storage"`
}

var VersionPattern = regexp.MustCompile(`^[0-9]+\.[0-9]+\.[0-9]+(-[0-9A-Za-z.-]+)?$`)
var SHA256Pattern = regexp.MustCompile(`^[a-f0-9]{64}$`)
var IDPattern = regexp.MustCompile(`^[a-f0-9]{8}-[a-f0-9]{4}-[a-f0-9]{4}-[a-f0-9]{4}-[a-f0-9]{12}$`)

func Relative(p string) bool {
	return p != "" && !strings.ContainsAny(p, "\\:\x00\r\n") && !strings.HasPrefix(p, "/") && path.Clean(p) == p && p != ".." && !strings.HasPrefix(p, "../")
}

func (s *Spec) Validate() error {
	if !regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9_-]{0,63}$`).MatchString(s.Project) {
		return errors.New("project must be a short filename-safe name")
	}
	if s.WorkDir == "" {
		s.WorkDir = "."
	}
	if !Relative(s.WorkDir) {
		return errors.New("workDir must be relative to the archive")
	}
	if err := s.ValidateRecipe(); err != nil {
		return err
	}
	if s.Recipe == "flutter" && s.FlutterVersion == "" {
		s.FlutterVersion = DefaultFlutterVersion
	}
	if s.Recipe == "flutter" && !regexp.MustCompile(`^[0-9]+\.[0-9]+\.[0-9]+$`).MatchString(s.FlutterVersion) {
		return errors.New("flutterVersion must be an exact stable version")
	}
	if !VersionPattern.MatchString(s.Version) || !regexp.MustCompile(`^[0-9]{1,9}$`).MatchString(s.BuildNumber) {
		return errors.New("invalid release version or build number")
	}
	if !regexp.MustCompile(`^[a-f0-9]{40}$`).MatchString(s.SourceSHA) || !SHA256Pattern.MatchString(s.ArchiveSHA256) {
		return errors.New("sourceSha and archiveSha256 must be hexadecimal digests")
	}
	u, err := url.Parse(s.APIURL)
	if s.APIURL != "" && (err != nil || u.Host == "" || (u.Scheme != "https" && u.Scheme != "http") || u.User != nil) {
		return errors.New("apiUrl must be an HTTP(S) URL without embedded credentials")
	}
	for _, p := range append(append([]string{}, s.PrepareScripts...), s.TestPaths...) {
		if !Relative(p) || strings.HasPrefix(p, "-") {
			return fmt.Errorf("invalid project-relative path: %q", p)
		}
	}
	for _, p := range s.SnapshotIncludes {
		if !Relative(p) || p == "." {
			return errors.New("snapshotIncludes must contain exact relative file paths")
		}
	}
	for _, p := range s.PrepareScripts {
		if !strings.HasSuffix(strings.ToLower(p), ".ps1") {
			return errors.New("prepareScripts must contain PowerShell scripts")
		}
	}
	if s.InstallerScript != "" && (!Relative(s.InstallerScript) || !strings.HasSuffix(strings.ToLower(s.InstallerScript), ".iss")) {
		return errors.New("installerScript must be a relative .iss path")
	}
	u, err = url.Parse(s.Storage.Endpoint)
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

type Artifact struct {
	Name   string `json:"name"`
	SHA256 string `json:"sha256"`
}

type Job struct {
	ID          string     `json:"id"`
	Project     string     `json:"project"`
	Version     string     `json:"version"`
	Status      string     `json:"status"`
	Stage       string     `json:"stage"`
	CreatedAt   time.Time  `json:"createdAt"`
	UpdatedAt   time.Time  `json:"updatedAt"`
	Error       string     `json:"error,omitempty"`
	Destination string     `json:"destination,omitempty"`
	Artifacts   []Artifact `json:"artifacts,omitempty"`
}

func (j Job) Active() bool { return j.Status == "queued" || j.Status == "running" }
