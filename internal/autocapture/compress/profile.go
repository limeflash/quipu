package compress

import (
	"bufio"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/Gentleman-Programming/engram/v3/internal/store"
)

// A project profile answers "what is this built with and how is it laid
// out" once, instead of hundreds of read-notes rediscovering it. Everything
// but the architecture paragraph is deterministic: manifests, file counts,
// commands that were actually run, files actually touched.

const (
	profilesFile   = "profiles.json"
	profileEvery   = 24 * time.Hour // rebuild at most daily unless a manifest changed
	activityWindow = 30 * 24 * time.Hour
	maxWalk        = 50000
)

type profileState struct {
	Root         string    `json:"root"`
	ManifestHash string    `json:"manifest_hash"`
	BuiltAt      time.Time `json:"built_at"`
	ObsID        int64     `json:"obs_id"`
	OneLine      string    `json:"one_line"`
}

func loadProfiles(dataDir string) map[string]*profileState {
	m := map[string]*profileState{}
	if data, err := os.ReadFile(filepath.Join(dataDir, profilesFile)); err == nil {
		_ = json.Unmarshal(data, &m)
	}
	return m
}

func saveProfiles(dataDir string, m map[string]*profileState) {
	data, _ := json.MarshalIndent(m, "", "  ")
	tmp := filepath.Join(dataDir, "."+profilesFile+".tmp")
	if os.WriteFile(tmp, data, 0o600) == nil {
		_ = os.Rename(tmp, filepath.Join(dataDir, profilesFile))
	}
}

type profileFacts struct {
	Stack     []string
	Languages []string
	Commands  []string
	Layout    []string
	HotFiles  []string
	Readme    string
}

// skipDirs are never walked: dependencies, build output, tool state.
var skipDirs = map[string]bool{".git": true, "node_modules": true, "vendor": true, "build": true, "dist": true,
	"target": true, "bin": true, "obj": true, "out": true, ".gradle": true, ".idea": true, ".vscode": true,
	".next": true, ".venv": true, "venv": true, "__pycache__": true, ".claude": true, ".engram": true, "coverage": true}

var langByExt = map[string]string{".go": "Go", ".ts": "TypeScript", ".tsx": "TypeScript", ".js": "JavaScript",
	".jsx": "JavaScript", ".mjs": "JavaScript", ".kt": "Kotlin", ".kts": "Kotlin", ".java": "Java", ".rs": "Rust",
	".py": "Python", ".cs": "C#", ".cpp": "C++", ".cc": "C++", ".c": "C", ".h": "C/C++", ".swift": "Swift",
	".rb": "Ruby", ".php": "PHP", ".lua": "Lua", ".sql": "SQL", ".sh": "Shell", ".ps1": "PowerShell",
	".vue": "Vue", ".svelte": "Svelte", ".dart": "Dart", ".gd": "GDScript", ".zig": "Zig"}

// manifestNames are hashed to detect a stack change and parsed for the stack.
var manifestNames = []string{"go.mod", "package.json", "Cargo.toml", "pyproject.toml", "requirements.txt",
	"build.gradle", "build.gradle.kts", "settings.gradle", "settings.gradle.kts", "pom.xml", "composer.json",
	"Gemfile", "deno.json", "Dockerfile", "docker-compose.yml", "docker-compose.yaml", "compose.yaml",
	"wrangler.toml", "wrangler.jsonc", "wrangler.json", "project.godot"}

// manifests returns the manifest files at the root and one level below
// (monorepos, Android app modules), relative path → content.
func manifests(root string) map[string]string {
	out := map[string]string{}
	dirs := []string{root}
	if entries, err := os.ReadDir(root); err == nil {
		for _, e := range entries {
			if e.IsDir() && !skipDirs[e.Name()] && !strings.HasPrefix(e.Name(), ".") {
				dirs = append(dirs, filepath.Join(root, e.Name()))
			}
		}
	}
	for _, dir := range dirs {
		entries, _ := os.ReadDir(dir)
		for _, e := range entries {
			name := e.Name()
			known := strings.HasSuffix(name, ".csproj")
			for _, m := range manifestNames {
				known = known || name == m
			}
			if e.IsDir() || !known {
				continue
			}
			if data, err := os.ReadFile(filepath.Join(dir, name)); err == nil && len(data) < 512<<10 {
				rel, _ := filepath.Rel(root, filepath.Join(dir, name))
				out[filepath.ToSlash(rel)] = string(data)
			}
		}
	}
	return out
}

func manifestHash(m map[string]string) string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	h := sha256.New()
	for _, k := range keys {
		h.Write([]byte(k + "\x00" + m[k] + "\x00"))
	}
	return hex.EncodeToString(h.Sum(nil))[:16]
}

var (
	goVersionRe    = regexp.MustCompile(`(?m)^go\s+(\d+\.\d+)`)
	goRequireRe    = regexp.MustCompile(`(?m)^\s*(?:require\s+)?([a-z0-9.\-]+\.[a-z]{2,}/[^\s]+)\s+v[^\s]+\s*$`)
	cargoDepRe     = regexp.MustCompile(`(?m)^([a-zA-Z0-9_\-]+)\s*=`)
	pyDepRe        = regexp.MustCompile(`(?m)^\s*"?([A-Za-z0-9_.\-]+)\s*(?:[<>=~!\[;"]|$)`)
	csTargetRe     = regexp.MustCompile(`<TargetFrameworks?>([^<]+)</TargetFrameworks?>`)
	csPackageRe    = regexp.MustCompile(`<PackageReference\s+Include="([^"]+)"`)
	gradleSdkRe    = regexp.MustCompile(`compileSdk\s*=?\s*(\d+)`)
	dockerFromRe   = regexp.MustCompile(`(?mi)^FROM\s+([^\s]+)`)
	majorVersionRe = regexp.MustCompile(`^v\d+$`)
)

// stackFrom reads the stack out of manifests, line-based.
// ponytail: regexes, not TOML/YAML parsers; enough for a one-line summary.
func stackFrom(m map[string]string) []string {
	var stack []string
	seen := map[string]bool{}
	add := func(s string) {
		if s != "" && !seen[s] && len(stack) < 14 {
			seen[s] = true
			stack = append(stack, s)
		}
	}
	short := func(mod string) string {
		parts := strings.Split(strings.TrimSuffix(mod, "/"), "/")
		last := parts[len(parts)-1]
		if majorVersionRe.MatchString(last) && len(parts) > 1 {
			last = parts[len(parts)-2]
		}
		return last
	}
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, path := range keys {
		body, base := m[path], filepath.Base(path)
		switch {
		case base == "go.mod":
			if v := goVersionRe.FindStringSubmatch(body); v != nil {
				add("Go " + v[1])
			} else {
				add("Go")
			}
			n := 0
			for _, line := range strings.Split(body, "\n") {
				// golang.org/x/* are standard-library extensions: noise in a stack line.
				if strings.Contains(line, "// indirect") || strings.Contains(line, "golang.org/x/") {
					continue
				}
				if r := goRequireRe.FindStringSubmatch(line); r != nil && n < 10 {
					add(short(r[1]))
					n++
				}
			}
		case base == "package.json":
			var pj struct {
				Dependencies    map[string]string `json:"dependencies"`
				DevDependencies map[string]string `json:"devDependencies"`
			}
			if json.Unmarshal([]byte(body), &pj) == nil {
				add("Node.js")
				var deps []string
				for d := range pj.Dependencies {
					deps = append(deps, d)
				}
				sort.Strings(deps)
				for _, d := range deps[:min(len(deps), 6)] {
					add(d)
				}
				if _, ok := pj.DevDependencies["typescript"]; ok {
					add("TypeScript")
				}
			}
		case base == "Cargo.toml":
			add("Rust")
			if i := strings.Index(body, "[dependencies]"); i >= 0 {
				sec := body[i+len("[dependencies]"):]
				if j := strings.Index(sec, "\n["); j >= 0 {
					sec = sec[:j]
				}
				for _, d := range cargoDepRe.FindAllStringSubmatch(sec, 6) {
					add(d[1])
				}
			}
		case base == "pyproject.toml" || base == "requirements.txt":
			add("Python")
			if base == "requirements.txt" {
				for _, d := range pyDepRe.FindAllStringSubmatch(body, 6) {
					add(d[1])
				}
			}
		case strings.HasPrefix(base, "build.gradle") || strings.HasPrefix(base, "settings.gradle"):
			if strings.Contains(body, "com.android.application") || strings.Contains(body, "com.android.library") {
				add("Android")
			}
			if strings.Contains(body, "kotlin") {
				add("Kotlin")
			}
			if strings.Contains(body, "compose") {
				add("Jetpack Compose")
			}
			if v := gradleSdkRe.FindStringSubmatch(body); v != nil {
				add("compileSdk " + v[1])
			}
		case strings.HasSuffix(base, ".csproj"):
			if v := csTargetRe.FindStringSubmatch(body); v != nil {
				add(".NET " + strings.TrimPrefix(v[1], "net"))
			}
			for _, p := range csPackageRe.FindAllStringSubmatch(body, 5) {
				add(p[1])
			}
		case base == "pom.xml":
			add("Java (Maven)")
		case base == "Dockerfile":
			if v := dockerFromRe.FindStringSubmatch(body); v != nil {
				add("Docker (" + v[1] + ")")
			} else {
				add("Docker")
			}
		case strings.Contains(base, "compose"):
			add("Docker Compose")
		case strings.HasPrefix(base, "wrangler"):
			add("Cloudflare Workers")
		case base == "project.godot":
			add("Godot")
		case base == "deno.json":
			add("Deno")
		case base == "composer.json":
			add("PHP (Composer)")
		case base == "Gemfile":
			add("Ruby (Bundler)")
		}
	}
	return stack
}

// layoutAndLanguages walks the tree once: top-level directories by file
// count and languages by extension.
func layoutAndLanguages(root string) (layout, languages []string) {
	dirCount, langCount := map[string]int{}, map[string]int{}
	n := 0
	_ = filepath.WalkDir(root, func(p string, e fs.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		if e.IsDir() {
			if p != root && (skipDirs[e.Name()] || strings.HasPrefix(e.Name(), ".")) {
				return filepath.SkipDir
			}
			return nil
		}
		if n++; n > maxWalk {
			return filepath.SkipAll
		}
		rel, _ := filepath.Rel(root, p)
		if top := strings.SplitN(filepath.ToSlash(rel), "/", 2); len(top) == 2 {
			dirCount[top[0]]++
		}
		if l := langByExt[strings.ToLower(filepath.Ext(p))]; l != "" {
			langCount[l]++
		}
		return nil
	})
	return topCounts(dirCount, 8, "%s/ (%d)"), topCounts(langCount, 4, "%s (%d)")
}

func topCounts(m map[string]int, n int, format string) []string {
	type kv struct {
		k string
		v int
	}
	var all []kv
	for k, v := range m {
		all = append(all, kv{k, v})
	}
	sort.Slice(all, func(i, j int) bool { return all[i].v > all[j].v || all[i].v == all[j].v && all[i].k < all[j].k })
	var out []string
	for _, e := range all[:min(len(all), n)] {
		out = append(out, fmt.Sprintf(format, e.k, e.v))
	}
	return out
}

var (
	cmdPrefixRe = regexp.MustCompile(`(?i)^\s*(?:(?:cd|set-location|sl|pushd|push-location)\s+[^;&|\n]+(?:;|&&)\s*|\$env:[A-Za-z_]+\s*=\s*[^;\n]+;\s*|[A-Z_][A-Z0-9_]*=\S+\s+)+`)
	buildTools  = map[string]bool{"go": true, "npm": true, "pnpm": true, "yarn": true, "npx": true, "bun": true,
		"cargo": true, "gradle": true, "gradlew": true, "gradlew.bat": true, "./gradlew": true, ".\\gradlew.bat": true,
		"make": true, "pytest": true, "python": true, "py": true, "dotnet": true, "mvn": true, "docker": true,
		"wrangler": true, "node": true, "deno": true, "tsc": true, "vitest": true, "jest": true, "flutter": true}
)

// normalizeCommand reduces a shell command to its build/test essence:
// "cd x; $env:A='b'; go test -count=1 ./internal/... 2>&1 | tail" → "go test -count=1 ./internal/...".
func normalizeCommand(cmd string) string {
	cmd = cmdPrefixRe.ReplaceAllString(strings.TrimSpace(strings.SplitN(cmd, "\n", 2)[0]), "")
	for _, sep := range []string{"|", ";", "&&", "2>", ">"} {
		if i := strings.Index(cmd, sep); i > 0 {
			cmd = cmd[:i]
		}
	}
	fields := strings.Fields(cmd)
	if len(fields) == 0 || !buildTools[strings.ToLower(strings.TrimSuffix(fields[0], ".exe"))] {
		return ""
	}
	return strings.Join(fields[:min(len(fields), 4)], " ")
}

// activity reads the touches log for one project: commands run and files
// touched (an edit weighs three reads), newest month only.
func activity(dataDir, project, root string, now time.Time) (commands, hot []string) {
	f, err := os.Open(filepath.Join(dataDir, touchesFile))
	if err != nil {
		return nil, nil
	}
	defer f.Close()
	cmdCount, fileScore := map[string]int{}, map[string]int{}
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 1<<20), 1<<20)
	for sc.Scan() {
		var e struct {
			At      time.Time `json:"at"`
			Project string    `json:"project"`
			Kind    string    `json:"kind"`
			Target  string    `json:"target"`
		}
		if json.Unmarshal(sc.Bytes(), &e) != nil || e.Project != project || now.Sub(e.At) > activityWindow {
			continue
		}
		switch e.Kind {
		case "cmd":
			if c := normalizeCommand(e.Target); c != "" {
				cmdCount[c]++
			}
		case "edit", "read", "":
			if !filepath.IsAbs(e.Target) || strings.ContainsAny(e.Target, "*?\n") {
				continue
			}
			rel, err := filepath.Rel(root, e.Target)
			if err != nil || strings.HasPrefix(rel, "..") {
				continue
			}
			w := 1
			if e.Kind == "edit" {
				w = 3
			}
			fileScore[worktreeRelative(filepath.ToSlash(rel))] += w
		}
	}
	return topCounts(cmdCount, 6, "`%s` ×%d"), topCounts(fileScore, 8, "%s (%d)")
}

var worktreeRe = regexp.MustCompile(`^\.claude/worktrees/[^/]+/`)

// worktreeRelative maps a file inside an agent worktree of the repository
// (.claude/worktrees/<name>/...) to the same path in the repository itself.
func worktreeRelative(rel string) string { return worktreeRe.ReplaceAllString(rel, "") }

func readmeHead(root string) string {
	for _, name := range []string{"README.md", "readme.md", "README.MD", "README"} {
		if data, err := os.ReadFile(filepath.Join(root, name)); err == nil {
			lines := strings.Split(string(data), "\n")
			return clip(strings.Join(lines[:min(len(lines), 40)], "\n"), 2500)
		}
	}
	return ""
}

const profileSystem = `You write a short architecture overview of a software project from facts about it: what it is, its main parts and how they fit together. 3 to 6 sentences, concrete names (directories, components, technologies) only, no marketing, no guessing beyond the facts. Write in %s.

Reply with one JSON object and nothing else, shaped exactly:
{"observations":[{"type":"architecture","title":"Architecture overview","what":"<the overview>","why":"","where":"<the main directories or components>","learned":"","topic_key":""}]}`

func (pf profileFacts) prompt(project string) string {
	var b strings.Builder
	fmt.Fprintf(&b, "Project: %s\n", project)
	list := func(title string, items []string) {
		if len(items) > 0 {
			fmt.Fprintf(&b, "%s: %s\n", title, strings.Join(items, " · "))
		}
	}
	list("Stack", pf.Stack)
	list("Languages", pf.Languages)
	list("Layout", pf.Layout)
	list("Commands run", pf.Commands)
	list("Most-touched files", pf.HotFiles)
	if pf.Readme != "" {
		b.WriteString("\nREADME (head):\n" + pf.Readme + "\n")
	}
	return b.String()
}

// oneLine is what SessionStart shows: the stack plus the most-used command.
func (pf profileFacts) oneLine() string {
	parts := pf.Stack[:min(len(pf.Stack), 5)]
	if len(pf.Commands) > 0 {
		cmd := strings.Trim(strings.SplitN(pf.Commands[0], " ×", 2)[0], "`")
		parts = append(append([]string{}, parts...), "run: "+cmd)
	}
	return strings.Join(parts, " · ")
}

func (pf profileFacts) content(architecture, where string, now time.Time) string {
	var b strings.Builder
	line := func(k string, items []string) {
		if len(items) > 0 {
			fmt.Fprintf(&b, "**%s**: %s\n", k, strings.Join(items, " · "))
		}
	}
	line("Stack", pf.Stack)
	line("Languages", pf.Languages)
	line("Commands", pf.Commands)
	line("Layout", pf.Layout)
	line("Hot files", pf.HotFiles)
	if architecture != "" {
		fmt.Fprintf(&b, "**Architecture**: %s\n", strings.TrimSpace(architecture))
	}
	if where != "" {
		fmt.Fprintf(&b, "**Main parts**: %s\n", strings.TrimSpace(where))
	}
	fmt.Fprintf(&b, "_Profile updated %s by auto-capture._", now.UTC().Format("2006-01-02"))
	return b.String()
}

// refreshProfile rebuilds one project's profile when it has none, a manifest
// changed, or the last build is a day old. The architecture paragraph is the
// only model call; when no model is available the profile is written
// without it.
func (d *drainer) refreshProfile(ctx context.Context, project, root string, profiles map[string]*profileState, force bool) (string, error) {
	man := manifests(root)
	hash := manifestHash(man)
	st := profiles[project]
	if st == nil {
		st = &profileState{}
		profiles[project] = st
	}
	if !force && st.ObsID != 0 && st.ManifestHash == hash && d.now.Sub(st.BuiltAt) < profileEvery {
		return "", nil
	}
	pf := profileFacts{Stack: stackFrom(man), Readme: readmeHead(root)}
	pf.Layout, pf.Languages = layoutAndLanguages(root)
	pf.Commands, pf.HotFiles = activity(d.dataDir, project, root, d.now)
	if len(pf.Stack) == 0 && len(pf.Languages) == 0 {
		return "", nil // nothing recognisable: not worth a record
	}

	arch, where := "", ""
	res, info, err := d.gen.generate(ctx, fmt.Sprintf(profileSystem, d.cfg.Language), pf.prompt(project), false)
	d.log("profile", project, info, len(res.Observations), 0, err)
	if err == nil && len(res.Observations) > 0 {
		arch, where = res.Observations[0].What, res.Observations[0].Where
	}
	content := pf.content(arch, where, d.now)
	if d.dryRun {
		if d.out != nil {
			d.out("[project_profile] " + project + "\n" + content + "\n")
		}
		return content, nil
	}
	sessionID := "profile-" + safeName(project)
	if err := d.sink.CreateSession(sessionID, project, root); err != nil {
		return "", err
	}
	id, err := d.sink.AddObservation(store.AddObservationParams{SessionID: sessionID, Type: "project_profile",
		Title: "Project profile: " + project, Content: content, ToolName: ToolName, Project: project,
		Scope: "project", TopicKey: "profile/" + strings.ToLower(safeName(project))})
	if err != nil {
		return "", err
	}
	*st = profileState{Root: root, ManifestHash: hash, BuiltAt: d.now, ObsID: id, OneLine: pf.oneLine()}
	return content, nil
}
