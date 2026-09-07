package main

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/jourloy/kilnbridge/internal/agent"
	"github.com/jourloy/kilnbridge/internal/client"
	"github.com/jourloy/kilnbridge/internal/model"
	"github.com/jourloy/kilnbridge/internal/platform"
	"github.com/jourloy/kilnbridge/internal/update"
)

var version = "0.1.0"

func main() {
	e := run(os.Args[1:])
	if e != nil {
		platform.AttachConsole()
		fmt.Fprintln(os.Stderr, "Kilnbridge:", e)
		if len(os.Args) == 1 {
			platform.Alert(e.Error())
		}
		os.Exit(1)
	}
}
func run(args []string) error {
	if len(args) > 0 {
		switch args[0] {
		case "version", "--version":
			platform.AttachConsole()
			fmt.Println("Kilnbridge " + version)
			return nil
		case "submit", "status", "wait", "cancel", "retry-upload", "snapshot":
			platform.AttachConsole()
			return clientCommand(args[0], args[1:])
		case "keygen", "sign-release":
			platform.AttachConsole()
			return releaseCommand(args[0], args[1:])
		case "apply-update":
			f := flag.NewFlagSet("apply-update", flag.ContinueOnError)
			dir := f.String("data", "", "application data directory")
			v := f.String("version", "", "version")
			parent := f.Int("parent", 0, "old agent process")
			if e := f.Parse(args[1:]); e != nil {
				return e
			}
			if *dir == "" || *parent <= 0 {
				return errors.New("data directory and parent PID are required")
			}
			e := update.Apply(*dir, *v, *parent)
			if e != nil {
				_ = platform.AtomicWrite(filepath.Join(*dir, "updates", "last-error.txt"), []byte(e.Error()), 0600)
			}
			return e
		case "serve":
			args = append([]string{"--background"}, args[1:]...)
		}
	}
	f := flag.NewFlagSet("kilnbridge", flag.ContinueOnError)
	background := f.Bool("background", false, "run the agent without opening settings")
	dir := f.String("data", "", "application data directory")
	nonce := f.String("update-nonce", "", "internal update health check")
	if e := f.Parse(args); e != nil {
		return e
	}
	if f.NArg() != 0 {
		return errors.New("unknown command; use submit, status, wait, cancel, retry-upload, snapshot, or version")
	}
	if *dir == "" {
		p, e := platform.DataDir()
		if e != nil {
			return e
		}
		*dir = p
	}
	absolute, e := filepath.Abs(*dir)
	if e != nil {
		return e
	}
	*dir = absolute
	if !*background && runtime.GOOS == "windows" {
		exe, e := os.Executable()
		if e != nil {
			return e
		}
		installed := update.InstalledPath(*dir)
		if !strings.EqualFold(filepath.Clean(exe), installed) {
			if _, e = os.Stat(installed); os.IsNotExist(e) {
				if e = update.CopyFile(exe, installed); e != nil {
					return e
				}
			}
			if e = platform.CreateShortcut(installed); e != nil {
				return fmt.Errorf("could not create the Start menu shortcut: %w", e)
			}
			cmd := exec.Command(installed, "--data", *dir)
			platform.Background(cmd)
			return cmd.Start()
		}
	}
	if *background {
		if e := os.MkdirAll(*dir, 0700); e != nil {
			return e
		}
		lock, e := platform.Lock(filepath.Join(*dir, "agent.lock"))
		if e != nil {
			return fmt.Errorf("another agent is using this data directory: %w", e)
		}
		defer lock.Close()
		ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
		defer stop()
		ctx, cancel := context.WithCancel(ctx)
		defer cancel()
		a, e := agent.NewApp(ctx, *dir, version)
		if e != nil {
			return e
		}
		a.Shutdown = cancel
		return a.Run(ctx, *nonce)
	}
	address, e := agent.ExistingSettings(*dir)
	if e != nil {
		exe, e := os.Executable()
		if e != nil {
			return e
		}
		cmd := exec.Command(exe, "--background", "--data", *dir)
		platform.Background(cmd)
		if e = cmd.Start(); e != nil {
			return e
		}
		for deadline := time.Now().Add(15 * time.Second); time.Now().Before(deadline); time.Sleep(200 * time.Millisecond) {
			address, e = agent.ExistingSettings(*dir)
			if e == nil {
				break
			}
		}
		if e != nil {
			return fmt.Errorf("the agent could not start; check that port 8743 is available: %w", e)
		}
	}
	uiCtx, closeUI := context.WithCancel(context.Background())
	defer closeUI()
	go agent.KeepSettingsOpen(uiCtx, address)
	return platform.OpenUI(address)
}

func clientCommand(command string, args []string) error {
	f := flag.NewFlagSet(command, flag.ContinueOnError)
	envPath := f.String("env", "", "literal dotenv file with agent and MinIO settings")
	source := f.String("source", ".", "Git working directory to snapshot")
	recipe := f.String("recipe", "", "Flutter Windows recipe JSON")
	id := f.String("job", "", "job ID")
	detach := f.Bool("detach", false, "return after the agent accepts the job")
	allowDirty := f.Bool("allow-dirty", false, "include uncommitted source changes")
	output := f.String("output", "source.zip", "snapshot output path")
	if e := f.Parse(args); e != nil {
		return e
	}
	if command == "snapshot" {
		sha, dirty, digest, e := client.Snapshot(*source, *output)
		if e != nil {
			return e
		}
		return platform.WriteJSON(*output+".json", map[string]any{"sourceSha": sha, "sourceDirty": dirty, "archiveSha256": digest})
	}
	env, e := client.ReadEnv(*envPath)
	if e != nil {
		return e
	}
	c, e := client.New(client.Env(env, "KILNBRIDGE_URL"), client.Env(env, "KILNBRIDGE_KEY"), client.Env(env, "KILNBRIDGE_PIN_SHA256"))
	if e != nil {
		return e
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()
	if command != "submit" {
		if !model.IDPattern.MatchString(*id) {
			return errors.New("--job must be a job UUID")
		}
		if command == "wait" {
			return c.Wait(ctx, *id, os.Stdout)
		}
		route := "/v1/jobs/" + *id
		method := "GET"
		if command != "status" {
			route += "/" + command
			method = "POST"
		}
		b, e := c.Request(ctx, method, route, nil, "")
		if e == nil {
			fmt.Println(string(b))
		}
		return e
	}
	if *recipe == "" {
		return errors.New("--recipe is required")
	}
	b, e := os.ReadFile(*recipe)
	if e != nil {
		return e
	}
	var s model.Spec
	if e = json.Unmarshal(b, &s); e != nil {
		return e
	}
	s.Storage = client.StorageFromEnv(env)
	if v := client.Env(env, "API_URL"); v != "" {
		s.APIURL = v
	}
	if s.WorkDir == "" {
		s.WorkDir = "."
	}
	if !model.Relative(s.WorkDir) {
		return errors.New("recipe workDir must be relative")
	}
	if s.Version == "" || s.BuildNumber == "" {
		b, e = os.ReadFile(filepath.Join(*source, filepath.FromSlash(s.WorkDir), "pubspec.yaml"))
		if e != nil {
			return e
		}
		for _, line := range strings.Split(string(b), "\n") {
			if strings.HasPrefix(line, "version:") {
				value := strings.Trim(strings.TrimSpace(strings.SplitN(strings.TrimPrefix(line, "version:"), "#", 2)[0]), "\"'")
				v, n, ok := strings.Cut(value, "+")
				if ok {
					s.Version = v
					s.BuildNumber = n
				}
				break
			}
		}
	}
	temp, e := os.CreateTemp("", "kilnbridge-source-*.zip")
	if e != nil {
		return e
	}
	archive := temp.Name()
	_ = temp.Close()
	defer os.Remove(archive)
	fmt.Println("Creating local source snapshot...")
	s.SourceSHA, s.SourceDirty, s.ArchiveSHA256, e = client.Snapshot(*source, archive, s.SnapshotIncludes...)
	if e != nil {
		return e
	}
	if s.SourceDirty && !*allowDirty {
		return errors.New("source has uncommitted changes; pass --allow-dirty to include and record them")
	}
	if e = s.Validate(); e != nil {
		return e
	}
	fmt.Println("Sending snapshot to the Windows agent...")
	j, e := c.Submit(ctx, archive, s)
	if e != nil {
		return e
	}
	fmt.Println("Job:", j.ID)
	if *detach {
		return nil
	}
	return c.Wait(ctx, j.ID, os.Stdout)
}

func releaseCommand(command string, args []string) error {
	f := flag.NewFlagSet(command, flag.ContinueOnError)
	keyPath := f.String("key", ".private/update.key", "base64 Ed25519 private key")
	publicPath := f.String("public", "internal/update/release.pub", "embedded public key")
	binary := f.String("binary", "dist/kilnbridge-windows-amd64.exe", "release executable")
	v := f.String("version", version, "release version")
	out := f.String("out", "dist", "release metadata directory")
	if e := f.Parse(args); e != nil {
		return e
	}
	if command == "keygen" {
		if _, e := os.Stat(*keyPath); !os.IsNotExist(e) {
			return errors.New("refusing to overwrite the update signing key")
		}
		pub, priv, e := ed25519.GenerateKey(rand.Reader)
		if e != nil {
			return e
		}
		if e = platform.AtomicWrite(*keyPath, []byte(base64.StdEncoding.EncodeToString(priv)+"\n"), 0600); e != nil {
			return e
		}
		return platform.AtomicWrite(*publicPath, []byte(base64.StdEncoding.EncodeToString(pub)+"\n"), 0644)
	}
	text := os.Getenv("UPDATE_SIGNING_KEY")
	if text == "" {
		b, e := os.ReadFile(*keyPath)
		if e != nil {
			return e
		}
		text = string(b)
	}
	key, e := base64.StdEncoding.DecodeString(strings.TrimSpace(text))
	if e != nil || len(key) != ed25519.PrivateKeySize {
		return errors.New("invalid Ed25519 signing key")
	}
	if !bytes.Equal(ed25519.PrivateKey(key).Public().(ed25519.PublicKey), update.PublicKey()) {
		return errors.New("private key does not match the public key embedded in this build")
	}
	raw, sig, e := update.Sign(*binary, *v, ed25519.PrivateKey(key))
	if e != nil {
		return e
	}
	if e = platform.AtomicWrite(filepath.Join(*out, "update.json"), raw, 0644); e != nil {
		return e
	}
	return platform.AtomicWrite(filepath.Join(*out, "update.json.sig"), sig, 0644)
}
