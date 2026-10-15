package main

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/Gentleman-Programming/engram/v3/internal/autocapture/compress"
	"github.com/Gentleman-Programming/engram/v3/internal/project"
	"github.com/Gentleman-Programming/engram/v3/internal/store"
)

func TestBuildStats(t *testing.T) {
	cfg := testConfig(t)
	s, err := store.New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if err := s.CreateSession("s1", "proj", ""); err != nil {
		t.Fatal(err)
	}
	add := func(typ, title, tool, project string) {
		t.Helper()
		if _, err := s.AddObservation(store.AddObservationParams{SessionID: "s1", Type: typ, Title: title, Content: "**What**: " + title,
			ToolName: tool, Project: project, Scope: "project"}); err != nil {
			t.Fatal(err)
		}
	}
	add("bugfix", "Fixed the race", compress.ToolName, "proj")
	add("decision", "Chose SQLite", compress.ToolName, "proj")
	add("discovery", "Imported long ago", compress.ImportToolName, "proj")
	add("feature", "Elsewhere", compress.ToolName, "other")
	add("session_summary", "Session summary: proj", compress.ToolName, "proj")

	now := time.Now()
	log := `{"at":"` + now.UTC().Format(time.RFC3339) + `","model":"deepseek-v4.1-flash","in":6000,"out":900,"records":2}` + "\n" +
		`{"at":"` + now.UTC().Format(time.RFC3339) + `","error":"quota"}` + "\n" +
		`{"at":"` + now.UTC().Format(time.RFC3339) + `","model":"x","dry_run":true,"in":99}` + "\n"
	if err := os.WriteFile(filepath.Join(cfg.DataDir, "autocapture.log"), []byte(log), 0o600); err != nil {
		t.Fatal(err)
	}
	state := `{"models":{"glm-5.3-flash":{"blocked_until":"` + now.Add(time.Hour).UTC().Format(time.RFC3339) + `","last_error":"HTTP 500"},
		"codex:gpt-6-luna":{"calls":["` + now.Add(-10*time.Minute).UTC().Format(time.RFC3339) + `"]}}}`
	if err := os.WriteFile(filepath.Join(cfg.DataDir, "autocapture-state.json"), []byte(state), 0o600); err != nil {
		t.Fatal(err)
	}

	detect := func(string) project.DetectionResult {
		return project.DetectionResult{Project: "proj", Source: project.SourceGitRemote}
	}
	rep, err := buildStats(s.DB(), cfg.DataDir, detect, "any-dir", now)
	if err != nil {
		t.Fatal(err)
	}
	if rep.Totals.Records != 4 || rep.Totals.Captured != 3 || rep.Totals.Imported != 1 || rep.Totals.Projects != 2 {
		t.Fatalf("totals %+v (summaries are not records)", rep.Totals)
	}
	today := rep.Days[len(rep.Days)-1]
	if len(rep.Days) != statsDays || today.Records != 3 || today.ByType["bugfix"] != 1 || today.ByType["discovery"] != 0 ||
		today.Calls != 2 || today.Errors != 1 || today.TokensIn != 6000 {
		t.Fatalf("today %+v (imports and dry runs excluded)", today)
	}
	if len(rep.Models) != 4 || rep.Models[1].Name != "glm-5.3-flash" || rep.Models[1].State != "backoff" || rep.Models[1].LastError != "HTTP 500" ||
		rep.Models[2].Name != "codex:gpt-6-luna" || rep.Models[2].CallsHour != 1 || rep.Models[0].State != "ready" {
		t.Fatalf("models %+v", rep.Models)
	}
	p := rep.Project
	if p == nil || p.Name != "proj" || p.Records != 3 || p.Captured != 2 || len(p.Recent) != 3 || p.Summary == nil {
		t.Fatalf("project %+v", p)
	}
	sources := map[string]string{}
	for _, r := range p.Recent {
		sources[r.Title] = r.Source
	}
	if sources["Imported long ago"] != "imported" || sources["Chose SQLite"] != "captured" {
		t.Fatalf("sources %v", sources)
	}
}
