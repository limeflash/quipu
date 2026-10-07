package main

import (
	"os"
	"time"

	"github.com/Gentleman-Programming/engram/v3/internal/autocapture"
)

// cmdHookCapture is `engram hook claude-post-tool-use`. The plugin calls the
// slimmer engram-capture binary instead (see cmd/engram-capture); this entry
// point keeps the hook working where only engram is installed.
func cmdHookCapture() {
	time.AfterFunc(autocapture.HookDeadline, func() { os.Exit(0) })
	autocapture.RunClaudeHook(os.Stdin, os.Stderr)
}
