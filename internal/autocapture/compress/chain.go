package compress

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"
)

// errNoModel means nothing could serve the call right now; the caller keeps
// the work queued and tries again later. Nothing is lost.
var errNoModel = errors.New("no model available")

type chatter interface {
	chat(ctx context.Context, model, system, user string, schema json.RawMessage) (string, usage, error)
}

type generator interface {
	generate(ctx context.Context, system, user string, wantSummary bool) (result, callInfo, error)
}

type callInfo struct {
	Model    string
	Usage    usage
	Dropped  int
	Attempts int
}

type modelState struct {
	BlockedUntil time.Time   `json:"blocked_until,omitempty"`
	Strikes      int         `json:"strikes,omitempty"`
	Disabled     bool        `json:"disabled,omitempty"`
	LastError    string      `json:"last_error,omitempty"`
	Calls        []time.Time `json:"calls,omitempty"` // last hour, for budgeted backends
}

// stateFile persists per-backend health across processes and restarts.
// ponytail: last writer wins between concurrent drains; a lost backoff only
// costs one extra refused call.
const stateFile = "autocapture-state.json"

type state struct {
	Models map[string]*modelState `json:"models"`
}

func loadState(dataDir string) *state {
	st := &state{Models: map[string]*modelState{}}
	if data, err := os.ReadFile(filepath.Join(dataDir, stateFile)); err == nil {
		_ = json.Unmarshal(data, st)
	}
	if st.Models == nil {
		st.Models = map[string]*modelState{}
	}
	return st
}

func (st *state) save(dataDir string) {
	data, _ := json.MarshalIndent(st, "", "  ")
	tmp := filepath.Join(dataDir, "."+stateFile+".tmp")
	if os.WriteFile(tmp, data, 0o600) == nil {
		_ = os.Rename(tmp, filepath.Join(dataDir, stateFile))
	}
}

func (st *state) get(name string) *modelState {
	if st.Models[name] == nil {
		st.Models[name] = &modelState{}
	}
	return st.Models[name]
}

// backend is one model behind one provider.
type backend struct {
	name     string // state key and log label
	provider string // a bad credential disables every backend of its provider
	model    string
	client   chatter
	perHour  int // call budget; 0 = unlimited
}

// chain tries the backends in order — Ollama models first, Claude last —
// honouring their health and budgets.
type chain struct {
	backends []backend
	st       *state
	dataDir  string
	now      func() time.Time
}

func (c *chain) generate(ctx context.Context, sys, user string, wantSummary bool) (result, callInfo, error) {
	now := c.now()
	defer c.st.save(c.dataDir)
	var lastErr error
	badProvider := map[string]bool{}
	for _, b := range c.backends {
		ms := c.st.get(b.name)
		if badProvider[b.provider] || ms.Disabled || now.Before(ms.BlockedUntil) || !c.withinBudget(b, ms, now) {
			continue
		}
		prompt, total := user, usage{}
		for attempt := 1; attempt <= 2; attempt++ {
			if b.perHour > 0 {
				ms.Calls = append(ms.Calls, now)
			}
			text, u, err := b.client.chat(ctx, b.model, sys, prompt, schema)
			total.In, total.Out = total.In+u.In, total.Out+u.Out
			if err == nil {
				var r result
				var dropped int
				r, dropped, err = parse(text, wantSummary)
				if err == nil {
					ms.Strikes, ms.LastError = 0, ""
					return r, callInfo{Model: b.name, Usage: total, Dropped: dropped, Attempts: attempt}, nil
				}
				err = &callError{Kind: errOutput, Status: 200, Msg: err.Error()}
			}
			lastErr = fmt.Errorf("%s: %w", b.name, err)
			var ce *callError
			if !errors.As(err, &ce) || ce.Kind != errOutput {
				penalize(ms, ce, now)
				if ce != nil && ce.Kind == errAuth {
					badProvider[b.provider] = true
				}
				break
			}
			if attempt == 2 || !c.withinBudget(b, ms, now) {
				break
			}
			// One repair round: show the model why its reply was refused.
			prompt = user + "\n\n## Your previous reply was rejected\n" + ce.Msg +
				"\nReply again with one JSON object of the required shape and nothing else."
		}
	}
	if lastErr == nil {
		lastErr = errors.New("every backend is blocked, retired, over budget or not configured")
	}
	return result{}, callInfo{}, fmt.Errorf("%w: %v", errNoModel, lastErr)
}

// withinBudget keeps the last hour of calls and blocks the backend until the
// oldest one ages out once the hourly budget is spent.
func (c *chain) withinBudget(b backend, ms *modelState, now time.Time) bool {
	if b.perHour <= 0 {
		return true
	}
	kept := ms.Calls[:0]
	for _, t := range ms.Calls {
		if now.Sub(t) < time.Hour {
			kept = append(kept, t)
		}
	}
	ms.Calls = kept
	if len(kept) >= b.perHour {
		ms.BlockedUntil = kept[0].Add(time.Hour)
		return false
	}
	return true
}

func penalize(ms *modelState, ce *callError, now time.Time) {
	if ce == nil {
		ms.BlockedUntil = now.Add(2 * time.Minute)
		return
	}
	ms.LastError = ce.Error()
	switch ce.Kind {
	case errQuota:
		ms.Strikes++
		backoff := 15 * time.Minute << min(ms.Strikes-1, 3) // 15m, 30m, 1h, 2h
		ms.BlockedUntil = now.Add(backoff)
	case errRetired:
		ms.Disabled = true
	case errAuth:
		ms.BlockedUntil = now.Add(time.Hour)
	default:
		ms.BlockedUntil = now.Add(2 * time.Minute)
	}
}
