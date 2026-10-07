package compress

import (
	"context"
	"fmt"
	"os"
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

// NewGenerator builds the model chain from config and stored model health.
func NewGenerator(cfg Config, dataDir string) (generator, error) {
	key := ollamaKey(dataDir)
	if key == "" {
		return nil, fmt.Errorf("no Ollama key: set OLLAMA_API_KEY or put it in %s", filepath.Join(dataDir, "ollama.key"))
	}
	return &chain{models: cfg.Ollama.Models, client: newOllama(cfg, key), st: loadState(dataDir),
		dataDir: dataDir, now: time.Now}, nil
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
	if !autocapture.Enabled(dataDir) {
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
