package client

import (
	"bufio"
	"bytes"
	"context"
	"crypto/sha256"
	"crypto/tls"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/jourloy/kilnbridge/internal/model"
)

type Client struct {
	URL  string
	Key  string
	HTTP *http.Client
}

func New(address, key, pin string) (*Client, error) {
	u, e := url.Parse(address)
	if e != nil || u.Scheme != "https" || u.Host == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" || (u.Path != "" && u.Path != "/") {
		return nil, errors.New("KILNBRIDGE_URL must be an HTTPS origin")
	}
	expected, e := hex.DecodeString(pin)
	if e != nil || len(expected) != 32 || key == "" {
		return nil, errors.New("KILNBRIDGE_KEY and KILNBRIDGE_PIN_SHA256 are required")
	}
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.TLSClientConfig = &tls.Config{MinVersion: tls.VersionTLS12, InsecureSkipVerify: true, // Certificate pin replaces public-CA/hostname validation for the private agent.
		VerifyConnection: func(cs tls.ConnectionState) error {
			if len(cs.PeerCertificates) == 0 {
				return errors.New("agent did not present a certificate")
			}
			cert := cs.PeerCertificates[0]
			sum := sha256.Sum256(cert.Raw)
			if !bytes.Equal(sum[:], expected) {
				return errors.New("agent certificate fingerprint does not match; no credentials were sent")
			}
			if time.Now().Before(cert.NotBefore) || time.Now().After(cert.NotAfter) {
				return errors.New("agent certificate has expired or is not valid yet")
			}
			return nil
		}}
	return &Client{URL: strings.TrimSuffix(address, "/"), Key: key, HTTP: &http.Client{Transport: transport, Timeout: 35 * time.Minute, CheckRedirect: func(*http.Request, []*http.Request) error { return errors.New("agent redirects are not allowed") }}}, nil
}
func (c *Client) Request(ctx context.Context, method, route string, body io.Reader, contentType string) ([]byte, error) {
	req, e := http.NewRequestWithContext(ctx, method, c.URL+route, body)
	if e != nil {
		return nil, e
	}
	req.Header.Set("Authorization", "Bearer "+c.Key)
	if contentType != "" {
		req.Header.Set("Content-Type", contentType)
	}
	res, e := c.HTTP.Do(req)
	if e != nil {
		return nil, e
	}
	defer res.Body.Close()
	b, e := io.ReadAll(io.LimitReader(res.Body, 2<<20))
	if e != nil {
		return nil, e
	}
	if res.StatusCode < 200 || res.StatusCode > 299 {
		var p struct {
			Error string `json:"error"`
		}
		if json.Unmarshal(b, &p) == nil && p.Error != "" {
			return nil, errors.New(p.Error)
		}
		return nil, fmt.Errorf("agent returned HTTP %d", res.StatusCode)
	}
	return b, nil
}
func (c *Client) Submit(ctx context.Context, archive string, s model.Spec) (model.Job, error) {
	var job model.Job
	raw, e := json.Marshal(s)
	if e != nil {
		return job, e
	}
	r, w := io.Pipe()
	multipartWriter := multipart.NewWriter(w)
	done := make(chan error, 1)
	go func() {
		var err error
		defer func() { _ = w.CloseWithError(err); done <- err }()
		var part io.Writer
		part, err = multipartWriter.CreateFormField("spec")
		if err != nil {
			return
		}
		_, err = part.Write(raw)
		if err != nil {
			return
		}
		part, err = multipartWriter.CreateFormFile("archive", "source.zip")
		if err != nil {
			return
		}
		var f *os.File
		f, err = os.Open(archive)
		if err != nil {
			return
		}
		defer f.Close()
		_, err = io.Copy(part, f)
		if err == nil {
			err = multipartWriter.Close()
		}
	}()
	b, e := c.Request(ctx, "POST", "/v1/jobs", r, multipartWriter.FormDataContentType())
	_ = r.Close()
	writeErr := <-done
	if e != nil {
		return job, e
	}
	if writeErr != nil {
		return job, writeErr
	}
	e = json.Unmarshal(b, &job)
	return job, e
}
func (c *Client) Wait(ctx context.Context, id string, out io.Writer) error {
	ticker := time.NewTicker(2 * time.Second)
	defer ticker.Stop()
	previous := ""
	for {
		b, e := c.Request(ctx, "GET", "/v1/jobs/"+id, nil, "")
		if e != nil {
			return fmt.Errorf("job %s was submitted; reconnect with the status command: %w", id, e)
		}
		var response struct {
			Job model.Job `json:"job"`
			Log string    `json:"log"`
		}
		if e = json.Unmarshal(b, &response); e != nil {
			return e
		}
		if response.Log != previous {
			delta := response.Log
			if strings.HasPrefix(delta, previous) {
				delta = strings.TrimPrefix(delta, previous)
			}
			fmt.Fprint(out, delta)
			previous = response.Log
		}
		if !response.Job.Active() {
			if response.Job.Status == "succeeded" {
				fmt.Fprintf(out, "\nResult: %s\n", response.Job.Destination)
				return nil
			}
			return fmt.Errorf("job %s: %s — %s", id, response.Job.Status, response.Job.Error)
		}
		select {
		case <-ctx.Done():
			return fmt.Errorf("job %s continues on the agent; cancel it explicitly if needed: %w", id, ctx.Err())
		case <-ticker.C:
		}
	}
}

// ReadEnv accepts literal dotenv values, never executable shell expressions.
func ReadEnv(p string) (map[string]string, error) {
	out := map[string]string{}
	if p == "" {
		return out, nil
	}
	f, e := os.Open(p)
	if e != nil {
		return nil, e
	}
	defer f.Close()
	scanner := bufio.NewScanner(f)
	lineNo := 0
	for scanner.Scan() {
		lineNo++
		line := strings.TrimSpace(scanner.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		line = strings.TrimPrefix(line, "export ")
		key, value, ok := strings.Cut(line, "=")
		if !ok {
			return nil, fmt.Errorf("invalid dotenv line %d", lineNo)
		}
		key = strings.TrimSpace(key)
		value = strings.TrimSpace(value)
		if strings.HasPrefix(value, "\"") || strings.HasPrefix(value, "'") {
			q := value[0]
			if len(value) < 2 || value[len(value)-1] != q {
				return nil, fmt.Errorf("unclosed dotenv quote on line %d", lineNo)
			}
			value = value[1 : len(value)-1]
		}
		out[key] = value
	}
	return out, scanner.Err()
}
func Env(values map[string]string, key string) string {
	if v, ok := values[key]; ok {
		return v
	}
	return os.Getenv(key)
}
func StorageFromEnv(env map[string]string) model.Storage {
	return model.Storage{Endpoint: Env(env, "MINIO_ENDPOINT"), Bucket: Env(env, "MINIO_BUCKET"), Region: Env(env, "MINIO_REGION"), AccessKey: Env(env, "MINIO_ACCESS_KEY_ID"), SecretKey: Env(env, "MINIO_SECRET_ACCESS_KEY"), SessionToken: Env(env, "MINIO_SESSION_TOKEN"), Prefix: Env(env, "MINIO_PREFIX")}
}

func Excluded(name string) bool {
	parts := strings.Split(filepath.ToSlash(name), "/")
	for _, p := range parts {
		switch strings.ToLower(p) {
		case ".git", ".dart_tool", "node_modules", ".pub-cache", ".gradle", ".secrets", ".private", ".idea", ".vscode", ".xcode-derived", "build", "dist", "out", "coverage", "__pycache__":
			return true
		}
	}
	base := strings.ToLower(parts[len(parts)-1])
	if strings.HasPrefix(base, ".env") || strings.HasSuffix(base, ".env") || strings.HasSuffix(base, ".p12") || strings.HasSuffix(base, ".pfx") || strings.HasSuffix(base, ".jks") || strings.HasSuffix(base, ".keystore") || strings.HasSuffix(base, ".key") || strings.HasSuffix(base, ".pem") || base == "key.properties" || base == "local.properties" || strings.HasSuffix(base, ".local-release.bak") {
		return true
	}
	return false
}
