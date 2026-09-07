package agent

import (
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"crypto/tls"
	"embed"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"time"

	"github.com/jourloy/kilnbridge/internal/build"
	"github.com/jourloy/kilnbridge/internal/model"
	"github.com/jourloy/kilnbridge/internal/platform"
	"github.com/jourloy/kilnbridge/internal/update"
)

//go:embed ui/*
var ui embed.FS

type Endpoint struct {
	URL string `json:"url"`
	PID int    `json:"pid"`
}
type UpdateState struct {
	Status    string    `json:"status"`
	Version   string    `json:"version,omitempty"`
	CheckedAt time.Time `json:"checkedAt,omitempty"`
	Error     string    `json:"error,omitempty"`
}
type App struct {
	M              *Manager
	Version        string
	Pin            string
	LocalURL       string
	Shutdown       context.CancelFunc
	updateMu       sync.Mutex
	updates        UpdateState
	pending        *update.Pending
	checking       bool
	buildSupported bool
}

func NewApp(ctx context.Context, dir, version string) (*App, error) {
	m, e := NewManager(ctx, dir)
	if e != nil {
		return nil, e
	}
	return &App{M: m, Version: version, updates: UpdateState{Status: "Not checked"}, buildSupported: runtime.GOOS == "windows"}, nil
}
func (a *App) Run(ctx context.Context, nonce string) error {
	ctx, stop := context.WithCancel(ctx)
	defer stop()
	a.M.ctx = ctx
	cfg := a.M.Config()
	tlsConfig, pin, e := a.M.secrets.TLS()
	if e != nil {
		return e
	}
	a.Pin = pin
	remote, e := net.Listen("tcp", cfg.Listen)
	if e != nil {
		return fmt.Errorf("cannot listen on %s: %w", cfg.Listen, e)
	}
	defer remote.Close()
	local, e := net.Listen("tcp", "127.0.0.1:0")
	if e != nil {
		return e
	}
	defer local.Close()
	a.LocalURL = "http://" + local.Addr().String()
	localServer := &http.Server{Handler: a.LocalHandler(local.Addr().String()), ReadHeaderTimeout: 5 * time.Second, IdleTimeout: 30 * time.Second, MaxHeaderBytes: 16384}
	remoteServer := &http.Server{Handler: a.RemoteHandler(), ReadHeaderTimeout: 10 * time.Second, IdleTimeout: 30 * time.Second, MaxHeaderBytes: 16384}
	errCh := make(chan error, 2)
	go func() { errCh <- localServer.Serve(local) }()
	go func() { errCh <- remoteServer.Serve(tls.NewListener(remote, tlsConfig)) }()
	if e = platform.WriteJSON(filepath.Join(a.M.dir, "endpoint.json"), Endpoint{URL: a.LocalURL, PID: os.Getpid()}); e != nil {
		return e
	}
	if nonce != "" {
		if len(nonce) != 64 {
			return errors.New("invalid update startup nonce")
		}
		if _, e = hex.DecodeString(nonce); e != nil {
			return e
		}
		if e = platform.AtomicWrite(filepath.Join(a.M.dir, "updates", "healthy-"+nonce), []byte(a.Version), 0600); e != nil {
			return e
		}
	}
	a.M.Start()
	go a.updateLoop(ctx)
	select {
	case <-ctx.Done():
		e = nil
	case e = <-errCh:
	}
	stopCtx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	_ = localServer.Shutdown(stopCtx)
	_ = remoteServer.Shutdown(stopCtx)
	stop()
	if a.Shutdown != nil {
		a.Shutdown()
	}
	a.M.Wait()
	_ = os.Remove(filepath.Join(a.M.dir, "endpoint.json"))
	return e
}

func authorized(r *http.Request, key string) bool {
	got := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
	if !strings.HasPrefix(r.Header.Get("Authorization"), "Bearer ") {
		return false
	}
	a := sha256.Sum256([]byte(got))
	b := sha256.Sum256([]byte(key))
	return subtle.ConstantTimeCompare(a[:], b[:]) == 1
}
func jsonResponse(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}
func problem(w http.ResponseWriter, status int, e error) {
	jsonResponse(w, status, map[string]string{"error": e.Error()})
}
func decode(w http.ResponseWriter, r *http.Request, v any) error {
	r.Body = http.MaxBytesReader(w, r.Body, 64<<10)
	d := json.NewDecoder(r.Body)
	d.DisallowUnknownFields()
	if e := d.Decode(v); e != nil {
		return e
	}
	var extra any
	if d.Decode(&extra) != io.EOF {
		return errors.New("request must contain one JSON object")
	}
	return nil
}

func (a *App) RemoteHandler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /v1/health", func(w http.ResponseWriter, r *http.Request) {
		jsonResponse(w, 200, map[string]any{"name": a.M.Config().Name, "version": a.Version, "platform": runtime.GOOS, "paused": a.M.Config().Paused})
	})
	mux.HandleFunc("GET /v1/jobs", func(w http.ResponseWriter, r *http.Request) { jsonResponse(w, 200, a.M.List()) })
	mux.HandleFunc("POST /v1/jobs", a.submit)
	mux.HandleFunc("GET /v1/jobs/{id}", a.job)
	mux.HandleFunc("POST /v1/jobs/{id}/cancel", a.cancel)
	mux.HandleFunc("POST /v1/jobs/{id}/retry-upload", a.retry)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Content-Type-Options", "nosniff")
		a.M.mu.Lock()
		key := a.M.secrets.Key
		a.M.mu.Unlock()
		if r.Header.Get("Origin") != "" || !authorized(r, key) {
			problem(w, 401, errors.New("a valid agent key is required"))
			return
		}
		mux.ServeHTTP(w, r)
	})
}
func (a *App) submit(w http.ResponseWriter, r *http.Request) {
	if !a.buildSupported {
		problem(w, 503, errors.New("this agent is running in development mode; Windows builds require Windows"))
		return
	}
	if e := a.M.Available(); e != nil {
		problem(w, 409, e)
		return
	}
	controller := http.NewResponseController(w)
	_ = controller.SetReadDeadline(time.Now().Add(30 * time.Minute))
	defer controller.SetReadDeadline(time.Time{})
	r.Body = http.MaxBytesReader(w, r.Body, build.MaxArchiveBytes+(128<<10))
	reader, e := r.MultipartReader()
	if e != nil {
		problem(w, 400, e)
		return
	}
	part, e := reader.NextPart()
	if e != nil || part.FormName() != "spec" {
		problem(w, 400, errors.New("first multipart field must be spec"))
		return
	}
	b, e := io.ReadAll(io.LimitReader(part, 65537))
	if e != nil || len(b) > 65536 {
		problem(w, 400, errors.New("job metadata is too large"))
		return
	}
	var s model.Spec
	if e = json.Unmarshal(b, &s); e != nil {
		problem(w, 400, e)
		return
	}
	if e = s.Validate(); e != nil {
		problem(w, 400, e)
		return
	}
	part, e = reader.NextPart()
	if e != nil || part.FormName() != "archive" {
		problem(w, 400, errors.New("second multipart field must be archive"))
		return
	}
	f, e := os.CreateTemp(a.M.dir, "incoming-*.zip")
	if e != nil {
		problem(w, 500, e)
		return
	}
	defer os.Remove(f.Name())
	h := sha256.New()
	n, e := io.Copy(io.MultiWriter(f, h), io.LimitReader(part, build.MaxArchiveBytes+1))
	closeErr := f.Close()
	if e != nil || closeErr != nil || n == 0 || n > build.MaxArchiveBytes {
		problem(w, 400, errors.New("archive upload failed or exceeded 2 GiB"))
		return
	}
	if hex.EncodeToString(h.Sum(nil)) != s.ArchiveSHA256 {
		problem(w, 400, errors.New("archive SHA-256 mismatch"))
		return
	}
	if _, e = reader.NextPart(); e != io.EOF {
		problem(w, 400, errors.New("unexpected multipart fields"))
		return
	}
	j, e := a.M.Add(f.Name(), s)
	if e != nil {
		problem(w, 409, e)
		return
	}
	jsonResponse(w, 202, j)
}
func (a *App) job(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	j, ok := a.M.Get(id)
	if !ok {
		problem(w, 404, os.ErrNotExist)
		return
	}
	log, e := a.M.Log(id)
	if e != nil {
		problem(w, 500, e)
		return
	}
	jsonResponse(w, 200, map[string]any{"job": j, "log": log})
}
func (a *App) cancel(w http.ResponseWriter, r *http.Request) {
	if e := a.M.Cancel(r.PathValue("id")); e != nil {
		problem(w, 409, e)
		return
	}
	jsonResponse(w, 200, map[string]bool{"ok": true})
}
func (a *App) retry(w http.ResponseWriter, r *http.Request) {
	if e := a.M.Retry(r.PathValue("id")); e != nil {
		problem(w, 409, e)
		return
	}
	jsonResponse(w, 202, map[string]bool{"ok": true})
}

func (a *App) LocalHandler(host string) http.Handler {
	mux := http.NewServeMux()
	files, _ := fs.Sub(ui, "ui")
	mux.Handle("/", http.FileServer(http.FS(files)))
	mux.HandleFunc("GET /admin/state", func(w http.ResponseWriter, r *http.Request) {
		a.M.mu.Lock()
		a.M.uiSeen = time.Now()
		cfg := a.M.config
		jobs := a.M.listLocked()
		tools := map[string]string{"status": a.M.toolsStatus, "log": a.M.toolsLog}
		a.M.mu.Unlock()
		a.updateMu.Lock()
		updates := a.updates
		a.updateMu.Unlock()
		jsonResponse(w, 200, map[string]any{"config": cfg, "version": a.Version, "platform": runtime.GOOS, "fingerprint": a.Pin, "addresses": addresses(cfg.Listen), "jobs": jobs, "tools": tools, "updates": updates})
	})
	mux.HandleFunc("POST /admin/stop", func(w http.ResponseWriter, r *http.Request) {
		if a.Shutdown == nil || !a.M.ReserveStop() {
			problem(w, 409, errors.New("finish or cancel accepted builds and tool setup before stopping the agent"))
			return
		}
		jsonResponse(w, 200, map[string]bool{"ok": true})
		a.Shutdown()
	})
	mux.HandleFunc("POST /admin/heartbeat", func(w http.ResponseWriter, r *http.Request) {
		a.M.mu.Lock()
		a.M.uiSeen = time.Now()
		a.M.mu.Unlock()
		jsonResponse(w, 200, map[string]bool{"ok": true})
	})
	mux.HandleFunc("GET /admin/key", func(w http.ResponseWriter, r *http.Request) {
		a.M.mu.Lock()
		defer a.M.mu.Unlock()
		jsonResponse(w, 200, map[string]string{"key": a.M.secrets.Key})
	})
	mux.HandleFunc("POST /admin/key/rotate", func(w http.ResponseWriter, r *http.Request) {
		a.M.mu.Lock()
		defer a.M.mu.Unlock()
		s := a.M.secrets
		s.Key = platform.Token()
		if e := saveSecrets(a.M.dir, s); e != nil {
			problem(w, 500, e)
			return
		}
		a.M.secrets = s
		jsonResponse(w, 200, map[string]string{"key": s.Key})
	})
	mux.HandleFunc("PUT /admin/config", func(w http.ResponseWriter, r *http.Request) {
		var c Config
		if e := decode(w, r, &c); e != nil {
			problem(w, 400, e)
			return
		}
		if e := c.Validate(); e != nil {
			problem(w, 400, e)
			return
		}
		a.M.mu.Lock()
		defer a.M.mu.Unlock()
		if c.Listen != a.M.config.Listen {
			problem(w, 400, errors.New("change the listener in config.json while the agent is stopped"))
			return
		}
		if c.AutoStart != a.M.config.AutoStart {
			exe, e := os.Executable()
			if e == nil {
				e = platform.SetAutostart(c.AutoStart, exe)
			}
			if e != nil {
				problem(w, 500, e)
				return
			}
		}
		if e := platform.WriteJSON(filepath.Join(a.M.dir, "config.json"), c); e != nil {
			problem(w, 500, e)
			return
		}
		a.M.config = c
		jsonResponse(w, 200, c)
	})
	mux.HandleFunc("POST /admin/tools/prepare", func(w http.ResponseWriter, r *http.Request) {
		if e := a.M.Prepare(); e != nil {
			problem(w, 409, e)
			return
		}
		jsonResponse(w, 202, map[string]bool{"ok": true})
	})
	mux.HandleFunc("GET /admin/jobs/{id}", a.job)
	mux.HandleFunc("POST /admin/jobs/{id}/cancel", a.cancel)
	mux.HandleFunc("POST /admin/jobs/{id}/retry-upload", a.retry)
	mux.HandleFunc("POST /admin/updates/check", func(w http.ResponseWriter, r *http.Request) {
		go a.checkUpdate(a.M.ctx)
		jsonResponse(w, 202, map[string]bool{"ok": true})
	})
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Security-Policy", "default-src 'self'; script-src 'self'; style-src 'self'; font-src 'self'; img-src 'self' data:; connect-src 'self'; frame-ancestors 'none'; base-uri 'none'; form-action 'none'")
		w.Header().Set("X-Frame-Options", "DENY")
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("Referrer-Policy", "no-referrer")
		w.Header().Set("Cache-Control", "no-store")
		if r.Host != host {
			problem(w, 403, errors.New("invalid local host"))
			return
		}
		if strings.HasPrefix(r.URL.Path, "/admin/") {
			if origin := r.Header.Get("Origin"); origin != "" && origin != "http://"+host {
				problem(w, 403, errors.New("invalid local origin"))
				return
			}
			a.M.mu.Lock()
			key := a.M.secrets.Admin
			a.M.mu.Unlock()
			if !authorized(r, key) {
				problem(w, 401, errors.New("open settings from the Kilnbridge application"))
				return
			}
		}
		mux.ServeHTTP(w, r)
	})
}
func addresses(listen string) []string {
	_, port, _ := net.SplitHostPort(listen)
	out := []string{}
	addrs, _ := net.InterfaceAddrs()
	for _, a := range addrs {
		ip, _, e := net.ParseCIDR(a.String())
		if e == nil && ip.To4() != nil && ip.IsPrivate() {
			out = append(out, "https://"+net.JoinHostPort(ip.String(), port))
		}
	}
	if len(out) == 0 {
		out = append(out, "https://127.0.0.1:"+port)
	}
	return out
}
func (a *App) SettingsURL() string {
	a.M.mu.Lock()
	defer a.M.mu.Unlock()
	return a.LocalURL + "/#" + a.M.secrets.Admin
}

func (a *App) checkUpdate(ctx context.Context) {
	a.updateMu.Lock()
	if a.checking {
		a.updateMu.Unlock()
		return
	}
	a.checking = true
	a.updates.Status = "Checking"
	a.updates.Error = ""
	a.updateMu.Unlock()
	p, e := update.Download(ctx, a.M.Config().Repository, a.Version, filepath.Join(a.M.dir, "updates"))
	a.updateMu.Lock()
	defer a.updateMu.Unlock()
	a.checking = false
	a.updates.CheckedAt = time.Now().UTC()
	if e != nil {
		a.updates.Status = "Could not check"
		a.updates.Error = e.Error()
		return
	}
	if p == nil {
		a.updates.Status = "Up to date"
		return
	}
	a.pending = p
	a.updates.Status = "Ready when idle"
	a.updates.Version = p.Version
}
func (a *App) updateLoop(ctx context.Context) {
	timer := time.NewTimer(30 * time.Second)
	defer timer.Stop()
	tick := time.NewTicker(15 * time.Second)
	defer tick.Stop()
	last := time.Time{}
	for {
		select {
		case <-ctx.Done():
			return
		case <-timer.C:
			if a.M.Config().AutoUpdate {
				last = time.Now()
				go a.checkUpdate(ctx)
			}
		case <-tick.C:
			if !a.M.Config().AutoUpdate {
				continue
			}
			if time.Since(last) > 6*time.Hour {
				last = time.Now()
				go a.checkUpdate(ctx)
			}
			a.updateMu.Lock()
			p := a.pending
			a.updateMu.Unlock()
			if p == nil || runtime.GOOS != "windows" {
				continue
			}
			exe, e := os.Executable()
			if e != nil || !strings.EqualFold(filepath.Clean(exe), update.InstalledPath(a.M.dir)) {
				continue
			}
			if a.M.ReserveUpdate() {
				if e = update.StartHelper(a.M.dir, p); e != nil {
					a.M.ReleaseUpdate()
					a.updateMu.Lock()
					a.updates.Status = "Could not install"
					a.updates.Error = e.Error()
					a.pending = nil
					a.updateMu.Unlock()
				} else {
					if a.Shutdown != nil {
						a.Shutdown()
					}
					return
				}
			}
		}
	}
}

// ExistingSettings reads only this application's per-user state. It never
// exposes the remote agent key on the command line or in the UI URL.
func ExistingSettings(dir string) (string, error) {
	b, e := os.ReadFile(filepath.Join(dir, "endpoint.json"))
	if e != nil {
		return "", e
	}
	_, s, e := loadConfig(dir)
	if e != nil {
		return "", e
	}
	var ep Endpoint
	if e = json.Unmarshal(b, &ep); e != nil {
		return "", e
	}
	if !strings.HasPrefix(ep.URL, "http://127.0.0.1:") {
		return "", errors.New("invalid local settings address")
	}
	req, e := http.NewRequest("GET", ep.URL+"/admin/key", nil)
	if e != nil {
		return "", e
	}
	req.Header.Set("Authorization", "Bearer "+s.Admin)
	client := &http.Client{Timeout: time.Second}
	res, e := client.Do(req)
	if e != nil {
		return "", e
	}
	defer res.Body.Close()
	if res.StatusCode != 200 {
		return "", errors.New("agent is not ready")
	}
	return ep.URL + "/#" + s.Admin, nil
}

func KeepSettingsOpen(ctx context.Context, address string) {
	base, key, ok := strings.Cut(address, "/#")
	if !ok {
		return
	}
	client := &http.Client{Timeout: 3 * time.Second}
	ticker := time.NewTicker(5 * time.Second)
	defer ticker.Stop()
	for {
		req, e := http.NewRequestWithContext(ctx, "POST", base+"/admin/heartbeat", nil)
		if e == nil {
			req.Header.Set("Authorization", "Bearer "+key)
			if res, e := client.Do(req); e == nil {
				_ = res.Body.Close()
			}
		}
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}
