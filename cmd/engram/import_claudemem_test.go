package main

import (
	"database/sql"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Gentleman-Programming/engram/v3/internal/autocapture/compress"
	"github.com/Gentleman-Programming/engram/v3/internal/store"
)

func TestImportClaudeMem(t *testing.T) {
	srcPath := filepath.Join(t.TempDir(), "claude-mem.db")
	src, err := sql.Open("sqlite", srcPath)
	if err != nil {
		t.Fatal(err)
	}
	defer src.Close()
	day := time.Date(2026, 9, 1, 10, 0, 0, 0, time.UTC).UnixMilli()
	min := int64(time.Minute / time.Millisecond)
	for _, q := range []string{
		`CREATE TABLE sdk_sessions (content_session_id TEXT, memory_session_id TEXT, project TEXT, started_at_epoch INTEGER, completed_at_epoch INTEGER)`,
		`CREATE TABLE observations (id INTEGER PRIMARY KEY, memory_session_id TEXT, project TEXT, text TEXT, type TEXT, title TEXT, subtitle TEXT,
			facts TEXT, narrative TEXT, concepts TEXT, files_read TEXT, files_modified TEXT, created_at_epoch INTEGER)`,
		`CREATE TABLE session_summaries (id INTEGER PRIMARY KEY, memory_session_id TEXT, project TEXT, request TEXT, investigated TEXT, learned TEXT,
			completed TEXT, next_steps TEXT, files_read TEXT, files_edited TEXT, notes TEXT, created_at_epoch INTEGER)`,
		`CREATE TABLE user_prompts (id INTEGER PRIMARY KEY, content_session_id TEXT, prompt_text TEXT, created_at_epoch INTEGER)`,
	} {
		if _, err := src.Exec(q); err != nil {
			t.Fatal(err)
		}
	}
	exec := func(q string, args ...any) {
		t.Helper()
		if _, err := src.Exec(q, args...); err != nil {
			t.Fatal(err)
		}
	}
	exec(`INSERT INTO sdk_sessions VALUES ('sess-a','mem-a','cs2farm-fresh/loving-feistel-77dc61',?,NULL), ('sess-b','mem-b','G:/projects/other',?,?)`, day, day, day+60*min)
	obs := `INSERT INTO observations (id, memory_session_id, project, type, title, subtitle, facts, narrative, concepts, files_read, files_modified, created_at_epoch) VALUES (?,?,?,?,?,?,?,?,?,?,?,?)`
	exec(obs, 1, "mem-a", "cs2farm-fresh", "decision", "**title**: Keep SQLite", "**subtitle**: WAL is enough", `["busy_timeout 5000","**facts**: one writer"]`,
		"**narrative**: Postgres was overkill; proxy postgres://app:Tr0ub4dor3xyz@db:5432 stays", `["how-it-works"]`, `[]`, `["C:\\r\\store.go"]`, day)
	exec(obs, 2, "mem-a", "cs2farm-fresh", "decision", "**title**: Keep SQLite", "**subtitle**: WAL is enough", `["busy_timeout 5000","**facts**: one writer"]`,
		"**narrative**: Postgres was overkill; proxy postgres://app:Tr0ub4dor3xyz@db:5432 stays", `["how-it-works"]`, `[]`, `["C:\\r\\store.go"]`, day+min) // exact repeat
	exec(obs, 3, "mem-a", "cs2farm-fresh/loving-feistel-77dc61", "<what>\n<title>garbage", "**Verification requested**: user asked", "", "", "", "", "", "", day+2*min)
	exec(obs, 4, "mem-a", "cs2farm-fresh", "bug", "", "", "[]", "", "", `["web/a.ts","web/b.ts"]`, "[]", day+3*min) // only files
	exec(obs, 5, "mem-a", "cs2farm-fresh", "discovery", "", "", "[]", "", "", "[]", "[]", day+4*min)                // nothing at all
	exec(obs, 6, "mem-b", "G:/projects/other", "change", "Late record", "", "", "after engram took over", "", "", "", day+30*min)
	exec(`INSERT INTO session_summaries VALUES (1,'mem-a','cs2farm-fresh','**request**: ship it','looked around','SQLite is fine','shipped v2','tag release','[]','["a.go"]','',?)`, day+5*min)
	exec(`INSERT INTO user_prompts VALUES (1,'sess-a','use key sk-ant-api03-AAAAAAAAAAAAAAAAAAAAAAAA please',?), (2,'sess-b','late prompt',?)`, day, day+40*min)

	cfg := testConfig(t)
	s, err := store.New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	// engram captured session b itself from minute 20 on.
	if err := s.CreateSession("sess-b", "other", ""); err != nil {
		t.Fatal(err)
	}
	if _, err := s.AddObservation(store.AddObservationParams{SessionID: "sess-b", Type: "bugfix", Title: "captured", Content: "x",
		ToolName: compress.ToolName, Project: "other", Scope: "project"}); err != nil {
		t.Fatal(err)
	}
	cut := time.UnixMilli(day + 20*min).UTC().Format("2006-01-02 15:04:05")
	if _, err := s.DB().Exec(`UPDATE observations SET created_at = ? WHERE session_id = 'sess-b'`, cut); err != nil {
		t.Fatal(err)
	}

	im := &cmImporter{src: src, dst: s, overrides: map[string]string{"cs2farm-fresh": "cs2farm"}}
	data, rep, err := im.build()
	if err != nil {
		t.Fatal(err)
	}
	if rep.Projects["cs2farm-fresh/loving-feistel-77dc61"] != "cs2farm" || rep.Projects["G:/projects/other"] != "other" {
		t.Fatalf("projects %v", rep.Projects)
	}
	if rep.Observations != 3 || rep.Duplicates != 1 || rep.Empty != 1 || rep.Overlap != 2 || rep.Summaries != 1 || rep.Prompts != 1 {
		t.Fatalf("report %+v", rep)
	}
	byID := map[string]store.Observation{}
	for _, o := range data.Observations {
		byID[o.SyncID] = o
	}
	d := byID["cm-obs-1"]
	if d.Title != "Keep SQLite" || d.Type != "decision" || *d.Project != "cs2farm" || d.SessionID != "sess-a" || *d.ToolName != compress.ImportToolName ||
		!strings.HasPrefix(d.Content, "WAL is enough\n\nPostgres was overkill") || !strings.Contains(d.Content, "- one writer") ||
		!strings.Contains(d.Content, "**Files modified**: C:\\r\\store.go") || !strings.Contains(d.Content, "**Concepts**: how-it-works") ||
		strings.Contains(d.Content, "Tr0ub4dor3xyz") || d.CreatedAt != "2026-09-01 10:00:00" {
		t.Fatalf("decision %+v\n%s", d, d.Content)
	}
	if g := byID["cm-obs-3"]; g.Type != "change" || g.Title != "Verification requested: user asked" {
		t.Fatalf("a bold label is content, a broken type becomes change: %+v", g)
	}
	if f := byID["cm-obs-4"]; f.Type != "bugfix" || f.Title != "Files: a.ts, b.ts" || !strings.Contains(f.Content, "web/a.ts") {
		t.Fatalf("a record with only files is kept: %+v", f)
	}
	sum := byID["cm-sum-1"]
	if sum.Type != "session_summary" || sum.Title != "Session summary: ship it" ||
		!strings.Contains(sum.Content, "## Goal\nship it") || !strings.Contains(sum.Content, "## Next Steps\ntag release") || !strings.Contains(sum.Content, "- a.go") {
		t.Fatalf("summary %+v\n%s", sum, sum.Content)
	}
	if p := data.Prompts[0]; strings.Contains(p.Content, "sk-ant-") || p.Project != "cs2farm" {
		t.Fatalf("prompt %+v", p)
	}

	res, err := s.Import(data)
	if err != nil || res.ObservationsImported != 4 || res.PromptsImported != 1 || res.SessionsImported != 1 {
		t.Fatalf("import %+v %v", res, err)
	}
	data, _, _ = im.build()
	if res, err = s.Import(data); err != nil || res.ObservationsImported != 0 || res.PromptsImported != 0 {
		t.Fatalf("a rerun must add nothing: %+v %v", res, err)
	}
}
