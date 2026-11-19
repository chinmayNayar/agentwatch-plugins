// Package state keeps small per-session and per-agent files under
// ~/.agentwatch/state: the run id for a session, verdicts waiting for their
// post-tool report, hold ids for retries, and the cached passport.
package state

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"time"

	"github.com/chinmayNayar/agentwatch-plugins/internal/client"
	"github.com/chinmayNayar/agentwatch-plugins/internal/config"
)

// Pending is a verdict waiting for the tool's post event.
type Pending struct {
	Decision  string `json:"d"`
	Source    string `json:"s"`
	WouldHave string `json:"w,omitempty"`
	HoldID    string `json:"h,omitempty"`
	Server    string `json:"sv,omitempty"`
	LatencyMS int64  `json:"l"`
	TS        int64  `json:"t"`
}

// Session is one client session (one run).
type Session struct {
	RunID          string             `json:"run_id,omitempty"`
	Agent          string             `json:"agent"`
	Killed         bool               `json:"killed,omitempty"`
	BypassReported bool               `json:"bypass_reported,omitempty"`
	Pending        map[string]Pending `json:"pending,omitempty"`
	Holds          map[string]string  `json:"holds,omitempty"` // input hash -> hold id
	Updated        int64              `json:"updated"`
}

func hash(s string) string {
	h := sha256.Sum256([]byte(s))
	return hex.EncodeToString(h[:])[:24]
}

// Hash is a short stable hash for keys and tags.
func Hash(s string) string { return hash(s) }

func sessionPath(client, session string) string {
	return filepath.Join(config.StateDir(), "sessions", client+"-"+hash(session)+".json")
}

// LoadSession reads a session file; a missing or broken file is a new session.
func LoadSession(client, session string) *Session {
	s := &Session{}
	if b, err := os.ReadFile(sessionPath(client, session)); err == nil {
		_ = json.Unmarshal(b, s)
	}
	if s.Pending == nil {
		s.Pending = map[string]Pending{}
	}
	if s.Holds == nil {
		s.Holds = map[string]string{}
	}
	return s
}

// Update applies fn under a short per-session lock (parallel tool calls run
// their hooks concurrently) and writes atomically.
func Update(client, session string, fn func(*Session)) *Session {
	p := sessionPath(client, session)
	_ = os.MkdirAll(filepath.Dir(p), 0o700)
	unlock := lock(p + ".lock")
	defer unlock()
	s := LoadSession(client, session)
	fn(s)
	// Bound the maps: an abandoned pending entry must not grow forever.
	cut := time.Now().Add(-30 * time.Minute).UnixMilli()
	for k, v := range s.Pending {
		if v.TS < cut {
			delete(s.Pending, k)
		}
	}
	if len(s.Holds) > 50 {
		s.Holds = map[string]string{}
	}
	s.Updated = time.Now().UnixMilli()
	b, _ := json.Marshal(s)
	_ = config.WriteFileAtomic(p, b, 0o600)
	return s
}

func lock(p string) func() {
	deadline := time.Now().Add(200 * time.Millisecond)
	for {
		f, err := os.OpenFile(p, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
		if err == nil {
			f.Close()
			return func() { _ = os.Remove(p) }
		}
		if info, serr := os.Stat(p); serr == nil && time.Since(info.ModTime()) > 5*time.Second {
			_ = os.Remove(p)
			continue
		}
		if time.Now().After(deadline) {
			// Proceed unlocked rather than stall the agent; worst case one
			// pending entry is lost and the step reports without its verdict.
			return func() {}
		}
		time.Sleep(5 * time.Millisecond)
	}
}

func policyPath(agent string) string {
	return filepath.Join(config.StateDir(), "policy-"+hash(agent)+".json")
}

// LoadPolicy returns the cached passport and its age.
func LoadPolicy(agent string) (*client.Policy, time.Duration) {
	b, err := os.ReadFile(policyPath(agent))
	if err != nil {
		return nil, 0
	}
	var p client.Policy
	if json.Unmarshal(b, &p) != nil {
		return nil, 0
	}
	return &p, time.Since(time.UnixMilli(p.FetchedAt))
}

// SavePolicy caches a passport.
func SavePolicy(p *client.Policy) {
	if p == nil || p.AgentName == "" {
		return
	}
	b, _ := json.Marshal(p)
	_ = config.WriteFileAtomic(policyPath(p.AgentName), b, 0o600)
}

// MarkActive records that an agent ran recently (the flusher refreshes
// passports for active agents).
func MarkActive(agent string) {
	p := filepath.Join(config.StateDir(), "active-agents.json")
	m := map[string]int64{}
	if b, err := os.ReadFile(p); err == nil {
		_ = json.Unmarshal(b, &m)
	}
	if t, ok := m[agent]; ok && time.Since(time.UnixMilli(t)) < time.Minute {
		return // marked recently: skip the write on the hot path
	}
	m[agent] = time.Now().UnixMilli()
	cut := time.Now().Add(-24 * time.Hour).UnixMilli()
	for k, v := range m {
		if v < cut {
			delete(m, k)
		}
	}
	b, _ := json.Marshal(m)
	_ = config.WriteFileAtomic(p, b, 0o600)
}

// ActiveAgents lists agents seen in the last window.
func ActiveAgents(window time.Duration) []string {
	p := filepath.Join(config.StateDir(), "active-agents.json")
	m := map[string]int64{}
	if b, err := os.ReadFile(p); err == nil {
		_ = json.Unmarshal(b, &m)
	}
	cut := time.Now().Add(-window).UnixMilli()
	var out []string
	for k, v := range m {
		if v >= cut {
			out = append(out, k)
		}
	}
	return out
}

// Stamp files: small timestamps for rate limiting (heartbeat).
func ReadStamp(name string) time.Time {
	b, err := os.ReadFile(filepath.Join(config.StateDir(), name))
	if err != nil {
		return time.Time{}
	}
	var ms int64
	_ = json.Unmarshal(b, &ms)
	return time.UnixMilli(ms)
}

func WriteStamp(name string) {
	b, _ := json.Marshal(time.Now().UnixMilli())
	_ = config.WriteFileAtomic(filepath.Join(config.StateDir(), name), b, 0o600)
}

// InstalledMarker records which clients have AgentWatch hooks installed, so a
// hook running inside another client can yield to that client's own install.
func SetInstalled(client string, on bool) {
	p := filepath.Join(config.StateDir(), "installed-"+client)
	if on {
		_ = os.MkdirAll(config.StateDir(), 0o700)
		_ = os.WriteFile(p, []byte("1"), 0o600)
	} else {
		_ = os.Remove(p)
	}
}

func IsInstalled(client string) bool {
	_, err := os.Stat(filepath.Join(config.StateDir(), "installed-"+client))
	return err == nil
}
