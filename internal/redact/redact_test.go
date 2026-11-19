package redact

import (
	"strings"
	"testing"
)

// Every secret below is assembled at run time from obviously fake parts, so
// the repository never contains a secret-shaped literal (gitleaks stays clean).
func fake(prefix string, c byte, n int) string { return prefix + strings.Repeat(string(c), n) }

var (
	awKey     = fake("aw_", '0', 32)
	gkKey     = fake("awgk_", 'A', 24)
	dvKey     = fake("awdv_", '0', 48)
	oaKey     = fake("sk-proj-", 'A', 28)
	antKey    = fake("sk-ant-api03-", 'B', 24)
	awsID     = fake("AKIA", 'Z', 16)
	ghPat     = fake("ghp_", 'C', 36)
	ghFine    = fake("github_pat_", 'D', 60)
	slackTok  = fake("xoxb-", '1', 20)
	stripeKey = fake("sk_"+"live_", '9', 24)
	googleKey = fake("AI"+"za", 'E', 35)
	jwt       = fake("ey"+"J", 'F', 20) + "." + fake("ey"+"J", 'G', 20) + "." + fake("", 'H', 30)
	bearer    = fake("tok", 'x', 12)
	pgURL     = "postgres" + "://app:" + fake("pw", 'q', 10) + "@db.internal:5432/app"
	redisURL  = "redis" + "://:" + fake("pw", 'r', 8) + "@cache:6379/0"
	dbURLVar  = "DATABASE" + "_URL"
	dbURL     = "postgresql" + "://u:" + fake("pw", 's', 8) + "@h/d"
)

var cases = []struct {
	name, in, secret, keep string
}{
	{"agentwatch key", "curl -H 'x-api-key: " + awKey + "' api", awKey, "curl"},
	{"gateway key", "export K=" + gkKey, gkKey, "export"},
	{"device token", dvKey, dvKey, ""},
	{"openai", "OPENAI=" + oaKey + " python x.py", oaKey, "python x.py"},
	{"anthropic", "key " + antKey, antKey, "key"},
	{"aws access key", "aws s3 ls --profile x " + awsID, awsID, "aws s3 ls"},
	{"aws secret assignment", "AWS_SECRET_ACCESS_KEY=wJalrXUtnFEMI/K7MDENG/bPxRfiCYEXAMPLEKEY make", "wJalrXUtnFEMI/K7MDENG/bPxRfiCYEXAMPLEKEY", "make"},
	{"github pat", "git clone https://" + ghPat + "@github.com/o/r", ghPat, "git clone"},
	{"github fine-grained", ghFine, ghFine, ""},
	{"slack", "post " + slackTok, slackTok, "post"},
	{"stripe", "stripe " + stripeKey, stripeKey, "stripe"},
	{"google", googleKey, googleKey, ""},
	{"jwt", "Bearer " + jwt, jwt, ""},
	{"postgres url", "psql " + pgURL, fake("pw", 'q', 10), "postgres://app:"},
	{"redis url", "REDIS=" + redisURL, fake("pw", 'r', 8), "redis://"},
	{"database_url", dbURLVar + `="` + dbURL + `" alembic upgrade`, fake("pw", 's', 8), "alembic upgrade"},
	{"auth header", `curl -H "Authorization: Bearer ` + bearer + `"`, bearer, "curl -H"},
	{"password flag", "mysql --password=Tr0ub4dor&3 -u root", "Tr0ub4dor", "mysql"},
	{"token flag spaced", "gh auth login --token zzzzzzzzzzzz", "zzzzzzzzzzzz", "gh auth login"},
	{"private key", "echo '-----BEGIN OPENSSH PRIVATE KEY-----\nb3BlbnNzaC1rZXktdjEAAAAABG5vbmU\n-----END OPENSSH PRIVATE KEY-----' > k", "b3BlbnNzaC1rZXktdjEAAAAABG5vbmU", "echo"},
	{"dotenv write", `echo "STRIPE_WEBHOOK=whsec_abc123 PORT=8080" >> .env`, "whsec_abc123", ">> .env"},
	{"dotenv any value", "cat > .env.local <<< 'FOO=bar123baz'", "bar123baz", ".env.local"},
}

func TestRedactsEveryFormat(t *testing.T) {
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := String(c.in)
			if strings.Contains(got, c.secret) {
				t.Fatalf("secret survived:\n in: %s\nout: %s", c.in, got)
			}
			if !strings.Contains(got, "[REDACTED:") {
				t.Fatalf("no redaction marker: %s", got)
			}
			if c.keep != "" && !strings.Contains(got, c.keep) {
				t.Fatalf("lost non-secret context %q: %s", c.keep, got)
			}
		})
	}
}

func TestLeavesOrdinaryCommandsAlone(t *testing.T) {
	for _, s := range []string{
		"git commit -m 'fix token parsing in lexer'",
		"go test ./... -run TestSecretRotation",
		"ls -la ~/.ssh",
		"npm install --save-dev typescript",
		`git config user.email "dev@example.com"`,
		"grep -rn password_reset src/",
		"cat README.md",
		"export PATH=$PATH:/usr/local/go/bin",
	} {
		if got := String(s); got != s {
			t.Errorf("changed an ordinary command:\n in: %s\nout: %s", s, got)
		}
	}
}

func TestValueRedactsSecretKeysAndNestedStrings(t *testing.T) {
	in := map[string]any{
		"command": "deploy --token abcdefabcdef",
		"env":     map[string]any{"API_KEY": "plain-looking", "PORT": "8080"},
		"headers": []any{"Authorization: Bearer xyzxyzxyzxyz"},
		"count":   3.0,
	}
	out := Value(in).(map[string]any)
	if s := out["command"].(string); strings.Contains(s, "abcdefabcdef") {
		t.Fatal(s)
	}
	env := out["env"].(map[string]any)
	if env["API_KEY"] != Placeholder("field") || env["PORT"] != "8080" {
		t.Fatalf("env: %#v", env)
	}
	if s := out["headers"].([]any)[0].(string); strings.Contains(s, "xyzxyzxyzxyz") {
		t.Fatal(s)
	}
	if out["count"] != 3.0 {
		t.Fatal("numbers must pass through")
	}
}

func TestValueIsBounded(t *testing.T) {
	var deep any = "x"
	for i := 0; i < 20; i++ {
		deep = map[string]any{"a": deep}
	}
	got := Value(deep)
	for i := 0; i < 9; i++ {
		got = got.(map[string]any)["a"]
	}
	if got != "[depth]" {
		t.Fatalf("depth not bounded: %#v", got)
	}
	big := make([]any, 500)
	if n := len(Value(big).([]any)); n != 200 {
		t.Fatalf("list not bounded: %d", n)
	}
}

func TestTruncateKeepsUTF8Whole(t *testing.T) {
	s := strings.Repeat("é", 10) // 2 bytes each
	got := Truncate(s, 5)
	if !strings.HasSuffix(got, "…[truncated]") || strings.ContainsRune(strings.TrimSuffix(got, "…[truncated]"), '�') {
		t.Fatalf("bad truncate: %q", got)
	}
}
