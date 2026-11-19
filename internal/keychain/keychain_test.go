package keychain

import (
	"os"
	"testing"
)

// Touches the real OS credential store, so it only runs when asked
// (CI sets AGENTWATCH_TEST_KEYCHAIN=1 on macOS and Windows runners).
func TestRoundTrip(t *testing.T) {
	if os.Getenv("AGENTWATCH_TEST_KEYCHAIN") != "1" {
		t.Skip("set AGENTWATCH_TEST_KEYCHAIN=1 to use the real credential store")
	}
	svc, acct := "agentwatch-test", "roundtrip"
	secret := `awdv_x'y"z$(rm)` + "\n tail"
	defer Delete(svc, acct)
	if err := Set(svc, acct, secret); err == ErrUnavailable {
		t.Skip("no credential store on this machine")
	} else if err != nil {
		t.Fatal(err)
	}
	got, err := Get(svc, acct)
	if err != nil || got != secret {
		t.Fatalf("got %q, %v", got, err)
	}
	_ = Delete(svc, acct)
	if _, err := Get(svc, acct); err == nil {
		t.Fatal("not deleted")
	}
}
