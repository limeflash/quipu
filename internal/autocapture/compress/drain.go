package compress

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/Gentleman-Programming/engram/v3/internal/autocapture"
	"github.com/Gentleman-Programming/engram/v3/internal/project"
	"github.com/Gentleman-Programming/engram/v3/internal/store"
)

// Sink is the part of *store.Store the compressor writes through.
type Sink interface {
	DetectProject(directory string) project.DetectionResult
	CreateSession(id, project, directory string) error
	AddObservation(p store.AddObservationParams) (int64, error)
	SessionObservations(sessionID string, limit int) ([]store.Observation, error)
}

// ToolName marks every observation this package writes, so search and
// context can rank agent-written memories first.
const ToolName = "autocapture"

const (
	claimPrefix = ".claimed-"
	badDir      = ".bad"
	staleClaim  = 30 * time.Minute
	logFile     = "autocapture.log"
	touchesFile = "touches.jsonl"
	// A turn with no state change gets a summary update only when the agent
	// said something substantial; a one-line answer is not worth a call.
	minFinalForSummary = 500
)

// Report is what one drain did.
type Report struct {
	Sessions     int
	Calls        int
	Observations int
	Summaries    int
	Deferred     int // sessions not ready yet
	Failed       int // sessions put back for a later retry
	Usage        usage
}

type drainer struct {
	sink    Sink
	gen     generator
	cfg     Config
	dataDir string
	spool   string
	owner   string
	now     time.Time
	dryRun  bool
	out     func(string) // dry-run printer
	projs   map[string]string
}

type spooled struct {
	path string
	ev   autocapture.Event
}

// Drain processes every session in the spool that is ready.
func Drain(ctx context.Context, sink Sink, gen generator, cfg Config, dataDir string, now time.Time, dryRun bool, out func(string)) (Report, error) {
	var rnd [4]byte
	_, _ = rand.Read(rnd[:])
	d := &drainer{sink: sink, gen: gen, cfg: cfg, dataDir: dataDir, spool: autocapture.SpoolDir(dataDir),
		owner: fmt.Sprintf("%d-%s", os.Getpid(), hex.EncodeToString(rnd[:])), now: now, dryRun: dryRun, out: out,
		projs: map[string]string{}}
	return d.run(ctx)
}

func (d *drainer) run(ctx context.Context) (Report, error) {
	var rep Report
	d.recoverStaleClaims()
	sessions, err := d.readSpool()
	if err != nil {
		return rep, err
	}
	ids := make([]string, 0, len(sessions))
	for id := range sessions {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	for _, id := range ids {
		if ctx.Err() != nil {
			break
		}
		files := sessions[id]
		if !d.ready(files) {
			rep.Deferred++
			continue
		}
		claimed, dir := d.claim(id, files)
		if len(claimed) == 0 {
			continue // another process took them
		}
		rep.Sessions++
		err := d.session(ctx, id, claimed, &rep)
		if d.dryRun || err != nil {
			d.release(claimed, dir)
		} else {
			_ = os.RemoveAll(dir)
		}
		if err != nil {
			rep.Failed++
			if errors.Is(err, errNoModel) {
				return rep, err // nothing will work this round; keep the rest queued
			}
		}
	}
	return rep, nil
}

func (d *drainer) readSpool() (map[string][]spooled, error) {
	entries, err := os.ReadDir(d.spool)
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	out := map[string][]spooled{}
	for _, e := range entries {
		name := e.Name()
		if e.IsDir() || strings.HasPrefix(name, ".") || !strings.HasSuffix(name, ".json") {
			continue
		}
		path := filepath.Join(d.spool, name)
		data, err := os.ReadFile(path)
		var ev autocapture.Event
		if err != nil || json.Unmarshal(data, &ev) != nil || ev.SessionID == "" {
			_ = os.MkdirAll(filepath.Join(d.spool, badDir), 0o700)
			_ = os.Rename(path, filepath.Join(d.spool, badDir, name))
			continue
		}
		out[ev.SessionID] = append(out[ev.SessionID], spooled{path: path, ev: ev})
	}
	for id := range out {
		sort.Slice(out[id], func(i, j int) bool { return out[id][i].ev.At.Before(out[id][j].ev.At) })
	}
	return out, nil
}

// ready: the turn ended, a long turn piled up enough events, or the session
// went quiet.
func (d *drainer) ready(files []spooled) bool {
	events := 0
	for _, f := range files {
		switch f.ev.Kind {
		case autocapture.KindTurnEnd:
			return true
		case autocapture.KindEvent:
			events++
		}
	}
	return events >= d.cfg.MaxEvents || d.now.Sub(files[0].ev.At) >= d.cfg.idle()
}

// claim moves files into a private directory. A rename succeeds for exactly
// one process, so two drains can never process the same event.
func (d *drainer) claim(sessionID string, files []spooled) ([]spooled, string) {
	dir := filepath.Join(d.spool, claimPrefix+d.owner+"-"+safeName(sessionID))
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, ""
	}
	var got []spooled
	for _, f := range files {
		dst := filepath.Join(dir, filepath.Base(f.path))
		if os.Rename(f.path, dst) == nil {
			got = append(got, spooled{path: dst, ev: f.ev})
		}
	}
	if len(got) == 0 {
		_ = os.Remove(dir)
	}
	return got, dir
}

func (d *drainer) release(files []spooled, dir string) {
	for _, f := range files {
		_ = os.Rename(f.path, filepath.Join(d.spool, filepath.Base(f.path)))
	}
	_ = os.Remove(dir)
}

// recoverStaleClaims returns events a crashed process had claimed.
func (d *drainer) recoverStaleClaims() {
	entries, _ := os.ReadDir(d.spool)
	for _, e := range entries {
		if !e.IsDir() || !strings.HasPrefix(e.Name(), claimPrefix) {
			continue
		}
		info, err := e.Info()
		if err != nil || d.now.Sub(info.ModTime()) < staleClaim {
			continue
		}
		dir := filepath.Join(d.spool, e.Name())
		files, _ := os.ReadDir(dir)
		for _, f := range files {
			_ = os.Rename(filepath.Join(dir, f.Name()), filepath.Join(d.spool, f.Name()))
		}
		_ = os.Remove(dir)
	}
}

// project resolves the project a directory belongs to. strict accepts only a
// repository-backed answer and returns "" otherwise: it is used for file
// paths, where a scratch or temp directory must not become a project. The
// session's working directory falls back to its own name.
func (d *drainer) project(dir string, strict bool) string {
	key := dir
	if strict {
		key = "\x00" + dir
	}
	if p, ok := d.projs[key]; ok {
		return p
	}
	p := ""
	if dir != "" {
		res := d.sink.DetectProject(dir)
		switch res.Source {
		case project.SourceConfig, project.SourceGitRemote, project.SourceGitRoot, project.SourceGitChild, project.SourceProcessOverride:
			p = res.Project
		default:
			if !strict {
				p = res.Project
			}
		}
	}
	if p == "" && !strict {
		p = strings.ToLower(filepath.Base(strings.ReplaceAll(dir, `\`, "/")))
		if p == "" || p == "." || p == "/" {
			p = "unknown"
		}
	}
	d.projs[key] = p
	return p
}

func (d *drainer) session(ctx context.Context, sessionID string, files []spooled, rep *Report) error {
	cwd := ""
	var prompts, touched []string
	var events []spooled
	var final string
	turnEnded := false
	seen := map[string]bool{}
	for _, f := range files {
		ev := f.ev
		if cwd == "" && ev.CWD != "" {
			cwd = ev.CWD
		}
		switch ev.Kind {
		case autocapture.KindPrompt:
			prompts = append(prompts, ev.Input)
		case autocapture.KindTouch:
			if !seen[ev.Target] && len(touched) < 50 {
				seen[ev.Target] = true
				touched = append(touched, ev.Target)
			}
		case autocapture.KindTurnEnd:
			turnEnded, final = true, ev.Output
		default:
			events = append(events, f)
		}
	}
	cwdProj := d.project(cwd, false)
	// One session often edits several repositories; an edit belongs to the repo
	// its file lives in, not to wherever the session was started.
	parts := map[string][]spooled{}
	var order []string
	for _, f := range events {
		p := cwdProj
		if dir := eventDir(f.ev); dir != "" {
			if q := d.project(dir, true); q != "" {
				p = q
			}
		}
		if parts[p] == nil {
			order = append(order, p)
		}
		parts[p] = append(parts[p], f)
	}
	wantSummary := turnEnded && (len(events) > 0 || len(final) >= minFinalForSummary)
	if len(events) == 0 && !wantSummary {
		d.appendTouches(sessionID, cwdProj, files)
		return nil
	}
	if len(order) == 0 {
		order = []string{cwdProj}
	}
	// The summary goes with the project that saw the most work, processed last.
	summaryProj := order[0]
	for _, p := range order {
		if len(parts[p]) > len(parts[summaryProj]) {
			summaryProj = p
		}
	}
	sort.SliceStable(order, func(i, j int) bool { return order[j] == summaryProj && order[i] != summaryProj })

	recorded, prevSummary, titles := d.existing(sessionID)
	for _, proj := range order {
		if !d.dryRun {
			if err := d.sink.CreateSession(sessionID, proj, cwd); err != nil {
				return fmt.Errorf("create session: %w", err)
			}
		}
		chunks := chunkEvents(parts[proj], d.cfg.MaxBatchChars)
		if len(chunks) == 0 {
			chunks = [][]spooled{nil}
		}
		for i, chunk := range chunks {
			withSummary := wantSummary && proj == summaryProj && i == len(chunks)-1
			if len(chunk) == 0 && !withSummary {
				continue
			}
			bt := batch{Project: proj, Prompts: prompts, Recorded: recorded, Touched: touched, WithSummary: withSummary}
			for _, f := range chunk {
				bt.Events = append(bt.Events, f.ev)
			}
			if withSummary {
				bt.PrevSummary, bt.FinalMsg = prevSummary, final
			}
			started := time.Now()
			res, info, err := d.gen.generate(ctx, system(d.cfg.Language, withSummary), bt.user(), withSummary)
			rep.Calls++
			rep.Usage.In += info.Usage.In
			rep.Usage.Out += info.Usage.Out
			d.log(sessionID, proj, info, len(res.Observations), time.Since(started), err)
			if err != nil {
				return err
			}
			for _, r := range res.Observations {
				// The model is told what is recorded but still restates it
				// now and then; an identical title is never a new memory.
				if titles[strings.ToLower(r.Title)] {
					continue
				}
				titles[strings.ToLower(r.Title)] = true
				if err := d.write(store.AddObservationParams{SessionID: sessionID, Type: r.Type, Title: r.Title,
					Content: r.content(), ToolName: ToolName, Project: proj, Scope: "project", TopicKey: r.TopicKey}); err != nil {
					return err
				}
				rep.Observations++
				recorded = append(recorded, recordedLine(r.Type, r.Title, r.TopicKey))
			}
			if res.Summary != nil {
				if err := d.write(store.AddObservationParams{SessionID: sessionID, Type: "session_summary",
					Title: "Session summary: " + proj, Content: res.Summary.content(d.now), ToolName: ToolName,
					Project: proj, Scope: "project", TopicKey: "session/" + safeName(sessionID)}); err != nil {
					return err
				}
				rep.Summaries++
			}
			// A finished chunk is never redone: a retry after a later failure
			// must not write its observations twice.
			if !d.dryRun {
				for _, f := range chunk {
					_ = os.Remove(f.path)
				}
			}
		}
	}
	d.appendTouches(sessionID, cwdProj, files)
	return nil
}

var leadingCD = regexp.MustCompile(`(?i)^\s*(?:cd|set-location|sl|pushd)\s+(?:-path\s+)?["']?([^"';&|\r\n]+?)["']?\s*(?:;|&&|\r?\n|$)`)

// eventDir is where an event happened: the directory of an absolute file
// target, or the directory a shell command starts by changing into.
func eventDir(ev autocapture.Event) string {
	if t := ev.Target; t != "" && filepath.IsAbs(t) {
		return filepath.Dir(t)
	}
	if m := leadingCD.FindStringSubmatch(ev.Input); m != nil && filepath.IsAbs(strings.TrimSpace(m[1])) {
		return strings.TrimSpace(m[1])
	}
	return ""
}

func recordedLine(typ, title, key string) string {
	if key != "" {
		return "[" + typ + "] " + title + " (topic_key " + key + ")"
	}
	return "[" + typ + "] " + title
}

func (d *drainer) write(p store.AddObservationParams) error {
	if d.dryRun {
		if d.out != nil {
			d.out(fmt.Sprintf("[%s] %s  (topic %q)\n%s\n", p.Type, p.Title, p.TopicKey, p.Content))
		}
		return nil
	}
	_, err := d.sink.AddObservation(p)
	return err
}

// existing lists what the session already holds — the agent's own mem_save
// records included — so the model does not restate it.
func (d *drainer) existing(sessionID string) (recorded []string, prevSummary string, titles map[string]bool) {
	titles = map[string]bool{}
	obs, err := d.sink.SessionObservations(sessionID, 200) // oldest first
	if err != nil {
		return nil, "", titles
	}
	defer func() {
		if len(recorded) > 40 {
			recorded = recorded[len(recorded)-40:]
		}
	}()
	key := "session/" + safeName(sessionID)
	for _, o := range obs {
		if o.TopicKey != nil && *o.TopicKey == key {
			prevSummary = o.Content
			continue
		}
		if o.Type == "session_summary" {
			continue
		}
		titles[strings.ToLower(o.Title)] = true
		k := ""
		if o.TopicKey != nil {
			k = *o.TopicKey
		}
		recorded = append(recorded, recordedLine(o.Type, o.Title, k))
	}
	return recorded, prevSummary, titles
}

func chunkEvents(events []spooled, maxChars int) [][]spooled {
	var chunks [][]spooled
	var cur []spooled
	size := 0
	for _, f := range events {
		n := len(renderEvent(f.ev))
		if len(cur) > 0 && size+n > maxChars {
			chunks = append(chunks, cur)
			cur, size = nil, 0
		}
		cur = append(cur, f)
		size += n
	}
	if len(cur) > 0 {
		chunks = append(chunks, cur)
	}
	return chunks
}

// appendTouches keeps reads for the project profile (milestone 5). Called
// only once a session succeeded, so a retry does not write them twice.
func (d *drainer) appendTouches(sessionID, proj string, files []spooled) {
	if d.dryRun {
		return
	}
	f, err := os.OpenFile(filepath.Join(d.dataDir, touchesFile), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		return
	}
	defer f.Close()
	enc := json.NewEncoder(f)
	for _, s := range files {
		if s.ev.Kind == autocapture.KindTouch {
			_ = enc.Encode(map[string]any{"at": s.ev.At, "session": sessionID, "project": proj, "tool": s.ev.Tool, "target": s.ev.Target})
		}
	}
}

func (d *drainer) log(sessionID, proj string, info callInfo, records int, took time.Duration, err error) {
	entry := map[string]any{"at": time.Now().UTC(), "session": sessionID, "project": proj, "model": info.Model,
		"in": info.Usage.In, "out": info.Usage.Out, "records": records, "dropped": info.Dropped,
		"attempts": info.Attempts, "ms": took.Milliseconds(), "dry_run": d.dryRun}
	if err != nil {
		entry["error"] = err.Error()
	}
	f, ferr := os.OpenFile(filepath.Join(d.dataDir, logFile), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if ferr != nil {
		return
	}
	defer f.Close()
	_ = json.NewEncoder(f).Encode(entry)
}

func safeName(s string) string {
	return strings.Map(func(r rune) rune {
		if r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '-' || r == '_' {
			return r
		}
		return '_'
	}, s)
}
