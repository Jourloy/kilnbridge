package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/jourloy/kilnbridge/internal/build"
	"github.com/jourloy/kilnbridge/internal/model"
	"github.com/jourloy/kilnbridge/internal/platform"
)

type Manager struct {
	mu          sync.Mutex
	wg          sync.WaitGroup
	dir         string
	config      Config
	secrets     secrets
	jobs        map[string]model.Job
	active      string
	cancel      context.CancelFunc
	signal      chan struct{}
	toolsStatus string
	toolsLog    string
	uiSeen      time.Time
	incoming    int
	closing     bool
	ctx         context.Context
	buildJob    func(context.Context, build.Runner, string, string, model.Spec) ([]model.Artifact, error)
	uploadJob   func(context.Context, string, string, model.Spec, []model.Artifact, func(string)) error
}

func NewManager(ctx context.Context, dir string) (*Manager, error) {
	c, s, e := loadConfig(dir)
	if e != nil {
		return nil, e
	}
	m := &Manager{dir: dir, config: c, secrets: s, jobs: map[string]model.Job{}, signal: make(chan struct{}, 1), ctx: ctx, toolsStatus: "Not checked"}
	m.buildJob = func(ctx context.Context, r build.Runner, dir, id string, s model.Spec) ([]model.Artifact, error) {
		return r.Build(ctx, dir, id, s)
	}
	m.uploadJob = build.Upload
	if e = os.MkdirAll(filepath.Join(dir, "jobs"), 0700); e != nil {
		return nil, e
	}
	entries, e := os.ReadDir(filepath.Join(dir, "jobs"))
	if e != nil {
		return nil, e
	}
	for _, entry := range entries {
		if !entry.IsDir() || !model.IDPattern.MatchString(entry.Name()) {
			continue
		}
		b, e := os.ReadFile(filepath.Join(dir, "jobs", entry.Name(), "job.json"))
		if e != nil {
			continue
		}
		var j model.Job
		if json.Unmarshal(b, &j) != nil || j.ID != entry.Name() {
			continue
		}
		if j.Status == "running" {
			j.Status = "failed"
			j.Stage = "Interrupted"
			j.Error = "The agent stopped during this build. Submit a new source snapshot."
			j.UpdatedAt = time.Now().UTC()
			_ = os.Remove(filepath.Join(m.jobDir(j.ID), "spec.bin"))
			_ = platform.WriteJSON(filepath.Join(m.jobDir(j.ID), "job.json"), j)
		}
		m.jobs[j.ID] = j
	}
	return m, nil
}
func (m *Manager) jobDir(id string) string { return filepath.Join(m.dir, "jobs", id) }
func (m *Manager) wake() {
	select {
	case m.signal <- struct{}{}:
	default:
	}
}
func (m *Manager) Start()            { m.wg.Add(1); go func() { defer m.wg.Done(); m.loop() }(); m.wake() }
func (m *Manager) Wait()             { m.wg.Wait() }
func (m *Manager) Config() Config    { m.mu.Lock(); defer m.mu.Unlock(); return m.config }
func (m *Manager) List() []model.Job { m.mu.Lock(); defer m.mu.Unlock(); return m.listLocked() }
func (m *Manager) listLocked() []model.Job {
	list := make([]model.Job, 0, len(m.jobs))
	for _, j := range m.jobs {
		list = append(list, j)
	}
	sort.Slice(list, func(i, j int) bool { return list[i].CreatedAt.After(list[j].CreatedAt) })
	return list
}
func (m *Manager) Get(id string) (model.Job, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	j, ok := m.jobs[id]
	return j, ok
}
func (m *Manager) Available() error { m.mu.Lock(); defer m.mu.Unlock(); return m.availableLocked() }
func (m *Manager) availableLocked() error {
	if m.closing {
		return errors.New("agent is shutting down")
	}
	if m.config.Paused {
		return errors.New("new builds are paused")
	}
	n := 0
	for _, j := range m.jobs {
		if j.Active() {
			n++
		}
	}
	if n >= 20 {
		return errors.New("build queue is full")
	}
	return nil
}
func (m *Manager) Add(archive string, s model.Spec) (model.Job, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if e := m.availableLocked(); e != nil {
		return model.Job{}, e
	}
	if e := s.Validate(); e != nil {
		return model.Job{}, e
	}
	j := model.Job{ID: platform.ID(), Project: s.Project, Version: s.Version, Status: "queued", Stage: "Queued", CreatedAt: time.Now().UTC(), UpdatedAt: time.Now().UTC()}
	dir := m.jobDir(j.ID)
	if e := os.Mkdir(dir, 0700); e != nil {
		return j, e
	}
	b, e := json.Marshal(s)
	if e == nil {
		b, e = platform.Protect(b)
	}
	if e == nil {
		e = platform.AtomicWrite(filepath.Join(dir, "spec.bin"), b, 0600)
	}
	if e == nil {
		e = os.Rename(archive, filepath.Join(dir, "source.zip"))
	}
	if e == nil {
		e = platform.WriteJSON(filepath.Join(dir, "job.json"), j)
	}
	if e != nil {
		_ = os.RemoveAll(dir)
		return j, e
	}
	m.jobs[j.ID] = j
	m.wake()
	return j, nil
}
func (m *Manager) loadSpec(id string) (model.Spec, error) {
	var s model.Spec
	b, e := os.ReadFile(filepath.Join(m.jobDir(id), "spec.bin"))
	if e == nil {
		b, e = platform.Unprotect(b)
	}
	if e == nil {
		e = json.Unmarshal(b, &s)
	}
	return s, e
}
func (m *Manager) change(id string, f func(*model.Job)) {
	m.mu.Lock()
	defer m.mu.Unlock()
	j, ok := m.jobs[id]
	if !ok {
		return
	}
	f(&j)
	j.UpdatedAt = time.Now().UTC()
	m.jobs[id] = j
	_ = platform.WriteJSON(filepath.Join(m.jobDir(id), "job.json"), j)
}

func (m *Manager) loop() {
	ticker := time.NewTicker(time.Hour)
	defer ticker.Stop()
	m.prune()
	for {
		select {
		case <-m.ctx.Done():
			return
		case <-ticker.C:
			m.prune()
		case <-m.signal:
			for {
				if m.ctx.Err() != nil {
					return
				}
				m.mu.Lock()
				var selected model.Job
				if !m.closing && m.active == "" {
					for _, j := range m.jobs {
						if j.Status == "queued" && (selected.ID == "" || j.CreatedAt.Before(selected.CreatedAt)) {
							selected = j
						}
					}
				}
				if selected.ID == "" {
					m.mu.Unlock()
					break
				}
				m.active = selected.ID
				ctx, cancel := context.WithTimeout(m.ctx, 4*time.Hour)
				m.cancel = cancel
				m.mu.Unlock()
				m.execute(ctx, selected)
				cancel()
				m.mu.Lock()
				m.active = ""
				m.cancel = nil
				m.mu.Unlock()
			}
		}
	}
}
func (m *Manager) execute(ctx context.Context, j model.Job) {
	s, e := m.loadSpec(j.ID)
	if e != nil {
		m.change(j.ID, func(j *model.Job) { j.Status = "failed"; j.Error = "Could not decrypt the saved job settings." })
		return
	}
	replacer := strings.NewReplacer(s.Storage.SecretKey, "[redacted]", s.Storage.AccessKey, "[redacted]")
	var logMu sync.Mutex
	log := func(line string) {
		logMu.Lock()
		defer logMu.Unlock()
		line = replacer.Replace(line)
		if s.Storage.SessionToken != "" {
			line = strings.ReplaceAll(line, s.Storage.SessionToken, "[redacted]")
		}
		m.appendLog(j.ID, line)
	}
	uploadOnly := j.Stage == "Retrying upload"
	m.change(j.ID, func(j *model.Job) { j.Status = "running"; j.Error = "" })
	artifacts := j.Artifacts
	if !uploadOnly {
		r := build.Runner{Tools: filepath.Join(m.dir, "tools"), Log: log, Stage: func(stage string) { m.change(j.ID, func(j *model.Job) { j.Stage = stage }) }}
		artifacts, e = m.buildJob(ctx, r, m.jobDir(j.ID), j.ID, s)
		if e == nil {
			m.change(j.ID, func(j *model.Job) { j.Artifacts = artifacts })
		}
	}
	packaged := e == nil
	if e == nil {
		m.change(j.ID, func(j *model.Job) { j.Stage = "Uploading to storage" })
		e = m.uploadJob(ctx, filepath.Join(m.jobDir(j.ID), "artifacts"), j.ID, s, artifacts, log)
	}
	if e != nil {
		message := replacer.Replace(e.Error())
		if s.Storage.SessionToken != "" {
			message = strings.ReplaceAll(message, s.Storage.SessionToken, "[redacted]")
		}
		log(message)
		m.change(j.ID, func(j *model.Job) {
			j.Status = "failed"
			if packaged {
				j.Status = "upload_failed"
			}
			if ctx.Err() != nil {
				j.Status = "cancelled"
			}
			j.Stage = "Stopped"
			j.Error = message
		})
		if !packaged || ctx.Err() != nil {
			_ = os.Remove(filepath.Join(m.jobDir(j.ID), "spec.bin"))
		}
		return
	}
	m.change(j.ID, func(j *model.Job) {
		j.Status = "succeeded"
		j.Stage = "Complete"
		j.Destination = fmt.Sprintf("s3://%s/%s/%s/", s.Storage.Bucket, s.Storage.Prefix, j.ID)
	})
	_ = os.Remove(filepath.Join(m.jobDir(j.ID), "spec.bin"))
	_ = os.Remove(filepath.Join(m.jobDir(j.ID), "source.zip"))
	_ = os.RemoveAll(filepath.Join(m.jobDir(j.ID), "source"))
}
func (m *Manager) appendLog(id, line string) {
	p := filepath.Join(m.jobDir(id), "build.log")
	f, e := os.OpenFile(p, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0600)
	if e != nil {
		return
	}
	defer f.Close()
	info, e := f.Stat()
	if e == nil && info.Size() > 16<<20 {
		return
	}
	fmt.Fprintf(f, "%s  %s\n", time.Now().UTC().Format("15:04:05"), line)
}
func (m *Manager) Log(id string) (string, error) {
	if _, ok := m.Get(id); !ok {
		return "", os.ErrNotExist
	}
	f, e := os.Open(filepath.Join(m.jobDir(id), "build.log"))
	if os.IsNotExist(e) {
		return "", nil
	}
	if e != nil {
		return "", e
	}
	defer f.Close()
	info, e := f.Stat()
	if e != nil {
		return "", e
	}
	if info.Size() > 128<<10 {
		_, _ = f.Seek(-(128 << 10), 2)
	}
	b := make([]byte, 128<<10)
	n, _ := f.Read(b)
	return string(b[:n]), nil
}
func (m *Manager) Cancel(id string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	j, ok := m.jobs[id]
	if !ok {
		return os.ErrNotExist
	}
	if !j.Active() {
		return errors.New("job is already finished")
	}
	if m.active == id && m.cancel != nil {
		m.cancel()
		return nil
	}
	j.Status = "cancelled"
	j.Stage = "Cancelled"
	j.UpdatedAt = time.Now().UTC()
	m.jobs[id] = j
	_ = os.Remove(filepath.Join(m.jobDir(id), "spec.bin"))
	return platform.WriteJSON(filepath.Join(m.jobDir(id), "job.json"), j)
}
func (m *Manager) Retry(id string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if e := m.availableLocked(); e != nil {
		return e
	}
	j, ok := m.jobs[id]
	if !ok {
		return os.ErrNotExist
	}
	if j.Status != "upload_failed" {
		return errors.New("only failed uploads can be retried")
	}
	j.Status = "queued"
	j.Stage = "Retrying upload"
	j.UpdatedAt = time.Now().UTC()
	if e := platform.WriteJSON(filepath.Join(m.jobDir(id), "job.json"), j); e != nil {
		return e
	}
	m.jobs[id] = j
	m.wake()
	return nil
}
func (m *Manager) Prepare() error {
	m.mu.Lock()
	if m.closing || m.active != "" {
		m.mu.Unlock()
		return errors.New("wait for the current operation to finish")
	}
	m.active = "tools"
	m.toolsStatus = "Preparing"
	m.toolsLog = ""
	m.wg.Add(1)
	m.mu.Unlock()
	go func() {
		defer m.wg.Done()
		ctx, cancel := context.WithTimeout(m.ctx, 2*time.Hour)
		defer cancel()
		r := build.Runner{Tools: filepath.Join(m.dir, "tools"), Log: func(s string) {
			m.mu.Lock()
			defer m.mu.Unlock()
			m.toolsLog += s + "\n"
			if len(m.toolsLog) > 64000 {
				m.toolsLog = m.toolsLog[len(m.toolsLog)-64000:]
			}
		}}
		e := r.Prepare(ctx, model.DefaultFlutterVersion)
		m.mu.Lock()
		m.toolsStatus = "Ready"
		if e != nil {
			m.toolsStatus = "Needs attention"
			m.toolsLog += e.Error() + "\n"
		}
		m.active = ""
		m.mu.Unlock()
		m.wake()
	}()
	return nil
}
func (m *Manager) prune() {
	m.mu.Lock()
	defer m.mu.Unlock()
	cutoff := time.Now().Add(-time.Duration(m.config.RetentionDays) * 24 * time.Hour)
	for id, j := range m.jobs {
		if !j.Active() && m.active != id && j.UpdatedAt.Before(cutoff) {
			if os.RemoveAll(m.jobDir(id)) == nil {
				delete(m.jobs, id)
			}
		}
	}
}
func (m *Manager) ReserveUpdate() bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.active != "" || m.incoming != 0 || m.closing || time.Since(m.uiSeen) < 20*time.Second {
		return false
	}
	for _, j := range m.jobs {
		if j.Active() {
			return false
		}
	}
	m.closing = true
	return true
}
func (m *Manager) ReleaseUpdate() { m.mu.Lock(); m.closing = false; m.mu.Unlock(); m.wake() }

func (m *Manager) ReserveStop() bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.active != "" || m.incoming != 0 || m.closing {
		return false
	}
	for _, j := range m.jobs {
		if j.Active() {
			return false
		}
	}
	m.closing = true
	return true
}

func (m *Manager) BeginSubmission() error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if err := m.availableLocked(); err != nil {
		return err
	}
	if m.incoming >= 4 {
		return errors.New("source upload capacity is full; retry after a transfer finishes")
	}
	m.incoming++
	return nil
}
func (m *Manager) EndSubmission() { m.mu.Lock(); m.incoming--; m.mu.Unlock() }
