package main

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/Gentleman-Programming/engram/v3/internal/autocapture"
	"github.com/Gentleman-Programming/engram/v3/internal/autocapture/compress"
	"github.com/Gentleman-Programming/engram/v3/internal/store"
)

// cmdAutocapture: `engram autocapture drain [--dry-run]` | `engram autocapture status`.
func cmdAutocapture(cfg store.Config) {
	sub := ""
	if len(os.Args) > 2 {
		sub = os.Args[2]
	}
	switch sub {
	case "drain":
		dry := len(os.Args) > 3 && os.Args[3] == "--dry-run"
		s, err := storeNew(cfg)
		if err != nil {
			fatal(err)
		}
		defer s.Close()
		rep, ok, err := compress.RunOnce(context.Background(), s, cfg.DataDir, dry, func(line string) { fmt.Println(line) })
		if !ok {
			fmt.Println("another drain is running; try again later")
			return
		}
		fmt.Printf("sessions %d, calls %d, observations %d, summaries %d, deferred %d, failed %d, tokens in %d / out %d\n",
			rep.Sessions, rep.Calls, rep.Observations, rep.Summaries, rep.Deferred, rep.Failed, rep.Usage.In, rep.Usage.Out)
		if err != nil {
			fatal(err)
		}
	case "status":
		autocaptureStatus(cfg.DataDir)
	case "context":
		autocaptureContext(cfg)
	case "probe":
		results, err := compress.Probe(context.Background(), cfg.DataDir)
		if err != nil {
			fatal(err)
		}
		for _, r := range results {
			status := "FAIL"
			if r.OK {
				status = "ok"
			}
			fmt.Printf("%-4s %-28s %6.1fs  in %6d / out %5d  %s\n", status, r.Backend, r.Took.Seconds(), r.Usage.In, r.Usage.Out, r.Detail)
		}
	default:
		fmt.Fprintln(os.Stderr, "usage: engram autocapture drain [--dry-run] | status | context | probe")
		exitFunc(1)
	}
}

// autocaptureContext is the Claude Code SessionStart hook: hook JSON in,
// additionalContext out. Any failure prints nothing — a session must never
// fail to start because of memory.
func autocaptureContext(cfg store.Config) {
	var in struct {
		SessionID string `json:"session_id"`
		CWD       string `json:"cwd"`
		Source    string `json:"source"`
	}
	data, err := io.ReadAll(io.LimitReader(os.Stdin, 1<<20))
	if err != nil || json.Unmarshal(data, &in) != nil || in.SessionID == "" {
		return
	}
	s, err := storeNew(cfg)
	if err != nil {
		return
	}
	defer s.Close()
	block := compress.SessionContext(s, in.SessionID, in.CWD, in.Source, time.Now().UTC())
	if block == "" {
		return
	}
	_ = json.NewEncoder(os.Stdout).Encode(map[string]any{
		"hookSpecificOutput": map[string]string{"hookEventName": "SessionStart", "additionalContext": block},
	})
}

func autocaptureStatus(dataDir string) {
	fmt.Printf("enabled: %v (%s)\n", autocapture.Enabled(dataDir), filepath.Join(dataDir, autocapture.ConfigFile))
	entries, _ := os.ReadDir(autocapture.SpoolDir(dataDir))
	queued, claimed := 0, 0
	for _, e := range entries {
		switch {
		case e.IsDir() && strings.HasPrefix(e.Name(), ".claimed-"):
			claimed++
		case !e.IsDir() && strings.HasSuffix(e.Name(), ".json") && !strings.HasPrefix(e.Name(), "."):
			queued++
		}
	}
	fmt.Printf("spool: %d queued, %d claimed batches\n", queued, claimed)
	if data, err := os.ReadFile(filepath.Join(dataDir, "autocapture-state.json")); err == nil {
		fmt.Printf("models: %s\n", strings.TrimSpace(string(data)))
	}

	f, err := os.Open(filepath.Join(dataDir, "autocapture.log"))
	if err != nil {
		return
	}
	defer f.Close()
	type day struct{ calls, errs, in, out, records int }
	days := map[string]*day{}
	var order []string
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 1<<20), 1<<20)
	for sc.Scan() {
		var e struct {
			At      time.Time `json:"at"`
			In      int       `json:"in"`
			Out     int       `json:"out"`
			Records int       `json:"records"`
			Error   string    `json:"error"`
			DryRun  bool      `json:"dry_run"`
			Model   string    `json:"model"`
		}
		if json.Unmarshal(sc.Bytes(), &e) != nil || e.At.IsZero() || e.DryRun || (e.Model == "" && e.Error == "") {
			continue
		}
		k := e.At.Local().Format("2006-01-02")
		if days[k] == nil {
			days[k] = &day{}
			order = append(order, k)
		}
		d := days[k]
		d.calls++
		d.in += e.In
		d.out += e.Out
		d.records += e.Records
		if e.Error != "" {
			d.errs++
		}
	}
	for _, k := range order[max(0, len(order)-7):] {
		d := days[k]
		fmt.Printf("%s  calls %d (errors %d)  tokens in %d / out %d  records %d\n", k, d.calls, d.errs, d.in, d.out, d.records)
	}
}
