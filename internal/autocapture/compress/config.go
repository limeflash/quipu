// Package compress turns the auto-capture spool into engram observations with
// a cheap model. It runs inside `engram mcp` (and on demand via
// `engram autocapture drain`), never in the hook — see FORK.md.
package compress

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/Gentleman-Programming/engram/v3/internal/autocapture"
)

// Config is ~/.engram/autocapture.json. Every field is optional.
type Config struct {
	Ollama struct {
		URL    string   `json:"url"`
		Models []string `json:"models"` // tried in order
		Think  []string `json:"think"`  // model-name prefixes that must run with thinking on
	} `json:"ollama"`
	Language      string `json:"language"`
	MaxBatchChars int    `json:"max_batch_chars"` // activity text per model call
	MaxEvents     int    `json:"max_events"`      // flush a still-running turn at this many events
	IdleMinutes   int    `json:"idle_minutes"`    // flush a session whose oldest event is this old
	IntervalSecs  int    `json:"interval_secs"`   // background drain period inside engram mcp
}

// Load reads the config and fills defaults. A missing or empty file is valid.
func Load(dataDir string) (Config, error) {
	var c Config
	data, err := os.ReadFile(filepath.Join(dataDir, autocapture.ConfigFile))
	if err != nil && !os.IsNotExist(err) {
		return c, err
	}
	if len(strings.TrimSpace(string(data))) > 0 {
		if err := json.Unmarshal(data, &c); err != nil {
			return c, err
		}
	}
	if c.Ollama.URL == "" {
		c.Ollama.URL = "https://ollama.com"
	}
	if len(c.Ollama.Models) == 0 {
		c.Ollama.Models = []string{"deepseek-v4.1-flash", "glm-5.3-flash"}
	}
	if c.Ollama.Think == nil {
		c.Ollama.Think = []string{"glm-"}
	}
	if c.Language == "" {
		c.Language = "English"
	}
	if c.MaxBatchChars <= 0 {
		c.MaxBatchChars = 24000
	}
	if c.MaxEvents <= 0 {
		c.MaxEvents = 40
	}
	if c.IdleMinutes <= 0 {
		c.IdleMinutes = 10
	}
	if c.IntervalSecs <= 0 {
		c.IntervalSecs = 60
	}
	return c, nil
}

func (c Config) idle() time.Duration { return time.Duration(c.IdleMinutes) * time.Minute }

// ollamaKey comes from OLLAMA_API_KEY or <dataDir>/ollama.key — never from
// the config file, which is easier to paste somewhere by accident.
func ollamaKey(dataDir string) string {
	if k := strings.TrimSpace(os.Getenv("OLLAMA_API_KEY")); k != "" {
		return k
	}
	data, _ := os.ReadFile(filepath.Join(dataDir, "ollama.key"))
	return strings.TrimSpace(string(data))
}
