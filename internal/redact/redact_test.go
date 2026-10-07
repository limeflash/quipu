package redact

import (
	"strings"
	"testing"
)

// Assembled at runtime so this file contains no string that looks like a live
// credential to a secret scanner.
func s(parts ...string) string { return strings.Join(parts, "") }

func TestRedactsSecrets(t *testing.T) {
	cases := []struct{ name, in string }{
		{"anthropic key", s("sk-", "ant-", "api03-", strings.Repeat("A", 24))},
		{"openai key", s("sk-", strings.Repeat("B", 32))},
		{"github pat", s("ghp_", strings.Repeat("C", 36))},
		{"github fine-grained", s("github_pat_", strings.Repeat("D", 30))},
		{"slack token", s("xoxb-", "123456789012-", strings.Repeat("E", 24))},
		{"aws access key", "AKIAIOSFODNN7EXAMPLE"},
		{"google api key", s("AIza", strings.Repeat("F", 35))},
		{"stripe live key", s("sk_live_", strings.Repeat("G", 24))},
		{"npm token", s("npm_", strings.Repeat("H", 36))},
		{"jwt", s("eyJhbGciOiJIUzI1NiJ9", ".", "eyJzdWIiOiIxMjM0NTY3ODkwIn0", ".", strings.Repeat("I", 20))},
		{"dotenv line", "API_KEY=abc123def456ghi789"},
		{"dotenv quoted", `DATABASE_PASSWORD="hunter2hunter2"`},
		{"json settings", `"CLAUDE_MEM_OPENROUTER_API_KEY": "7d104ceeaa11bb22cc33dd44ee55ff66"`},
		{"shell export", "export GITHUB_TOKEN=zzzzzzzzzzzzzzzzzzzz"},
		{"yaml secret", "client_secret: s3cr3tv4lu3here"},
		{"url credentials", "postgres://admin:s3cr3tpass@db.example.com:5432/app"},
		{"auth header in text", "Authorization: Bearer abcdefghijklmnopqrstuvwxyz"},
		{"pem block", "-----BEGIN RSA PRIVATE KEY-----\nMIIEpQIBAAKCAQEA\n-----END RSA PRIVATE KEY-----"},
		{"sql password", "CREATE USER app WITH PASSWORD 'Tr0ub4dor3xyz'"},
		{"password prose en", "the password is hunter2hunter"},
		{"password prose ru", "пароль: mySup3rP4ss"},
		{"cli quoted", "mysql -u root -p'Sup3rS3cret' app"},
		{"cli long", "tool --password=Sup3rS3cret"},
		{"curl basic", "curl -u admin:Sup3rS3cret https://x"},
		{"xml", "<password>Xm1P4ssw0rd99</password>"},
		{"seed 12 words", "abandon ability able about above absent absorb abstract absurd abuse access accident"},
		{"seed 24 words", "legal winner thank year wave sausage worth useful legal winner thank year " +
			"wave sausage worth useful legal winner thank year wave sausage worth title"},
		{"seed numbered", "1. abandon 2. ability 3. able 4. about 5. above 6. absent " +
			"7. absorb 8. abstract 9. absurd 10. abuse 11. access 12. accident"},
		{"seed newline separated", "abandon\nability\nable\nabout\nabove\nabsent\nabsorb\nabstract\nabsurd\nabuse\naccess\naccident"},
		{"seed labelled", "mnemonic: abandon ability able about above absent absorb abstract absurd abuse access accident"},
	}
	for _, c := range cases {
		out, hits := Text(c.in)
		if out == c.in || len(hits) == 0 {
			t.Errorf("%s: not redacted: %q", c.name, out)
		}
	}
}

// The half that matters more: redacting a git SHA or a path quietly poisons
// every memory.
func TestLeavesNonSecretsAlone(t *testing.T) {
	cases := []struct{ name, in string }{
		{"git sha", "commit 4653136a1b2c3d4e5f60718293a4b5c6d7e8f900 landed"},
		{"short sha", "see a811ff21 for the fix"},
		{"uuid", "session a811ff21-ea95-4681-82d1-382ae7336200 started"},
		{"file path", "read src/store/store.c and pipeline.c"},
		{"token null", "token: null"},
		{"secret false", "has_secret: false"},
		{"prose", "the secret sauce here is the call graph, not the parser"},
		{"empty key", `"CLAUDE_MEM_OPENROUTER_API_KEY": ""`},
		{"version", "claude-mem v13.15.0 with bun 1.3.14"},
		{"key name only", "set CLAUDE_MEM_OPENROUTER_API_KEY in settings.json"},
		{"url under token name", "token_url: https://ollama.com/settings/keys"},
		{"hex digest", "content_hash = 32c3a715ffbad3a1"},
		{"port and numbers", "listening on 127.0.0.1:11435 upstream ollama.com"},
		{"docker port", "docker run -p 8080:8080 image"},
		{"password required", "the password is required for this step"},
		{"password no separator", "password required"},
		{"password incorrect", "password: incorrect"},
		{"prose with bip39 words", "the client can access the network and absorb the response before the " +
			"display can update, which is absurd but useful when the actual index is ready"},
		{"short bip39 run", "able about above absent absorb"},
		{"code identifiers", `const access = require("./access"); return access.ready && index.valid`},
		{"long technical prose", "The indexing pipeline runs in memory, releases the buffer after the write, " +
			"and reports partial parses so the caller can fall back to text search when " +
			"a file was only partially understood by the parser during the second pass"},
	}
	for _, c := range cases {
		if out, _ := Text(c.in); out != c.in {
			t.Errorf("%s: changed to %q", c.name, out)
		}
	}
}

func TestNoNestedPlaceholders(t *testing.T) {
	for _, in := range []string{
		"seed: abandon ability able about above absent absorb abstract absurd abuse access accident",
		"DATABASE_URL=postgres://appuser:Tr0ub4dor3xyz@db.internal:5432/app",
		"the password is Wr1ttenOutLoud99 and API_KEY=abc123def456",
		"<password>Xm1P4ssw0rd99</password> token=zzzzzzzzzzzz",
	} {
		out, _ := Text(in)
		if strings.Contains(out, "\x00") || strings.Contains(out, "[SECRET:[") {
			t.Errorf("nested or unthawed placeholder: %q", out)
		}
		if strings.Contains(out, "Tr0ub4dor3xyz") || strings.Contains(out, "Wr1ttenOutLoud99") || strings.Contains(out, "Xm1P4ssw0rd99") {
			t.Errorf("secret survived: %q", out)
		}
	}
}

func TestPlaceholderTemplate(t *testing.T) {
	defer func(old string) { Template = old }(Template)
	in := "AWS_SECRET_ACCESS_KEY=abcd1234efgh5678"
	for _, c := range []struct{ tpl, want string }{
		{"[SECRET:{type}]", "[SECRET:assigned-secret]"},
		{"******", "******"},
	} {
		Template = c.tpl
		if out := String(in); !strings.Contains(out, c.want) || strings.Contains(out, "abcd1234") {
			t.Errorf("template %q: got %q", c.tpl, out)
		}
	}
}

func TestQuotesKeptAroundValue(t *testing.T) {
	out := String(`DATABASE_PASSWORD="hunter2hunter2" next`)
	if out != `DATABASE_PASSWORD="[SECRET:assigned-secret]" next` {
		t.Errorf("got %q", out)
	}
	out = String("пароль: mySup3rP4ss")
	if out != "пароль: [SECRET:password-prose]" {
		t.Errorf("got %q", out)
	}
}
