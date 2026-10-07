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
	BlockedUntil time.Time `json:"blocked_until,omitempty"`
	Strikes      int       `json:"strikes,omitempty"`
	Disabled     bool      `json:"disabled,omitempty"`
	LastError    string    `json:"last_error,omitempty"`
}

// stateFile persists per-model health across processes and restarts.
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

func (st *state) get(model string) *modelState {
	if st.Models[model] == nil {
		st.Models[model] = &modelState{}
	}
	return st.Models[model]
}

// chain tries the configured models in order, honouring their state.
type chain struct {
	models  []string
	client  chatter
	st      *state
	dataDir string
	now     func() time.Time
}

func (c *chain) generate(ctx context.Context, sys, user string, wantSummary bool) (result, callInfo, error) {
	now := c.now()
	defer c.st.save(c.dataDir)
	var lastErr error
	for _, m := range c.models {
		ms := c.st.get(m)
		if ms.Disabled || now.Before(ms.BlockedUntil) {
			continue
		}
		prompt, total := user, usage{}
		for attempt := 1; attempt <= 2; attempt++ {
			text, u, err := c.client.chat(ctx, m, sys, prompt, schema)
			total.In, total.Out = total.In+u.In, total.Out+u.Out
			if err == nil {
				var r result
				var dropped int
				r, dropped, err = parse(text, wantSummary)
				if err == nil {
					ms.Strikes, ms.LastError = 0, ""
					return r, callInfo{Model: m, Usage: total, Dropped: dropped, Attempts: attempt}, nil
				}
				err = &callError{Kind: errOutput, Status: 200, Msg: err.Error()}
			}
			lastErr = err
			var ce *callError
			if !errors.As(err, &ce) || ce.Kind != errOutput {
				c.penalize(ms, ce, now)
				if ce != nil && ce.Kind == errAuth {
					return result{}, callInfo{Model: m, Usage: total}, fmt.Errorf("%w: %v", errNoModel, err)
				}
				break
			}
			// One repair round: show the model why its reply was refused.
			prompt = user + "\n\n## Your previous reply was rejected\n" + ce.Msg +
				"\nReply again with one JSON object of the required shape and nothing else."
		}
	}
	if lastErr == nil {
		lastErr = errors.New("every model is blocked, retired or misconfigured")
	}
	return result{}, callInfo{}, fmt.Errorf("%w: %v", errNoModel, lastErr)
}

func (c *chain) penalize(ms *modelState, ce *callError, now time.Time) {
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
