package compress

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Gentleman-Programming/engram/v3/internal/store"
)

func TestStackFromManifests(t *testing.T) {
	got := strings.Join(stackFrom(map[string]string{
		"go.mod": "module x\n\ngo 1.26.1\n\nrequire (\n\tmodernc.org/sqlite v1.46.2\n\tgithub.com/mark3labs/mcp-go v0.44.0\n" +
			"\tgithub.com/foo/bar/v3 v3.1.0\n\tgolang.org/x/net v0.56.0\n\tgolang.org/x/sys v0.46.0 // indirect\n)\n",
		"app/build.gradle.kts": "plugins { id(\"com.android.application\"); kotlin(\"android\") }\nandroid { compileSdk = 35 }\nbuildFeatures { compose = true }",
		"Dockerfile":           "FROM golang:1.26-alpine AS build\nRUN go build",
		"edge/wrangler.toml":   "name = \"edge\"",
	}), " | ")
	for _, want := range []string{"Go 1.26", "sqlite", "mcp-go", "bar", "Android", "Kotlin", "Jetpack Compose",
		"compileSdk 35", "Docker (golang:1.26-alpine)", "Cloudflare Workers"} {
		if !strings.Contains(got, want) {
			t.Errorf("stack lacks %q: %s", want, got)
		}
	}
	if strings.Contains(got, "sys") || strings.Contains(got, "net") {
		t.Errorf("indirect or golang.org/x dependency listed: %s", got)
	}
}

func TestNormalizeCommand(t *testing.T) {
	for in, want := range map[string]string{
		"go test ./...": "go test ./...",
		`cd G:\proj; $env:GOOS="js"; go vet ./cmd/edge 2>&1 | Select -First 5`: "go vet ./cmd/edge",
		"CGO_ENABLED=0 go build -o bin/x ./cmd/x":                              "go build -o bin/x",
		"./gradlew :app:assembleRelease --console=plain":                       "./gradlew :app:assembleRelease --console=plain",
		"pnpm exec vitest run":                                                 "pnpm exec vitest run",
		"git commit -m x":                                                      "",
		"rm -rf build":                                                         "",
	} {
		if got := normalizeCommand(in); got != want {
			t.Errorf("%q: got %q want %q", in, got, want)
		}
	}
}

func writeActivity(t *testing.T, dataDir string, lines ...map[string]any) {
	t.Helper()
	f, err := os.Create(filepath.Join(dataDir, touchesFile))
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	for _, l := range lines {
		_ = json.NewEncoder(f).Encode(l)
	}
}

func TestActivityRanksCommandsAndFiles(t *testing.T) {
	dataDir, root := t.TempDir(), t.TempDir()
	at := t0.Add(-time.Hour)
	file := func(name string) string { return filepath.Join(root, name) }
	writeActivity(t, dataDir,
		map[string]any{"at": at, "project": "p", "kind": "cmd", "target": "go test ./..."},
		map[string]any{"at": at, "project": "p", "kind": "cmd", "target": "cd x; go test ./..."},
		map[string]any{"at": at, "project": "p", "kind": "cmd", "target": "go build ./cmd/x"},
		map[string]any{"at": at, "project": "p", "kind": "edit", "target": file("a.go")},
		map[string]any{"at": at, "project": "p", "kind": "read", "target": file("b.go")},
		map[string]any{"at": at, "project": "p", "target": filepath.Join(root, ".claude", "worktrees", "wt1", "b.go")}, // legacy line, in a worktree
		map[string]any{"at": at, "project": "other", "kind": "edit", "target": file("z.go")},
		map[string]any{"at": t0.Add(-60 * 24 * time.Hour), "project": "p", "kind": "edit", "target": file("old.go")},
	)
	cmds, hot := activity(dataDir, "p", root, t0)
	if strings.Join(cmds, ",") != "`go test ./...` ×2,`go build ./cmd/x` ×1" {
		t.Fatalf("commands %v", cmds)
	}
	if strings.Join(hot, ",") != "a.go (3),b.go (2)" {
		t.Fatalf("hot files %v (edit weighs 3, other projects and old entries excluded)", hot)
	}
}

func TestProfileBuiltOnceAndRebuiltOnManifestChange(t *testing.T) {
	dataDir, root := t.TempDir(), t.TempDir()
	_ = os.WriteFile(filepath.Join(root, "go.mod"), []byte("module m\n\ngo 1.26\n"), 0o600)
	_ = os.MkdirAll(filepath.Join(root, "internal", "store"), 0o700)
	_ = os.WriteFile(filepath.Join(root, "internal", "store", "s.go"), []byte("package store"), 0o600)
	_ = os.WriteFile(filepath.Join(root, "README.md"), []byte("# M\nA memory tool."), 0o600)
	sink := &fakeSink{}
	gen := &fakeGen{reply: func(n int, user string, _ bool) (result, error) {
		if !strings.Contains(user, "Stack: Go 1.2") || !strings.Contains(user, "A memory tool.") {
			t.Errorf("profile prompt lacks facts:\n%s", user)
		}
		return result{Observations: []record{{Type: "architecture", Title: "Architecture overview",
			What: "A Go CLI with an internal store.", Where: "internal/store"}}}, nil
	}}
	d := &drainer{sink: sink, gen: gen, cfg: testConfig(), dataDir: dataDir, now: t0,
		projs: map[string]string{}, roots: map[string]string{"m": root}}
	var rep Report
	d.refreshProfiles(context.Background(), &rep)
	if rep.Profiles != 1 || len(sink.obs) != 1 {
		t.Fatalf("profiles=%d obs=%d", rep.Profiles, len(sink.obs))
	}
	o := sink.obs[0]
	if o.Type != "project_profile" || o.TopicKey != "profile/m" || o.ToolName != ToolName ||
		!strings.Contains(o.Content, "**Stack**: Go 1.26") || !strings.Contains(o.Content, "**Architecture**: A Go CLI") ||
		!strings.Contains(o.Content, "internal/ (1)") {
		t.Fatalf("profile record %+v", o)
	}
	if p := loadProfiles(dataDir)["m"]; p == nil || p.OneLine != "Go 1.26" || p.ObsID != 1 {
		t.Fatalf("profile state %+v", p)
	}

	d.now = t0.Add(time.Hour)
	rep = Report{}
	d.refreshProfiles(context.Background(), &rep)
	if rep.Profiles != 0 || len(gen.users) != 1 {
		t.Fatal("an unchanged profile must not be rebuilt within a day")
	}
	_ = os.WriteFile(filepath.Join(root, "go.mod"), []byte("module m\n\ngo 1.27\n"), 0o600)
	d.refreshProfiles(context.Background(), &rep)
	if rep.Profiles != 1 || !strings.Contains(sink.obs[1].Content, "Go 1.27") {
		t.Fatal("a manifest change must rebuild the profile")
	}
}

func TestProfileWithoutModelStillWritten(t *testing.T) {
	dataDir, root := t.TempDir(), t.TempDir()
	_ = os.WriteFile(filepath.Join(root, "package.json"), []byte(`{"dependencies":{"hono":"4"}}`), 0o600)
	sink := &fakeSink{}
	gen := &fakeGen{reply: func(int, string, bool) (result, error) { return result{}, errNoModel }}
	d := &drainer{sink: sink, gen: gen, cfg: testConfig(), dataDir: dataDir, now: t0,
		projs: map[string]string{}, roots: map[string]string{"w": root}}
	d.refreshProfiles(context.Background(), &Report{})
	if len(sink.obs) != 1 || strings.Contains(sink.obs[0].Content, "Architecture") || !strings.Contains(sink.obs[0].Content, "hono") {
		t.Fatalf("got %+v", sink.obs)
	}
}

func TestProfileLineInSessionStart(t *testing.T) {
	dataDir := t.TempDir()
	saveProfiles(dataDir, map[string]*profileState{"android": {ObsID: 77, OneLine: "Kotlin · Android · run: ./gradlew test"}})
	r := &fakeReader{byProject: map[string][]store.Observation{"android": {
		{ID: 77, Type: "project_profile", Title: "Project profile: android"},
		{ID: 5, Type: "bugfix", Title: "real record", CreatedAt: ts(time.Hour)},
	}}}
	out := SessionContext(r, dataDir, "s", `x\android`, "startup", now0)
	if !strings.Contains(out, "Project profile (#77") || !strings.Contains(out, "Kotlin · Android · run: ./gradlew test") {
		t.Fatalf("profile line missing:\n%s", out)
	}
	if strings.Contains(out, "#77 [project_profile]") {
		t.Fatalf("profile repeated as a record:\n%s", out)
	}
}
