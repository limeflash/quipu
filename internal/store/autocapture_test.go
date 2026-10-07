package store

import "testing"

func TestRecentSessionObservationsPutsTheUpdatedSummaryFirst(t *testing.T) {
	s := newTestStore(t)
	if err := s.CreateSession("s1", "p", ""); err != nil {
		t.Fatal(err)
	}
	add := func(title, topic string) {
		t.Helper()
		if _, err := s.AddObservation(AddObservationParams{SessionID: "s1", Type: "decision", Title: title,
			Content: "content of " + title, Project: "p", Scope: "project", TopicKey: topic}); err != nil {
			t.Fatal(err)
		}
	}
	add("summary", "session/s1")
	for _, title := range []string{"old a", "old b", "new c"} {
		add(title, "")
	}
	// Months-old rows, as an import from claude-mem writes them.
	if _, err := s.DB().Exec(`UPDATE observations SET created_at = '2026-08-01 10:00:00', updated_at = '2026-08-01 10:00:00' WHERE title IN ('old a', 'old b')`); err != nil {
		t.Fatal(err)
	}
	add("summary", "session/s1") // the next turn upserts the summary
	if _, err := s.DB().Exec(`UPDATE observations SET updated_at = datetime('now', '+1 minute') WHERE topic_key = 'session/s1'`); err != nil {
		t.Fatal(err)
	}

	obs, err := s.RecentSessionObservations("s1", 2)
	if err != nil {
		t.Fatal(err)
	}
	if len(obs) != 2 || obs[0].Title != "summary" || obs[1].Title != "new c" {
		t.Fatalf("got %+v", obs)
	}
}
