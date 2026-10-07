package main

import (
	"crypto/sha256"
	"database/sql"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/Gentleman-Programming/engram/v3/internal/autocapture/compress"
	"github.com/Gentleman-Programming/engram/v3/internal/project"
	"github.com/Gentleman-Programming/engram/v3/internal/redact"
	"github.com/Gentleman-Programming/engram/v3/internal/store"
)

// cmdImportClaudeMem: engram import claude-mem [--db PATH] [--root DIR]... [--map FROM=TO]... [--dry-run]
//
// Reads a claude-mem database (read-only) and writes its observations, session
// summaries and prompts through Store.Import with stable sync ids, so a rerun
// adds nothing twice. Everything is redacted again on the way in.
func cmdImportClaudeMem(cfg store.Config, args []string) {
	home, _ := os.UserHomeDir()
	fs := flag.NewFlagSet("import claude-mem", flag.ExitOnError)
	dbPath := fs.String("db", filepath.Join(home, ".claude-mem", "claude-mem.db"), "claude-mem database")
	dry := fs.Bool("dry-run", false, "print what would be imported, write nothing")
	var roots, maps multiFlag
	fs.Var(&roots, "root", "directory holding the repositories; a project named X resolves through ROOT/X (repeatable)")
	fs.Var(&maps, "map", "FROM=TO project rename, applied after worktree suffixes are dropped (repeatable)")
	_ = fs.Parse(args)

	src, err := sql.Open("sqlite", "file:"+filepath.ToSlash(*dbPath)+"?mode=ro&_pragma=query_only(1)")
	if err != nil {
		fatal(err)
	}
	defer src.Close()
	s, err := storeNew(cfg)
	if err != nil {
		fatal(err)
	}
	defer s.Close()

	overrides := map[string]string{}
	for _, m := range maps {
		from, to, ok := strings.Cut(m, "=")
		if !ok {
			fatal(fmt.Errorf("--map %q: want FROM=TO", m))
		}
		overrides[from] = to
	}
	im := &cmImporter{src: src, dst: s, roots: roots, overrides: overrides}
	data, rep, err := im.build()
	if err != nil {
		fatal(err)
	}
	rep.print(os.Stdout)
	if *dry {
		fmt.Println("dry run: nothing written")
		return
	}
	res, err := s.Import(data)
	if err != nil {
		fatal(err)
	}
	fmt.Printf("written: sessions %d, observations %d (already there %d), prompts %d\n",
		res.SessionsImported, res.ObservationsImported, res.ObservationsSkippedStale, res.PromptsImported)
}

type multiFlag []string

func (m *multiFlag) String() string     { return strings.Join(*m, ",") }
func (m *multiFlag) Set(v string) error { *m = append(*m, v); return nil }

// cmDest is the part of *store.Store the import reads.
type cmDest interface {
	InspectProject(directory string) project.DetectionResult
	SessionObservations(sessionID string, limit int) ([]store.Observation, error)
}

type cmImporter struct {
	src       *sql.DB
	dst       cmDest
	roots     []string
	overrides map[string]string

	projects map[string]string // claude-mem name → engram project
	dirs     map[string]string // engram project → directory, when found under a root
}

type cmReport struct {
	Projects                     map[string]string
	PerProject                   map[string]int
	Observations, Summaries      int
	Prompts, Sessions            int
	Duplicates, Empty, Overlap   int
	Retyped, Cleaned, Redactions int
}

func (r *cmReport) print(w io.Writer) {
	names := make([]string, 0, len(r.Projects))
	for k := range r.Projects {
		names = append(names, k)
	}
	sort.Strings(names)
	fmt.Fprintln(w, "projects (claude-mem → engram):")
	for _, k := range names {
		fmt.Fprintf(w, "  %-50s → %s\n", k, r.Projects[k])
	}
	proj := make([]string, 0, len(r.PerProject))
	for k := range r.PerProject {
		proj = append(proj, k)
	}
	sort.Slice(proj, func(i, j int) bool { return r.PerProject[proj[i]] > r.PerProject[proj[j]] })
	fmt.Fprintln(w, "records per engram project:")
	for _, k := range proj {
		fmt.Fprintf(w, "  %-30s %6d\n", k, r.PerProject[k])
	}
	fmt.Fprintf(w, "observations %d, session summaries %d, prompts %d, sessions %d\n", r.Observations, r.Summaries, r.Prompts, r.Sessions)
	fmt.Fprintf(w, "skipped: %d exact duplicates, %d empty, %d already captured by engram; fixed: %d types, %d field prefixes; secrets redacted: %d\n",
		r.Duplicates, r.Empty, r.Overlap, r.Retyped, r.Cleaned, r.Redactions)
}

type cmSession struct {
	id, project string
	started     int64
	completed   sql.NullInt64
}

func (im *cmImporter) build() (*store.ExportData, *cmReport, error) {
	rep := &cmReport{Projects: map[string]string{}, PerProject: map[string]int{}}
	im.projects, im.dirs = map[string]string{}, map[string]string{}
	data := &store.ExportData{ExportedAt: time.Now().UTC().Format(time.RFC3339)}

	// memory_session_id → the agent's own session id, which engram uses too.
	sessions := map[string]*cmSession{}
	byContent := map[string]*cmSession{}
	rows, err := im.src.Query(`SELECT content_session_id, ifnull(memory_session_id,''), project, started_at_epoch, completed_at_epoch FROM sdk_sessions`)
	if err != nil {
		return nil, nil, fmt.Errorf("read sessions: %w", err)
	}
	for rows.Next() {
		var mem string
		cs := &cmSession{}
		if err := rows.Scan(&cs.id, &mem, &cs.project, &cs.started, &cs.completed); err != nil {
			rows.Close()
			return nil, nil, err
		}
		cs.project = im.project(cs.project, rep)
		if mem != "" {
			sessions[mem] = cs
		}
		byContent[cs.id] = cs
	}
	rows.Close()

	// Whatever engram captured itself wins: claude-mem rows of a session are
	// taken only up to the first auto-captured record of that session.
	cutoff := map[string]int64{}
	for id := range byContent {
		obs, _ := im.dst.SessionObservations(id, 5000)
		for _, o := range obs {
			if o.ToolName != nil && *o.ToolName == compress.ToolName {
				if t, ok := parseStoreTime(o.CreatedAt); ok {
					cutoff[id] = t.UnixMilli()
				}
				break
			}
		}
	}
	captured := func(sessionID string, at int64) bool {
		c, ok := cutoff[sessionID]
		return ok && at >= c
	}

	seen := map[[32]byte]bool{}
	tool := compress.ImportToolName
	addObs := func(o store.Observation, at int64) {
		o.ToolName, o.Scope = &tool, "project"
		o.CreatedAt = storeTime(at)
		o.UpdatedAt = o.CreatedAt
		data.Observations = append(data.Observations, o)
		rep.PerProject[*o.Project]++
	}

	rows, err = im.src.Query(`SELECT id, memory_session_id, project, type, ifnull(title,''), ifnull(subtitle,''), ifnull(facts,''), ifnull(narrative,''),
		ifnull(text,''), ifnull(concepts,''), ifnull(files_read,''), ifnull(files_modified,''), created_at_epoch FROM observations ORDER BY id`)
	if err != nil {
		return nil, nil, fmt.Errorf("read observations: %w", err)
	}
	for rows.Next() {
		var id, at int64
		var mem, proj, typ, title, sub, facts, narr, text, concepts, read, mod string
		if err := rows.Scan(&id, &mem, &proj, &typ, &title, &sub, &facts, &narr, &text, &concepts, &read, &mod, &at); err != nil {
			rows.Close()
			return nil, nil, err
		}
		sess := sessions[mem]
		if sess == nil {
			sess = &cmSession{id: mem}
		}
		if captured(sess.id, at) {
			rep.Overlap++
			continue
		}
		o, ok := cmObservation(typ, title, sub, facts, narr, text, concepts, read, mod, rep)
		if !ok {
			rep.Empty++
			continue
		}
		p := im.project(proj, rep)
		key := sha256.Sum256([]byte(p + "\x00" + o.Type + "\x00" + o.Title + "\x00" + o.Content))
		if seen[key] {
			rep.Duplicates++
			continue
		}
		seen[key] = true
		o.SyncID, o.SessionID, o.Project = "cm-obs-"+strconv.FormatInt(id, 10), sess.id, &p
		addObs(o, at)
		rep.Observations++
	}
	rows.Close()

	rows, err = im.src.Query(`SELECT id, memory_session_id, project, ifnull(request,''), ifnull(investigated,''), ifnull(learned,''), ifnull(completed,''),
		ifnull(next_steps,''), ifnull(files_read,''), ifnull(files_edited,''), ifnull(notes,''), created_at_epoch FROM session_summaries ORDER BY id`)
	if err != nil {
		return nil, nil, fmt.Errorf("read summaries: %w", err)
	}
	for rows.Next() {
		var id, at int64
		var mem, proj, request, investigated, learned, completed, next, read, edited, notes string
		if err := rows.Scan(&id, &mem, &proj, &request, &investigated, &learned, &completed, &next, &read, &edited, &notes, &at); err != nil {
			rows.Close()
			return nil, nil, err
		}
		sess := sessions[mem]
		if sess == nil {
			sess = &cmSession{id: mem}
		}
		if captured(sess.id, at) {
			rep.Overlap++
			continue
		}
		content := cmSummary(request, investigated, learned, completed, next, read, edited, notes, rep)
		if content == "" {
			rep.Empty++
			continue
		}
		p := im.project(proj, rep)
		addObs(store.Observation{SyncID: "cm-sum-" + strconv.FormatInt(id, 10), SessionID: sess.id, Type: "session_summary",
			Title: "Session summary: " + clipRunes(oneLineText(cmClean(request, rep)), 100), Content: content, Project: &p}, at)
		rep.Summaries++
	}
	rows.Close()

	rows, err = im.src.Query(`SELECT id, content_session_id, prompt_text, created_at_epoch FROM user_prompts ORDER BY id`)
	if err != nil {
		return nil, nil, fmt.Errorf("read prompts: %w", err)
	}
	for rows.Next() {
		var id, at int64
		var sid, text string
		if err := rows.Scan(&id, &sid, &text, &at); err != nil {
			rows.Close()
			return nil, nil, err
		}
		if captured(sid, at) {
			rep.Overlap++
			continue
		}
		text = strings.TrimSpace(cmRedact(text, rep))
		if text == "" {
			continue
		}
		p := ""
		if cs := byContent[sid]; cs != nil {
			p = cs.project
		}
		data.Prompts = append(data.Prompts, store.Prompt{SyncID: "cm-prompt-" + strconv.FormatInt(id, 10), SessionID: sid,
			Content: clipRunes(text, 8000), Project: p, CreatedAt: storeTime(at)})
		rep.Prompts++
	}
	rows.Close()

	// Sessions last: only those something above refers to.
	used := map[string]bool{}
	for _, o := range data.Observations {
		used[o.SessionID] = true
	}
	for _, p := range data.Prompts {
		used[p.SessionID] = true
	}
	for id := range used {
		cs := byContent[id]
		if cs == nil { // a summary or record whose session row claude-mem no longer has
			cs = &cmSession{id: id}
		}
		sess := store.Session{ID: id, Project: cs.project, Directory: im.dirs[cs.project], StartedAt: storeTime(cs.started)}
		if cs.started == 0 {
			sess.StartedAt = storeTime(time.Now().UnixMilli())
		}
		if cs.completed.Valid {
			end := storeTime(cs.completed.Int64)
			sess.EndedAt = &end
		}
		data.Sessions = append(data.Sessions, sess)
	}
	sort.Slice(data.Sessions, func(i, j int) bool { return data.Sessions[i].ID < data.Sessions[j].ID })
	rep.Sessions = len(data.Sessions)
	return data, rep, nil
}

// project maps a claude-mem project name to engram's: worktree suffixes
// ("repo/adoring-khayyam-465209") and paths are reduced to the repository
// name, renames apply, and a repository found under one of the roots is
// named by engram's own detection (git remote, bindings), read-only.
func (im *cmImporter) project(name string, rep *cmReport) string {
	if p, ok := im.projects[name]; ok {
		return p
	}
	base := strings.TrimSpace(name)
	if strings.ContainsAny(base, `:\`) || strings.HasPrefix(base, "/") {
		base = filepath.Base(filepath.FromSlash(base))
	} else if i := strings.IndexByte(base, '/'); i > 0 {
		base = base[:i]
	}
	p := base
	if to, ok := im.overrides[base]; ok {
		p = to
	} else {
		for _, root := range im.roots {
			dir := filepath.Join(root, base)
			if st, err := os.Stat(dir); err != nil || !st.IsDir() {
				continue
			}
			if d := im.detectDir(dir); d != "" {
				p = d
				if im.dirs[d] == "" {
					im.dirs[d] = dir
				}
				break
			}
		}
	}
	p, _ = store.NormalizeProject(p)
	if p == "" {
		p = "unknown"
	}
	im.projects[name] = p
	rep.Projects[name] = p
	return p
}

func (im *cmImporter) detectDir(dir string) string {
	r := im.dst.InspectProject(dir) // never writes a binding into a repository
	if r.Error != nil || r.Source == project.SourceAmbiguous {
		return ""
	}
	return r.Project
}

// claude-mem's parser sometimes kept the XML field name: "**title**: …".
var cmFieldPrefix = regexp.MustCompile(`^\s*\*\*(?:title|subtitle|narrative|facts?|concepts?|type|text|request|investigated|learned|completed|next_steps|notes)\*\*:\s*`)

func cmClean(s string, rep *cmReport) string {
	if loc := cmFieldPrefix.FindStringIndex(s); loc != nil {
		s = s[loc[1]:]
		rep.Cleaned++
	}
	return strings.TrimSpace(s)
}

func cmRedact(s string, rep *cmReport) string {
	out, n := redact.Text(s)
	for _, c := range n {
		rep.Redactions += c
	}
	return out
}

var cmTypes = map[string]string{
	"discovery": "discovery", "change": "change", "feature": "feature", "bugfix": "bugfix", "refactor": "refactor",
	"decision": "decision", "security_note": "security_note", "security_alert": "security_note", "sensitive": "security_note",
	"test": "discovery", "gotcha": "learning", "pattern": "pattern", "bug": "bugfix", "discovery</code>": "discovery",
}

// cmObservation renders one claude-mem observation in engram's shape. It
// returns false when nothing usable is left.
func cmObservation(typ, title, sub, facts, narr, text, concepts, read, mod string, rep *cmReport) (store.Observation, bool) {
	t, ok := cmTypes[strings.TrimSpace(typ)]
	if !ok {
		t = "change"
	}
	if t != strings.TrimSpace(typ) {
		rep.Retyped++
	}
	title, sub, narr, text = cmClean(title, rep), cmClean(sub, rep), cmClean(narr, rep), cmClean(text, rep)
	modified, readFiles := jsonList(mod), jsonList(read)
	var fs []string
	for _, f := range jsonList(facts) {
		if f = cmClean(f, rep); f != "" {
			fs = append(fs, f)
		}
	}
	if title == "" {
		for _, alt := range append([]string{sub, narr, text}, fs...) {
			if alt != "" {
				title = clipRunes(oneLineText(alt), 100)
				break
			}
		}
	}
	if title == "" && len(modified)+len(readFiles) > 0 { // only the files survived
		var names []string
		for _, f := range append(modified, readFiles...) {
			names = append(names, filepath.Base(filepath.FromSlash(f)))
		}
		title = clipRunes("Files: "+strings.Join(names, ", "), 100)
	}
	if title == "" {
		return store.Observation{}, false
	}
	var b strings.Builder
	for _, part := range []string{sub, narr, text} {
		if part != "" {
			b.WriteString(part + "\n\n")
		}
	}
	for _, f := range fs {
		b.WriteString("- " + f + "\n")
	}
	if len(modified) > 0 {
		b.WriteString("\n**Files modified**: " + strings.Join(modified, ", ") + "\n")
	}
	if len(readFiles) > 0 {
		b.WriteString("**Files read**: " + strings.Join(readFiles, ", ") + "\n")
	}
	if c := jsonList(concepts); len(c) > 0 {
		b.WriteString("**Concepts**: " + strings.Join(c, ", ") + "\n")
	}
	content := strings.TrimSpace(cmRedact(b.String(), rep))
	if content == "" {
		content = title
	}
	// A title is plain text in every listing; "**Label**: rest" reads "Label: rest".
	return store.Observation{Type: t, Title: strings.ReplaceAll(cmRedact(title, rep), "**", ""), Content: content}, true
}

// cmSummary renders a claude-mem turn summary with the headings
// mem_session_summary and the auto-capture summaries use.
func cmSummary(request, investigated, learned, completed, next, read, edited, notes string, rep *cmReport) string {
	var b strings.Builder
	sec := func(h, v string) {
		if v = cmClean(v, rep); v != "" {
			fmt.Fprintf(&b, "## %s\n%s\n\n", h, v)
		}
	}
	sec("Goal", request)
	sec("Discoveries", learned)
	sec("Accomplished", completed)
	sec("Next Steps", next)
	sec("Investigated", investigated)
	var files []string
	for _, f := range append(jsonList(edited), jsonList(read)...) {
		files = append(files, "- "+f)
	}
	sec("Relevant Files", strings.Join(files, "\n"))
	sec("Notes", notes)
	if b.Len() == 0 {
		return ""
	}
	return strings.TrimSpace(cmRedact(b.String(), rep))
}

// jsonList reads claude-mem's list columns: a JSON array, or plain text.
func jsonList(s string) []string {
	s = strings.TrimSpace(s)
	if s == "" || s == "[]" || s == "null" {
		return nil
	}
	var out []string
	if strings.HasPrefix(s, "[") && json.Unmarshal([]byte(s), &out) == nil {
		var keep []string
		seen := map[string]bool{}
		for _, v := range out {
			if v = strings.TrimSpace(v); v != "" && !seen[v] {
				seen[v] = true
				keep = append(keep, v)
			}
		}
		return keep
	}
	return []string{s}
}

func storeTime(ms int64) string { return time.UnixMilli(ms).UTC().Format("2006-01-02 15:04:05") }

func parseStoreTime(s string) (time.Time, bool) {
	for _, layout := range []string{"2006-01-02 15:04:05", time.RFC3339Nano} {
		if t, err := time.Parse(layout, s); err == nil {
			return t, true
		}
	}
	return time.Time{}, false
}

func oneLineText(s string) string { return strings.Join(strings.Fields(s), " ") }

func clipRunes(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n-1]) + "…"
}
