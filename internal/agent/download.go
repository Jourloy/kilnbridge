package agent

import (
	"errors"
	"mime"
	"net/http"
	"os"
	"path/filepath"

	"github.com/jourloy/kilnbridge/internal/model"
)

// Hold retention and shutdown while serving the immutable result archive.
func (m *Manager) beginDownload(id string) (model.Job, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	j, ok := m.jobs[id]
	if !ok {
		return j, os.ErrNotExist
	}
	if m.closing || m.outgoing >= 4 {
		return j, errors.New("download capacity unavailable; retry after current transfers finish")
	}
	if j.Result == nil {
		return j, errors.New("this job has no downloadable result yet")
	}
	m.outgoing++
	m.downloads[id]++
	return j, nil
}

func (m *Manager) endDownload(id string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.outgoing--
	m.downloads[id]--
	if m.downloads[id] == 0 {
		delete(m.downloads, id)
	}
}

func (a *App) result(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	j, err := a.M.beginDownload(id)
	if err != nil {
		status := http.StatusConflict
		if errors.Is(err, os.ErrNotExist) {
			status = http.StatusNotFound
		}
		problem(w, status, err)
		return
	}
	defer a.M.endDownload(id)
	root, err := os.OpenRoot(a.M.jobDir(id))
	if err != nil {
		problem(w, 404, errors.New("result is no longer retained on the agent"))
		return
	}
	defer root.Close()
	info, err := root.Lstat("result.zip")
	if err != nil || !info.Mode().IsRegular() || info.Size() != j.Result.Size {
		problem(w, 409, errors.New("the retained result is missing or changed"))
		return
	}
	f, err := root.Open("result.zip")
	if err != nil {
		problem(w, 404, errors.New("result is unavailable"))
		return
	}
	defer f.Close()
	w.Header().Set("Cache-Control", "private, no-store")
	w.Header().Set("Content-Type", "application/zip")
	w.Header().Set("Content-Disposition", mime.FormatMediaType("attachment", map[string]string{"filename": filepath.Base(j.Result.Name)}))
	w.Header().Set("ETag", `"`+j.Result.SHA256+`"`)
	http.ServeContent(w, r, j.Result.Name, info.ModTime(), f)
}
