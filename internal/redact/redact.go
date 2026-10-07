// Package redact removes secrets from text before it is stored or sent to a
// model. Ported from limeflash/claude-mem-ollama-proxy (redact.js, MIT).
//
// Design rule: a false positive costs more than it looks. Over-redacting turns
// memories into noise, so patterns are specific. Bare hex strings (git SHAs)
// and UUIDs are never touched.
//
// Go's RE2 has no backreferences or lookahead, so the rules that need a
// matching closing quote or tag check it in their replace function instead.
package redact

import (
	"os"
	"regexp"
	"strconv"
	"strings"
)

// Template is what a secret is replaced with; {type} expands to the rule that
// matched, so a reader still knows an AWS key stood there.
var Template = envOr("ENGRAM_REDACT_PLACEHOLDER", "[SECRET:{type}]")

func envOr(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

func placeholder(rule string) string { return strings.ReplaceAll(Template, "{type}", rule) }

// Values that match a secret-shaped pattern but are plainly not secrets.
var notSecret = map[string]bool{
	"true": true, "false": true, "null": true, "undefined": true, "none": true, "nil": true,
	"empty": true, "nothing": true, "changeme": true, "change_me": true, "your_api_key_here": true,
	"yourkeyhere": true, "xxx": true, "xxxx": true, "placeholder": true, "example": true,
	"redacted": true, "[redacted]": true, "todo": true, "fixme": true,
	// words that follow "password is …" in ordinary sentences
	"required": true, "correct": true, "incorrect": true, "wrong": true, "invalid": true,
	"missing": true, "hidden": true, "needed": true, "changed": true, "reset": true,
	"protected": true, "expired": true, "strong": true, "weak": true, "stored": true,
	"hashed": true, "blank": true, "unknown": true, "above": true, "below": true, "here": true,
	"optional": true, "disabled": true, "enabled": true, "rotated": true, "revoked": true,
	"valid": true, "unset": true,
	"нужен": true, "неверный": true, "верный": true, "пустой": true, "изменён": true,
	"изменен": true, "сброшен": true, "обязателен": true, "скрыт": true, "указан": true,
	"отсутствует": true,
}

// inProse reports a plain word followed by more words on the same line:
// "Password: encrypted by the store", "secrets: password_hash, totp_seed".
// A config value ends its line, so .env and YAML keep being redacted.
// ponytail: an unquoted all-letter password inside a sentence ("password is
// swordfish and ...") now passes; quoted or with a digit it does not.
func inProse(value, after string) bool {
	return plainWordRe.MatchString(value) && proseNextRe.MatchString(after)
}

func looksSecret(value string) bool {
	v := strings.ToLower(value)
	if notSecret[v] || strings.ContainsRune(v, 0) {
		return false
	}
	// "--surface-bg": a CSS custom property or a CLI flag, never a value.
	return !strings.HasPrefix(v, "[secret") && !strings.HasPrefix(v, "[redacted") && !strings.HasPrefix(v, "******") &&
		!strings.HasPrefix(v, "--")
}

// Key names whose value is a secret regardless of shape. Deliberately narrow.
const secretKey = `(?:api[_-]?keys?|apikey|secret|token|password|passwd|pwd|access[_-]?key|private[_-]?key|credentials?|auth[_-]?token|bearer|mnemonic|seed[_-]?phrase|passphrase)`

const quotes = "\"'`"

type rule struct {
	id string
	re *regexp.Regexp
	// replace returns the replacement for one match; g[0] is the whole match
	// and after is the text that follows it. Returning g[0] means "not a
	// secret, leave it". nil replaces with the mark.
	replace func(mark string, g []string, after string) string
}

var rules = []rule{
	// Passwords written out in prose: "password is hunter2", "пароль: hunter2".
	// The separator may be plain whitespace only when a quote follows, so SQL's
	// `WITH PASSWORD 'secret'` is caught but "password required" is not.
	{
		id: "password-prose",
		re: regexp.MustCompile(`(?i)(?:^|[^\p{L}\d_])(пароль|пасс|password|passphrase|мнемоника)(\s*(?:is|—|–|-|:|=)+\s*|\s+)([` + quotes + `]?)([^\s` + quotes + `,;]{6,})([` + quotes + `]?)`),
		replace: func(mark string, g []string, after string) string {
			word, sep, open, value, closing := g[1], g[2], g[3], g[4], g[5]
			if strings.TrimSpace(sep) == "" && open == "" {
				return g[0]
			}
			if open != "" && closing != open {
				return g[0]
			}
			if !looksSecret(value) || open == "" && inProse(value, after) {
				return g[0]
			}
			lead := g[0][:strings.Index(g[0], word)]
			tail := ""
			if open == "" {
				tail = closing
			}
			return lead + word + ": " + open + mark + open + tail
		},
	},

	// Provider-specific, effectively zero false positives.
	{id: "anthropic-key", re: regexp.MustCompile(`\bsk-ant-[A-Za-z0-9_-]{16,}`)},
	// sk-ant- and sk-or-v1- cannot match here: a '-' follows within three chars.
	{id: "openai-key", re: regexp.MustCompile(`\bsk-[A-Za-z0-9]{20,}`)},
	{id: "github-token", re: regexp.MustCompile(`\b(?:gh[pousr]_[A-Za-z0-9]{16,}|github_pat_[A-Za-z0-9_]{20,})`)},
	{id: "slack-token", re: regexp.MustCompile(`\bxox[baprs]-[A-Za-z0-9-]{10,}`)},
	{id: "aws-access-key", re: regexp.MustCompile(`\b(?:AKIA|ASIA|ABIA|ACCA)[0-9A-Z]{16}\b`)},
	{id: "google-api-key", re: regexp.MustCompile(`\bAIza[0-9A-Za-z_-]{35}\b`)},
	{id: "stripe-key", re: regexp.MustCompile(`\b(?:sk|rk|pk)_(?:live|test)_[A-Za-z0-9]{16,}`)},
	{id: "npm-token", re: regexp.MustCompile(`\bnpm_[A-Za-z0-9]{36}\b`)},
	{id: "openrouter-key", re: regexp.MustCompile(`\bsk-or-v1-[A-Za-z0-9]{16,}`)},
	{id: "jwt", re: regexp.MustCompile(`\beyJ[A-Za-z0-9_-]{10,}\.eyJ[A-Za-z0-9_-]{10,}\.[A-Za-z0-9_-]{8,}`)},

	{id: "private-key-block", re: regexp.MustCompile(`(?s)-----BEGIN (?:[A-Z]+ )?PRIVATE KEY-----.*?-----END (?:[A-Z]+ )?PRIVATE KEY-----`)},

	// scheme://user:pass@host — the user may be empty (redis://:pass@host).
	{
		id: "url-credentials",
		re: regexp.MustCompile(`(?i)\b([a-z][a-z0-9+.-]*://)([^\s:/@]{0,64}):([^\s:/@]{1,256})@`),
		replace: func(mark string, g []string, after string) string {
			return g[1] + g[2] + ":" + mark + "@"
		},
	},

	// Quoted CLI credential flags: mysql -p'secret', --password="secret". The
	// short flag needs a quote so `docker run -p 8080:8080` is left alone.
	{
		id: "cli-password",
		re: regexp.MustCompile(`(--password[=\s]|--passwd[=\s]|--pass[=\s]|-p)(["'])([^"'\n]{4,})(["'])`),
		replace: func(mark string, g []string, after string) string {
			if g[2] != g[4] || !looksSecret(g[3]) {
				return g[0]
			}
			return g[1] + g[2] + mark + g[2]
		},
	},
	// Unquoted long form only: --password=secret
	{
		id: "cli-password",
		re: regexp.MustCompile(`(--password=|--passwd=|--pass=)([^\s"'\n]{6,})`),
		replace: func(mark string, g []string, after string) string {
			if !looksSecret(g[2]) {
				return g[0]
			}
			return g[1] + mark
		},
	},
	// curl -u user:pass / --user user:pass
	{
		id: "cli-basic-auth",
		re: regexp.MustCompile(`(\s(?:-u|--user)\s+)([A-Za-z0-9_.@-]{1,64}):([^\s'"]{4,})`),
		replace: func(mark string, g []string, after string) string {
			if !looksSecret(g[3]) {
				return g[0]
			}
			return g[1] + g[2] + ":" + mark
		},
	},

	{
		id: "xml-secret",
		re: regexp.MustCompile(`(?i)<(password|passwd|secret|token|apikey|api-key|credential)>([^<\n]{4,})</(password|passwd|secret|token|apikey|api-key|credential)>`),
		replace: func(mark string, g []string, after string) string {
			if !strings.EqualFold(g[1], g[3]) || !looksSecret(g[2]) {
				return g[0]
			}
			return "<" + g[1] + ">" + mark + "</" + g[3] + ">"
		},
	},

	{
		id:      "auth-header",
		re:      regexp.MustCompile(`(?i)\b(Authorization\s*:\s*(?:Bearer|Basic|Token)\s+)([A-Za-z0-9+/=._~-]{12,})`),
		replace: func(mark string, g []string, _ string) string { return g[1] + mark },
	},

	// Generic KEY = VALUE: .env lines, JSON settings, shell exports, YAML.
	{
		id: "assigned-secret",
		re: regexp.MustCompile(`(?i)([A-Za-z0-9_.-]*` + secretKey + `[A-Za-z0-9_.-]*)(["']?\s*[:=]\s*)(["']?)([^\s"',;}\])]{8,})(["']?)`),
		replace: func(mark string, g []string, after string) string {
			key, sep, open, value, closing := g[1], g[2], g[3], g[4], g[5]
			if open != "" && closing != open {
				return g[0]
			}
			if !looksSecret(value) || open == "" && inProse(value, after) {
				return g[0]
			}
			// A path or URL under a "token"-ish name is usually a location.
			if locationRe.MatchString(value) && !secretQueryRe.MatchString(value) {
				return g[0]
			}
			tail := ""
			if open == "" {
				tail = closing
			}
			return key + sep + open + mark + open + tail
		},
	},
}

var (
	locationRe    = regexp.MustCompile(`^(?:https?://|\.{0,2}/)`)
	plainWordRe   = regexp.MustCompile(`^\p{L}+(?:[-_.']\p{L}+)*[.:!?]?$`)
	proseNextRe   = regexp.MustCompile(`^[,;)]?[ \t]+[\p{L}(]`)
	existingRe    = regexp.MustCompile(`\[(?:SECRET|REDACTED)(?::[A-Za-z0-9_-]+)?\]`)
	secretQueryRe = regexp.MustCompile(`(?i)[?&](?:token|key|secret)=`)
	thawRe        = regexp.MustCompile("\x00(\\d+)\x00")
	wordRe        = regexp.MustCompile(`[A-Za-z]+`)
	seedGapRe     = regexp.MustCompile(`^[\s,;.\d)\-\]|"']*$`)
)

// replaceAll is ReplaceAllStringFunc with access to submatches.
func replaceAll(re *regexp.Regexp, s string, f func(g []string, after string) string) (string, int) {
	idx := re.FindAllStringSubmatchIndex(s, -1)
	if idx == nil {
		return s, 0
	}
	var b strings.Builder
	last, n := 0, 0
	for _, m := range idx {
		g := make([]string, len(m)/2)
		for i := range g {
			if m[2*i] >= 0 {
				g[i] = s[m[2*i]:m[2*i+1]]
			}
		}
		r := f(g, s[m[1]:min(len(s), m[1]+16)])
		if r != g[0] {
			n++
		}
		b.WriteString(s[last:m[0]])
		b.WriteString(r)
		last = m[1]
	}
	b.WriteString(s[last:])
	return b.String(), n
}

// A mnemonic is a run of dictionary words, so it is found by tokenising, not
// by a regex. Twelve consecutive BIP-39 words do not occur in ordinary text.
const seedMinWords = 12

func redactSeedPhrases(text, mark string) (string, int) {
	toks := wordRe.FindAllStringIndex(text, -1)
	type span struct{ s, e int }
	var runs []span
	isWord := func(i int) bool {
		_, ok := bip39[strings.ToLower(text[toks[i][0]:toks[i][1]])]
		return ok
	}
	for i := 0; i < len(toks); {
		if !isWord(i) {
			i++
			continue
		}
		j := i
		for j+1 < len(toks) && isWord(j+1) {
			gap := text[toks[j][1]:toks[j+1][0]]
			if len(gap) > 8 || !seedGapRe.MatchString(gap) {
				break
			}
			j++
		}
		if j-i+1 >= seedMinWords {
			runs = append(runs, span{toks[i][0], toks[j][1]})
		}
		i = j + 1
	}
	for k := len(runs) - 1; k >= 0; k-- {
		text = text[:runs[k].s] + mark + text[runs[k].e:]
	}
	return text, len(runs)
}

// Text redacts one string and returns it with a per-rule hit count.
//
// Placeholders are frozen into NUL-delimited tokens as they are inserted and
// thawed at the end; otherwise a later rule reads "[SECRET:seed-phrase]" as
// key SECRET, value seed-phrase, and nests it.
func Text(text string) (string, map[string]int) {
	hits := map[string]int{}
	if text == "" {
		return text, hits
	}
	var frozen []string
	freeze := func(s string) string {
		frozen = append(frozen, s)
		return "\x00" + strconv.Itoa(len(frozen)-1) + "\x00"
	}

	// Text redacted earlier (claude-mem, an agent) keeps its placeholders.
	out := existingRe.ReplaceAllStringFunc(text, freeze)
	out, n := redactSeedPhrases(out, freeze(placeholder("seed-phrase")))
	if n > 0 {
		hits["seed-phrase"] = n
	}
	for _, r := range rules {
		mark := freeze(placeholder(r.id))
		replace := r.replace
		var count int
		out, count = replaceAll(r.re, out, func(g []string, after string) string {
			if replace == nil {
				return mark
			}
			return replace(mark, g, after)
		})
		if count > 0 {
			hits[r.id] += count
		}
	}
	out = thawRe.ReplaceAllStringFunc(out, func(tok string) string {
		i, _ := strconv.Atoi(strings.Trim(tok, "\x00"))
		return frozen[i]
	})
	return out, hits
}

// String is Text without the hit counts.
func String(s string) string {
	out, _ := Text(s)
	return out
}
