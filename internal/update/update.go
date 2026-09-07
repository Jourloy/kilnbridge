// Package update verifies a signed release before it may replace the agent.
package update

import (
	"context"
	"crypto/ed25519"
	"crypto/sha256"
	_ "embed"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/jourloy/kilnbridge/internal/platform"
)

//go:embed release.pub
var publicKeyText string

const AssetName = "kilnbridge-windows-amd64.exe"
const MaxBinarySize int64 = 256 << 20

type Manifest struct {
	Version string `json:"version"`
	Asset   string `json:"asset"`
	SHA256  string `json:"sha256"`
	Size    int64  `json:"size"`
}
type Pending struct {
	Version   string
	Directory string
}
type release struct {
	Tag        string `json:"tag_name"`
	Draft      bool   `json:"draft"`
	Prerelease bool   `json:"prerelease"`
	Assets     []struct {
		Name string `json:"name"`
		URL  string `json:"browser_download_url"`
	} `json:"assets"`
}

func PublicKey() ed25519.PublicKey {
	b, _ := base64.StdEncoding.DecodeString(strings.TrimSpace(publicKeyText))
	return ed25519.PublicKey(b)
}

func Verify(raw, signature []byte, key ed25519.PublicKey) (Manifest, error) {
	var m Manifest
	sig, e := base64.StdEncoding.DecodeString(strings.TrimSpace(string(signature)))
	if e != nil || len(key) != ed25519.PublicKeySize || !ed25519.Verify(key, raw, sig) {
		return m, errors.New("release signature is invalid")
	}
	if e = json.Unmarshal(raw, &m); e != nil {
		return m, e
	}
	if strings.HasPrefix(m.Version, "v") {
		return m, errors.New("signed version must not include a tag prefix")
	}
	if _, e = parseVersion(m.Version); e != nil {
		return m, e
	}
	b, e := hex.DecodeString(m.SHA256)
	if e != nil || len(b) != 32 || m.Asset != AssetName || m.Size <= 0 || m.Size > MaxBinarySize {
		return m, errors.New("signed release metadata is invalid")
	}
	return m, nil
}
func parseVersion(s string) ([3]uint64, error) {
	var v [3]uint64
	p := strings.Split(strings.TrimPrefix(s, "v"), ".")
	if len(p) != 3 {
		return v, errors.New("release version must be stable semver")
	}
	for i, s := range p {
		if s == "" || (len(s) > 1 && s[0] == '0') {
			return v, errors.New("invalid release version")
		}
		n, e := strconv.ParseUint(s, 10, 32)
		if e != nil {
			return v, e
		}
		v[i] = n
	}
	return v, nil
}
func Newer(next, current string) bool {
	a, e := parseVersion(next)
	if e != nil {
		return false
	}
	b, e := parseVersion(current)
	if e != nil {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return a[i] > b[i]
		}
	}
	return false
}

func Download(ctx context.Context, repo, current, cache string) (*Pending, error) {
	client := &http.Client{Timeout: 20 * time.Minute}
	b, e := get(ctx, client, "https://api.github.com/repos/"+repo+"/releases/latest", 256<<10)
	if e != nil {
		return nil, e
	}
	var rel release
	if e = json.Unmarshal(b, &rel); e != nil {
		return nil, e
	}
	if rel.Draft || rel.Prerelease || !Newer(rel.Tag, current) {
		return nil, nil
	}
	assets := map[string]string{}
	for _, a := range rel.Assets {
		u, e := url.Parse(a.URL)
		if e != nil || u.Scheme != "https" || u.Host != "github.com" || !strings.HasPrefix(strings.ToLower(u.Path), "/"+strings.ToLower(repo)+"/releases/download/") {
			return nil, errors.New("release asset is outside the configured GitHub repository")
		}
		if _, ok := assets[a.Name]; ok {
			return nil, errors.New("duplicate release asset")
		}
		assets[a.Name] = a.URL
	}
	for _, name := range []string{"update.json", "update.json.sig", AssetName} {
		if assets[name] == "" {
			return nil, fmt.Errorf("release is missing %s", name)
		}
	}
	raw, e := get(ctx, client, assets["update.json"], 64<<10)
	if e != nil {
		return nil, e
	}
	sig, e := get(ctx, client, assets["update.json.sig"], 4096)
	if e != nil {
		return nil, e
	}
	m, e := Verify(raw, sig, PublicKey())
	if e != nil {
		return nil, e
	}
	if strings.TrimPrefix(rel.Tag, "v") != strings.TrimPrefix(m.Version, "v") || !Newer(m.Version, current) {
		return nil, errors.New("release tag does not match signed metadata")
	}
	dir := filepath.Join(cache, strings.TrimPrefix(m.Version, "v"))
	if e = os.MkdirAll(dir, 0700); e != nil {
		return nil, e
	}
	target := filepath.Join(dir, AssetName)
	if e = VerifyFile(target, m); e != nil {
		req, e := http.NewRequestWithContext(ctx, http.MethodGet, assets[AssetName], nil)
		if e != nil {
			return nil, e
		}
		req.Header.Set("User-Agent", "Kilnbridge")
		res, e := client.Do(req)
		if e != nil {
			return nil, e
		}
		defer res.Body.Close()
		if res.StatusCode != 200 {
			return nil, fmt.Errorf("release download returned HTTP %d", res.StatusCode)
		}
		f, e := os.CreateTemp(dir, "download-*")
		if e != nil {
			return nil, e
		}
		temp := f.Name()
		defer os.Remove(temp)
		n, e := io.Copy(f, io.LimitReader(res.Body, m.Size+1))
		closeErr := f.Close()
		if e != nil {
			return nil, e
		}
		if closeErr != nil {
			return nil, closeErr
		}
		if n != m.Size {
			return nil, errors.New("release download size mismatch")
		}
		if e = VerifyFile(temp, m); e != nil {
			return nil, e
		}
		if e = os.Rename(temp, target); e != nil {
			return nil, e
		}
	}
	if e = platform.AtomicWrite(filepath.Join(dir, "update.json"), raw, 0600); e != nil {
		return nil, e
	}
	if e = platform.AtomicWrite(filepath.Join(dir, "update.json.sig"), sig, 0600); e != nil {
		return nil, e
	}
	return &Pending{Version: m.Version, Directory: dir}, nil
}
func get(ctx context.Context, client *http.Client, address string, limit int64) ([]byte, error) {
	req, e := http.NewRequestWithContext(ctx, "GET", address, nil)
	if e != nil {
		return nil, e
	}
	req.Header.Set("User-Agent", "Kilnbridge")
	req.Header.Set("Accept", "application/vnd.github+json")
	res, e := client.Do(req)
	if e != nil {
		return nil, e
	}
	defer res.Body.Close()
	if res.StatusCode != 200 {
		return nil, fmt.Errorf("GitHub returned HTTP %d", res.StatusCode)
	}
	b, e := io.ReadAll(io.LimitReader(res.Body, limit+1))
	if int64(len(b)) > limit {
		return nil, errors.New("release response is too large")
	}
	return b, e
}
func VerifyFile(p string, m Manifest) error {
	f, e := os.Open(p)
	if e != nil {
		return e
	}
	defer f.Close()
	info, e := f.Stat()
	if e != nil {
		return e
	}
	if info.Size() != m.Size {
		return errors.New("release size mismatch")
	}
	h := sha256.New()
	if _, e = io.Copy(h, f); e != nil {
		return e
	}
	if hex.EncodeToString(h.Sum(nil)) != m.SHA256 {
		return errors.New("release SHA-256 mismatch")
	}
	return nil
}

func Sign(binary, version string, privateKey ed25519.PrivateKey) ([]byte, []byte, error) {
	if _, e := parseVersion(version); e != nil {
		return nil, nil, e
	}
	f, e := os.Open(binary)
	if e != nil {
		return nil, nil, e
	}
	defer f.Close()
	h := sha256.New()
	n, e := io.Copy(h, f)
	if e != nil {
		return nil, nil, e
	}
	m := Manifest{Version: version, Asset: AssetName, SHA256: hex.EncodeToString(h.Sum(nil)), Size: n}
	raw, e := json.MarshalIndent(m, "", "  ")
	if e != nil {
		return nil, nil, e
	}
	raw = append(raw, '\n')
	return raw, []byte(base64.StdEncoding.EncodeToString(ed25519.Sign(privateKey, raw)) + "\n"), nil
}
