// Package redact removes secrets from anything the sensor sends or spools.
//
// It runs on the developer's machine before an event is written to the spool,
// so a secret never reaches disk in ~/.agentwatch or the network. It is
// pattern-based: it catches the formats listed below, not every secret that
// could exist. PRIVACY.md says so.
package redact

import (
	"github.com/chinmayNayar/agentwatch-plugins/internal/lazyre"
	"strings"
)

type rule struct {
	kind string
	re   *lazyre.RE
	// keep is the number of leading capture groups to keep verbatim; the rest
	// of the match is replaced. 0 = replace the whole match.
	keep int
}

var rules = []rule{
	// Private keys first: they contain base64 that other rules would split.
	{"private_key", lazyre.New(`-----BEGIN [A-Z0-9 ]*PRIVATE KEY-----(?s:.*?)(?:-----END [A-Z0-9 ]*PRIVATE KEY-----|$)`), 0},
	// Credentials inside URLs: scheme://user:password@host.
	{"url_password", lazyre.New(`(?i)\b([a-z][a-z0-9+.\-]*://[^\s:/@'"]*:)[^\s@'"]+(@)`), 1},
	{"agentwatch_key", lazyre.New(`\baw(?:gk|dv|s)?_[A-Za-z0-9_\-]{20,}`), 0},
	{"anthropic_key", lazyre.New(`\bsk-ant-[A-Za-z0-9_\-]{20,}`), 0},
	{"openai_key", lazyre.New(`\bsk-(?:proj-|svcacct-|admin-)?[A-Za-z0-9_\-]{20,}`), 0},
	{"stripe_key", lazyre.New(`\b(?:sk|rk|pk)_(?:live|test)_[A-Za-z0-9]{16,}`), 0},
	{"aws_access_key", lazyre.New(`\b(?:AKIA|ASIA|AGPA|AIDA|AROA)[0-9A-Z]{16}\b`), 0},
	{"github_token", lazyre.New(`\b(?:gh[pousr]_[A-Za-z0-9]{30,}|github_pat_[A-Za-z0-9_]{40,})`), 0},
	{"slack_token", lazyre.New(`\bxox[abposr]-[A-Za-z0-9\-]{10,}`), 0},
	{"google_api_key", lazyre.New(`\bAIza[0-9A-Za-z_\-]{35}`), 0},
	{"jwt", lazyre.New(`\beyJ[A-Za-z0-9_\-]{8,}\.[A-Za-z0-9_\-]{8,}\.[A-Za-z0-9_\-]{8,}`), 0},
	{"npm_token", lazyre.New(`\bnpm_[A-Za-z0-9]{36}\b`), 0},
	// Headers.
	{"auth_header", lazyre.New(`(?i)(\bauthorization\s*[:=]\s*(?:bearer\s+|basic\s+|token\s+)?)[^\s'"]+`), 1},
	{"api_key_header", lazyre.New(`(?i)(\bx-api-key\s*[:=]\s*)[^\s'"]+`), 1},
	// KEY=value / KEY: value where the key names a secret.
	{"secret_assignment", lazyre.New(`(?i)(\b[A-Z0-9_]*(?:SECRET|TOKEN|PASSWORD|PASSWD|PWD|API_?KEY|PRIVATE_?KEY|ACCESS_?KEY|CREDENTIALS?|DATABASE_URL|REDIS_URL|MONGO(?:DB)?_URI|DSN|CONN(?:ECTION)?_STRING)[A-Z0-9_]*\s*[=:]\s*)("[^"]*"|'[^']*'|[^\s'"&;|]+)`), 1},
	// CLI flags: --password x, --token=x, -p x is too ambiguous and is left alone.
	{"secret_flag", lazyre.New(`(?i)(--(?:password|passwd|token|secret|api-key|apikey|auth-token|access-token|client-secret)(?:=|\s+))("[^"]*"|'[^']*'|[^\s'"]+)`), 1},
}

// envAssign matches every KEY=value pair; applied only to text that touches a
// .env file, where every value is a secret until proven otherwise.
var envAssign = lazyre.New(`(\b[A-Z][A-Z0-9_]*=)("[^"]*"|'[^']*'|[^\s'"&;|]+)`)
var envFile = lazyre.New(`(?i)(^|[\s/'"=])\.env(\.[a-z0-9_\-]+)?\b`)

// Placeholder returned for a redacted value.
func Placeholder(kind string) string { return "[REDACTED:" + kind + "]" }

// String redacts every recognised secret in s.
func String(s string) string {
	if s == "" {
		return s
	}
	out := s
	for _, r := range rules {
		out = r.re.ReplaceAllStringFunc(out, func(m string) string {
			if r.keep == 0 {
				return Placeholder(r.kind)
			}
			sub := r.re.FindStringSubmatch(m)
			prefix := strings.Join(sub[1:1+r.keep], "")
			suffix := ""
			if r.kind == "url_password" && len(sub) > 2 {
				prefix, suffix = sub[1], sub[2]
			}
			return prefix + Placeholder(r.kind) + suffix
		})
	}
	if envFile.MatchString(out) {
		out = envAssign.ReplaceAllString(out, "${1}"+Placeholder("env_value"))
	}
	return out
}

var sensitiveKey = lazyre.New(`(?i)(secret|token|password|passwd|api_?key|apikey|private_?key|access_?key|credential|authorization|cookie|session_?key|client_?secret)`)

// SensitiveKey reports whether a map key names a secret by itself.
func SensitiveKey(k string) bool { return sensitiveKey.MatchString(k) }

// Value redacts a decoded JSON value: strings are scanned, values under a
// secret-named key are replaced whole, and depth/size are bounded.
func Value(v any) any { return value("", v, 0) }

func value(key string, v any, depth int) any {
	if depth > 8 {
		return "[depth]"
	}
	switch t := v.(type) {
	case map[string]any:
		out := make(map[string]any, len(t))
		n := 0
		for k, x := range t {
			if n >= 200 {
				break
			}
			n++
			out[k] = value(k, x, depth+1)
		}
		return out
	case []any:
		lim := len(t)
		if lim > 200 {
			lim = 200
		}
		out := make([]any, lim)
		for i := 0; i < lim; i++ {
			out[i] = value(key, t[i], depth+1)
		}
		return out
	case string:
		if key != "" && SensitiveKey(key) && t != "" {
			return Placeholder("field")
		}
		return String(t)
	default:
		return v
	}
}

// Truncate bounds a string for transport, marking the cut.
func Truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	// Do not cut inside a UTF-8 sequence.
	cut := n
	for cut > 0 && !utf8Start(s[cut]) {
		cut--
	}
	return s[:cut] + "…[truncated]"
}

func utf8Start(b byte) bool { return b&0xC0 != 0x80 }
