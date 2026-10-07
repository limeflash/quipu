package compress

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/Gentleman-Programming/engram/v3/internal/autocapture"
)

const (
	lockFile = "autocapture.lock"
	// A drain holding the lock longer than this is assumed dead.
	staleLock = 15 * time.Minute
)

// NewGenerator builds the backend chain — Ollama models, then Claude — from
// config, credentials and stored health.
func NewGenerator(cfg Config, dataDir string) (generator, error) {
	c := &chain{st: loadState(dataDir), dataDir: dataDir, now: time.Now}
	if key := ollamaKey(dataDir); key != "" {
		client := newOllama(cfg, key)
		for _, m := range cfg.Ollama.Models {
			c.backends = append(c.backends, backend{name: m, provider: "ollama", model: m, client: client})
		}
	}
	if b, ok := codexBackend(cfg, dataDir); ok {
		c.backends = append(c.backends, b)
	}
	if b, ok := claudeBackend(cfg, dataDir); ok {
		c.backends = append(c.backends, b)
	}
	if len(c.backends) == 0 {
		return nil, fmt.Errorf("no model configured: put an Ollama key in %s and/or a `claude setup-token` token in %s",
			filepath.Join(dataDir, "ollama.key"), filepath.Join(dataDir, "claude.token"))
	}
	return c, nil
}

func codexBackend(cfg Config, dataDir string) (backend, bool) {
	if (cfg.Codex.Enabled != nil && !*cfg.Codex.Enabled) || !codexLoggedIn() {
		return backend{}, false
	}
	if _, err := os.Stat(cfg.Codex.Exe); err != nil {
		if _, err := exec.LookPath(cfg.Codex.Exe); err != nil {
			return backend{}, false
		}
	}
	return backend{name: "codex:" + cfg.Codex.Model, provider: "codex", model: cfg.Codex.Model,
		client: newCodex(cfg, dataDir), perHour: cfg.Codex.MaxCallsPerHour}, true
}

func claudeBackend(cfg Config, dataDir string) (backend, bool) {
	token := claudeToken(dataDir)
	if token == "" || (cfg.Claude.Enabled != nil && !*cfg.Claude.Enabled) {
		return backend{}, false
	}
	return backend{name: "claude:" + cfg.Claude.Model, provider: "claude", model: cfg.Claude.Model,
		client: newClaude(cfg, dataDir, token), perHour: cfg.Claude.MaxCallsPerHour}, true
}

// ProbeResult is one backend's answer to a tiny test call.
type ProbeResult struct {
	Backend string
	OK      bool
	Detail  string
	Took    time.Duration
	Usage   usage
}

// Probe sends every configured backend one small request, bypassing health
// state and budgets, so a user can check credentials right after setting
// them up.
func Probe(ctx context.Context, dataDir string) ([]ProbeResult, error) {
	cfg, err := Load(dataDir)
	if err != nil {
		return nil, err
	}
	g, err := NewGenerator(cfg, dataDir)
	if err != nil {
		return nil, err
	}
	var out []ProbeResult
	user := "Project: probe\n\n## Activity\n### 12:00 Edit db/store.go\n--- old\nbusy_timeout=0\n+++ new\nbusy_timeout=5000\n" +
		"--- output\nfixes 'database is locked' with three parallel sessions\n"
	for _, b := range g.(*chain).backends {
		start := time.Now()
		text, u, err := b.client.chat(ctx, b.model, system(cfg.Language, false), user, schema)
		r := ProbeResult{Backend: b.name, Took: time.Since(start), Usage: u}
		if err == nil {
			var res result
			if res, _, err = parse(text, false); err == nil {
				r.OK, r.Detail = true, fmt.Sprintf("%d record(s)", len(res.Observations))
				if len(res.Observations) > 0 {
					r.Detail += ": " + res.Observations[0].Title
				}
			}
		}
		if err != nil {
			r.Detail = err.Error()
		}
		out = append(out, r)
	}
	return out, nil
}

// BuildProfile rebuilds, right now, the profile of the repository dir is in.
func BuildProfile(ctx context.Context, sink Sink, dataDir, dir string) (string, error) {
	cfg, err := Load(dataDir)
	if err != nil {
		return "", err
	}
	gen, err := NewGenerator(cfg, dataDir)
	if err != nil {
		return "", err
	}
	d := &drainer{sink: sink, gen: gen, cfg: cfg, dataDir: dataDir, now: time.Now(),
		projs: map[string]string{}, roots: map[string]string{}}
	p := d.project(dir, true)
	if p == "" || d.roots[p] == "" {
		return "", fmt.Errorf("%s is not inside a repository", dir)
	}
	profiles := loadProfiles(dataDir)
	content, err := d.refreshProfile(ctx, p, d.roots[p], profiles, true)
	if err == nil {
		saveProfiles(dataDir, profiles)
	}
	return content, err
}

// RunOnce drains the spool once under the cross-process lock. ok is false
// when another process holds the lock.
func RunOnce(ctx context.Context, sink Sink, dataDir string, dryRun bool, out func(string)) (rep Report, ok bool, err error) {
	if !lock(dataDir) {
		return rep, false, nil
	}
	defer unlock(dataDir)
	cfg, err := Load(dataDir)
	if err != nil {
		return rep, true, err
	}
	gen, err := NewGenerator(cfg, dataDir)
	if err != nil {
		return rep, true, err
	}
	rep, err = Drain(ctx, sink, gen, cfg, dataDir, time.Now(), dryRun, out)
	return rep, true, err
}

// Start runs the drain in the background for as long as ctx lives. It is a
// no-op unless auto-capture is enabled. Errors go to autocapture.log, never
// to stdout, which belongs to the MCP protocol.
func Start(ctx context.Context, sink Sink, dataDir string) (stop func()) {
	// A fallback model's own process (ENGRAM_INTERNAL) must never start a
	// second compressor.
	if !autocapture.Enabled(dataDir) || os.Getenv("ENGRAM_INTERNAL") == "1" {
		return func() {}
	}
	cfg, err := Load(dataDir)
	if err != nil {
		return func() {}
	}
	ctx, cancel := context.WithCancel(ctx)
	done := make(chan struct{})
	go func() {
		defer close(done)
		timer := time.NewTimer(15 * time.Second) // let the session settle first
		defer timer.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-timer.C:
				if _, _, err := RunOnce(ctx, sink, dataDir, false, nil); err != nil && ctx.Err() == nil {
					logLine(dataDir, "drain: "+err.Error())
				}
				timer.Reset(time.Duration(cfg.IntervalSecs) * time.Second)
			}
		}
	}()
	return func() { cancel(); <-done }
}

// lock is a best-effort cross-process mutex: create-exclusive, with a
// timestamp so a crashed holder expires. Correctness does not depend on it —
// spool files are claimed by rename — it only keeps parallel sessions from
// all calling the model at once.
func lock(dataDir string) bool {
	path := filepath.Join(dataDir, lockFile)
	for attempt := 0; attempt < 2; attempt++ {
		f, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
		if err == nil {
			fmt.Fprintf(f, "%d %d\n", os.Getpid(), time.Now().Unix())
			f.Close()
			return true
		}
		data, _ := os.ReadFile(path)
		fields := strings.Fields(string(data))
		if len(fields) == 2 {
			if ts, err := strconv.ParseInt(fields[1], 10, 64); err == nil && time.Since(time.Unix(ts, 0)) < staleLock {
				return false
			}
		}
		_ = os.Remove(path) // stale or unreadable: take it over
	}
	return false
}

func unlock(dataDir string) { _ = os.Remove(filepath.Join(dataDir, lockFile)) }

func logLine(dataDir, msg string) {
	f, err := os.OpenFile(filepath.Join(dataDir, logFile), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		return
	}
	defer f.Close()
	fmt.Fprintf(f, "{\"at\":%q,\"note\":%q}\n", time.Now().UTC().Format(time.RFC3339), msg)
}
