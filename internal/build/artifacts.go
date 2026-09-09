package build

import (
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"strings"

	"github.com/jourloy/kilnbridge/internal/model"
)

// Copy selected outputs into the retained job directory, independent of the
// build workspace. Upload retries use these exact bytes and recorded hashes.
func collectArtifacts(ctx context.Context, jobDir, work, id string, s model.Spec) ([]model.Artifact, error) {
	if s.ArtifactPaths == nil {
		return nil, errors.New("explicit artifacts are required")
	}
	if err := s.ValidateArtifacts(); err != nil {
		return nil, err
	}
	root, err := os.OpenRoot(work)
	if err != nil {
		return nil, err
	}
	defer root.Close()
	var files []string
	for _, selected := range s.ArtifactPaths {
		// WalkDir does not inspect the ancestors of a selected nested file.
		for name := selected; name != "."; name = path.Dir(name) {
			info, err := root.Lstat(name)
			if err != nil {
				return nil, fmt.Errorf("artifact %q: %w", selected, err)
			}
			if info.Mode()&os.ModeSymlink != 0 {
				return nil, fmt.Errorf("artifact %q contains a symbolic link", selected)
			}
		}
		selectedFiles, err := artifactFiles(ctx, root, selected, false)
		if err != nil {
			return nil, err
		}
		if len(selectedFiles) == 0 {
			return nil, fmt.Errorf("artifact directory %q contains no files", selected)
		}
		files = append(files, selectedFiles...)
	}
	out := filepath.Join(jobDir, "artifacts")
	if err := os.Mkdir(out, 0700); err != nil {
		return nil, err
	}
	destination, err := os.OpenRoot(out)
	if err != nil {
		return nil, err
	}
	defer destination.Close()
	for _, name := range files {
		if err := copyArtifact(ctx, root, destination, name); err != nil {
			return nil, fmt.Errorf("artifact %q: %w", name, err)
		}
	}
	return Manifest(out, id, s)
}

// Walk through an os.Root so a concurrently replaced path cannot escape its
// source root. Links and special files are rejected, regardless of extension.
func artifactFiles(ctx context.Context, root *os.Root, selected string, skipMetadata bool) ([]string, error) {
	var files []string
	seen := map[string]bool{}
	err := fs.WalkDir(root.FS(), selected, func(name string, entry fs.DirEntry, err error) error {
		if err != nil {
			return fmt.Errorf("artifact %q: %w", name, err)
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		if name == "." {
			return nil
		}
		if !model.SafeRelativeName(name) {
			return fmt.Errorf("invalid artifact filename: %q", name)
		}
		if entry.Type()&os.ModeSymlink != 0 || (!entry.IsDir() && !entry.Type().IsRegular()) {
			return fmt.Errorf("artifact %q must be a regular file or directory", name)
		}
		if model.ArtifactMetadataPath(name) {
			if skipMetadata && !entry.IsDir() {
				return nil
			}
			return fmt.Errorf("artifact %q conflicts with release metadata", name)
		}
		key := strings.ToLower(name)
		if seen[key] {
			return fmt.Errorf("case-insensitive artifact collision: %q", name)
		}
		seen[key] = true
		if !entry.IsDir() {
			files = append(files, name)
		}
		return nil
	})
	return files, err
}

func copyArtifact(ctx context.Context, source, destination *os.Root, name string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	r, err := source.Open(name)
	if err != nil {
		return err
	}
	defer r.Close()
	info, err := r.Stat()
	if err != nil {
		return err
	}
	if !info.Mode().IsRegular() {
		return errors.New("source is not a regular file")
	}
	if err := destination.MkdirAll(path.Dir(name), 0700); err != nil {
		return err
	}
	w, err := destination.OpenFile(name, os.O_CREATE|os.O_EXCL|os.O_WRONLY, info.Mode().Perm()|0600)
	if err != nil {
		return err
	}
	_, copyErr := io.Copy(w, artifactReader{ctx: ctx, reader: r})
	closeErr := w.Close()
	if copyErr != nil {
		return copyErr
	}
	return closeErr
}

type artifactReader struct {
	ctx    context.Context
	reader io.Reader
}

func (r artifactReader) Read(p []byte) (int, error) {
	if err := r.ctx.Err(); err != nil {
		return 0, err
	}
	return r.reader.Read(p)
}
