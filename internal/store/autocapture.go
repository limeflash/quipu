package store

// RecentSessionObservations returns a session's observations, most recently
// updated first. Auto-capture reads it before every model call to find the
// session summary — a topic upsert, so its updated_at moves every turn — and
// the latest records. SessionObservations is oldest first: in a long session,
// or one whose earlier history was imported from claude-mem, its first rows
// are months old and the summary never shows up among them.
func (s *Store) RecentSessionObservations(sessionID string, limit int) ([]Observation, error) {
	if limit <= 0 {
		limit = 200
	}
	return s.queryObservations(`
		SELECT `+observationSelectColumns+`
		FROM observations
		WHERE session_id = ? AND deleted_at IS NULL
		ORDER BY datetime(updated_at) DESC, id DESC
		LIMIT ?`, sessionID, limit)
}
