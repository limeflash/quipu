package autocapture

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
)

// ConfigFile is the opt-in switch: without it in the data directory the
// capture hook does nothing, so an unconfigured fork behaves like upstream.
// It will also hold provider settings (milestone 2).
const ConfigFile = "autocapture.json"

// Enabled reports whether auto-capture is switched on for dataDir.
func Enabled(dataDir string) bool {
	_, err := os.Stat(filepath.Join(dataDir, ConfigFile))
	return err == nil
}

// SpoolDir is where hooks drop events for the compressor to pick up.
func SpoolDir(dataDir string) string { return filepath.Join(dataDir, "spool") }

// Write stores one event as its own file: no shared file means no locks
// between parallel sessions, and the temp-then-rename keeps a reader from
// ever seeing half an event.
func Write(dir string, ev Event) error {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	data, err := json.Marshal(ev)
	if err != nil {
		return err
	}
	var rnd [4]byte
	_, _ = rand.Read(rnd[:])
	name := fmt.Sprintf("%020d-%d-%s.json", ev.At.UnixNano(), os.Getpid(), hex.EncodeToString(rnd[:]))
	tmp := filepath.Join(dir, "."+name+".tmp")
	if err := os.WriteFile(tmp, data, 0o600); err != nil {
		return err
	}
	if err := os.Rename(tmp, filepath.Join(dir, name)); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	return nil
}
