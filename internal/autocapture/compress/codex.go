package compress

import (
	"bufio"
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

// codexClient runs a fallback model through the user's own ChatGPT plan:
// `codex exec` with the user config ignored (so no hooks, no MCP servers),
// hooks disabled again for good measure, a read-only sandbox and an ephemeral
// session, answering to a JSON schema written to a file.
type codexClient struct {
	exe     string
	effort  string
	dir     string // working directory; also holds the schema and reply files
	timeout time.Duration
	run     func(ctx context.Context, exe string, args []string, stdin, dir string, env []string) (stdout, stderr []byte, err error)
}

func newCodex(cfg Config, dataDir string) *codexClient {
	return &codexClient{exe: cfg.Codex.Exe, effort: cfg.Codex.Effort, dir: filepath.Join(dataDir, "codex-work"),
		timeout: time.Duration(cfg.Codex.TimeoutSecs) * time.Second, run: runProcess}
}

// codexSchema is the strict-mode form of the reply contract that OpenAI
// structured output requires: every property listed as required, no extra
// properties, the summary null when it was not asked for.
const codexSchema = `{"type":"object","additionalProperties":false,"required":["observations","summary"],"properties":{` +
	`"observations":{"type":"array","items":{"type":"object","additionalProperties":false,` +
	`"required":["type","title","what","why","where","learned","topic_key","updates"],"properties":{` +
	`"type":{"type":"string","enum":["decision","architecture","bugfix","feature","refactor","config","pattern","discovery","learning"]},` +
	`"title":{"type":"string"},"what":{"type":"string"},"why":{"type":"string"},"where":{"type":"string"},` +
	`"learned":{"type":"string"},"topic_key":{"type":"string"},"updates":{"anyOf":[{"type":"integer"},{"type":"null"}]}}}},` +
	`"summary":{"anyOf":[{"type":"null"},{"type":"object","additionalProperties":false,` +
	`"required":["goal","discoveries","accomplished","next_steps","files"],"properties":{` +
	`"goal":{"type":"string"},"discoveries":{"type":"array","items":{"type":"string"}},` +
	`"accomplished":{"type":"array","items":{"type":"string"}},"next_steps":{"type":"array","items":{"type":"string"}},` +
	`"files":{"type":"array","items":{"type":"string"}}}}]}}}`

func (c *codexClient) chat(ctx context.Context, model, system, user string, _ json.RawMessage) (string, usage, error) {
	if err := os.MkdirAll(c.dir, 0o700); err != nil {
		return "", usage{}, &callError{Kind: errTransient, Msg: err.Error()}
	}
	schemaFile := filepath.Join(c.dir, "schema.json")
	if err := os.WriteFile(schemaFile, []byte(codexSchema), 0o600); err != nil {
		return "", usage{}, &callError{Kind: errTransient, Msg: err.Error()}
	}
	var rnd [4]byte
	_, _ = rand.Read(rnd[:])
	replyFile := filepath.Join(c.dir, "reply-"+hex.EncodeToString(rnd[:])+".json")
	defer os.Remove(replyFile)

	args := []string{"exec", "-m", model, "-c", "model_reasoning_effort=" + c.effort,
		"--ignore-user-config", "--disable", "hooks", "--ephemeral",
		"-s", "read-only", "--skip-git-repo-check", "-C", c.dir,
		"--json", "--output-schema", schemaFile, "-o", replyFile, "-"}
	ctx, cancel := context.WithTimeout(ctx, c.timeout)
	defer cancel()
	stdout, stderr, runErr := c.run(ctx, c.exe, args, system+"\n\n"+user, c.dir, codexEnv())

	u, failure := scanCodexEvents(stdout)
	if errors.Is(ctx.Err(), context.DeadlineExceeded) {
		return "", u, &callError{Kind: errTransient, Msg: "codex timed out"}
	}
	if errors.Is(runErr, exec.ErrNotFound) {
		return "", u, &callError{Kind: errRetired, Msg: "codex CLI not found: " + c.exe}
	}
	if failure != "" || runErr != nil {
		msg := strings.TrimSpace(failure + " " + string(stderr))
		if msg == "" {
			msg = runErr.Error()
		}
		return "", u, &callError{Kind: classifyCodex(msg), Msg: firstLine(msg)}
	}
	reply, _ := os.ReadFile(replyFile)
	if len(bytes.TrimSpace(reply)) == 0 {
		return "", u, &callError{Kind: errOutput, Status: 200, Msg: "empty reply"}
	}
	return string(reply), u, nil
}

// scanCodexEvents reads `codex exec --json` output: token usage from
// turn.completed, the reason from turn.failed or error events.
func scanCodexEvents(stdout []byte) (usage, string) {
	var u usage
	var failures []string
	sc := bufio.NewScanner(bytes.NewReader(stdout))
	sc.Buffer(make([]byte, 1<<20), 8<<20)
	for sc.Scan() {
		var ev struct {
			Type    string `json:"type"`
			Message string `json:"message"`
			Error   struct {
				Message string `json:"message"`
			} `json:"error"`
			Usage struct {
				Input  int `json:"input_tokens"`
				Output int `json:"output_tokens"`
			} `json:"usage"`
		}
		if json.Unmarshal(sc.Bytes(), &ev) != nil {
			continue
		}
		switch ev.Type {
		case "turn.completed":
			u.In, u.Out = u.In+ev.Usage.Input, u.Out+ev.Usage.Output
		case "turn.failed", "error":
			failures = append(failures, strings.TrimSpace(ev.Error.Message+" "+ev.Message))
		}
	}
	return u, strings.Join(failures, "; ")
}

func classifyCodex(msg string) string {
	m := strings.ToLower(msg)
	switch {
	case strings.Contains(m, "usage limit") || strings.Contains(m, "rate limit") || strings.Contains(m, "429") ||
		strings.Contains(m, "quota") || strings.Contains(m, "try again at"):
		return errQuota
	case strings.Contains(m, "401") || strings.Contains(m, "unauthorized") || strings.Contains(m, "log in") ||
		strings.Contains(m, "login") || strings.Contains(m, "not logged"):
		return errAuth
	case strings.Contains(m, "model") && (strings.Contains(m, "not supported") || strings.Contains(m, "not found") ||
		strings.Contains(m, "does not exist")):
		return errRetired
	}
	return errTransient
}

// codexEnv drops what a parent Codex session exports about itself, keeping
// CODEX_HOME so the child finds the same login.
func codexEnv() []string {
	var env []string
	for _, kv := range os.Environ() {
		name := strings.ToUpper(kv[:max(0, strings.IndexByte(kv, '='))])
		if (strings.HasPrefix(name, "CODEX_") && name != "CODEX_HOME") || name == "ENGRAM_INTERNAL" {
			continue
		}
		env = append(env, kv)
	}
	return append(env, "ENGRAM_INTERNAL=1")
}

// codexExe prefers the native binary behind the npm shim: no cmd.exe or node
// in between, and no batch-file argument quoting.
func codexExe(configured string) string {
	if configured != "" {
		return configured
	}
	shim, err := exec.LookPath("codex")
	if err != nil {
		return "codex"
	}
	matches, _ := filepath.Glob(filepath.Join(filepath.Dir(shim), "node_modules", "@openai", "codex",
		"node_modules", "@openai", "codex-*", "vendor", "*", "bin", "codex*"))
	for _, m := range matches {
		if base := strings.ToLower(filepath.Base(m)); base == "codex" || base == "codex.exe" {
			return m
		}
	}
	return shim
}

// codexLoggedIn reports whether Codex has a stored login to use.
func codexLoggedIn() bool {
	home := os.Getenv("CODEX_HOME")
	if home == "" {
		h, _ := os.UserHomeDir()
		home = filepath.Join(h, ".codex")
	}
	_, err := os.Stat(filepath.Join(home, "auth.json"))
	return err == nil
}
