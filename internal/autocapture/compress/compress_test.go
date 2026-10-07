package compress

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
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
func (f *fakeSink) SessionObservations(string, int) ([]store.Observation, error) {
	return f.existing, nil
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

func TestEventDir(t *testing.T) {
	abs := `C:\work\repoB`
	if filepath.Separator == '/' {
		abs = "/work/repoB"
	}
	for _, c := range []struct {
		ev   autocapture.Event
		want string
	}{
		{autocapture.Event{Target: filepath.Join(abs, "a.go")}, abs},
		{autocapture.Event{Target: "rel/a.go"}, ""},
		{autocapture.Event{Input: "cd " + abs + "; go test ./..."}, abs},
		{autocapture.Event{Input: `Set-Location "` + abs + `" && make`}, abs},
		{autocapture.Event{Input: "cd sub && make"}, ""},
		{autocapture.Event{Input: "go test ./..."}, ""},
	} {
		if got := eventDir(c.ev); got != c.want {
			t.Errorf("%+v: got %q want %q", c.ev, got, c.want)
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
	c := &chain{models: []string{"old", "busy", "good"}, client: chat, st: loadState(dir), dataDir: dir, now: func() time.Time { return t0 }}
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

func TestChainStopsOnAuth(t *testing.T) {
	chat := &scriptedChat{reply: func(string, int) (string, error) { return "", &callError{Kind: errAuth, Status: 401} }}
	c := &chain{models: []string{"a", "b"}, client: chat, st: loadState(t.TempDir()), dataDir: t.TempDir(), now: time.Now}
	if _, _, err := c.generate(context.Background(), "s", "u", false); !errors.Is(err, errNoModel) || len(chat.calls) != 1 {
		t.Fatalf("auth failure must stop the chain: err=%v calls=%d", err, len(chat.calls))
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
