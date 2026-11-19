//go:build !windows

package keychain

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/hex"
	"os/exec"
	"runtime"
	"strings"
	"time"
)

func run(stdin string, name string, args ...string) (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, name, args...)
	cmd.Stdin = strings.NewReader(stdin)
	var out, errb bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &errb
	err := cmd.Run()
	return out.String(), err
}

func have(name string) bool { _, err := exec.LookPath(name); return err == nil }

// quote for `security -i`, which tokenises its stdin like a shell.
func q(s string) string { return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'" }

// Secrets are stored base64-encoded: `security -w` prints a value holding
// non-printable bytes as hex, so the round trip is only exact for plain ASCII.
func enc(s string) string { return base64.StdEncoding.EncodeToString([]byte(s)) }

func dec(s string) (string, error) {
	b, err := base64.StdEncoding.DecodeString(strings.TrimSpace(s))
	if err != nil {
		return "", ErrNotFound
	}
	return string(b), nil
}

// Set stores secret under service/account.
func Set(service, account, secret string) error {
	secret = enc(secret)
	switch runtime.GOOS {
	case "darwin":
		if !have("security") {
			return ErrUnavailable
		}
		// -X takes the password hex-encoded, so no quoting issue can leak it.
		cmd := "add-generic-password -U -s " + q(service) + " -a " + q(account) + " -X " + hex.EncodeToString([]byte(secret)) + "\n"
		_, err := run(cmd, "security", "-i")
		if err != nil {
			return ErrUnavailable
		}
		return nil
	default:
		if !have("secret-tool") {
			return ErrUnavailable
		}
		_, err := run(secret, "secret-tool", "store", "--label=AgentWatch sensor", "service", service, "account", account)
		if err != nil {
			return ErrUnavailable
		}
		return nil
	}
}

// Get reads a secret.
func Get(service, account string) (string, error) {
	switch runtime.GOOS {
	case "darwin":
		if !have("security") {
			return "", ErrUnavailable
		}
		out, err := run("", "security", "find-generic-password", "-s", service, "-a", account, "-w")
		if err != nil {
			return "", ErrNotFound
		}
		return dec(out)
	default:
		if !have("secret-tool") {
			return "", ErrUnavailable
		}
		out, err := run("", "secret-tool", "lookup", "service", service, "account", account)
		if err != nil || out == "" {
			return "", ErrNotFound
		}
		return dec(out)
	}
}

// Delete removes a secret (no error when absent).
func Delete(service, account string) error {
	switch runtime.GOOS {
	case "darwin":
		if have("security") {
			_, _ = run("", "security", "delete-generic-password", "-s", service, "-a", account)
		}
	default:
		if have("secret-tool") {
			_, _ = run("", "secret-tool", "clear", "service", service, "account", account)
		}
	}
	return nil
}
