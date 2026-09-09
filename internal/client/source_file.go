package client

import (
	"archive/zip"
	"fmt"
	"io"
	"os"
	"path/filepath"
)

func addSnapshotFile(z *zip.Writer, root *os.Root, name string, before os.FileInfo) error {
	f, e := root.Open(filepath.FromSlash(name))
	if e != nil {
		return e
	}
	defer f.Close()
	h, e := zip.FileInfoHeader(before)
	if e != nil {
		return e
	}
	h.Name = name
	h.Method = zip.Deflate
	w, e := z.CreateHeader(h)
	if e != nil {
		return e
	}
	n, e := io.Copy(w, io.LimitReader(f, before.Size()+1))
	if e != nil {
		return e
	}
	after, e := f.Stat()
	if e != nil {
		return e
	}
	if !os.SameFile(before, after) || n != before.Size() || after.Size() != before.Size() || !after.ModTime().Equal(before.ModTime()) {
		return fmt.Errorf("%s changed while archiving; retry after edits finish", name)
	}
	return nil
}
