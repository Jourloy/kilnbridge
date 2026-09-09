package build

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"

	"github.com/jourloy/kilnbridge/internal/model"
)

type githubRelease struct {
	ID        int64  `json:"id"`
	Tag       string `json:"tag_name"`
	Draft     bool   `json:"draft"`
	Immutable bool   `json:"immutable"`
	UploadURL string `json:"upload_url"`
	HTMLURL   string `json:"html_url"`
}

type githubAsset struct {
	ID     int64  `json:"id"`
	Name   string `json:"name"`
	State  string `json:"state"`
	Size   int64  `json:"size"`
	Digest string `json:"digest"`
}

func PublishGitHub(ctx context.Context, file string, s model.Spec, result model.Result, log func(string)) (string, error) {
	client := &http.Client{Timeout: 35 * time.Minute, CheckRedirect: func(*http.Request, []*http.Request) error {
		return errors.New("GitHub publication redirects are not allowed")
	}}
	return publishGitHub(ctx, client, file, s, result, log)
}

func publishGitHub(ctx context.Context, client *http.Client, file string, s model.Spec, result model.Result, log func(string)) (string, error) {
	if err := s.ValidatePublication(); err != nil {
		return "", err
	}
	if s.Publish != "github" || !model.SafeRelativeName(result.Name) || strings.Contains(result.Name, "/") {
		return "", errors.New("invalid GitHub publication")
	}
	f, err := os.Open(file)
	if err != nil {
		return "", err
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil || !info.Mode().IsRegular() || info.Size() != result.Size || result.Size <= 0 || result.Size >= 2<<30 {
		return "", errors.New("GitHub release bundles must be smaller than 2 GiB; download locally or use S3 for larger results")
	}
	digest, err := FileSHA256(file)
	if err != nil || digest != result.SHA256 {
		return "", errors.New("result bundle changed after packaging")
	}
	p := githubPublisher{client: client, token: s.GitHub.Token, base: "https://api.github.com/repos/" + s.GitHub.Repository}
	release, err := p.findRelease(ctx, s.GitHub.Tag)
	if err != nil {
		return "", err
	}
	if release.ID == 0 {
		// Never silently tag the repository's default branch for a local snapshot.
		status, err := p.request(ctx, "GET", p.base+"/git/ref/tags/"+url.PathEscape(s.GitHub.Tag), nil, "", 0, nil)
		if err != nil {
			return "", err
		}
		if status == 404 {
			status, err = p.request(ctx, "GET", p.base+"/commits/"+s.SourceSHA, nil, "", 0, nil)
			if err != nil || status != 200 {
				return "", errors.New("push the source commit to the GitHub repository or create the target tag/release before publishing")
			}
		} else if status != 200 {
			return "", githubStatus(status)
		}
		body, _ := json.Marshal(map[string]any{"tag_name": s.GitHub.Tag, "target_commitish": s.SourceSHA, "name": s.GitHub.Tag, "draft": true})
		status, err = p.request(ctx, "POST", p.base+"/releases", bytes.NewReader(body), "application/json", int64(len(body)), &release)
		if err != nil || status != 201 {
			if err == nil {
				err = githubStatus(status)
			}
			return "", err
		}
	}
	if release.ID <= 0 || release.Tag != s.GitHub.Tag {
		return "", errors.New("the target GitHub release is invalid")
	}
	if s.GitHub.Draft && !release.Draft {
		return "", errors.New("the target release is already public; choose another tag or set github.draft to false")
	}
	assetsURL := fmt.Sprintf("%s/releases/%d/assets", p.base, release.ID)
	found, err := p.findAsset(ctx, assetsURL, result.Name)
	if err != nil {
		return "", err
	}
	uploaded := found.State == "uploaded" && found.Digest == "sha256:"+digest && found.Size == result.Size
	if release.Immutable && !uploaded {
		return "", errors.New("the target GitHub release is immutable and cannot receive new assets")
	}
	if found.ID != 0 && !uploaded {
		// GitHub can leave an empty starter asset after a failed upstream upload.
		if found.State != "starter" || found.Size != 0 {
			return "", errors.New("a different asset already uses this job's bundle name; existing assets were kept")
		}
		status, err := p.request(ctx, "DELETE", fmt.Sprintf("%s/releases/assets/%d", p.base, found.ID), nil, "", 0, nil)
		if err != nil {
			return "", err
		}
		if status != 204 {
			return "", githubStatus(status)
		}
	}
	if !uploaded {
		address, _, _ := strings.Cut(release.UploadURL, "{")
		u, err := url.Parse(address)
		expectedPath := fmt.Sprintf("/repos/%s/releases/%d/assets", s.GitHub.Repository, release.ID)
		if err != nil || u.Scheme != "https" || u.Host != "uploads.github.com" || u.User != nil || !strings.EqualFold(u.Path, expectedPath) || u.RawQuery != "" || u.Fragment != "" {
			return "", errors.New("GitHub returned an unexpected upload URL")
		}
		u.RawQuery = url.Values{"name": {result.Name}}.Encode()
		if log != nil {
			log("Uploading " + result.Name + " to GitHub Releases")
		}
		var asset githubAsset
		status, err := p.request(ctx, "POST", u.String(), f, "application/zip", result.Size, &asset)
		if err != nil {
			return "", err
		}
		if status != 201 {
			return "", githubStatus(status)
		}
		if asset.Name != result.Name || asset.State != "uploaded" || asset.Size != result.Size || asset.Digest != "sha256:"+digest {
			return "", errors.New("GitHub did not confirm the uploaded bundle checksum; retry publication to reconcile the result")
		}
	}
	if release.Draft && !s.GitHub.Draft {
		body := []byte(`{"draft":false,"make_latest":"false"}`)
		status, err := p.request(ctx, "PATCH", fmt.Sprintf("%s/releases/%d", p.base, release.ID), bytes.NewReader(body), "application/json", int64(len(body)), &release)
		if err != nil {
			return "", err
		}
		if status != 200 {
			return "", githubStatus(status)
		}
	}
	if !s.GitHub.Draft {
		if release.Draft {
			return "", errors.New("GitHub has not published the release; retry publication")
		}
		return "https://github.com/" + s.GitHub.Repository + "/releases/tag/" + url.PathEscape(s.GitHub.Tag), nil
	}
	if u, err := url.Parse(release.HTMLURL); err == nil && u.Scheme == "https" && u.Host == "github.com" && u.User == nil && strings.HasPrefix(u.Path, "/"+s.GitHub.Repository+"/releases/") {
		return u.String(), nil
	}
	return "https://github.com/" + s.GitHub.Repository + "/releases", nil
}

type githubPublisher struct {
	client      *http.Client
	token, base string
}

func (p githubPublisher) request(ctx context.Context, method, address string, body io.Reader, contentType string, size int64, out any) (int, error) {
	req, err := http.NewRequestWithContext(ctx, method, address, body)
	if err != nil {
		return 0, err
	}
	req.Header.Set("Authorization", "Bearer "+p.token)
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("X-GitHub-Api-Version", "2026-03-10")
	req.Header.Set("User-Agent", "Kilnbridge")
	if contentType != "" {
		req.Header.Set("Content-Type", contentType)
	}
	req.ContentLength = size
	res, err := p.client.Do(req)
	if err != nil {
		return 0, err
	}
	defer res.Body.Close()
	if res.StatusCode >= 200 && res.StatusCode < 300 && out != nil {
		err = json.NewDecoder(io.LimitReader(res.Body, 8<<20)).Decode(out)
	}
	return res.StatusCode, err
}

func githubStatus(status int) error {
	return fmt.Errorf("GitHub publication returned HTTP %d; check the repository, release and token Contents write permission", status)
}

func (p githubPublisher) findRelease(ctx context.Context, tag string) (githubRelease, error) {
	var release githubRelease
	status, err := p.request(ctx, "GET", p.base+"/releases/tags/"+url.PathEscape(tag), nil, "", 0, &release)
	if err != nil {
		return release, err
	}
	if status == 200 {
		return release, nil
	}
	if status != 404 {
		return release, githubStatus(status)
	}
	// The tag endpoint does not find unpublished drafts. Include them on retry.
	for page := 1; page <= 100; page++ {
		var releases []githubRelease
		status, err = p.request(ctx, "GET", fmt.Sprintf("%s/releases?per_page=100&page=%d", p.base, page), nil, "", 0, &releases)
		if err != nil {
			return release, err
		}
		if status != 200 {
			return release, githubStatus(status)
		}
		for _, candidate := range releases {
			if candidate.Tag == tag {
				return candidate, nil
			}
		}
		if len(releases) < 100 {
			return release, nil
		}
	}
	return release, errors.New("too many GitHub releases to find the target draft")
}

func (p githubPublisher) findAsset(ctx context.Context, address, name string) (githubAsset, error) {
	for page := 1; page <= 100; page++ {
		var assets []githubAsset
		status, err := p.request(ctx, "GET", fmt.Sprintf("%s?per_page=100&page=%d", address, page), nil, "", 0, &assets)
		if err != nil {
			return githubAsset{}, err
		}
		if status != 200 {
			return githubAsset{}, githubStatus(status)
		}
		for _, a := range assets {
			if a.Name == name {
				return a, nil
			}
		}
		if len(assets) < 100 {
			return githubAsset{}, nil
		}
	}
	return githubAsset{}, errors.New("too many GitHub assets to reconcile the upload")
}
