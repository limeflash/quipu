package compress

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/Gentleman-Programming/engram/v3/internal/autocapture"
	"github.com/Gentleman-Programming/engram/v3/internal/store"
)

// The other drain tests use fakeSink; this one runs two chunks of one session
// against the real store, so upstream changes to sessions, ownership or
// updates show up here first.
func TestDrainAgainstRealStore(t *testing.T) {
	s, err := store.New(store.FallbackConfig(t.TempDir()))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()

	repo := t.TempDir()
	if err := os.MkdirAll(filepath.Join(repo, ".engram"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(repo, ".engram", "config.json"), []byte(`{"project_name":"proj"}`), 0o644); err != nil {
		t.Fatal(err)
	}
	spool := t.TempDir()
	chunk := func(at time.Time, input string) {
		spoolEvents(t, spool,
			autocapture.Event{V: 1, Agent: "claude", Kind: autocapture.KindEvent, SessionID: "s1", CWD: repo, Tool: "Edit", Target: "a.go", Input: input, At: at},
			autocapture.Event{V: 1, Kind: autocapture.KindTurnEnd, SessionID: "s1", CWD: repo, Output: "Done.", At: at.Add(time.Second)},
		)
	}

	chunk(t0, "add retry to the uploader")
	first := &fakeGen{reply: func(int, string, bool) (result, error) {
		return result{Observations: []record{{Type: "bugfix", Title: "Uploader gives up after one failure", What: "no retry"}},
			Summary: &summary{Goal: "fix uploads", Accomplished: []string{"found the bug"}}}, nil
	}}
	if _, err := Drain(context.Background(), s, first, testConfig(), spool, t0.Add(time.Minute), false, nil); err != nil {
		t.Fatal(err)
	}
	// What the agent saved itself in the same session must never be rewritten.
	agentID, err := s.AddObservation(store.AddObservationParams{SessionID: "s1", Type: "decision", Title: "Agent's own note", Content: "keep me", Project: "proj", Scope: "project"})
	if err != nil {
		t.Fatal(err)
	}
	recs, err := s.RecentSessionObservations("s1", 50)
	if err != nil {
		t.Fatal(err)
	}
	var bugID int64
	for _, o := range recs {
		if o.Title == "Uploader gives up after one failure" {
			bugID = o.ID
			if o.Project == nil || *o.Project != "proj" || o.ToolName == nil || *o.ToolName != ToolName {
				t.Fatalf("captured record lost its project or marker: %+v", o)
			}
		}
	}
	if bugID == 0 {
		t.Fatalf("first chunk not written: %d records", len(recs))
	}

	chunk(t0.Add(time.Hour), "retry with backoff")
	second := &fakeGen{reply: func(int, string, bool) (result, error) {
		return result{Observations: []record{
			{Type: "bugfix", Title: "Uploader retries with backoff", What: "three tries, doubling delay", Updates: bugID},
			{Type: "decision", Title: "Overwrite attempt", What: "must not land on the agent's note", Updates: agentID},
		}, Summary: &summary{Goal: "fix uploads", Accomplished: []string{"retry with backoff"}}}, nil
	}}
	rep, err := Drain(context.Background(), s, second, testConfig(), spool, t0.Add(time.Hour+time.Minute), false, nil)
	if err != nil {
		t.Fatal(err)
	}
	if rep.Updated != 1 {
		t.Fatalf("report %+v", rep)
	}
	if o, err := s.GetObservation(bugID); err != nil || o.Title != "Uploader retries with backoff" {
		t.Fatalf("continuation did not rewrite #%d: %v %+v", bugID, err, o)
	}
	if o, err := s.GetObservation(agentID); err != nil || o.Title != "Agent's own note" {
		t.Fatalf("agent record changed: %v %+v", err, o)
	}
	recs, _ = s.RecentSessionObservations("s1", 50)
	summaries := 0
	for _, o := range recs {
		if o.Type == "session_summary" {
			summaries++
		}
	}
	if summaries != 1 {
		t.Fatalf("want one evolving session summary, got %d", summaries)
	}
}
