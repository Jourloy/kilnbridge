package client

import (
	"archive/zip"
	"bytes"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"

	"github.com/jourloy/kilnbridge/internal/build"
)

type SourceInfo struct {
	Type   string `json:"sourceType"`
	SHA    string `json:"sourceSha,omitempty"`
	Dirty  bool   `json:"sourceDirty,omitempty"`
	Digest string `json:"archiveSha256"`
}

// Snapshot preserves the Git-only entry point for existing callers.
func Snapshot(source, output string, includeFiles ...string) (string, bool, string, error) {
	info, err := PrepareSource("git", source, output, includeFiles...)
	return info.SHA, info.Dirty, info.Digest, err
}

// PrepareSource freezes source bytes before metadata inference and upload.
// ZIP input is copied byte-for-byte; folder/Git inputs use the same snapshot filters.
func PrepareSource(kind, source, output string, includeFiles ...string) (info SourceInfo, err error) {
	if kind == "" {
		kind = "git"
	}
	info.Type = kind
	if kind != "git" && kind != "folder" && kind != "zip" {
		return info, errors.New("--source-type must be git, folder or zip")
	}
	source, err = filepath.Abs(source)
	if err != nil {
		return info, err
	}
	output, err = filepath.Abs(output)
	if err != nil {
		return info, err
	}
	if sameSourcePath(source, output) || sameSourcePath(source, output+".json") {
		return info, errors.New("source must differ from the output ZIP and its metadata sidecar")
	}
	if kind == "zip" && len(includeFiles) != 0 {
		return info, errors.New("snapshotIncludes applies to Git/folder snapshots; prepare all required files inside the ZIP")
	}
	var root *os.Root
	var names []string
	var entries map[string]os.FileInfo
	if kind != "zip" {
		root, err = os.OpenRoot(source)
		if err != nil {
			return info, fmt.Errorf("source must be a directory: %w", err)
		}
		defer root.Close()
		includes := map[string]bool{}
		for _, name := range includeFiles {
			if !build.SafeArchiveName(name) || strings.HasSuffix(name, "/") {
				return info, fmt.Errorf("invalid snapshot include: %q", name)
			}
			entry, e := sourceEntry(root, name)
			if e != nil {
				return info, e
			}
			if !entry.Mode().IsRegular() {
				return info, fmt.Errorf("snapshot include must be an exact file: %q", name)
			}
			includes[name] = true
		}
		selected := map[string]bool{}
		if kind == "git" {
			b, e := exec.Command("git", "-C", source, "rev-parse", "HEAD").Output()
			if e != nil {
				return info, errors.New("source must have a Git commit; use --source-type folder for a directory without Git")
			}
			info.SHA = strings.TrimSpace(string(b))
			b, e = exec.Command("git", "-C", source, "status", "--porcelain", "--untracked-files=normal", "--", ".").Output()
			if e != nil {
				return info, e
			}
			info.Dirty = len(bytes.TrimSpace(b)) > 0
			b, e = exec.Command("git", "-C", source, "ls-files", "-z", "--cached", "--others", "--exclude-standard", "--", ".").Output()
			if e != nil {
				return info, e
			}
			for _, name := range strings.Split(string(b), "\x00") {
				if name != "" && !Excluded(name) {
					selected[name] = true
				}
			}
			for name := range includes {
				if exec.Command("git", "-C", source, "ls-files", "--error-unmatch", "--", name).Run() != nil {
					info.Dirty = true
				}
			}
		} else {
			err = fs.WalkDir(root.FS(), ".", func(name string, d fs.DirEntry, walkErr error) error {
				if walkErr != nil {
					return walkErr
				}
				if name == "." {
					return nil
				}
				if Excluded(name) {
					if d.IsDir() {
						for include := range includes {
							if strings.HasPrefix(include, name+"/") {
								return nil
							}
						}
						return fs.SkipDir
					}
					return nil
				}
				selected[name] = true
				return nil
			})
			if err != nil {
				return info, err
			}
		}
		for name := range includes {
			selected[name] = true
		}
		entries = map[string]os.FileInfo{}
		var size uint64
		for name := range selected {
			p := filepath.Join(source, filepath.FromSlash(name))
			if sameSourcePath(p, output) || sameSourcePath(p, output+".json") {
				if includes[name] {
					return info, errors.New("snapshotIncludes must not include the snapshot output")
				}
				continue
			}
			entry, e := sourceEntry(root, name)
			if os.IsNotExist(e) && kind == "git" && !includes[name] {
				continue
			} // Deleted tracked file.
			if e != nil {
				return info, e
			}
			if !entry.Mode().IsRegular() && !(kind == "folder" && entry.IsDir()) {
				return info, fmt.Errorf("source contains a symbolic link, submodule or special file: %q", name)
			}
			if entry.Mode().IsRegular() {
				if uint64(entry.Size()) > build.MaxExtractedBytes-size {
					return info, errors.New("source exceeds the 8 GiB extracted limit")
				}
				size += uint64(entry.Size())
			}
			entries[name] = entry
			names = append(names, name)
			if len(names) > 100000 {
				return info, errors.New("source exceeds the 100000-entry limit")
			}
		}
		if len(names) == 0 {
			return info, errors.New("source contains no included files or directories")
		}
		sort.Strings(names)
	}
	temp, err := os.CreateTemp(filepath.Dir(output), ".kilnbridge-source-*.zip")
	if err != nil {
		return info, err
	}
	defer os.Remove(temp.Name())
	defer temp.Close()
	if kind == "zip" {
		err = copySourceZIP(source, temp)
	} else {
		z := zip.NewWriter(&sourceWriter{w: temp, remaining: build.MaxArchiveBytes})
		for _, name := range names {
			if entries[name].IsDir() {
				_, err = z.Create(name + "/")
			} else {
				err = addSnapshotFile(z, root, name, entries[name])
			}
			if err != nil {
				break
			}
		}
		closeErr := z.Close()
		if err == nil {
			err = closeErr
		}
	}
	if err != nil {
		return info, err
	}
	if err = temp.Close(); err != nil {
		return info, err
	}
	if err = build.ValidateArchive(temp.Name()); err != nil {
		return info, err
	}
	info.Digest, err = build.FileSHA256(temp.Name())
	if err != nil {
		return info, err
	}
	err = os.Rename(temp.Name(), output)
	return info, err
}

func sourceEntry(root *os.Root, name string) (os.FileInfo, error) {
	if !build.SafeArchiveName(name) {
		return nil, fmt.Errorf("source contains a filename Windows cannot safely extract: %q", name)
	}
	parts := strings.Split(name, "/")
	var info os.FileInfo
	for i := range parts {
		var err error
		info, err = root.Lstat(filepath.FromSlash(strings.Join(parts[:i+1], "/")))
		if err != nil {
			return nil, err
		}
		if info.Mode()&os.ModeSymlink != 0 {
			return nil, fmt.Errorf("source contains a symbolic link: %q", name)
		}
	}
	return info, nil
}

func sameSourcePath(a, b string) bool {
	if a == b {
		return true
	}
	x, errA := os.Stat(a)
	y, errB := os.Stat(b)
	return errA == nil && errB == nil && os.SameFile(x, y)
}

func copySourceZIP(source string, destination *os.File) error {
	f, err := os.Open(source)
	if err != nil {
		return err
	}
	defer f.Close()
	before, err := f.Stat()
	if err != nil {
		return err
	}
	if !before.Mode().IsRegular() || before.Size() > build.MaxArchiveBytes {
		return errors.New("source ZIP must be a regular file no larger than 2 GiB")
	}
	n, err := io.Copy(destination, io.LimitReader(f, before.Size()+1))
	if err != nil {
		return err
	}
	after, err := f.Stat()
	if err != nil {
		return err
	}
	if n != before.Size() || after.Size() != before.Size() || !after.ModTime().Equal(before.ModTime()) {
		return errors.New("ZIP changed while copying; retry after the producer finishes")
	}
	return nil
}

type sourceWriter struct {
	w         io.Writer
	remaining int64
}

func (w *sourceWriter) Write(p []byte) (int, error) {
	if int64(len(p)) > w.remaining {
		return 0, errors.New("source ZIP exceeds the 2 GiB limit")
	}
	n, err := w.w.Write(p)
	w.remaining -= int64(n)
	return n, err
}

// SourceFile reads metadata from the frozen archive, irrespective of input type.
func SourceFile(archive, name string) ([]byte, error) {
	if !build.SafeArchiveName(name) {
		return nil, errors.New("invalid source metadata path")
	}
	z, err := zip.OpenReader(archive)
	if err != nil {
		return nil, err
	}
	defer z.Close()
	for _, f := range z.File {
		if f.Name != name {
			continue
		}
		if !f.Mode().IsRegular() || f.UncompressedSize64 > 1<<20 {
			return nil, errors.New("source metadata must be a regular file up to 1 MiB")
		}
		r, err := f.Open()
		if err != nil {
			return nil, err
		}
		defer r.Close()
		return io.ReadAll(io.LimitReader(r, (1<<20)+1))
	}
	return nil, fmt.Errorf("source archive is missing %s; check workDir", name)
}
