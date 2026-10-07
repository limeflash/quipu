package store

// AllThenAny is what an unset match mode means where agents and people search
// (mem_search, `engram search`): every query term must match, and when
// nothing does, any term may, ranked by BM25. An agent searches with a handful
// of keywords and one of them is usually not in the record it wants ("folder"
// for a record that says "directory"); strict AND alone then returns nothing
// for a fact that is stored. An explicit mode is passed through unchanged.
// Store.Search itself keeps upstream's strict default — callers use it to
// prove absence.
func AllThenAny[T any](opts SearchOptions, search func(SearchOptions) ([]T, error)) ([]T, error) {
	if opts.MatchMode != "" {
		return search(opts)
	}
	opts.MatchMode = "all"
	res, err := search(opts)
	if err != nil || len(res) > 0 {
		return res, err
	}
	opts.MatchMode = "any"
	return search(opts)
}

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
