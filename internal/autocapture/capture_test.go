package autocapture

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
	"unicode/utf8"
)

var now = time.Date(2026, 10, 7, 12, 0, 0, 0, time.UTC)

func hook(tool string, input map[string]any, response any) []byte {
	b, _ := json.Marshal(map[string]any{
		"session_id": "s1", "cwd": `C:\repo`, "hook_event_name": "PostToolUse",
		"tool_name": tool, "tool_input": input, "tool_response": response,
	})
	return b
}

func mustCapture(t *testing.T, raw []byte) Event {
	t.Helper()
	ev, ok := FromClaude(raw, now)
	if !ok {
		t.Fatalf("expected an event for %s", raw)
	}
	return ev
}

func TestEditIsAnEvent(t *testing.T) {
	ev := mustCapture(t, hook("Edit", map[string]any{"file_path": "internal/db/store.go", "old_string": "a", "new_string": "b"},
		map[string]any{"originalFile": strings.Repeat("x", 100000)}))
	if ev.Kind != KindEvent || ev.Target != "internal/db/store.go" || !strings.Contains(ev.Input, "+++ new\nb") {
		t.Fatalf("got %+v", ev)
	}
	if ev.Output != "" {
		t.Fatalf("edit response (whole original file) must not be kept, got %d chars", len(ev.Output))
	}
}

func TestReadIsAContentFreeTouch(t *testing.T) {
	ev := mustCapture(t, hook("Read", map[string]any{"file_path": "README.md"}, map[string]any{"content": "secret stuff"}))
	if ev.Kind != KindTouch || ev.Target != "README.md" || ev.Input != "" || ev.Output != "" {
		t.Fatalf("got %+v", ev)
	}
}

func TestShellReadVersusWrite(t *testing.T) {
	ev := mustCapture(t, hook("Bash", map[string]any{"command": "git status && cat go.mod"}, map[string]any{"stdout": "x"}))
	if ev.Kind != KindTouch || ev.Output != "" {
		t.Fatalf("read-only command should be a touch, got %+v", ev)
	}
	ev = mustCapture(t, hook("Bash", map[string]any{"command": "go test ./..."},
		map[string]any{"stdout": "ok", "stderr": "warning: x"}))
	if ev.Kind != KindEvent || ev.Input != "go test ./..." || !strings.Contains(ev.Output, "[stderr]\nwarning: x") {
		t.Fatalf("got %+v", ev)
	}
}

func TestSecretsInOutputAreRedacted(t *testing.T) {
	key := "AKIA" + "IOSFODNN7EXAMPLE"
	ev := mustCapture(t, hook("Bash", map[string]any{"command": "printenv | tee env.txt"},
		map[string]any{"stdout": "AWS_ACCESS_KEY_ID=" + key}))
	if strings.Contains(ev.Output, key) {
		t.Fatalf("secret leaked: %q", ev.Output)
	}
}

// A private block cut in half by truncation leaks its tail (engram #1558):
// stripping must happen first.
func TestPrivateStrippedBeforeTruncation(t *testing.T) {
	// The block opens 30 chars before the head cut, so "TOPSECRET" lands in
	// the kept head and its closing tag in the omitted middle.
	out := strings.Repeat("a", maxContent/2-30) + "<private>TOPSECRET-" + strings.Repeat("z", 50) + "-TAILSECRET</private>" + strings.Repeat("b", 10000)
	ev := mustCapture(t, hook("Bash", map[string]any{"command": "make deploy"}, map[string]any{"stdout": out}))
	if strings.Contains(ev.Output, "SECRET") {
		t.Fatalf("private content leaked: %q", ev.Output)
	}
}

// The coarse pre-redaction cut can split a secret; the half it leaves must
// fall in the part the final cut discards.
func TestSecretSplitByCoarseCutDoesNotLeak(t *testing.T) {
	key := "AKIA" + "IOSFODNN7EXAMPLE"
	out := strings.Repeat("a", 2*maxContent-10) + key + strings.Repeat("b", 50000)
	ev := mustCapture(t, hook("Bash", map[string]any{"command": "make"}, map[string]any{"stdout": out}))
	if strings.Contains(ev.Output, "AKIA") || len(ev.Output) > maxContent+64 {
		t.Fatalf("partial secret kept or output too long: len=%d", len(ev.Output))
	}
}

func TestBinaryOutputReplaced(t *testing.T) {
	bin := string([]byte{0xff, 0xfe, 0x00, 0x01}) + strings.Repeat("\x00\x02", 50000)
	ev := mustCapture(t, hook("Bash", map[string]any{"command": "make"}, map[string]any{"stdout": bin}))
	if !strings.HasPrefix(ev.Output, "[binary ") {
		t.Fatalf("got %q", ev.Output[:min(40, len(ev.Output))])
	}
}

func TestBlobsAndTruncation(t *testing.T) {
	blob := strings.Repeat("QUJD", 500)
	ev := mustCapture(t, hook("Write", map[string]any{"file_path": "img.txt", "content": "data:image/png;base64," + blob}, nil))
	if strings.Contains(ev.Input, blob) || !strings.Contains(ev.Input, "[base64 2000 chars]") {
		t.Fatalf("blob not stripped: %q", ev.Input)
	}
	long := strings.Repeat("я", 10000) // 2 bytes per rune
	got := truncate(long, maxContent)
	if len(got) > maxContent+64 || !utf8.ValidString(got) || !strings.Contains(got, "chars omitted") {
		t.Fatalf("bad truncation: len=%d valid=%v", len(got), utf8.ValidString(got))
	}
}

func TestSensitiveFilesWithheld(t *testing.T) {
	ev := mustCapture(t, hook("Write", map[string]any{"file_path": `C:\repo\.env.local`, "content": "HELLO=world"}, nil))
	if strings.Contains(ev.Input, "HELLO") {
		t.Fatalf("sensitive content kept: %q", ev.Input)
	}
	ev = mustCapture(t, hook("Write", map[string]any{"file_path": ".env.example", "content": "HELLO=world"}, nil))
	if !strings.Contains(ev.Input, "HELLO") {
		t.Fatalf(".env.example is not sensitive: %q", ev.Input)
	}
}

func TestToolSelection(t *testing.T) {
	for _, c := range []struct {
		tool string
		want string // "" = skipped
	}{
		{"TodoWrite", ""},
		{"Task", ""},
		{"mcp__engram__mem_save", ""},
		{"mcp__plugin_engram_engram__mem_search", ""},
		{"mcp__serena__replace_symbol_body", KindEvent},
		{"mcp__codebase-memory-mcp__search_graph", KindTouch},
	} {
		ev, ok := FromClaude(hook(c.tool, map[string]any{"q": "x"}, "r"), now)
		if (c.want == "") == ok || (ok && ev.Kind != c.want) {
			t.Errorf("%s: ok=%v kind=%q, want %q", c.tool, ok, ev.Kind, c.want)
		}
	}
	if _, ok := FromClaude([]byte("not json"), now); ok {
		t.Error("malformed input must be skipped")
	}
}

func TestPromptAndTurnEnd(t *testing.T) {
	raw, _ := json.Marshal(map[string]any{"session_id": "s1", "cwd": "C:\\repo", "hook_event_name": "UserPromptSubmit",
		"prompt": "fix the login bug <private>my password is hunter2hunter</private>"})
	ev := mustCapture(t, raw)
	if ev.Kind != KindPrompt || strings.Contains(ev.Input, "hunter2") || !strings.Contains(ev.Input, "fix the login bug") {
		t.Fatalf("got %+v", ev)
	}

	transcript := t.TempDir() + string(os.PathSeparator) + "t.jsonl"
	lines := []string{
		`{"type":"user","message":{"content":"hi"}}`,
		`{"type":"assistant","message":{"content":[{"type":"text","text":"first answer"}]}}`,
		`{"type":"assistant","message":{"content":[{"type":"text","text":"Fixed the login bug in auth.go."},{"type":"tool_use","name":"Edit"}]}}`,
		`{"type":"system","subtype":"stop"}`,
	}
	if err := os.WriteFile(transcript, []byte(strings.Join(lines, "\n")+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	raw, _ = json.Marshal(map[string]any{"session_id": "s1", "cwd": "C:\\repo", "hook_event_name": "Stop", "transcript_path": transcript})
	ev = mustCapture(t, raw)
	if ev.Kind != KindTurnEnd || ev.Output != "Fixed the login bug in auth.go." {
		t.Fatalf("got %+v", ev)
	}

	raw, _ = json.Marshal(map[string]any{"session_id": "s1", "hook_event_name": "Stop", "last_assistant_message": "done"})
	if ev = mustCapture(t, raw); ev.Output != "done" {
		t.Fatalf("last_assistant_message should win, got %q", ev.Output)
	}
}

// Codex sends exec as "Bash" with a command string and a string response, and
// edits as "apply_patch" with the patch text in tool_input.command.
func TestCodexPayloads(t *testing.T) {
	patch := "*** Begin Patch\n*** Update File: internal/db/store.go\n@@\n-busy_timeout=0\n+busy_timeout=5000\n*** End Patch"
	ev := mustCapture(t, hook("apply_patch", map[string]any{"command": patch}, "Success. Updated the following files:\nM internal/db/store.go"))
	want := filepath.Join(`C:\repo`, "internal/db/store.go")
	if ev.Kind != KindEvent || ev.Target != want || !strings.Contains(ev.Input, "+busy_timeout=5000") {
		t.Fatalf("got %+v (want target %q)", ev, want)
	}
	ev = mustCapture(t, hook("Bash", map[string]any{"command": "go test ./..."}, "ok  \tpkg\t0.2s"))
	if ev.Kind != KindEvent || ev.Output != "ok  \tpkg\t0.2s" {
		t.Fatalf("string tool_response not kept: %+v", ev)
	}
	if ev := mustCapture(t, hook("Bash", map[string]any{"command": "rg -n TODO src"}, "x")); ev.Kind != KindTouch {
		t.Fatalf("read-only Codex exec must be a touch: %+v", ev)
	}
}

func TestReadOnlyCommand(t *testing.T) {
	read := []string{
		"ls -la", "cat a.go | grep foo", "git status", "git log --oneline -5", "git diff HEAD~1",
		"cd repo && git status", "rg TODO internal/", "Get-Content x.txt | Select-String foo",
		"git branch -a", "git remote -v", "FOO=1 cat x", "sed -n '1,20p' a.go", "go list ./...",
		"Get-ChildItem C:\\x 2>$null", "ls 2>&1", "cat x > /dev/null",
	}
	write := []string{
		"go test ./...", "rm -rf x", "echo hi > out.txt", "git commit -m x", "git branch feature",
		"git remote add origin u", "git branch -D old", "sed -i s/a/b/ f", "gh pr create",
		"gh api -X POST repos/x", "Get-Content a | Set-Content b", "npm install", "unknowncmd",
		"cat a; rm b", "",
	}
	for _, c := range read {
		if !ReadOnlyCommand(c) {
			t.Errorf("should be read-only: %q", c)
		}
	}
	for _, c := range write {
		if ReadOnlyCommand(c) {
			t.Errorf("should not be read-only: %q", c)
		}
	}
}

func TestWriteSpool(t *testing.T) {
	dir := t.TempDir()
	if Enabled(dir) {
		t.Fatal("must be off without config file")
	}
	ev := mustCapture(t, hook("Edit", map[string]any{"file_path": "a.go", "old_string": "x", "new_string": "y"}, nil))
	for i := 0; i < 3; i++ {
		if err := Write(SpoolDir(dir), ev); err != nil {
			t.Fatal(err)
		}
	}
	entries, _ := os.ReadDir(SpoolDir(dir))
	if len(entries) != 3 {
		t.Fatalf("want 3 files, got %d", len(entries))
	}
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), ".") {
			t.Fatalf("temp file left behind: %s", e.Name())
		}
		data, _ := os.ReadFile(SpoolDir(dir) + string(os.PathSeparator) + e.Name())
		var back Event
		if json.Unmarshal(data, &back) != nil || back.Target != "a.go" || back.V != 1 {
			t.Fatalf("bad spool file: %s", data)
		}
	}
}
