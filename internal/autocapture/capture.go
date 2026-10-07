// Package autocapture records what coding agents change, for later compression
// into engram observations by a model (see FORK.md and docs/fork/DESIGN.md).
//
// The hook side is deliberately dumb and fast: classify one tool call, clean
// it, append it to a spool directory, exit. No store, no network.
package autocapture

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/Gentleman-Programming/engram/v3/internal/redact"
)

const (
	// KindEvent is a state-changing tool call: an edit, a write, a command
	// that is not read-only. Its content goes to the model later.
	KindEvent = "event"
	// KindTouch is a read: which file, URL or query was looked at. No content
	// is kept — in claude-mem 58% of observations paraphrased code that was
	// only read, which is where the tokens went.
	KindTouch = "touch"
	// KindPrompt is what the user asked (UserPromptSubmit).
	KindPrompt = "prompt"
	// KindTurnEnd marks the end of an agent turn (Stop) and carries the
	// agent's final message; it triggers compression and the session summary.
	KindTurnEnd = "turn_end"

	maxContent = 4096 // per input / output, head + tail
	maxTarget  = 300
	maxPrompt  = 2048
)

// Event is one spooled tool call.
type Event struct {
	V         int       `json:"v"`
	Agent     string    `json:"agent"`
	Kind      string    `json:"kind"`
	SessionID string    `json:"session_id"`
	CWD       string    `json:"cwd"`
	Tool      string    `json:"tool"`
	Target    string    `json:"target,omitempty"`
	Input     string    `json:"input,omitempty"`
	Output    string    `json:"output,omitempty"`
	At        time.Time `json:"at"`
}

type claudeInput struct {
	SessionID      string          `json:"session_id"`
	CWD            string          `json:"cwd"`
	HookEventName  string          `json:"hook_event_name"`
	ToolName       string          `json:"tool_name"`
	ToolInput      map[string]any  `json:"tool_input"`
	ToolResponse   json.RawMessage `json:"tool_response"`
	Prompt         string          `json:"prompt"`
	TranscriptPath string          `json:"transcript_path"`
	LastAssistant  string          `json:"last_assistant_message"`
}

// FromClaude turns one Claude Code hook payload (PostToolUse,
// UserPromptSubmit or Stop) into an event. ok is false when there is nothing
// worth recording.
func FromClaude(raw []byte, now time.Time) (ev Event, ok bool) {
	var in claudeInput
	if json.Unmarshal(raw, &in) != nil || in.SessionID == "" {
		return Event{}, false
	}
	ev = Event{V: 1, Agent: "claude", SessionID: in.SessionID, CWD: in.CWD, Tool: in.ToolName, At: now.UTC()}
	switch in.HookEventName {
	case "UserPromptSubmit":
		if strings.TrimSpace(in.Prompt) == "" {
			return Event{}, false
		}
		ev.Kind, ev.Input = KindPrompt, cleanText(in.Prompt, maxPrompt)
		return ev, true
	case "Stop":
		last := in.LastAssistant
		if last == "" {
			last = lastAssistantText(in.TranscriptPath)
		}
		ev.Kind, ev.Output = KindTurnEnd, cleanText(last, maxContent)
		return ev, true
	}
	if in.ToolName == "" {
		return Event{}, false
	}
	arg := func(k string) string { s, _ := in.ToolInput[k].(string); return s }

	switch in.ToolName {
	case "Edit":
		ev.Kind, ev.Target = KindEvent, arg("file_path")
		ev.Input = "--- old\n" + arg("old_string") + "\n+++ new\n" + arg("new_string")
	case "MultiEdit":
		ev.Kind, ev.Target = KindEvent, arg("file_path")
		edits, _ := in.ToolInput["edits"].([]any)
		var b strings.Builder
		for _, e := range edits {
			m, _ := e.(map[string]any)
			old, _ := m["old_string"].(string)
			repl, _ := m["new_string"].(string)
			b.WriteString("--- old\n" + old + "\n+++ new\n" + repl + "\n")
		}
		ev.Input = b.String()
	case "Write":
		ev.Kind, ev.Target, ev.Input = KindEvent, arg("file_path"), arg("content")
	case "apply_patch": // Codex: the patch text arrives as tool_input.command
		patch := arg("command")
		if strings.TrimSpace(patch) == "" {
			return Event{}, false
		}
		ev.Kind, ev.Target, ev.Input = KindEvent, patchFile(patch, in.CWD), patch
	case "NotebookEdit":
		ev.Kind, ev.Target, ev.Input = KindEvent, arg("notebook_path"), arg("new_source")
	case "Bash", "PowerShell":
		cmd := arg("command")
		if strings.TrimSpace(cmd) == "" {
			return Event{}, false
		}
		if ReadOnlyCommand(cmd) {
			ev.Kind, ev.Target = KindTouch, cmd
		} else {
			ev.Kind, ev.Input, ev.Output = KindEvent, cmd, shellOutput(in.ToolResponse)
		}
	case "Read", "NotebookRead":
		ev.Kind, ev.Target = KindTouch, firstNonEmpty(arg("file_path"), arg("notebook_path"))
	case "Grep", "Glob":
		ev.Kind, ev.Target = KindTouch, strings.TrimSpace(arg("pattern")+" "+arg("path"))
	case "LS":
		ev.Kind, ev.Target = KindTouch, arg("path")
	case "WebFetch":
		ev.Kind, ev.Target = KindTouch, arg("url")
	case "WebSearch":
		ev.Kind, ev.Target = KindTouch, arg("query")
	default:
		if !strings.HasPrefix(in.ToolName, "mcp__") || isEngramTool(in.ToolName) {
			return Event{}, false
		}
		args, _ := json.Marshal(in.ToolInput)
		if mutatingMCP(in.ToolName) {
			ev.Kind, ev.Input, ev.Output = KindEvent, string(args), responseText(in.ToolResponse)
		} else {
			ev.Kind, ev.Target = KindTouch, string(args)
		}
	}
	if ev.Kind == KindTouch && strings.TrimSpace(ev.Target) == "" {
		return Event{}, false
	}
	return clean(ev), true
}

// clean applies the capture pipeline in an order that matters:
//
//  1. private spans are cut first, before any truncation can split one in
//     half and leak its tail;
//  2. a coarse cut to 4× the limit keeps redaction cheap on huge outputs
//     (200 KB of test output took ~350 ms to redact whole);
//  3. blobs and secrets are removed;
//  4. the final cut keeps only the outer quarter of each coarse half, so a
//     secret split by the coarse cut sits in the discarded middle.
func clean(ev Event) Event {
	if ev.Kind == KindEvent && sensitivePath(ev.Target) {
		ev.Input, ev.Output = "[content withheld: sensitive file]", ""
	}
	ev.Target = cleanText(ev.Target, maxTarget)
	ev.Input = cleanText(ev.Input, maxContent)
	ev.Output = cleanText(ev.Output, maxContent)
	return ev
}

var patchFileRe = regexp.MustCompile(`(?m)^\*\*\* (?:Update|Add|Delete) File: (.+?)\s*$`)

// patchFile names the first file a Codex patch touches, made absolute against
// the session directory so the compressor can tell which repository it is in.
func patchFile(patch, cwd string) string {
	m := patchFileRe.FindStringSubmatch(patch)
	if m == nil {
		return ""
	}
	p := m[1]
	if !filepath.IsAbs(p) && cwd != "" {
		p = filepath.Join(cwd, p)
	}
	return p
}

func cleanText(s string, limit int) string {
	s = privateRe.ReplaceAllString(s, "[private]")
	if binary(s) {
		return fmt.Sprintf("[binary %d bytes]", len(s))
	}
	s = truncate(s, 4*limit)
	s = stripBase64(s)
	s = redact.String(s)
	return truncate(s, limit)
}

// lastAssistantText returns the text of the agent's final message from the
// tail of a Claude Code transcript. Only the tail is read: transcripts grow
// to many megabytes and the hook must stay fast.
func lastAssistantText(path string) string {
	if path == "" {
		return ""
	}
	f, err := os.Open(path)
	if err != nil {
		return ""
	}
	defer f.Close()
	const tail = 512 << 10
	if st, err := f.Stat(); err == nil && st.Size() > tail {
		_, _ = f.Seek(-tail, io.SeekEnd)
	}
	data, _ := io.ReadAll(f)
	lines := strings.Split(string(data), "\n")
	for i := len(lines) - 1; i >= 0; i-- {
		var line struct {
			Type    string `json:"type"`
			Message struct {
				Content json.RawMessage `json:"content"`
			} `json:"message"`
		}
		if json.Unmarshal([]byte(lines[i]), &line) != nil || line.Type != "assistant" {
			continue
		}
		var blocks []struct {
			Type string `json:"type"`
			Text string `json:"text"`
		}
		if json.Unmarshal(line.Message.Content, &blocks) != nil {
			continue
		}
		var b strings.Builder
		for _, bl := range blocks {
			if bl.Type == "text" {
				b.WriteString(bl.Text)
			}
		}
		if b.Len() > 0 {
			return b.String()
		}
	}
	return ""
}

var (
	privateRe = regexp.MustCompile(`(?is)<private>.*?</private>`)
	base64Re  = regexp.MustCompile(`[A-Za-z0-9+/]{200,}={0,2}`)
)

// Binary content and base64 runs are replaced with a size note: they cost
// tokens and carry nothing a memory needs (claude-mem #3839).
func binary(s string) bool {
	if s == "" {
		return false
	}
	if !utf8.ValidString(s) {
		return true
	}
	n, bad := 0, 0
	for _, r := range s {
		n++
		if r < 0x20 && r != '\n' && r != '\r' && r != '\t' {
			bad++
		}
	}
	return bad*10 > n
}

func stripBase64(s string) string {
	return base64Re.ReplaceAllStringFunc(s, func(b string) string {
		return fmt.Sprintf("[base64 %d chars]", len(b))
	})
}

// truncate keeps the head and the tail — errors and results tend to sit at
// the end of command output — cutting on rune boundaries.
func truncate(s string, limit int) string {
	if len(s) <= limit {
		return s
	}
	half := limit / 2
	cut := half
	for cut > 0 && !utf8.RuneStart(s[cut]) {
		cut--
	}
	start := len(s) - half
	for start < len(s) && !utf8.RuneStart(s[start]) {
		start++
	}
	return s[:cut] + fmt.Sprintf("\n…[%d chars omitted]…\n", start-cut) + s[start:]
}

// sensitivePath reports files whose content must never be captured, redacted
// or not.
func sensitivePath(p string) bool {
	if p == "" {
		return false
	}
	base := strings.ToLower(filepath.Base(strings.ReplaceAll(p, `\`, "/")))
	for _, safe := range []string{".example", ".sample", ".template", ".dist"} {
		if strings.HasSuffix(base, safe) {
			return false
		}
	}
	for _, pat := range []string{".env", ".env.*", "*.pem", "*.key", "*.p12", "*.pfx", "*.keystore", "*.jks",
		"id_rsa*", "id_ed25519*", "id_ecdsa*", "*.kdbx", ".npmrc", ".netrc", ".pypirc", "credentials*", "secrets.*"} {
		if ok, _ := filepath.Match(pat, base); ok {
			return true
		}
	}
	return false
}

func isEngramTool(name string) bool {
	return strings.HasPrefix(name, "mcp__engram__") || strings.HasPrefix(name, "mcp__plugin_engram_engram__")
}

// mutatingMCP guesses from the tool name whether an MCP call changes state
// (Serena's replace_symbol_body, a create_issue, …).
// ponytail: name heuristic; add a per-server allowlist if it misclassifies.
var mutatingVerbs = regexp.MustCompile(`(?i)(^|_)(replace|insert|rename|delete|remove|write|create|edit|update|apply|set|add|move|commit|push|merge|deploy|send|post)(_|$)`)

func mutatingMCP(name string) bool {
	parts := strings.SplitN(name, "__", 3)
	return mutatingVerbs.MatchString(parts[len(parts)-1])
}

func shellOutput(raw json.RawMessage) string {
	var r struct {
		Stdout string `json:"stdout"`
		Stderr string `json:"stderr"`
	}
	if json.Unmarshal(raw, &r) == nil && (r.Stdout != "" || r.Stderr != "") {
		if r.Stderr == "" {
			return r.Stdout
		}
		return strings.TrimSpace(r.Stdout + "\n[stderr]\n" + r.Stderr)
	}
	return responseText(raw)
}

func responseText(raw json.RawMessage) string {
	var s string
	if json.Unmarshal(raw, &s) == nil {
		return s
	}
	return string(raw)
}

func firstNonEmpty(v ...string) string {
	for _, s := range v {
		if s != "" {
			return s
		}
	}
	return ""
}
