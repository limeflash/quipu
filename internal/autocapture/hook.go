package autocapture

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// HookDeadline bounds the whole PostToolUse hook. A lost event is acceptable;
// an agent waiting on memory is not. Callers enforce it with os.Exit.
const HookDeadline = 2 * time.Second

// RunHook is the capture hook for Claude Code and Codex — their hook payloads
// share one shape: read one payload, spool it, return. It opens no store,
// makes no network call and never writes to stdout, because it runs on every
// tool call of every session. agent labels the events ("claude", "codex").
func RunHook(agent string, stdin io.Reader, stderr io.Writer) {
	// Our own fallback `claude -p` runs must not capture themselves.
	if os.Getenv("ENGRAM_INTERNAL") == "1" {
		return
	}
	dataDir, ok := DataDir()
	if !ok || !Enabled(dataDir) {
		return
	}
	raw, err := io.ReadAll(io.LimitReader(stdin, 32<<20))
	if err != nil {
		return
	}
	ev, ok := FromClaude(raw, time.Now())
	if !ok {
		return
	}
	if agent != "" {
		ev.Agent = agent
	}
	if err := Write(SpoolDir(dataDir), ev); err != nil {
		fmt.Fprintln(stderr, "engram: auto-capture:", err)
	}
}

// DataDir resolves engram's data directory the way the engram CLI does
// (ENGRAM_DATA_DIR, else <home>/.engram) without importing the store, which
// would pull the whole engram binary into the capture hook.
func DataDir() (string, bool) {
	if dir := os.Getenv("ENGRAM_DATA_DIR"); strings.TrimSpace(dir) != "" {
		return dir, true
	}
	if home, err := os.UserHomeDir(); err == nil {
		return filepath.Join(home, ".engram"), true
	}
	for _, env := range []string{"USERPROFILE", "HOME"} {
		if v := os.Getenv(env); v != "" {
			return filepath.Join(v, ".engram"), true
		}
	}
	if v := os.Getenv("LOCALAPPDATA"); v != "" {
		return filepath.Join(filepath.Dir(filepath.Dir(v)), ".engram"), true
	}
	return "", false
}
