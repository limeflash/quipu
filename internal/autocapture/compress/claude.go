package compress

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/Gentleman-Programming/engram/v3/internal/command"
)

// claudeClient runs the fallback model through the user's own Claude Code
// subscription: `claude -p` with no tools, no hooks, no MCP and no session
// file, so the call cannot act, cannot capture itself and leaves no trace.
type claudeClient struct {
	exe     string
	effort  string
	token   string
	dir     string
	timeout time.Duration
	run     func(ctx context.Context, exe string, args []string, stdin, dir string, env []string) (stdout, stderr []byte, err error)
}

func newClaude(cfg Config, dataDir, token string) *claudeClient {
	return &claudeClient{exe: cfg.Claude.Exe, effort: cfg.Claude.Effort, token: token, dir: dataDir,
		timeout: time.Duration(cfg.Claude.TimeoutSecs) * time.Second, run: runProcess}
}

func runProcess(ctx context.Context, exe string, args []string, stdin, dir string, env []string) ([]byte, []byte, error) {
	cmd := command.NewContext(ctx, exe, args...) // hidden window on Windows
	cmd.Stdin = strings.NewReader(stdin)
	cmd.Dir = dir
	cmd.Env = env
	var out, errb bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &errb
	err := cmd.Run()
	return out.Bytes(), errb.Bytes(), err
}

func (c *claudeClient) args(model, system string, schema json.RawMessage) []string {
	return []string{"-p", "--model", model, "--effort", c.effort,
		"--output-format", "json", "--json-schema", string(schema),
		"--system-prompt", system,
		"--tools", "", "--strict-mcp-config", "--disable-slash-commands",
		"--settings", `{"disableAllHooks":true}`,
		"--no-session-persistence"}
}

// env drops what a parent Claude Code session exports: inside the desktop app
// ANTHROPIC_BASE_URL and CLAUDE_CODE_* point a child at the host's relay,
// where its requests fail with 401. The child authenticates with the
// long-lived token from `claude setup-token` instead.
func (c *claudeClient) env() []string {
	var env []string
	for _, kv := range os.Environ() {
		name := strings.ToUpper(kv[:max(0, strings.IndexByte(kv, '='))])
		if strings.HasPrefix(name, "ANTHROPIC_") || strings.HasPrefix(name, "CLAUDE") ||
			(strings.HasPrefix(name, "USE_") && strings.HasSuffix(name, "OAUTH")) || name == "ENGRAM_INTERNAL" {
			continue
		}
		env = append(env, kv)
	}
	return append(env, "CLAUDE_CODE_OAUTH_TOKEN="+c.token, "ENGRAM_INTERNAL=1")
}

func (c *claudeClient) chat(ctx context.Context, model, system, user string, schema json.RawMessage) (string, usage, error) {
	ctx, cancel := context.WithTimeout(ctx, c.timeout)
	defer cancel()
	stdout, stderr, runErr := c.run(ctx, c.exe, c.args(model, system, schema), user, c.dir, c.env())

	var out struct {
		IsError          bool            `json:"is_error"`
		Result           string          `json:"result"`
		StructuredOutput json.RawMessage `json:"structured_output"`
		Usage            struct {
			Input       int `json:"input_tokens"`
			CacheCreate int `json:"cache_creation_input_tokens"`
			CacheRead   int `json:"cache_read_input_tokens"`
			Output      int `json:"output_tokens"`
		} `json:"usage"`
	}
	parsed := json.Unmarshal(bytes.TrimSpace(stdout), &out) == nil
	u := usage{In: out.Usage.Input + out.Usage.CacheCreate + out.Usage.CacheRead, Out: out.Usage.Output}

	if errors.Is(ctx.Err(), context.DeadlineExceeded) {
		return "", u, &callError{Kind: errTransient, Msg: "claude timed out"}
	}
	if errors.Is(runErr, exec.ErrNotFound) {
		return "", u, &callError{Kind: errRetired, Msg: "claude CLI not found: " + c.exe}
	}
	if !parsed || out.IsError || runErr != nil {
		msg := strings.TrimSpace(out.Result + " " + string(stderr))
		if msg == "" && runErr != nil {
			msg = runErr.Error()
		}
		return "", u, &callError{Kind: classifyClaude(msg), Msg: firstLine(msg)}
	}
	if s := strings.TrimSpace(string(out.StructuredOutput)); s != "" && s != "null" {
		return s, u, nil
	}
	if strings.TrimSpace(out.Result) == "" {
		return "", u, &callError{Kind: errOutput, Status: 200, Msg: "empty result"}
	}
	return out.Result, u, nil
}

func classifyClaude(msg string) string {
	m := strings.ToLower(msg)
	switch {
	case strings.Contains(m, "authenticat") || strings.Contains(m, "401") || strings.Contains(m, "oauth") ||
		strings.Contains(m, "not logged in") || strings.Contains(m, "/login") || strings.Contains(m, "invalid api key"):
		return errAuth
	case strings.Contains(m, "usage limit") || strings.Contains(m, "rate limit") || strings.Contains(m, "429") ||
		strings.Contains(m, "limit reached"):
		return errQuota
	case strings.Contains(m, "model") && (strings.Contains(m, "not found") || strings.Contains(m, "not available") ||
		strings.Contains(m, "invalid")):
		return errRetired
	}
	return errTransient
}

func firstLine(s string) string {
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		s = s[:i]
	}
	if len(s) > 300 {
		s = s[:300]
	}
	return s
}

// claudeToken comes from ENGRAM_CLAUDE_TOKEN or <dataDir>/claude.token. A
// CLAUDE_CODE_OAUTH_TOKEN inherited from a parent session is deliberately not
// used: it belongs to that session's host.
func claudeToken(dataDir string) string {
	if t := strings.TrimSpace(os.Getenv("ENGRAM_CLAUDE_TOKEN")); t != "" {
		return t
	}
	data, _ := os.ReadFile(filepath.Join(dataDir, "claude.token"))
	return strings.TrimSpace(string(data))
}

// claudeExe finds the CLI: configured path, PATH, then the native installer's
// default location.
func claudeExe(configured string) string {
	if configured != "" {
		return configured
	}
	if p, err := exec.LookPath("claude"); err == nil {
		return p
	}
	home, _ := os.UserHomeDir()
	for _, p := range []string{filepath.Join(home, ".local", "bin", "claude.exe"), filepath.Join(home, ".local", "bin", "claude")} {
		if _, err := os.Stat(p); err == nil {
			return p
		}
	}
	return "claude"
}
