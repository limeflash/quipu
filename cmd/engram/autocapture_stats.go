package main

import (
	"bufio"
	"database/sql"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/Gentleman-Programming/engram/v3/internal/autocapture"
	"github.com/Gentleman-Programming/engram/v3/internal/autocapture/compress"
	"github.com/Gentleman-Programming/engram/v3/internal/project"
	"github.com/Gentleman-Programming/engram/v3/internal/store"
)

// statsReport is what `engram autocapture stats --json` prints: everything a
// dashboard draws from — the quipu-memory mod for Claude Code among them.
type statsReport struct {
	GeneratedAt string        `json:"generated_at"`
	DBBytes     int64         `json:"db_bytes"`
	Totals      statsTotals   `json:"totals"`
	Project     *statsProject `json:"project,omitempty"`
	Projects    []statsRow    `json:"projects"`
	Days        []statsDay    `json:"days"`
	Models      []statsModel  `json:"models"`
	Spool       int           `json:"spool"`
}

type statsTotals struct {
	Records  int `json:"records"`
	Captured int `json:"captured"`
	Agent    int `json:"agent"`
	Imported int `json:"imported"`
	Projects int `json:"projects"`
}

type statsProject struct {
	Name      string         `json:"name"`
	Records   int            `json:"records"`
	Captured  int            `json:"captured"`
	ProfileID int64          `json:"profile_id,omitempty"`
	Profile   string         `json:"profile,omitempty"`
	Summary   *statsSummary  `json:"summary,omitempty"`
	Recent    []statsRecord  `json:"recent"`
	Types     map[string]int `json:"types"`
}

type statsSummary struct {
	ID        int64  `json:"id"`
	SessionID string `json:"session_id"`
	UpdatedAt string `json:"updated_at"`
	Digest    string `json:"digest"`
}

type statsRecord struct {
	ID        int64  `json:"id"`
	Type      string `json:"type"`
	Title     string `json:"title"`
	Source    string `json:"source"` // captured | agent | imported
	CreatedAt string `json:"created_at"`
	Preview   string `json:"preview"`
}

type statsRow struct {
	Name     string `json:"name"`
	Records  int    `json:"records"`
	Captured int    `json:"captured"`
	LastAt   string `json:"last_at"`
}

type statsDay struct {
	Date      string         `json:"date"`
	Records   int            `json:"records"`
	ByType    map[string]int `json:"by_type"`
	Calls     int            `json:"calls"`
	Errors    int            `json:"errors"`
	TokensIn  int            `json:"tokens_in"`
	TokensOut int            `json:"tokens_out"`
}

type statsModel struct {
	Name         string `json:"name"`
	State        string `json:"state"` // ready | backoff | disabled
	BlockedUntil string `json:"blocked_until,omitempty"`
	LastError    string `json:"last_error,omitempty"`
	CallsHour    int    `json:"calls_hour"`
	LastCall     string `json:"last_call,omitempty"`
}

const statsDays = 14

// autocaptureStats: `engram autocapture stats --json [--dir DIR | --project NAME]`.
// --dir resolves the project as the session-start block does (git, bindings:
// a second or two on Windows); --project skips that for a caller that already
// knows the name.
func autocaptureStats(cfg store.Config, args []string) {
	dir, name := "", ""
	for i := 0; i+1 < len(args); i++ {
		switch args[i] {
		case "--dir":
			dir, i = args[i+1], i+1
		case "--project":
			name, i = args[i+1], i+1
		}
	}
	s, err := storeNew(cfg)
	if err != nil {
		fatal(err)
	}
	defer s.Close()
	detect := func(d string) project.DetectionResult { return s.InspectProject(d) }
	if name != "" {
		dir, detect = name, func(string) project.DetectionResult {
			return project.DetectionResult{Project: name, Source: project.SourceExplicitOverride}
		}
	}
	rep, err := buildStats(s.DB(), cfg.DataDir, detect, dir, time.Now())
	if err != nil {
		fatal(err)
	}
	enc := json.NewEncoder(os.Stdout)
	enc.SetEscapeHTML(false)
	_ = enc.Encode(rep)
}

func buildStats(db *sql.DB, dataDir string, detect func(string) project.DetectionResult, dir string, now time.Time) (*statsReport, error) {
	rep := &statsReport{GeneratedAt: now.UTC().Format(time.RFC3339), Projects: []statsRow{}, Models: []statsModel{}}
	if st, err := os.Stat(filepath.Join(dataDir, "engram.db")); err == nil {
		rep.DBBytes = st.Size()
	}
	const live = `deleted_at IS NULL AND type NOT IN ('session_summary','project_profile')`

	t := &rep.Totals
	if err := db.QueryRow(`SELECT count(*), coalesce(sum(tool_name='autocapture'),0), coalesce(sum(tool_name='claude-mem'),0),
		count(DISTINCT project) FROM observations WHERE `+live).Scan(&t.Records, &t.Captured, &t.Imported, &t.Projects); err != nil {
		return nil, fmt.Errorf("totals: %w", err)
	}
	t.Agent = t.Records - t.Captured - t.Imported

	rows, err := db.Query(`SELECT project, count(*), coalesce(sum(tool_name='autocapture'),0), max(created_at) FROM observations
		WHERE ` + live + ` AND coalesce(project,'') != '' GROUP BY project ORDER BY max(datetime(created_at)) DESC LIMIT 10`)
	if err != nil {
		return nil, fmt.Errorf("projects: %w", err)
	}
	for rows.Next() {
		var r statsRow
		if err := rows.Scan(&r.Name, &r.Records, &r.Captured, &r.LastAt); err != nil {
			rows.Close()
			return nil, err
		}
		rep.Projects = append(rep.Projects, r)
	}
	rows.Close()

	// Days, local time, oldest first: what was written (imports excluded) and
	// what writing it cost, from the call log.
	days := map[string]*statsDay{}
	for i := statsDays - 1; i >= 0; i-- {
		d := &statsDay{Date: now.AddDate(0, 0, -i).Format("2006-01-02"), ByType: map[string]int{}}
		days[d.Date] = d
		rep.Days = append(rep.Days, *d)
	}
	rows, err = db.Query(`SELECT date(created_at,'localtime'), type, count(*) FROM observations
		WHERE `+live+` AND coalesce(tool_name,'') != 'claude-mem' AND datetime(created_at) >= datetime('now', ?)
		GROUP BY 1, 2`, fmt.Sprintf("-%d days", statsDays))
	if err != nil {
		return nil, fmt.Errorf("days: %w", err)
	}
	for rows.Next() {
		var date, typ string
		var n int
		if err := rows.Scan(&date, &typ, &n); err != nil {
			rows.Close()
			return nil, err
		}
		if d := days[date]; d != nil {
			d.ByType[typ] += n
			d.Records += n
		}
	}
	rows.Close()
	readCallLog(filepath.Join(dataDir, "autocapture.log"), func(at time.Time, in, out int, failed bool) {
		if d := days[at.Local().Format("2006-01-02")]; d != nil {
			d.Calls++
			d.TokensIn += in
			d.TokensOut += out
			if failed {
				d.Errors++
			}
		}
	})
	for i := range rep.Days {
		rep.Days[i] = *days[rep.Days[i].Date]
	}

	rep.Models = modelStates(dataDir, now)
	if entries, err := os.ReadDir(autocapture.SpoolDir(dataDir)); err == nil {
		for _, e := range entries {
			if !e.IsDir() && strings.HasSuffix(e.Name(), ".json") && !strings.HasPrefix(e.Name(), ".") {
				rep.Spool++
			}
		}
	}

	if strings.TrimSpace(dir) == "" {
		return rep, nil
	}
	res := detect(dir)
	if res.Project == "" || res.Error != nil || res.Source == project.SourceAmbiguous {
		return rep, nil
	}
	p := &statsProject{Name: res.Project, Recent: []statsRecord{}, Types: map[string]int{}}
	p.ProfileID, p.Profile = compress.ProfileLine(dataDir, p.Name)
	rows, err = db.Query(`SELECT type, count(*), coalesce(sum(tool_name='autocapture'),0) FROM observations WHERE `+live+` AND project = ? GROUP BY type`, p.Name)
	if err != nil {
		return nil, fmt.Errorf("project types: %w", err)
	}
	for rows.Next() {
		var typ string
		var n, captured int
		if err := rows.Scan(&typ, &n, &captured); err != nil {
			rows.Close()
			return nil, err
		}
		p.Types[typ] = n
		p.Records += n
		p.Captured += captured
	}
	rows.Close()
	rows, err = db.Query(`SELECT id, type, title, coalesce(tool_name,''), created_at, substr(content, 1, 600) FROM observations
		WHERE `+live+` AND project = ? ORDER BY datetime(created_at) DESC, id DESC LIMIT 12`, p.Name)
	if err != nil {
		return nil, fmt.Errorf("project records: %w", err)
	}
	for rows.Next() {
		var r statsRecord
		var tool string
		if err := rows.Scan(&r.ID, &r.Type, &r.Title, &tool, &r.CreatedAt, &r.Preview); err != nil {
			rows.Close()
			return nil, err
		}
		r.Source = sourceOf(tool)
		p.Recent = append(p.Recent, r)
	}
	rows.Close()
	var sum statsSummary
	var content string
	err = db.QueryRow(`SELECT id, session_id, updated_at, content FROM observations WHERE deleted_at IS NULL AND project = ?
		AND type = 'session_summary' ORDER BY datetime(updated_at) DESC, id DESC LIMIT 1`, p.Name).Scan(&sum.ID, &sum.SessionID, &sum.UpdatedAt, &content)
	if err == nil {
		sum.Digest = compress.SummaryDigest(content)
		p.Summary = &sum
	}
	rep.Project = p
	return rep, nil
}

func sourceOf(tool string) string {
	switch tool {
	case compress.ToolName:
		return "captured"
	case compress.ImportToolName:
		return "imported"
	}
	return "agent"
}

// readCallLog walks autocapture.log, one model call per line; dry runs and
// lines that are no call are skipped.
func readCallLog(path string, each func(at time.Time, in, out int, failed bool)) {
	f, err := os.Open(path)
	if err != nil {
		return
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 1<<20), 1<<20)
	for sc.Scan() {
		var e struct {
			At     time.Time `json:"at"`
			In     int       `json:"in"`
			Out    int       `json:"out"`
			Error  string    `json:"error"`
			DryRun bool      `json:"dry_run"`
			Model  string    `json:"model"`
		}
		if json.Unmarshal(sc.Bytes(), &e) != nil || e.At.IsZero() || e.DryRun || (e.Model == "" && e.Error == "") {
			continue
		}
		each(e.At, e.In, e.Out, e.Error != "")
	}
}

// modelStates lists the backend chain in the order it is tried, each with the
// back-off state the compressor keeps for it.
func modelStates(dataDir string, now time.Time) []statsModel {
	cfg, _ := compress.Load(dataDir)
	var names []string
	names = append(names, cfg.Ollama.Models...)
	if cfg.Codex.Enabled == nil || *cfg.Codex.Enabled {
		names = append(names, "codex:"+cfg.Codex.Model)
	}
	if cfg.Claude.Enabled == nil || *cfg.Claude.Enabled {
		names = append(names, "claude:"+cfg.Claude.Model)
	}
	var state struct {
		Models map[string]struct {
			BlockedUntil time.Time   `json:"blocked_until"`
			Disabled     bool        `json:"disabled"`
			LastError    string      `json:"last_error"`
			Calls        []time.Time `json:"calls"`
		} `json:"models"`
	}
	if data, err := os.ReadFile(filepath.Join(dataDir, "autocapture-state.json")); err == nil {
		_ = json.Unmarshal(data, &state)
	}
	out := make([]statsModel, 0, len(names))
	for _, n := range names {
		m := statsModel{Name: n, State: "ready"}
		if st, ok := state.Models[n]; ok {
			switch {
			case st.Disabled:
				m.State = "disabled"
			case now.Before(st.BlockedUntil):
				m.State, m.BlockedUntil = "backoff", st.BlockedUntil.UTC().Format(time.RFC3339)
			}
			if m.State != "ready" {
				m.LastError = st.LastError
			}
			for _, c := range st.Calls {
				if now.Sub(c) < time.Hour {
					m.CallsHour++
				}
				if m.LastCall == "" || c.UTC().Format(time.RFC3339) > m.LastCall {
					m.LastCall = c.UTC().Format(time.RFC3339)
				}
			}
		}
		out = append(out, m)
	}
	return out
}
