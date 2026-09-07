package build

import (
	"archive/zip"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/jourloy/kilnbridge/internal/model"
)

const MaxArchiveBytes int64 = 2 << 30
const MaxExtractedBytes uint64 = 8 << 30

func FileSHA256(p string) (string, error) {
	f, err := os.Open(p)
	if err != nil {
		return "", err
	}
	defer f.Close()
	h := sha256.New()
	if _, err = io.Copy(h, f); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

// Windows normalizes case, device names, trailing dots and spaces. Reject all
// ambiguous names even when validation runs on another operating system.
func SafeArchiveName(name string) bool {
	name = strings.TrimSuffix(name, "/")
	if !model.Relative(name) || name == "." {
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

func Extract(archive, destination string) error {
	r, err := zip.OpenReader(archive)
	if err != nil {
		return err
	}
	defer r.Close()
	if len(r.File) == 0 || len(r.File) > 100000 {
		return errors.New("archive must contain 1–100000 entries")
	}
	seen := map[string]bool{}
	var total uint64
	for _, f := range r.File {
		key := strings.ToLower(strings.TrimSuffix(f.Name, "/"))
		if !SafeArchiveName(f.Name) || seen[key] || f.Mode()&os.ModeSymlink != 0 || (!f.FileInfo().IsDir() && !f.Mode().IsRegular()) {
			return fmt.Errorf("unsafe or duplicate archive entry: %q", f.Name)
		}
		seen[key] = true
		if f.UncompressedSize64 > MaxExtractedBytes-total {
			return errors.New("archive expands beyond the 8 GiB limit")
		}
		total += f.UncompressedSize64
	}
	if err := os.Mkdir(destination, 0700); err != nil {
		return fmt.Errorf("extraction needs a fresh directory: %w", err)
	}
	for _, f := range r.File {
		target := filepath.Join(destination, filepath.FromSlash(f.Name))
		if f.FileInfo().IsDir() {
			if err := os.MkdirAll(target, 0700); err != nil {
				return err
			}
			continue
		}
		if err := os.MkdirAll(filepath.Dir(target), 0700); err != nil {
			return err
		}
		if err := extractFile(f, target); err != nil {
			return err
		}
	}
	return nil
}

func extractFile(f *zip.File, target string) error {
	r, err := f.Open()
	if err != nil {
		return err
	}
	defer r.Close()
	w, err := os.OpenFile(target, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		return err
	}
	n, copyErr := io.Copy(w, io.LimitReader(r, int64(f.UncompressedSize64)+1))
	closeErr := w.Close()
	if copyErr != nil {
		return copyErr
	}
	if closeErr != nil {
		return closeErr
	}
	if uint64(n) != f.UncompressedSize64 {
		return errors.New("archive entry size mismatch")
	}
	return nil
}

func ZipDirectory(source, destination string) error {
	f, err := os.Create(destination)
	if err != nil {
		return err
	}
	z := zip.NewWriter(f)
	err = filepath.WalkDir(source, func(p string, d os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if d.IsDir() {
			return nil
		}
		if d.Type()&os.ModeSymlink != 0 {
			return errors.New("build outputs must not contain symbolic links")
		}
		rel, err := filepath.Rel(source, p)
		if err != nil {
			return err
		}
		w, err := z.Create(filepath.ToSlash(rel))
		if err != nil {
			return err
		}
		r, err := os.Open(p)
		if err != nil {
			return err
		}
		defer r.Close()
		_, err = io.Copy(w, r)
		return err
	})
	zerr := z.Close()
	ferr := f.Close()
	if err != nil {
		return err
	}
	if zerr != nil {
		return zerr
	}
	return ferr
}
