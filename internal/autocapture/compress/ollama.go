package compress

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

// Error kinds decide what happens to a model after a failed call.
const (
	errQuota     = "quota"     // 429 / usage limit: back off, try the next model
	errRetired   = "retired"   // 410 / unknown model: disable permanently
	errAuth      = "auth"      // 401 / 403: stop, nothing else will work either
	errTransient = "transient" // network, 5xx: try the next model, retry later
	errOutput    = "output"    // answered, but not with usable JSON
)

type callError struct {
	Kind   string
	Status int
	Msg    string
}

func (e *callError) Error() string {
	if e.Status == 0 {
		return fmt.Sprintf("%s: %s", e.Kind, e.Msg)
	}
	return fmt.Sprintf("%s (HTTP %d): %s", e.Kind, e.Status, e.Msg)
}

type usage struct{ In, Out int }

type ollamaClient struct {
	url, key string
	think    []string
	http     *http.Client
}

func newOllama(cfg Config, key string) *ollamaClient {
	return &ollamaClient{url: strings.TrimRight(cfg.Ollama.URL, "/"), key: key, think: cfg.Ollama.Think,
		http: &http.Client{Timeout: 3 * time.Minute}}
}

// chat calls the native /api/chat endpoint. The OpenAI-compatible one cannot
// switch thinking off for every model, which is what made claude-mem's calls
// return empty content.
func (o *ollamaClient) chat(ctx context.Context, model, system, user string, schema json.RawMessage) (string, usage, error) {
	think := false
	for _, p := range o.think {
		if strings.HasPrefix(model, p) {
			think = true
		}
	}
	body, _ := json.Marshal(map[string]any{
		"model": model, "stream": false, "think": think, "format": schema,
		"options":  map[string]any{"temperature": 0.2, "num_predict": 4096},
		"messages": []map[string]string{{"role": "system", "content": system}, {"role": "user", "content": user}},
	})
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, o.url+"/api/chat", bytes.NewReader(body))
	if err != nil {
		return "", usage{}, err
	}
	req.Header.Set("Content-Type", "application/json")
	if o.key != "" {
		req.Header.Set("Authorization", "Bearer "+o.key)
	}
	resp, err := o.http.Do(req)
	if err != nil {
		return "", usage{}, &callError{Kind: errTransient, Msg: err.Error()}
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 8<<20))

	var out struct {
		Message struct {
			Content string `json:"content"`
		} `json:"message"`
		PromptEvalCount int    `json:"prompt_eval_count"`
		EvalCount       int    `json:"eval_count"`
		Error           string `json:"error"`
	}
	_ = json.Unmarshal(raw, &out)
	msg := out.Error
	if msg == "" {
		msg = strings.TrimSpace(string(raw[:min(len(raw), 300)]))
	}
	u := usage{In: out.PromptEvalCount, Out: out.EvalCount}
	switch {
	case resp.StatusCode == http.StatusOK && out.Error == "":
		if strings.TrimSpace(out.Message.Content) == "" {
			return "", u, &callError{Kind: errOutput, Status: 200, Msg: "empty content"}
		}
		return out.Message.Content, u, nil
	case resp.StatusCode == http.StatusTooManyRequests || strings.Contains(strings.ToLower(msg), "usage limit"):
		return "", u, &callError{Kind: errQuota, Status: resp.StatusCode, Msg: msg}
	case resp.StatusCode == http.StatusGone || resp.StatusCode == http.StatusNotFound:
		return "", u, &callError{Kind: errRetired, Status: resp.StatusCode, Msg: msg}
	case resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden:
		return "", u, &callError{Kind: errAuth, Status: resp.StatusCode, Msg: msg}
	default:
		return "", u, &callError{Kind: errTransient, Status: resp.StatusCode, Msg: msg}
	}
}
