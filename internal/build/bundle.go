package build

import (
	"archive/zip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/jourloy/kilnbridge/internal/model"
)

// CreateBundle retains a deterministic, verified transfer archive outside the
// artifacts directory. Downloading and retrying publication use the same bytes.
func CreateBundle(ctx context.Context, out, destination string, artifacts []model.Artifact) (model.Result, error) {
	var result model.Result
	if len(artifacts) == 0 {
		return result, errors.New("no artifacts to bundle")
	}
	root, err := os.OpenRoot(out)
	if err != nil {
		return result, err
	}
	defer root.Close()
	files := append([]model.Artifact{}, artifacts...)
	sort.Slice(files, func(i, j int) bool { return files[i].Name < files[j].Name })
	seen := map[string]bool{}
	for _, a := range files {
		key := strings.ToLower(a.Name)
		if !model.SafeRelativeName(a.Name) || model.ArtifactMetadataPath(a.Name) || seen[key] || !model.SHA256Pattern.MatchString(a.SHA256) {
			return result, fmt.Errorf("invalid bundle artifact: %q", a.Name)
		}
		seen[key] = true
	}
	files = append(files, model.Artifact{Name: "SHA256SUMS.txt"}, model.Artifact{Name: "manifest.json"})
	temp, err := os.CreateTemp(filepath.Dir(destination), ".result-*.zip")
	if err != nil {
		return result, err
	}
	defer os.Remove(temp.Name())
	defer temp.Close()
	hash := sha256.New()
	z := zip.NewWriter(io.MultiWriter(temp, hash))
	for _, a := range files {
		if err := bundleFile(ctx, root, z, a); err != nil {
			_ = z.Close()
			return result, err
		}
	}
	if err = z.Close(); err != nil {
		return result, err
	}
	info, err := temp.Stat()
	if err != nil {
		return result, err
	}
	if err = temp.Close(); err != nil {
		return result, err
	}
	if err = os.Rename(temp.Name(), destination); err != nil {
		return result, err
	}
	return model.Result{Name: filepath.Base(destination), SHA256: hex.EncodeToString(hash.Sum(nil)), Size: info.Size()}, nil
}

func bundleFile(ctx context.Context, root *os.Root, z *zip.Writer, a model.Artifact) error {
	info, err := root.Lstat(a.Name)
	if err != nil || !info.Mode().IsRegular() {
		return fmt.Errorf("bundle input %q must be a regular file", a.Name)
	}
	f, err := root.Open(a.Name)
	if err != nil {
		return err
	}
	defer f.Close()
	header := &zip.FileHeader{Name: a.Name, Method: zip.Deflate}
	header.SetMode(info.Mode().Perm())
	w, err := z.CreateHeader(header)
	if err != nil {
		return err
	}
	hash := sha256.New()
	if _, err = io.Copy(io.MultiWriter(w, hash), artifactReader{ctx: ctx, reader: f}); err != nil {
		return err
	}
	if a.SHA256 != "" && hex.EncodeToString(hash.Sum(nil)) != a.SHA256 {
		return fmt.Errorf("artifact %q changed after packaging", a.Name)
	}
	return nil
}
