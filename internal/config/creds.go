package config

import (
	"encoding/json"
	"errors"
	"os"
	"strings"

	"github.com/chinmayNayar/agentwatch-plugins/internal/keychain"
)

const keyringService = "agentwatch"

// Credential is what the sensor authenticates with.
type Credential struct {
	Token    string `json:"token"`
	Kind     string `json:"kind"` // device | api_key
	DeviceID string `json:"device_id,omitempty"`
	APIURL   string `json:"api_url,omitempty"`
	Source   string `json:"-"` // keychain | file | env | plugin option
}

// keyringDisabled lets tests (and machines with a broken Secret Service)
// force the file fallback.
func keyringDisabled() bool { return os.Getenv("AGENTWATCH_NO_KEYCHAIN") == "1" }

// LoadCredential finds a token: the plugin's sensitive option, env, keychain,
// then the 0600 file. It never logs or returns the token in an error.
func LoadCredential() (*Credential, error) {
	if v, n := firstEnv("CLAUDE_PLUGIN_OPTION_API_KEY", "AGENTWATCH_API_KEY", "AGENTWATCH_TOKEN"); v != "" {
		kind := "api_key"
		if strings.HasPrefix(v, "awdv_") {
			kind = "device"
		}
		src := "env"
		if n == "CLAUDE_PLUGIN_OPTION_API_KEY" {
			src = "plugin option"
		}
		return &Credential{Token: v, Kind: kind, Source: src}, nil
	}
	if !keyringDisabled() {
		if s, err := keychain.Get(keyringService, keyringUser()); err == nil && s != "" {
			var c Credential
			if json.Unmarshal([]byte(s), &c) == nil && c.Token != "" {
				c.Source = "keychain"
				return &c, nil
			}
		}
	}
	b, err := os.ReadFile(CredsPath())
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, ErrNoCredential
		}
		return nil, errors.New("credentials file unreadable")
	}
	var c Credential
	if json.Unmarshal(b, &c) != nil || c.Token == "" {
		return nil, errors.New("credentials file is corrupt; sign in again")
	}
	c.Source = "file"
	return &c, nil
}

// ErrNoCredential: this machine has not signed in. Callers that talk to a
// person add SignInHint(), which names a command that works on this machine.
var ErrNoCredential = errors.New("not signed in")

// SaveCredential stores in the OS keychain, falling back to a 0600 file.
// Returns where it went.
func SaveCredential(c *Credential) (string, error) {
	b, _ := json.Marshal(c)
	if !keyringDisabled() {
		if err := keychain.Set(keyringService, keyringUser(), string(b)); err == nil {
			_ = os.Remove(CredsPath())
			return "keychain", nil
		}
	}
	if err := os.MkdirAll(Home(), 0o700); err != nil {
		return "", err
	}
	if err := WriteFileAtomic(CredsPath(), b, 0o600); err != nil {
		return "", err
	}
	return "file", nil
}

// DeleteCredential removes the token from every store.
func DeleteCredential() {
	if !keyringDisabled() {
		_ = keychain.Delete(keyringService, keyringUser())
	}
	_ = os.Remove(CredsPath())
}

func keyringUser() string {
	// One credential per AGENTWATCH_HOME, so test homes never touch the real one.
	return "sensor:" + Home()
}
