package autocapture

import (
	"path/filepath"
	"regexp"
	"strings"
)

// Commands that only look at things. Lowercase, without path or .exe.
var readOnlyCommands = map[string]bool{
	// POSIX
	"cat": true, "head": true, "tail": true, "less": true, "more": true, "ls": true, "dir": true,
	"tree": true, "find": true, "fd": true, "rg": true, "grep": true, "egrep": true, "fgrep": true,
	"ag": true, "wc": true, "stat": true, "file": true, "pwd": true, "echo": true, "printf": true,
	"which": true, "where": true, "whereis": true, "type": true, "du": true, "df": true, "sort": true,
	"uniq": true, "cut": true, "jq": true, "yq": true, "diff": true, "cmp": true, "md5sum": true,
	"sha256sum": true, "sha1sum": true, "date": true, "env": true, "printenv": true, "uname": true,
	"whoami": true, "hostname": true, "id": true, "ps": true, "basename": true, "dirname": true,
	"realpath": true, "readlink": true, "nproc": true, "true": true,
	// PowerShell
	"get-content": true, "gc": true, "get-childitem": true, "gci": true, "get-item": true, "gi": true,
	"select-string": true, "sls": true, "test-path": true, "get-location": true, "resolve-path": true,
	"get-command": true, "get-process": true, "measure-object": true, "format-table": true, "ft": true,
	"format-list": true, "fl": true, "select-object": true, "select": true, "where-object": true,
	"where.exe": true, "sort-object": true, "get-date": true, "write-output": true, "write-host": true,
	"out-string": true, "convertto-json": true, "convertfrom-json": true, "get-filehash": true,
	"get-itemproperty": true, "get-ciminstance": true, "get-nettcpconnection": true, "get-service": true,
	"get-acl": true, "foreach-object": true, "%": true, "?": true, "out-null": true, "out-host": true,
}

// Subcommands of version-control and build tools that only read.
var readOnlySub = map[string]map[string]bool{
	"git": {"status": true, "log": true, "diff": true, "show": true, "blame": true, "rev-parse": true,
		"ls-files": true, "describe": true, "shortlog": true, "grep": true, "reflog": true, "remote": true,
		"branch": true, "ls-remote": true, "cat-file": true},
	"go":    {"doc": true, "list": true, "env": true, "version": true},
	"npm":   {"ls": true, "list": true, "view": true, "outdated": true},
	"gh":    {"pr": true, "issue": true, "run": true, "repo": true, "api": true, "search": true},
	"cargo": {"tree": true, "metadata": true},
}

// Arguments that turn an otherwise read-only subcommand into a write.
var writeFlags = regexp.MustCompile(`(?i)(\s-[dDmM]\b|\s--delete\b|\s--move\b|\s--set\b|\s--unset\b|\s--add\b|\s(create|merge|close|edit|delete|comment)\b|\s-X\s*(POST|PUT|PATCH|DELETE))`)

var (
	segmentSplit  = regexp.MustCompile(`&&|\|\||[;|\n]`)
	harmlessRedir = regexp.MustCompile(`(?i)\d?>&\d|\d?>\s*/dev/null|\d?>\s*\$null|\d?>\s*nul\b`)
	envAssign     = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*=\S*$`)
)

// ReadOnlyCommand reports whether every segment of a shell command only reads.
// Unknown commands count as writes: capturing a read by mistake costs a few
// tokens, dropping a write loses the memory.
// ponytail: first-word heuristic; quoted separators and subshells are not parsed.
func ReadOnlyCommand(cmd string) bool {
	if strings.Contains(harmlessRedir.ReplaceAllString(cmd, ""), ">") {
		return false
	}
	seen := false
	for _, seg := range segmentSplit.Split(cmd, -1) {
		fields := strings.Fields(seg)
		for len(fields) > 0 && envAssign.MatchString(fields[0]) {
			fields = fields[1:]
		}
		if len(fields) == 0 {
			continue
		}
		name := strings.ToLower(filepath.Base(strings.ReplaceAll(fields[0], `\`, "/")))
		name = strings.TrimSuffix(name, ".exe")
		switch {
		case name == "cd" || name == "set-location" || name == "pushd" || name == "popd" || name == "sl":
			continue
		case name == "sed":
			if !strings.Contains(seg, " -n") || strings.Contains(seg, " -i") {
				return false
			}
		case readOnlySub[name] != nil:
			sub := ""
			if len(fields) > 1 {
				sub = strings.ToLower(fields[1])
			}
			if !readOnlySub[name][sub] || writeFlags.MatchString(seg) {
				return false
			}
			// `git branch x` / `git remote add …` write; only flag-only forms list.
			if name == "git" && (sub == "branch" || sub == "remote") {
				for _, a := range fields[2:] {
					if !strings.HasPrefix(a, "-") {
						return false
					}
				}
			}
		case !readOnlyCommands[name]:
			return false
		}
		seen = true
	}
	return seen
}
