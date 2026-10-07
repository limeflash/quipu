// engram-capture is the capture hook (PostToolUse, UserPromptSubmit, Stop)
// for Claude Code and — with the argument "codex" — Codex, as its own small
// binary.
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
	agent := "claude"
	if len(os.Args) > 1 {
		agent = os.Args[1]
	}
	autocapture.RunHook(agent, os.Stdin, os.Stderr)
}
