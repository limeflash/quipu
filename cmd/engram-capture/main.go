// engram-capture is the Claude Code PostToolUse hook as its own small binary.
//
// It runs on every tool call, and on Windows a process start costs roughly in
// proportion to image size: a 2 MB Go binary starts in ~20 ms, the 30 MB
// engram binary in ~105 ms (measured 2026-10-07, warm cache). It must import
// nothing beyond internal/autocapture to stay small.
package main

import (
	"os"
	"time"

	"github.com/Gentleman-Programming/engram/v3/internal/autocapture"
)

func main() {
	time.AfterFunc(autocapture.HookDeadline, func() { os.Exit(0) })
	autocapture.RunClaudeHook(os.Stdin, os.Stderr)
}
