package client

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"net/http"
	"os"
	"path/filepath"
	"strings"

	"github.com/jourloy/kilnbridge/internal/model"
)

func (c *Client) Job(ctx context.Context, id string) (model.Job, error) {
	var response struct {
		Job model.Job `json:"job"`
	}
	if !model.IDPattern.MatchString(id) {
		return response.Job, errors.New("job must be a UUID")
	}
	b, err := c.Request(ctx, "GET", "/v1/jobs/"+id, nil, "")
	if err != nil {
		return response.Job, err
	}
	if err = json.Unmarshal(b, &response); err != nil {
		return response.Job, err
	}
	if response.Job.ID != id {
		return response.Job, errors.New("agent returned a different job")
	}
	return response.Job, nil
}

// Downloads are authenticated through the same pinned connection as submission.
// A failed transfer removes its partial file and never overwrites an existing one.
func (c *Client) Download(ctx context.Context, id, output string) (string, error) {
	j, err := c.Job(ctx, id)
	if err != nil {
		return "", err
	}
	result := j.Result
	if result == nil {
		return "", errors.New("no result is available; wait for packaging to finish or use an updated agent")
	}
	if !model.SafeRelativeName(result.Name) || strings.Contains(result.Name, "/") || !model.SHA256Pattern.MatchString(result.SHA256) || result.Size <= 0 || result.Size == math.MaxInt64 {
		return "", errors.New("agent returned invalid result metadata")
	}
	if output == "" {
		output = result.Name
	}
	output, err = filepath.Abs(output)
	if err != nil {
		return "", err
	}
	req, err := http.NewRequestWithContext(ctx, "GET", c.URL+"/v1/jobs/"+id+"/result", nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("Authorization", "Bearer "+c.Key)
	res, err := c.HTTP.Do(req)
	if err != nil {
		return "", err
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		return "", fmt.Errorf("result download returned HTTP %d; the result may have expired", res.StatusCode)
	}
	if res.ContentLength >= 0 && res.ContentLength != result.Size {
		return "", errors.New("result download size does not match job metadata")
	}
	if err = os.MkdirAll(filepath.Dir(output), 0700); err != nil {
		return "", err
	}
	f, err := os.OpenFile(output, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		return "", fmt.Errorf("choose a new --output file; existing files are never overwritten: %w", err)
	}
	complete := false
	defer func() {
		_ = f.Close()
		if !complete {
			_ = os.Remove(output)
		}
	}()
	hash := sha256.New()
	n, err := io.Copy(io.MultiWriter(f, hash), io.LimitReader(res.Body, result.Size+1))
	if err != nil {
		return "", err
	}
	if n != result.Size || hex.EncodeToString(hash.Sum(nil)) != result.SHA256 {
		return "", errors.New("downloaded result failed size or SHA-256 verification")
	}
	if err = f.Close(); err != nil {
		return "", err
	}
	complete = true
	return output, nil
}

func PublicationFromEnv(s *model.Spec, env map[string]string) error {
	// An explicit local mode must not forward ambient storage credentials.
	if s.Publish == "" || s.Publish == "s3" {
		s.Storage = StorageFromEnv(env)
	} else {
		s.Storage = model.Storage{}
	}
	if s.PublicationMode() == "github" {
		s.GitHub.Token = Env(env, "KILNBRIDGE_GITHUB_TOKEN")
		if s.GitHub.Token == "" {
			s.GitHub.Token = Env(env, "GITHUB_TOKEN")
		}
		if s.GitHub.Token == "" {
			s.GitHub.Token = Env(env, "GH_TOKEN")
		}
	} else {
		s.GitHub = model.GitHubRelease{}
	}
	return s.ValidatePublication()
}
