package compress

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Gentleman-Programming/engram/v3/internal/autocapture"
	"github.com/Gentleman-Programming/engram/v3/internal/project"
	"github.com/Gentleman-Programming/engram/v3/internal/store"
)

var t0 = time.Date(2026, 10, 7, 12, 0, 0, 0, time.UTC)

type fakeSink struct {
	obs      []store.AddObservationParams
	updates  map[int64]store.UpdateObservationParams
	sessions map[string]string
	existing []store.Observation
	failAdd  bool
}

func (f *fakeSink) DetectProject(dir string) project.DetectionResult {
	switch {
	case strings.Contains(dir, "repoB"):
		return project.DetectionResult{Project: "repob", Source: project.SourceGitRemote}
	case strings.Contains(dir, "scratch"):
		return project.DetectionResult{Project: "scratch", Source: project.SourceDirBasename}
	}
	return project.DetectionResult{Project: "proj", Source: project.SourceGitRoot}
}
func (f *fakeSink) CreateSession(id, p, dir string) error {
	if f.sessions == nil {
		f.sessions = map[string]string{}
	}
	f.sessions[id] = p
	return nil
}
func (f *fakeSink) AddObservation(p store.AddObservationParams) (int64, error) {
	if f.failAdd {
		return 0, errors.New("disk full")
	}
	f.obs = append(f.obs, p)
	return int64(len(f.obs)), nil
}
func (f *fakeSink) RecentSessionObservations(string, int) ([]store.Observation, error) {
	return f.existing, nil
}
func (f *fakeSink) UpdateObservation(id int64, p store.UpdateObservationParams) (*store.Observation, error) {
	if f.updates == nil {
		f.updates = map[int64]store.UpdateObservationParams{}
	}
	f.updates[id] = p
	return &store.Observation{ID: id}, nil
}

// fakeGen records what it was asked and replays canned results.
type fakeGen struct {
	users   []string
	reply   func(n int, user string, wantSummary bool) (result, error)
	summary []bool
}

func (g *fakeGen) generate(_ context.Context, _, user string, wantSummary bool) (result, callInfo, error) {
	g.users = append(g.users, user)
	g.summary = append(g.summary, wantSummary)
	r, err := g.reply(len(g.users), user, wantSummary)
	return r, callInfo{Model: "fake", Usage: usage{In: 100, Out: 10}}, err
}

func spoolEvents(t *testing.T, dir string, evs ...autocapture.Event) {
	t.Helper()
	for _, ev := range evs {
		if err := autocapture.Write(autocapture.SpoolDir(dir), ev); err != nil {
			t.Fatal(err)
		}
	}
}

func ev(kind, tool string, at time.Time, input string) autocapture.Event {
	return autocapture.Event{V: 1, Agent: "claude", Kind: kind, SessionID: "s1", CWD: `C:\repo`, Tool: tool,
		Target: "a.go", Input: input, At: at}
}

func queued(t *testing.T, dir string) int {
	t.Helper()
	entries, _ := os.ReadDir(autocapture.SpoolDir(dir))
	n := 0
	for _, e := range entries {
		if !e.IsDir() && strings.HasSuffix(e.Name(), ".json") && !strings.HasPrefix(e.Name(), ".") {
			n++
		}
		if e.IsDir() && strings.HasPrefix(e.Name(), claimPrefix) {
			t.Fatalf("claim directory left behind: %s", e.Name())
		}
	}
	return n
}

func testConfig() Config {
	c, _ := Load(os.TempDir() + "/does-not-exist")
	return c
}

func TestTurnProducesObservationsAndSummary(t *testing.T) {
	dir := t.TempDir()
	spoolEvents(t, dir,
		ev(autocapture.KindPrompt, "", t0, "make sqlite survive parallel sessions"),
		ev(autocapture.KindEvent, "Edit", t0.Add(time.Second), "busy_timeout=5000"),
		ev(autocapture.KindTouch, "Read", t0.Add(2*time.Second), ""),
		autocapture.Event{V: 1, Kind: autocapture.KindTurnEnd, SessionID: "s1", CWD: `C:\repo`, Output: "Added busy_timeout.", At: t0.Add(3 * time.Second)},
	)
	sink := &fakeSink{existing: []store.Observation{{Type: "decision", Title: "Use WAL mode"}}}
	gen := &fakeGen{reply: func(int, string, bool) (result, error) {
		return result{
			Observations: []record{{Type: "config", Title: "SQLite busy_timeout", What: "Set busy_timeout=5000", Why: "locked errors", TopicKey: "config/sqlite"}},
			Summary:      &summary{Goal: "Fix locking", Accomplished: []string{"busy_timeout"}},
		}, nil
	}}
	rep, err := Drain(context.Background(), sink, gen, testConfig(), dir, t0.Add(time.Minute), false, nil)
	if err != nil {
		t.Fatal(err)
	}
	if rep.Calls != 1 || rep.Observations != 1 || rep.Summaries != 1 || !gen.summary[0] {
		t.Fatalf("report %+v summary=%v", rep, gen.summary)
	}
	u := gen.users[0]
	for _, want := range []string{"make sqlite survive", "[decision] Use WAL mode", "busy_timeout=5000", "Added busy_timeout.", "## Files looked at"} {
		if !strings.Contains(u, want) {
			t.Errorf("prompt lacks %q:\n%s", want, u)
		}
	}
	o := sink.obs[0]
	if o.ToolName != ToolName || o.Project != "proj" || o.TopicKey != "config/sqlite" || !strings.HasPrefix(o.Content, "**What**: Set busy_timeout=5000\n**Why**: locked errors") {
		t.Fatalf("observation %+v", o)
	}
	s := sink.obs[1]
	if s.Type != "session_summary" || s.TopicKey != "session/s1" || !strings.Contains(s.Content, "## Goal\nFix locking") {
		t.Fatalf("summary %+v", s)
	}
	if sink.sessions["s1"] != "proj" || queued(t, dir) != 0 {
		t.Fatalf("session not created or spool not emptied")
	}
	if data, _ := os.ReadFile(filepath.Join(dir, touchesFile)); !strings.Contains(string(data), `"target":"a.go"`) {
		t.Fatalf("touch not kept: %s", data)
	}
}

func TestNotReadyStaysQueued(t *testing.T) {
	dir := t.TempDir()
	spoolEvents(t, dir, ev(autocapture.KindEvent, "Edit", t0, "x"))
	gen := &fakeGen{reply: func(int, string, bool) (result, error) { return result{}, nil }}
	rep, _ := Drain(context.Background(), &fakeSink{}, gen, testConfig(), dir, t0.Add(time.Minute), false, nil)
	if rep.Deferred != 1 || len(gen.users) != 0 || queued(t, dir) != 1 {
		t.Fatalf("mid-turn event must wait: %+v", rep)
	}
	// ...until the session goes quiet.
	rep, _ = Drain(context.Background(), &fakeSink{}, gen, testConfig(), dir, t0.Add(11*time.Minute), false, nil)
	if rep.Calls != 1 || gen.summary[0] || queued(t, dir) != 0 {
		t.Fatalf("idle session must flush without summary: %+v", rep)
	}
}

func TestFailureKeepsEverything(t *testing.T) {
	dir := t.TempDir()
	spoolEvents(t, dir, ev(autocapture.KindEvent, "Edit", t0, "x"),
		autocapture.Event{V: 1, Kind: autocapture.KindTurnEnd, SessionID: "s1", At: t0.Add(time.Second)})
	gen := &fakeGen{reply: func(int, string, bool) (result, error) { return result{}, fmt.Errorf("%w: quota", errNoModel) }}
	rep, err := Drain(context.Background(), &fakeSink{}, gen, testConfig(), dir, t0.Add(time.Minute), false, nil)
	if !errors.Is(err, errNoModel) || rep.Failed != 1 || queued(t, dir) != 2 {
		t.Fatalf("events must stay queued on failure: err=%v rep=%+v queued=%d", err, rep, queued(t, dir))
	}
}

// A chunk that succeeded is not redone when a later chunk fails.
func TestFinishedChunksAreNotRepeated(t *testing.T) {
	dir := t.TempDir()
	cfg := testConfig()
	cfg.MaxBatchChars = 200
	big := strings.Repeat("y", 150)
	spoolEvents(t, dir, ev(autocapture.KindEvent, "Edit", t0, big), ev(autocapture.KindEvent, "Edit", t0.Add(time.Second), big),
		autocapture.Event{V: 1, Kind: autocapture.KindTurnEnd, SessionID: "s1", At: t0.Add(2 * time.Second)})
	sink := &fakeSink{}
	gen := &fakeGen{reply: func(n int, _ string, _ bool) (result, error) {
		if n == 2 {
			return result{}, errors.New("timeout")
		}
		return result{Observations: []record{{Type: "feature", Title: fmt.Sprint("chunk ", n), What: "w"}}}, nil
	}}
	_, _ = Drain(context.Background(), sink, gen, cfg, dir, t0.Add(time.Minute), false, nil)
	if len(sink.obs) != 1 || queued(t, dir) != 2 { // second edit + turn end remain
		t.Fatalf("obs=%d queued=%d", len(sink.obs), queued(t, dir))
	}
	gen.reply = func(n int, user string, wantSummary bool) (result, error) {
		if strings.Contains(user, "chunk 1") == false || !wantSummary {
			return result{}, fmt.Errorf("retry must see chunk 1 as recorded and ask for the summary")
		}
		return result{Observations: []record{{Type: "feature", Title: "chunk 3", What: "w"}}, Summary: &summary{Goal: "g"}}, nil
	}
	sink.existing = []store.Observation{{Type: "feature", Title: "chunk 1"}}
	if _, err := Drain(context.Background(), sink, gen, cfg, dir, t0.Add(2*time.Minute), false, nil); err != nil {
		t.Fatal(err)
	}
	if len(sink.obs) != 3 || queued(t, dir) != 0 {
		t.Fatalf("obs=%d queued=%d", len(sink.obs), queued(t, dir))
	}
}

func TestDryRunWritesNothing(t *testing.T) {
	dir := t.TempDir()
	spoolEvents(t, dir, ev(autocapture.KindEvent, "Edit", t0, "x"),
		autocapture.Event{V: 1, Kind: autocapture.KindTurnEnd, SessionID: "s1", At: t0.Add(time.Second)})
	sink := &fakeSink{}
	var printed []string
	gen := &fakeGen{reply: func(int, string, bool) (result, error) {
		return result{Observations: []record{{Type: "bugfix", Title: "t", What: "w"}}, Summary: &summary{Goal: "g"}}, nil
	}}
	if _, err := Drain(context.Background(), sink, gen, testConfig(), dir, t0.Add(time.Minute), true, func(s string) { printed = append(printed, s) }); err != nil {
		t.Fatal(err)
	}
	if len(sink.obs) != 0 || len(sink.sessions) != 0 || queued(t, dir) != 2 || len(printed) != 2 {
		t.Fatalf("dry run must not write: obs=%d queued=%d printed=%d", len(sink.obs), queued(t, dir), len(printed))
	}
}

// Edits are attributed to the repository their file lives in; a scratch
// directory never becomes a project; the summary follows the busiest project.
func TestEventsAttributedToTheirRepository(t *testing.T) {
	dir := t.TempDir()
	at := func(s int) time.Time { return t0.Add(time.Duration(s) * time.Second) }
	mk := func(target string, s int) autocapture.Event {
		e := ev(autocapture.KindEvent, "Edit", at(s), "x")
		e.Target = target
		return e
	}
	spoolEvents(t, dir, mk(`C:\work\repoB\a.go`, 1), mk(`C:\work\repoB\b.go`, 2), mk(`C:\tmp\scratch\x.txt`, 3), mk("rel/path.go", 4),
		autocapture.Event{V: 1, Kind: autocapture.KindTurnEnd, SessionID: "s1", CWD: `C:\repo`, At: at(5)})
	sink := &fakeSink{}
	var projects []string
	gen := &fakeGen{reply: func(n int, user string, wantSummary bool) (result, error) {
		projects = append(projects, strings.SplitN(strings.TrimPrefix(user, "Project: "), "\n", 2)[0])
		r := result{Observations: []record{{Type: "feature", Title: fmt.Sprint("r", n), What: "w"}}}
		if wantSummary {
			r.Summary = &summary{Goal: "g"}
		}
		return r, nil
	}}
	if _, err := Drain(context.Background(), sink, gen, testConfig(), dir, t0.Add(time.Minute), false, nil); err != nil {
		t.Fatal(err)
	}
	if strings.Join(projects, ",") != "proj,repob" || !gen.summary[1] || gen.summary[0] {
		t.Fatalf("calls per project = %v, summary flags %v (want proj then repob with the summary)", projects, gen.summary)
	}
	if sink.obs[0].Project != "proj" || sink.obs[1].Project != "repob" || sink.obs[2].Type != "session_summary" || sink.obs[2].Project != "repob" {
		t.Fatalf("observations %+v", sink.obs)
	}
}

func TestEventDirs(t *testing.T) {
	abs, cwd := `C:\work\repoB`, `C:\work\repoA`
	if filepath.Separator == '/' {
		abs, cwd = "/work/repoB", "/work/repoA"
	}
	sh := func(cmd string) autocapture.Event { return autocapture.Event{Tool: "PowerShell", Input: cmd, CWD: cwd} }
	for _, c := range []struct {
		ev   autocapture.Event
		want string // first candidate
	}{
		{autocapture.Event{Target: filepath.Join(abs, "a.go"), CWD: cwd}, abs},
		{autocapture.Event{Tool: "Edit", Target: "rel/a.go", CWD: cwd}, cwd},
		{sh("cd " + abs + "; go test ./..."), abs},
		{sh(`Set-Location "` + abs + `" && make`), abs},
		// the rwsimgpro case: the directory change is not the first statement
		{sh(`$env:PATH="C:\tools;$env:PATH"; Set-Location ` + abs + `; pnpm test`), abs},
		{sh("git -C " + abs + " status"), abs},
		{sh("go vet " + filepath.Join(abs, "cmd", "x")), filepath.Join(abs, "cmd")},
		{sh("cd sub && make"), cwd},
		{sh("go test ./..."), cwd},
	} {
		got := eventDirs(c.ev)
		if len(got) == 0 || got[0] != c.want {
			t.Errorf("%q: got %q want first %q", c.ev.Input+c.ev.Target, got, c.want)
		}
		if got[len(got)-1] != cwd {
			t.Errorf("%q: the shell's own cwd must stay the last resort, got %q", c.ev.Input, got)
		}
	}
}

func TestRepeatedTitleIsSkipped(t *testing.T) {
	dir := t.TempDir()
	spoolEvents(t, dir, ev(autocapture.KindEvent, "Edit", t0, "x"),
		autocapture.Event{V: 1, Kind: autocapture.KindTurnEnd, SessionID: "s1", At: t0.Add(time.Second)})
	sink := &fakeSink{existing: []store.Observation{{Type: "bugfix", Title: "Workerd hang on throw"}}}
	gen := &fakeGen{reply: func(int, string, bool) (result, error) {
		return result{Observations: []record{{Type: "bugfix", Title: "workerd HANG on throw", What: "w"}, {Type: "feature", Title: "new", What: "w"}},
			Summary: &summary{Goal: "g"}}, nil
	}}
	if _, err := Drain(context.Background(), sink, gen, testConfig(), dir, t0.Add(time.Minute), false, nil); err != nil {
		t.Fatal(err)
	}
	if len(sink.obs) != 2 || sink.obs[0].Title != "new" {
		t.Fatalf("got %+v", sink.obs)
	}
}

func TestTopicKeysOnlyForEvolvingTypes(t *testing.T) {
	r, _, err := parse(`{"observations":[
		{"type":"bugfix","title":"a","what":"w","topic_key":"proxy/harness"},
		{"type":"decision","title":"b","what":"w","topic_key":"proxy/window"},
		{"type":"config","title":"c","what":"w","topic_key":"proxy/window"}]}`, false)
	if err != nil {
		t.Fatal(err)
	}
	if r.Observations[0].TopicKey != "" || r.Observations[1].TopicKey != "proxy/window" || r.Observations[2].TopicKey != "" {
		t.Fatalf("keys: %q %q %q", r.Observations[0].TopicKey, r.Observations[1].TopicKey, r.Observations[2].TopicKey)
	}
	many := `{"observations":[` + strings.TrimSuffix(strings.Repeat(`{"type":"feature","title":"t","what":"w"},`, 12), ",") + `]}`
	if r, dropped, _ := parse(many, false); len(r.Observations) != maxRecordsPerCall || dropped != 4 {
		t.Fatalf("cap: kept %d dropped %d", len(r.Observations), dropped)
	}
}

func TestParse(t *testing.T) {
	r, dropped, err := parse("Sure!\n```json\n{\"observations\":[{\"type\":\"Bugfix\",\"title\":\"x\",\"what\":\"y\",\"topic_key\":\"Bad Key\"},{\"type\":\"nonsense\",\"title\":\"a\",\"what\":\"b\"},{\"type\":\"config\",\"title\":\"\",\"what\":\"b\"}]}\n```", false)
	if err != nil || len(r.Observations) != 1 || dropped != 2 || r.Observations[0].Type != "bugfix" || r.Observations[0].TopicKey != "" {
		t.Fatalf("r=%+v dropped=%d err=%v", r, dropped, err)
	}
	if _, _, err := parse(`{"observations":[]}`, true); err == nil {
		t.Fatal("missing summary must be an error when one was asked for")
	}
	if _, _, err := parse("no json here", false); err == nil {
		t.Fatal("garbage must be an error")
	}
}

type scriptedChat struct {
	calls []string
	reply func(model string, n int) (string, error)
}

func (s *scriptedChat) chat(_ context.Context, model, _, user string, _ json.RawMessage) (string, usage, error) {
	s.calls = append(s.calls, model+"|"+user)
	text, err := s.reply(model, len(s.calls))
	return text, usage{In: 10, Out: 1}, err
}

func ollamaBackends(chat chatter, models ...string) []backend {
	var bs []backend
	for _, m := range models {
		bs = append(bs, backend{name: m, provider: "ollama", model: m, client: chat})
	}
	return bs
}

func TestChainFallsThroughAndRemembers(t *testing.T) {
	dir := t.TempDir()
	chat := &scriptedChat{reply: func(model string, n int) (string, error) {
		switch model {
		case "old":
			return "", &callError{Kind: errRetired, Status: 410}
		case "busy":
			return "", &callError{Kind: errQuota, Status: 429}
		}
		if n == 3 {
			return "not json", nil // first try on "good": repaired on the second
		}
		return `{"observations":[]}`, nil
	}}
	c := &chain{backends: ollamaBackends(chat, "old", "busy", "good"), st: loadState(dir), dataDir: dir, now: func() time.Time { return t0 }}
	_, info, err := c.generate(context.Background(), "sys", "user", false)
	if err != nil || info.Model != "good" || info.Attempts != 2 {
		t.Fatalf("info=%+v err=%v", info, err)
	}
	if !strings.Contains(chat.calls[3], "previous reply was rejected") {
		t.Fatalf("repair prompt missing: %q", chat.calls[3])
	}
	st := loadState(dir)
	if !st.Models["old"].Disabled || !st.Models["busy"].BlockedUntil.After(t0) {
		t.Fatalf("state not persisted: %+v %+v", st.Models["old"], st.Models["busy"])
	}
	// Next call skips both without asking them.
	chat.calls = nil
	_, info, _ = c.generate(context.Background(), "sys", "user", false)
	if len(chat.calls) != 1 || info.Model != "good" {
		t.Fatalf("blocked models were called again: %v", chat.calls)
	}
}

// A bad credential disables its own provider only: the Ollama key failing must
// not keep the Claude fallback from running.
func TestAuthFailureSkipsOnlyItsProvider(t *testing.T) {
	ollama := &scriptedChat{reply: func(string, int) (string, error) { return "", &callError{Kind: errAuth, Status: 401} }}
	claude := &scriptedChat{reply: func(string, int) (string, error) { return `{"observations":[]}`, nil }}
	bs := append(ollamaBackends(ollama, "a", "b"), backend{name: "claude:sonnet", provider: "claude", model: "sonnet", client: claude})
	c := &chain{backends: bs, st: loadState(t.TempDir()), dataDir: t.TempDir(), now: time.Now}
	_, info, err := c.generate(context.Background(), "s", "u", false)
	if err != nil || info.Model != "claude:sonnet" || len(ollama.calls) != 1 {
		t.Fatalf("err=%v model=%q ollama calls=%d (second Ollama model must be skipped)", err, info.Model, len(ollama.calls))
	}
	only := &chain{backends: ollamaBackends(ollama, "a"), st: loadState(t.TempDir()), dataDir: t.TempDir(), now: time.Now}
	if _, _, err := only.generate(context.Background(), "s", "u", false); !errors.Is(err, errNoModel) {
		t.Fatalf("with no other provider the drain must stop: %v", err)
	}
}

func TestClaudeIsTheFallbackAndHasABudget(t *testing.T) {
	dir := t.TempDir()
	clock := t0
	ollama := &scriptedChat{reply: func(string, int) (string, error) { return "", &callError{Kind: errQuota, Status: 429} }}
	claude := &scriptedChat{reply: func(string, int) (string, error) { return `{"observations":[]}`, nil }}
	bs := append(ollamaBackends(ollama, "deepseek"), backend{name: "claude:sonnet", provider: "claude", model: "sonnet", client: claude, perHour: 2})
	c := &chain{backends: bs, st: loadState(dir), dataDir: dir, now: func() time.Time { return clock }}
	for i := 0; i < 2; i++ {
		if _, info, err := c.generate(context.Background(), "s", "u", false); err != nil || info.Model != "claude:sonnet" {
			t.Fatalf("call %d: model=%q err=%v", i, info.Model, err)
		}
	}
	if len(ollama.calls) != 1 {
		t.Fatalf("a model out of quota must be skipped while it backs off, called %d times", len(ollama.calls))
	}
	if _, _, err := c.generate(context.Background(), "s", "u", false); !errors.Is(err, errNoModel) || len(claude.calls) != 2 {
		t.Fatalf("the hourly budget must stop the third Claude call: err=%v calls=%d", err, len(claude.calls))
	}
	clock = t0.Add(61 * time.Minute)
	ollama.reply = func(string, int) (string, error) { return `{"observations":[]}`, nil }
	if _, info, err := c.generate(context.Background(), "s", "u", false); err != nil || info.Model != "deepseek" {
		t.Fatalf("after an hour Ollama is retried first: model=%q err=%v", info.Model, err)
	}
}

func TestCodexClient(t *testing.T) {
	t.Setenv("CODEX_SANDBOX", "seatbelt")
	dir := t.TempDir()
	var gotArgs, gotEnv []string
	var gotStdin string
	events := `{"type":"thread.started"}` + "\n" + `{"type":"turn.completed","usage":{"input_tokens":16155,"output_tokens":135}}` + "\n"
	reply := `{"observations":[],"summary":null}`
	c := &codexClient{exe: "codex.exe", effort: "max", dir: dir, timeout: time.Minute,
		run: func(_ context.Context, _ string, args []string, stdin, _ string, env []string) ([]byte, []byte, error) {
			gotArgs, gotStdin, gotEnv = args, stdin, env
			for i, a := range args {
				if a == "-o" {
					_ = os.WriteFile(args[i+1], []byte(reply), 0o600)
				}
			}
			return []byte(events), nil, nil
		}}
	text, u, err := c.chat(context.Background(), "gpt-6-luna", "SYS", "USER", schema)
	if err != nil || text != reply || u.In != 16155 || u.Out != 135 {
		t.Fatalf("text=%q u=%+v err=%v", text, u, err)
	}
	joined := strings.Join(gotArgs, " ")
	for _, want := range []string{"exec -m gpt-6-luna", "-c model_reasoning_effort=max", "--ignore-user-config",
		"--disable hooks", "--ephemeral", "-s read-only", "--json", "--output-schema"} {
		if !strings.Contains(joined, want) {
			t.Errorf("args lack %q: %s", want, joined)
		}
	}
	if gotStdin != "SYS\n\nUSER" {
		t.Fatalf("stdin %q", gotStdin)
	}
	if env := strings.Join(gotEnv, "\n"); strings.Contains(env, "CODEX_SANDBOX") || !strings.Contains(env, "ENGRAM_INTERNAL=1") {
		t.Fatalf("env:\n%s", env)
	}
	// The schema must satisfy strict structured output and still parse with
	// a null summary when none was asked for.
	if r, _, err := parse(text, false); err != nil || r.Summary != nil {
		t.Fatalf("parse: %v", err)
	}
	if _, _, err := parse(text, true); err == nil {
		t.Fatal("null summary must be rejected when a summary was asked for")
	}

	for _, c2 := range []struct {
		events string
		kind   string
	}{
		{`{"type":"turn.failed","error":{"message":"You've hit your usage limit. Try again at 9:00 PM."}}`, errQuota},
		{`{"type":"error","message":"401 Unauthorized: please log in"}`, errAuth},
		{`{"type":"turn.failed","error":{"message":"stream disconnected"}}`, errTransient},
	} {
		events, reply = c2.events, ""
		_, _, err := c.chat(context.Background(), "gpt-6-luna", "s", "u", schema)
		var ce *callError
		if !errors.As(err, &ce) || ce.Kind != c2.kind {
			t.Errorf("%s: got %v, want %s", c2.events, err, c2.kind)
		}
	}
	if left, _ := filepath.Glob(filepath.Join(dir, "reply-*")); len(left) != 0 {
		t.Fatalf("reply files left behind: %v", left)
	}
}

func TestClaudeClient(t *testing.T) {
	t.Setenv("ANTHROPIC_BASE_URL", "http://host-relay")
	t.Setenv("CLAUDE_CODE_SESSION_ID", "parent")
	dir := t.TempDir()
	var gotArgs, gotEnv []string
	var gotStdin, gotDir string
	reply := `{"type":"result","is_error":false,"result":"","structured_output":{"observations":[]},"usage":{"input_tokens":10,"cache_creation_input_tokens":100,"cache_read_input_tokens":1000,"output_tokens":5}}`
	var runErr error
	c := &claudeClient{exe: "claude", effort: "high", token: "tok", dir: dir, timeout: time.Minute,
		run: func(_ context.Context, _ string, args []string, stdin, d string, env []string) ([]byte, []byte, error) {
			gotArgs, gotStdin, gotDir, gotEnv = args, stdin, d, env
			return []byte(reply), nil, runErr
		}}
	text, u, err := c.chat(context.Background(), "claude-sonnet-5-5", "SYS", "USER", schema)
	if err != nil || text != `{"observations":[]}` || u.In != 1110 || u.Out != 5 {
		t.Fatalf("text=%q u=%+v err=%v", text, u, err)
	}
	joined := strings.Join(gotArgs, "\x1f")
	for _, want := range []string{"-p", "--model\x1fclaude-sonnet-5-5", "--effort\x1fhigh", "--system-prompt\x1fSYS",
		"--tools\x1f\x1f", "--strict-mcp-config", "--no-session-persistence", `{"disableAllHooks":true}`, "--json-schema"} {
		if !strings.Contains(joined, want) {
			t.Errorf("args lack %q: %q", want, gotArgs)
		}
	}
	env := strings.Join(gotEnv, "\n")
	if strings.Contains(env, "ANTHROPIC_BASE_URL") || strings.Contains(env, "CLAUDE_CODE_SESSION_ID") ||
		!strings.Contains(env, "CLAUDE_CODE_OAUTH_TOKEN=tok") || !strings.Contains(env, "ENGRAM_INTERNAL=1") {
		t.Fatalf("env not scrubbed / token missing:\n%s", env)
	}
	if gotStdin != "USER" || gotDir != dir {
		t.Fatalf("stdin=%q dir=%q", gotStdin, gotDir)
	}

	for _, c2 := range []struct {
		reply string
		err   error
		kind  string
	}{
		{`{"is_error":true,"result":"Failed to authenticate. API Error: 401 OAuth access token is invalid."}`, errors.New("exit status 1"), errAuth},
		{`{"is_error":true,"result":"Claude AI usage limit reached|1791400000"}`, errors.New("exit status 1"), errQuota},
		{`{"is_error":true,"result":"API Error: 529 Overloaded"}`, nil, errTransient},
		{"", exec.ErrNotFound, errRetired},
		{`{"is_error":false,"result":""}`, nil, errOutput},
	} {
		reply, runErr = c2.reply, c2.err
		_, _, err := c.chat(context.Background(), "m", "s", "u", schema)
		var ce *callError
		if !errors.As(err, &ce) || ce.Kind != c2.kind {
			t.Errorf("reply %q: got %v, want %s", c2.reply, err, c2.kind)
		}
	}
}

func TestOllamaClient(t *testing.T) {
	var got map[string]any
	status, body := 200, `{"message":{"content":"{\"observations\":[]}"},"prompt_eval_count":42,"eval_count":7}`
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/chat" || r.Header.Get("Authorization") != "Bearer k" {
			w.WriteHeader(400)
			return
		}
		_ = json.NewDecoder(r.Body).Decode(&got)
		w.WriteHeader(status)
		_, _ = w.Write([]byte(body))
	}))
	defer srv.Close()
	cfg := testConfig()
	cfg.Ollama.URL = srv.URL
	o := newOllama(cfg, "k")
	text, u, err := o.chat(context.Background(), "deepseek-v4.1-flash", "s", "u", schema)
	if err != nil || text != `{"observations":[]}` || u.In != 42 || u.Out != 7 || got["think"] != false || got["stream"] != false {
		t.Fatalf("text=%q u=%+v err=%v req=%v", text, u, err, got)
	}
	if _, _, _ = o.chat(context.Background(), "glm-5.3-flash", "s", "u", schema); got["think"] != true {
		t.Fatalf("glm must run with thinking on")
	}
	for _, c := range []struct {
		status int
		body   string
		kind   string
	}{
		{429, `{"error":"rate limited"}`, errQuota},
		{403, `{"error":"you have reached your weekly usage limit"}`, errQuota},
		{410, `{"error":"model retired"}`, errRetired},
		{401, `{"error":"unauthorized"}`, errAuth},
		{502, `bad gateway`, errTransient},
		{200, `{"message":{"content":""}}`, errOutput},
	} {
		status, body = c.status, c.body
		_, _, err := o.chat(context.Background(), "m", "s", "u", schema)
		var ce *callError
		if !errors.As(err, &ce) || ce.Kind != c.kind {
			t.Errorf("HTTP %d %s: got %v, want kind %s", c.status, c.body, err, c.kind)
		}
	}
}

func TestExistingFindsSummaryPastImportedHistory(t *testing.T) {
	key := "session/s"
	obs := []store.Observation{ // newest first, as RecentSessionObservations returns them
		{Type: "session_summary", Title: "Session summary: p", Content: "## Goal\nlatest", TopicKey: &key},
		{Type: "bugfix", Title: "newest record"},
		{Type: "feature", Title: "older record"},
		{Type: "session_summary", Title: "Session summary: imported turn"},
	}
	for i := 0; i < 300; i++ {
		obs = append(obs, store.Observation{Type: "discovery", Title: fmt.Sprintf("imported %d", i)})
	}
	d := &drainer{sink: &fakeSink{existing: obs}}
	recorded, prev, titles, _ := d.existing("s")
	if prev != "## Goal\nlatest" {
		t.Fatalf("summary not found: %q", prev)
	}
	if len(recorded) != 40 || !strings.Contains(recorded[39], "newest record") || !strings.Contains(recorded[38], "older record") {
		t.Fatalf("want the 40 newest, oldest first; got %v … %v", recorded[:2], recorded[38:])
	}
	if !titles["newest record"] || titles["session summary: imported turn"] {
		t.Fatalf("titles %v", len(titles))
	}
}

// The audit case: one chunk records a failing self-check, the next finds the
// cause. The second must rewrite the first, not stand beside it.
func TestContinuationRewritesEarlierRecord(t *testing.T) {
	dir := t.TempDir()
	spoolEvents(t, dir,
		ev(autocapture.KindEvent, "Edit", t0, "clear demand index when satisfied from disk"),
		autocapture.Event{V: 1, Kind: autocapture.KindTurnEnd, SessionID: "s1", CWD: `C:\repo`, Output: "Fixed.", At: t0.Add(time.Second)},
	)
	auto := ToolName
	sink := &fakeSink{existing: []store.Observation{
		{ID: 7, Type: "bugfix", Title: "Evict self-check keeps chunks older than head-3", ToolName: &auto,
			Content: "**What**: disk held chunk 5 far behind the read head\n**Why**: unknown"},
		{ID: 8, Type: "decision", Title: "Agent saved this itself"},
	}}
	gen := &fakeGen{reply: func(int, string, bool) (result, error) {
		return result{Observations: []record{
			{Type: "bugfix", Title: "Stale demand re-fetched evicted chunk", What: "chunk 5 came back because the demand index stayed set", Updates: 7},
			{Type: "decision", Title: "Rewrite of the agent's record", What: "must not land on #8", Updates: 8},
			{Type: "feature", Title: "Unknown target", What: "no #99 in this session", Updates: 99},
		}, Summary: &summary{Goal: "g", Accomplished: []string{"a"}}}, nil
	}}
	rep, err := Drain(context.Background(), sink, gen, testConfig(), dir, t0.Add(time.Minute), false, nil)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(gen.users[0], "#7 [bugfix] Evict self-check keeps chunks older than head-3 — disk held chunk 5 far behind the read head") {
		t.Fatalf("the model must see the record's id and gist:\n%s", gen.users[0])
	}
	u, ok := sink.updates[7]
	if rep.Updated != 1 || !ok || *u.Title != "Stale demand re-fetched evicted chunk" || !strings.Contains(*u.Content, "demand index stayed set") {
		t.Fatalf("record 7 not rewritten: report %+v, updates %v", rep, sink.updates)
	}
	if _, touched := sink.updates[8]; touched {
		t.Fatal("an agent-written record was overwritten")
	}
	var added []string
	for _, o := range sink.obs {
		if o.Type != "session_summary" {
			added = append(added, o.Title)
		}
	}
	if strings.Join(added, "|") != "Rewrite of the agent's record|Unknown target" || rep.Observations != 2 {
		t.Fatalf("records with an unusable target must be added as new: %v", added)
	}
}
